package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const balanceUsage = `Usage:
  cfctl credits show   --workspace <wks_... or org_...> [--limit N] [flags]
  cfctl credits grant  --workspace <wks_... or org_...> --credits N --reason <text> [--expires DATE] [flags]
  cfctl credits adjust --workspace <wks_... or org_...> --credits <+N or -N> --reason <text> [--expires DATE] [flags]

The credits of a workspace (spend control v1, D-31), until checkout exists (P7-01c). They are the customer's currency: a video is a first edit plus two
revisions, and the price list says how many credits that is (show prints the balance in videos from it). show prints the plan, the balance and the newest ledger rows. grant adds a top-up. adjust is an operator's correction: a positive
number adds credits, a negative one takes them away (and fails when the workspace has fewer). Every grant and adjustment writes a ledger row
with who ran it (cfctl:<OS user>) and the reason, and nothing is ever edited or deleted. It reads DATABASE_URL (the app's own role) and never
prints a secret.

  --workspace  the workspace: a wks_... id or a Clerk org_... id
  --credits    how many credits (grant: positive; adjust: signed)
  --reason     why (required for grant and adjust; it is kept in the ledger)
  --expires    when the credits added expire: YYYY-MM-DD (through that day, UTC) or an RFC 3339 time. Default: the price list's top-up
               expiry (12 months from now)
  --limit      show: how many ledger rows to print (default 20)
  --env-file   the env file; default .env at the repository root, and a relative name is taken from there too
  --yes        grant and adjust ask for this unless CF_ENV is set to local or ci, the database host is localhost, 127.0.0.1, ::1 or the
               compose service "postgres", and CF_ENV and DATABASE_URL come from the same place (staging always needs it)
  --allow-production
               grant and adjust are refused when CF_ENV is production without this, and with it they still need --yes

Local example:  go run ./cmd/cfctl credits grant --workspace wks_01... --credits 60 --reason "founder testing"
`

// creditsDeps are what cmdCredits needs from outside, replaced in tests.
type creditsDeps struct {
	now func() time.Time
}

func cmdCredits(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	return cmdCreditsWith(args, stdout, stderr, lookup, deps, creditsDeps{now: time.Now})
}

func cmdCreditsWith(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps, cd creditsDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, balanceUsage)
		return 2
	}
	command := args[0]
	switch command {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, balanceUsage)
		return 0
	case "show", "grant", "adjust":
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl credits: unknown command %q\n\n%s", command, balanceUsage)
		return 2
	}

	flags := flag.NewFlagSet("credits "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, balanceUsage) }
	workspaceArg := flags.String("workspace", "", "workspace")
	creditsArg := flags.Int("credits", 0, "credits")
	reasonArg := flags.String("reason", "", "reason")
	expiresArg := flags.String("expires", "", "expiry")
	limit := flags.Int("limit", 20, "ledger rows to print")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm on staging or any database that is not this machine")
	allowProd := flags.Bool("allow-production", false, "allow grant and adjust on production")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl credits %s: unexpected argument %q\n", command, flags.Arg(0))
		return 2
	}

	// Arguments first, so a typo never needs a database.
	w := strings.TrimSpace(*workspaceArg)
	var (
		wantWS  ids.WorkspaceID
		wantOrg string
	)
	if strings.HasPrefix(w, "org_") {
		wantOrg = w
	} else if id, err := ids.ParseWorkspaceID(w); err == nil {
		wantWS = id
	} else {
		_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --workspace must be a workspace id like wks_01... or a Clerk organization id like org_...\n", command)
		return 2
	}
	reason := strings.TrimSpace(*reasonArg)
	var expires *time.Time
	if command != "show" {
		if reason == "" {
			_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --reason is required: every change says why\n", command)
			return 2
		}
		if len(reason) > 500 {
			_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --reason is at most 500 characters\n", command)
			return 2
		}
		switch {
		case command == "grant" && *creditsArg < 1, command == "adjust" && *creditsArg == 0:
			_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --credits is required (grant: a positive number; adjust: +N or -N)\n", command)
			return 2
		case *creditsArg > 1_000_000 || *creditsArg < -1_000_000:
			_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --credits is out of range\n", command)
			return 2
		}
		if *expiresArg != "" {
			if command == "adjust" && *creditsArg < 0 {
				_, _ = fmt.Fprintln(stderr, "cfctl credits adjust: --expires belongs to credits that are added")
				return 2
			}
			t, err := parseUntil(*expiresArg)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl credits %s: %v\n", command, strings.Replace(err.Error(), "--until", "--expires", 1))
				return 2
			}
			if !t.After(cd.now()) {
				_, _ = fmt.Fprintf(stderr, "cfctl credits %s: --expires must be in the future\n", command)
				return 2
			}
			expires = &t
		}
	} else if *limit < 1 || *limit > 1000 {
		_, _ = fmt.Fprintln(stderr, "cfctl credits show: --limit must be between 1 and 1000")
		return 2
	}

	env, code := loadAgentEnv("credits", *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	cfEnv := env.vars["CF_ENV"]
	if command != "show" {
		if msg := mutationRefusal("credits "+command, "it changes what a workspace may spend", "DATABASE_URL", cfEnv,
			sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), env.vars["DATABASE_URL"], *yes, *allowProd); msg != "" {
			_, _ = fmt.Fprintf(errOut, "cfctl credits: %s (env file %s)\n", msg, displayPath(env.root, env.path))
			return 2
		}
	}

	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, "credits", deps)
	if code != 0 {
		return code
	}
	defer st.Close()

	var (
		ws  store.Workspace
		err error
	)
	if wantOrg != "" {
		ws, err = st.GetWorkspaceByClerkOrgID(ctx, wantOrg)
	} else {
		ws, err = st.GetWorkspace(ctx, wantWS)
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl credits %s: that workspace was not found (%v)\n", command, err)
		return 1
	}
	list, err := credits.LoadDefault()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl credits %s: %v\n", command, err)
		return 1
	}
	svc := credits.NewService(st, list)
	svc.Now = cd.now
	now := cd.now()

	_, _ = fmt.Fprintf(out, "cfctl credits %s (CF_ENV=%s from %s, DATABASE_URL from %s, env file %s)\n",
		command, cmp.Or(cfEnv, "(unset)"), sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), displayPath(env.root, env.path))

	actor := planOperator(lookup)
	switch command {
	case "grant":
		exp := now
		if expires != nil {
			exp = *expires
		} else {
			exp = svc.TopUpExpiry(now)
		}
		_, created, gerr := st.GrantTopUp(ctx, store.GrantTopUpParams{
			Workspace: ws.ID, Credits: *creditsArg, ExpiresAt: exp, Actor: actor, Reason: reason, Plan: "", ListVersion: list.Version, Now: now,
		})
		if gerr != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl credits grant failed: %v\n", gerr)
			return 1
		}
		if created {
			_, _ = fmt.Fprintf(out, "  granted     %d credits, expiring %s\n", *creditsArg, exp.UTC().Format(time.RFC3339))
		}
	case "adjust":
		exp := time.Time{}
		if *creditsArg > 0 {
			if expires != nil {
				exp = *expires
			} else {
				exp = svc.TopUpExpiry(now)
			}
		}
		moved, aerr := st.AdjustCredits(ctx, store.AdjustCreditsParams{Workspace: ws.ID, Credits: *creditsArg, ExpiresAt: exp, Actor: actor, Reason: reason, Now: now})
		var ins *store.InsufficientCreditsError
		if errors.As(aerr, &ins) {
			_, _ = fmt.Fprintf(errOut, "cfctl credits adjust: the workspace has %d credits to take from, not %d. Nothing was changed\n", ins.Available, ins.Needed)
			return 1
		}
		if aerr != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl credits adjust failed: %v\n", aerr)
			return 1
		}
		_, _ = fmt.Fprintf(out, "  adjusted    %+d credits\n", moved)
	}
	return printCredits(ctx, out, errOut, st, list, ws, now, *limit)
}

// printCredits prints the plan, the balance and the newest ledger rows of a workspace. Free text from the ledger (a reason) is quoted
// with %q, so it cannot forge lines or move the cursor.
func printCredits(ctx context.Context, out, errOut io.Writer, st *store.Store, list *credits.PriceList, ws store.Workspace, now time.Time, limit int) int {
	ent, err := st.GetEntitlements(ctx, ws.ID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl credits: reading the plan failed: %v\n", err)
		return 1
	}
	bal, err := st.CreditBalance(ctx, ws.ID, now)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl credits: reading the balance failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "  workspace   %q (%s)\n  plan        %s (%s)\n", ws.Name, ws.ID, ent.Plan, ent.Status)
	_, _ = fmt.Fprintf(out, "  available   %d credits (%.1f videos), %d held by runs in progress\n", bal.Available, float64(bal.Available)/float64(list.CreditsPerVideo()), bal.Held)
	if bal.Allowance > 0 {
		end := "does not expire"
		if bal.PeriodEnd != nil {
			end = "expires " + bal.PeriodEnd.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(out, "  allowance   %d of %d left, %s\n", bal.AllowanceRemaining, bal.Allowance, end)
	}
	for _, t := range bal.TopUps {
		_, _ = fmt.Fprintf(out, "  top-up      %d left, expires %s\n", t.Credits, t.ExpiresAt.UTC().Format(time.RFC3339))
	}
	rows, err := st.CreditLedger(ctx, ws.ID, limit)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl credits: reading the ledger failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "\nledger (newest %d):\n", len(rows))
	for _, r := range rows {
		line := fmt.Sprintf("  %s  %-15s %+5d", r.CreatedAt.UTC().Format("2006-01-02 15:04:05"), r.Kind, r.Credits)
		if r.Action != nil {
			line += "  " + *r.Action
		}
		if r.ExpiresAt != nil {
			line += "  expires " + r.ExpiresAt.UTC().Format("2006-01-02")
		}
		if r.Actor != nil {
			line += fmt.Sprintf("  by %q", clip(*r.Actor, 60))
		}
		if r.Reason != nil {
			line += fmt.Sprintf("  %q", clip(*r.Reason, 80))
		}
		_, _ = fmt.Fprintln(out, line)
	}
	return 0
}
