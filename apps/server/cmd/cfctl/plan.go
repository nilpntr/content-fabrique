package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const planUsage = `Usage:
  cfctl plan grant  --user <email or Clerk user id> --plan <trial|studio|creator|pro> [--until DATE] [flags]
  cfctl plan revoke --user <email or Clerk user id> [flags]
  cfctl plan show   --user <email or Clerk user id> [flags]

Sets, removes or shows the plan of a person's workspace, until billing exists (P7-01). Without a plan the workspace sees
the paywall and the API answers 402 plan_required to its paid endpoints. The person must have signed in once, because that
is what creates their workspace. It reads DATABASE_URL (the app's own role: it needs DML only) and prints the workspace
name, its id, the plan, status and end of the period. Never prints the URL or a secret.

  --user      the person: an e-mail address (any case) or a Clerk user id (user_...)
  --plan      the plan to grant: trial, studio, creator or pro
  --until     the last day of the plan: YYYY-MM-DD (the plan runs through that day and ends at the start of the next, UTC)
              or an RFC 3339 time (the exact end). Default: no end.
              A date in the past is accepted (the plan is then not active): that is how expiry is tried out
  --workspace which workspace, when the person is in several: a wks_... id or a Clerk org_... id. The candidates are listed when it is needed
  --env-file  the env file; default .env at the repository root, and a relative name is taken from there too
              (task plan runs in apps/server). A non-empty value in the file wins; the process environment fills the rest
  --yes       grant and revoke ask for this unless CF_ENV is set to local or ci, the database host is localhost, 127.0.0.1,
              ::1 or the compose service "postgres", and CF_ENV and DATABASE_URL come from the same place
  --force     replace a plan that the merchant of record manages (its source is not manual); refused without it
  --allow-production
              grant and revoke are refused when CF_ENV is production without this, and with it they still need --yes

Local example:  task plan -- grant --user you@example.com --plan creator
`

// planOperator names who runs the command, for the audit column granted_by. It is the OS user, reduced to safe characters.
func planOperator(lookup lookupEnv) string {
	name, _ := lookup("USER")
	if name == "" {
		name, _ = lookup("USERNAME")
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	return "cfctl:" + cmp.Or(b.String(), "unknown")
}

// parseUntil reads --until: a day (the plan runs through the end of that day, UTC) or an RFC 3339 time.
func parseUntil(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		return t.Add(24 * time.Hour), nil // the end is exclusive: the whole of the named day counts
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("--until must be a date like 2026-12-31 or an RFC 3339 time like 2026-12-31T18:00:00Z")
}

func cmdPlan(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, planUsage)
		return 2
	}
	command := args[0]
	switch command {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, planUsage)
		return 0
	case "grant", "revoke", "show":
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl plan: unknown command %q\n\n%s", command, planUsage)
		return 2
	}

	flags := flag.NewFlagSet("plan "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, planUsage) }
	userArg := flags.String("user", "", "e-mail or Clerk user id")
	planArg := flags.String("plan", "", "plan to grant")
	untilArg := flags.String("until", "", "last day of the plan")
	workspaceArg := flags.String("workspace", "", "workspace id, when the person is in several")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm on staging or any database that is not this machine")
	allowProd := flags.Bool("allow-production", false, "allow grant and revoke on production")
	force := flags.Bool("force", false, "replace a plan managed by the merchant of record")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl plan %s: unexpected argument %q\n", command, flags.Arg(0))
		return 2
	}

	// Arguments first, so a typo never needs a database.
	person := strings.TrimSpace(*userArg)
	if person == "" {
		_, _ = fmt.Fprintf(stderr, "cfctl plan %s: --user is required (an e-mail address or a Clerk user id)\n", command)
		return 2
	}
	var (
		plan    plans.Plan
		until   *time.Time
		wantWS  ids.WorkspaceID
		wantOrg string
	)
	if command == "grant" {
		plan = plans.Plan(strings.ToLower(strings.TrimSpace(*planArg)))
		if !plan.Grantable() {
			_, _ = fmt.Fprintf(stderr, "cfctl plan grant: --plan must be one of trial, studio, creator, pro (use revoke to remove a plan)\n")
			return 2
		}
		if *untilArg != "" {
			t, err := parseUntil(*untilArg)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl plan grant: %v\n", err)
				return 2
			}
			until = &t
		}
	} else if *planArg != "" || *untilArg != "" {
		_, _ = fmt.Fprintf(stderr, "cfctl plan %s: --plan and --until belong to grant\n", command)
		return 2
	}
	if w := strings.TrimSpace(*workspaceArg); strings.HasPrefix(w, "org_") {
		wantOrg = w
	} else if w != "" {
		id, err := ids.ParseWorkspaceID(w)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl plan %s: --workspace must be a workspace id like wks_01... or a Clerk organization id like org_... (the candidates are listed when it is needed)\n", command)
			return 2
		}
		wantWS = id
	}

	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	explicit := *envFileArg != ""
	path := *envFileArg
	if !explicit {
		path = filepath.Join(root, ".env")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file := map[string]string{}
	parsed, err := envfile.ParseFile(path)
	switch {
	case err == nil:
		file = parsed
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// process environment only
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl plan: cannot read the env file %s\n", displayPath(root, path))
		return 2
	}
	vars := doctor.MergeEnv(example, file, lookup)

	// Everything printed below goes through the redactor, so an error that quotes a value cannot leak it.
	redactor := doctor.NewRedactor(example, vars)
	out, errOut := redactor.Writer(stdout), redactor.Writer(stderr)

	dsn := vars["DATABASE_URL"]
	if dsn == "" {
		_, _ = fmt.Fprintf(errOut, "cfctl plan: DATABASE_URL is not set in %s or the environment\n", displayPath(root, path))
		return 2
	}
	if err := config.ValidatePostgresURL(dsn); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl plan: DATABASE_URL: %v\n", err)
		return 2
	}

	cfEnv := vars["CF_ENV"]
	cfSource := sourceOf("CF_ENV", file, lookup)
	urlSource := sourceOf("DATABASE_URL", file, lookup)
	if command == "grant" || command == "revoke" {
		if msg := mutationRefusal("plan "+command, "it changes what a workspace may use", "DATABASE_URL", cfEnv, cfSource, urlSource, dsn, *yes, *allowProd); msg != "" {
			_, _ = fmt.Fprintf(errOut, "cfctl plan: %s (env file %s)\n", msg, displayPath(root, path))
			return 2
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, dsn, store.Options{AppName: "cfctl", MaxConns: 2})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl plan: %v\n", err)
		return 1
	}
	defer st.Close()

	_, _ = fmt.Fprintf(out, "cfctl plan %s (CF_ENV=%s from %s, DATABASE_URL from %s, env file %s)\n",
		command, cmp.Or(cfEnv, "(unset)"), cfSource, urlSource, displayPath(root, path))

	ws, code := chooseWorkspace(ctx, st, person, wantWS, wantOrg, command, errOut)
	if code != 0 {
		return code
	}

	var ent store.Entitlements
	switch command {
	case "grant":
		ent, err = st.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plan, PeriodEnd: until, Source: store.SourceManual, GrantedBy: planOperator(lookup), Force: *force})
	case "revoke":
		ent, err = st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: ws.ID, Source: store.SourceManual, GrantedBy: planOperator(lookup), Force: *force})
	default:
		ent, err = st.GetEntitlements(ctx, ws.ID)
	}
	if errors.Is(err, store.ErrPlanSourceConflict) {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: the plan of this workspace is managed by the merchant of record (its source is not manual), so a hand edit would be overwritten by billing or disagree with what the customer pays. Nothing was changed. Pass --force if you mean it\n", command)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s failed: %v\n", command, err)
		return 1
	}
	printEntitlements(out, ws, ent)
	if command == "grant" && until != nil && !ent.Active {
		_, _ = fmt.Fprintln(out, "note: that end has already passed, so the plan is not active")
	}
	return 0
}

// chooseWorkspace resolves --user (and --workspace) to exactly one workspace of that person, or explains what to pass.
func chooseWorkspace(ctx context.Context, st *store.Store, person string, want ids.WorkspaceID, wantOrg, command string, errOut io.Writer) (store.WorkspaceRef, int) {
	people, err := st.FindPeople(ctx, person)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: looking up the person failed: %v\n", command, err)
		return store.WorkspaceRef{}, 1
	}
	switch {
	case len(people) == 0:
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: no user matches that e-mail address or Clerk user id. They must sign in to the app once, which creates their workspace\n", command)
		return store.WorkspaceRef{}, 1
	case len(people) > 1:
		// An e-mail address is not unique to one account. Granting to "whoever matched first" could unlock a stranger.
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: that e-mail address belongs to %d users. Pass the Clerk user id of the one you mean as --user:\n", command, len(people))
		for _, p := range people {
			_, _ = fmt.Fprintf(errOut, "  %s\n", safeID(p.ClerkUserID))
		}
		return store.WorkspaceRef{}, 2
	}
	refs, err := st.WorkspacesOfUser(ctx, people[0].ID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: looking up the workspaces failed: %v\n", command, err)
		return store.WorkspaceRef{}, 1
	}
	if len(refs) == 0 {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: that user is in no workspace. They must create or join one in the app first\n", command)
		return store.WorkspaceRef{}, 1
	}
	if !want.IsZero() || wantOrg != "" {
		for _, r := range refs {
			if r.ID == want || (wantOrg != "" && r.ClerkOrgID == wantOrg) {
				return r, 0
			}
		}
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: that user is not in that workspace. Their workspaces:\n", command)
		listWorkspaces(errOut, refs)
		return store.WorkspaceRef{}, 2
	}
	if len(refs) > 1 {
		_, _ = fmt.Fprintf(errOut, "cfctl plan %s: that user is in %d workspaces; choose one with --workspace <id>:\n", command, len(refs))
		listWorkspaces(errOut, refs)
		return store.WorkspaceRef{}, 2
	}
	return refs[0], 0
}

func listWorkspaces(w io.Writer, refs []store.WorkspaceRef) {
	for _, r := range refs {
		// The name is text a user typed: %q quotes it and escapes control characters, so it cannot forge lines or move the
		// cursor. Either id works for --workspace.
		_, _ = fmt.Fprintf(w, "  %s  %s  %q  created %s  role %s  %d %s\n", r.ID, safeID(r.ClerkOrgID), r.Name,
			r.CreatedAt.UTC().Format("2006-01-02"), r.Role, r.Members, pluralMembers(r.Members))
	}
}

// safeID prints a Clerk id as it is when it is plain (letters, digits, underscore and hyphen, which is what Clerk makes),
// so it can be pasted into --workspace or --user, and quoted with escapes otherwise.
func safeID(id string) string {
	if id == "" {
		return `""`
	}
	for _, r := range id {
		plain := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !plain {
			return fmt.Sprintf("%q", id)
		}
	}
	return id
}

func pluralMembers(n int) string {
	if n == 1 {
		return "member"
	}
	return "members"
}

func printEntitlements(w io.Writer, ws store.WorkspaceRef, e store.Entitlements) {
	end := "none (does not end)"
	if e.CurrentPeriodEnd != nil {
		end = e.CurrentPeriodEnd.UTC().Format(time.RFC3339)
	}
	active := "no"
	if e.Active {
		active = "yes"
	}
	_, _ = fmt.Fprintf(w, "  workspace   %q (%s)\n", ws.Name, ws.ID)
	_, _ = fmt.Fprintf(w, "  plan        %s\n", e.Plan)
	_, _ = fmt.Fprintf(w, "  status      %s\n", e.Status)
	_, _ = fmt.Fprintf(w, "  period end  %s\n", end)
	_, _ = fmt.Fprintf(w, "  active      %s\n", active)
}
