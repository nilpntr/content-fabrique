package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// planRig is a migrated database with a few people in it, and the env file that points cfctl at it.
type planRig struct {
	t       *testing.T
	st      *store.Store
	dsn     string
	envFile string
	proc    map[string]string
}

func newPlanRig(t *testing.T) *planRig {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "plan-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r := &planRig{t: t, st: st, dsn: dsn, proc: map[string]string{"USER": "sam"}}
	// DATABASE_MIGRATE_URL is present and wrong on purpose: cfctl plan must use DATABASE_URL, the app's own role.
	r.envFile = filepath.Join(t.TempDir(), "plan.env")
	r.writeEnv("CF_ENV=local\nDATABASE_URL=" + dsn + "\nDATABASE_MIGRATE_URL=postgres://nobody:nope@db.invalid:1/x\n")
	return r
}

func (r *planRig) writeEnv(body string) {
	r.t.Helper()
	if err := os.WriteFile(r.envFile, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// person creates a user who is a member of the named workspaces (created on first mention) and returns their email.
func (r *planRig) person(clerkID string, workspaces ...string) string {
	r.t.Helper()
	ctx := context.Background()
	email := clerkID + "@example.com"
	u, err := r.st.UpsertUserByClerkID(ctx, clerkID, email)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, name := range workspaces {
		w, err := r.st.GetWorkspaceByClerkOrgID(ctx, "org_"+name)
		if err != nil {
			if w, err = r.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_" + name, Name: name}); err != nil {
				r.t.Fatal(err)
			}
		}
		if _, err := r.st.AddMember(ctx, w.ID, u.ID, store.RoleOwner); err != nil {
			r.t.Fatal(err)
		}
	}
	return email
}

func (r *planRig) workspace(name string) store.Workspace {
	r.t.Helper()
	w, err := r.st.GetWorkspaceByClerkOrgID(context.Background(), "org_"+name)
	if err != nil {
		r.t.Fatal(err)
	}
	return w
}

func (r *planRig) active(name string) bool {
	r.t.Helper()
	e, err := r.st.GetEntitlements(context.Background(), r.workspace(name).ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return e.Active
}

func (r *planRig) run(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code = cmdPlan(append(args[:1:1], append([]string{"--env-file", r.envFile}, args[1:]...)...), &out, &errb, fakeProcessEnv(r.proc))
	return code, out.String(), errb.String()
}

func TestPlanGrantRevokeShow(t *testing.T) {
	r := newPlanRig(t)
	email := r.person("user_ada", "Fit Studio")

	// Nothing granted yet.
	code, out, errOut := r.run("show", "--user", email)
	if code != 0 || !strings.Contains(out, "plan        none") || !strings.Contains(out, "active      no") {
		t.Fatalf("show before a grant = %d\n%s%s", code, out, errOut)
	}

	// Local rules: this database is loopback, CF_ENV and the URL come from the one env file, but the test container's
	// host may be a Docker host, so --yes keeps the test independent of that. The guard has its own test below.
	code, out, errOut = r.run("grant", "--user", email, "--plan", "creator", "--yes")
	if code != 0 {
		t.Fatalf("grant = %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"\"Fit Studio\" (wks_", "plan        creator", "status      active", "period end  none (does not end)", "active      yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("grant output lacks %q:\n%s", want, out)
		}
	}
	if !r.active("Fit Studio") {
		t.Fatal("the workspace is not active after the grant")
	}
	e, _ := r.st.GetEntitlements(context.Background(), r.workspace("Fit Studio").ID)
	if e.Plan != plans.Creator || e.Source != store.SourceManual || e.GrantedBy != "cfctl:sam" {
		t.Fatalf("stored = %+v", e)
	}

	// An e-mail in another case and a Clerk user id find the same person.
	if code, out, _ := r.run("show", "--user", "USER_ADA@Example.com"); code != 0 || !strings.Contains(out, "plan        creator") {
		t.Fatalf("show by e-mail in other case = %d\n%s", code, out)
	}
	if code, out, _ := r.run("show", "--user", "user_ada"); code != 0 || !strings.Contains(out, "plan        creator") {
		t.Fatalf("show by Clerk user id = %d\n%s", code, out)
	}

	// --until: a date runs through the end of that day, UTC.
	code, out, errOut = r.run("grant", "--user", email, "--plan", "pro", "--until", "2999-12-31", "--yes")
	if code != 0 || !strings.Contains(out, "plan        pro") || !strings.Contains(out, "period end  3000-01-01T00:00:00Z") || !strings.Contains(out, "active      yes") {
		t.Fatalf("grant --until = %d\n%s%s", code, out, errOut)
	}
	// A date in the past is accepted and says so: the plan is not active (that is how expiry is tried out).
	code, out, errOut = r.run("grant", "--user", email, "--plan", "pro", "--until", "2020-01-01", "--yes")
	if code != 0 || !strings.Contains(out, "active      no") || !strings.Contains(out, "already passed") {
		t.Fatalf("grant --until in the past = %d\n%s%s", code, out, errOut)
	}
	if r.active("Fit Studio") {
		t.Fatal("an expired grant is active")
	}
	if code, out, _ = r.run("grant", "--user", email, "--plan", "studio", "--until", "2999-01-01T10:00:00+02:00", "--yes"); code != 0 || !strings.Contains(out, "period end  2999-01-01T08:00:00Z") {
		t.Fatalf("grant --until RFC 3339 = %d\n%s", code, out)
	}

	// Revoke brings the paywall back.
	code, out, errOut = r.run("revoke", "--user", email, "--yes")
	if code != 0 || !strings.Contains(out, "plan        none") || !strings.Contains(out, "status      canceled") || !strings.Contains(out, "active      no") {
		t.Fatalf("revoke = %d\n%s%s", code, out, errOut)
	}
	if r.active("Fit Studio") {
		t.Fatal("the workspace is still active after the revoke")
	}
}

func TestPlanNeedsOneWorkspace(t *testing.T) {
	r := newPlanRig(t)
	both := r.person("user_both", "Alpha", "Beta")
	r.person("user_beta", "Beta")

	// Two workspaces: refused, with the candidates by name and TypeID, and nothing changed.
	code, out, errOut := r.run("grant", "--user", both, "--plan", "pro", "--yes")
	if code != 2 || !strings.Contains(errOut, "2 workspaces") || !strings.Contains(errOut, "--workspace") {
		t.Fatalf("ambiguous grant = %d\n%s%s", code, out, errOut)
	}
	for _, name := range []string{"Alpha", "Beta"} {
		if !strings.Contains(errOut, `"`+name+`"`) || !strings.Contains(errOut, r.workspace(name).ID.String()) || !strings.Contains(errOut, "org_"+name) {
			t.Errorf("the candidates do not list %s with its id:\n%s", name, errOut)
		}
		if r.active(name) {
			t.Errorf("%s was granted although the choice was ambiguous", name)
		}
	}
	if strings.Contains(errOut, "user_") || strings.Contains(errOut, "example.com") {
		t.Errorf("the candidate list shows more than the workspaces:\n%s", errOut)
	}

	for _, want := range []string{"created ", "role owner", "members"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the candidates lack %q:\n%s", want, errOut)
		}
	}

	// By TypeID.
	code, out, errOut = r.run("grant", "--user", both, "--plan", "pro", "--workspace", r.workspace("Alpha").ID.String(), "--yes")
	if code != 0 || !strings.Contains(out, "\"Alpha\" (") {
		t.Fatalf("grant with --workspace = %d\n%s%s", code, out, errOut)
	}
	if !r.active("Alpha") || r.active("Beta") {
		t.Fatalf("Alpha = %v, Beta = %v; only Alpha was named", r.active("Alpha"), r.active("Beta"))
	}
	// By Clerk organization id.
	if code, out, errOut = r.run("grant", "--user", both, "--plan", "studio", "--workspace", "org_Beta", "--yes"); code != 0 || !strings.Contains(out, "\"Beta\" (") {
		t.Fatalf("grant with an org id = %d\n%s%s", code, out, errOut)
	}
	// Revoke is as strict as grant.
	if code, _, errOut = r.run("revoke", "--user", both, "--yes"); code != 2 || !strings.Contains(errOut, "--workspace") {
		t.Fatalf("ambiguous revoke = %d %s", code, errOut)
	}
	if !r.active("Alpha") || !r.active("Beta") {
		t.Fatal("an ambiguous revoke changed something")
	}

	// A workspace the person is not in cannot be named, even though it exists.
	only := r.person("user_alone", "Gamma")
	code, _, errOut = r.run("grant", "--user", only, "--plan", "pro", "--workspace", r.workspace("Alpha").ID.String(), "--yes")
	if code != 2 || !strings.Contains(errOut, "not in that workspace") || !strings.Contains(errOut, "Gamma") || strings.Contains(errOut, "Alpha") {
		t.Fatalf("a workspace of someone else = %d\n%s", code, errOut)
	}
	code, _, errOut = r.run("grant", "--user", only, "--plan", "pro", "--workspace", "org_Alpha", "--yes")
	if code != 2 {
		t.Fatalf("someone else's org id = %d\n%s", code, errOut)
	}
	// Alpha already had the plan from above; the refused grants did not touch Gamma.
	if r.active("Gamma") {
		t.Fatal("Gamma was granted by a refused command")
	}

	// A person with exactly one workspace needs no --workspace; the one next to them is not touched (tenancy).
	if code, out, errOut = r.run("grant", "--user", "user_beta", "--plan", "trial", "--yes"); code != 0 || !strings.Contains(out, "\"Beta\" (") {
		t.Fatalf("grant for the single-workspace person = %d\n%s%s", code, out, errOut)
	}
}

func TestPlanUnknownPeopleAndBadArguments(t *testing.T) {
	r := newPlanRig(t)
	r.person("user_ada", "Fit")
	if _, err := r.st.UpsertUserByClerkID(context.Background(), "user_drifter", "drifter@example.com"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		args    []string
		code    int
		wantErr string
	}{
		{"unknown user", []string{"grant", "--user", "nobody@example.com", "--plan", "pro", "--yes"}, 1, "sign in to the app once"},
		{"user in no workspace", []string{"grant", "--user", "drifter@example.com", "--plan", "pro", "--yes"}, 1, "in no workspace"},
		{"no user", []string{"grant", "--plan", "pro"}, 2, "--user is required"},
		{"no plan", []string{"grant", "--user", "user_ada"}, 2, "--plan must be one of"},
		{"plan none", []string{"grant", "--user", "user_ada", "--plan", "none"}, 2, "use revoke"},
		{"unknown plan", []string{"grant", "--user", "user_ada", "--plan", "platinum"}, 2, "--plan must be one of"},
		{"bad until", []string{"grant", "--user", "user_ada", "--plan", "pro", "--until", "next friday"}, 2, "--until must be"},
		{"plan on revoke", []string{"revoke", "--user", "user_ada", "--plan", "pro"}, 2, "belong to grant"},
		{"bad workspace", []string{"grant", "--user", "user_ada", "--plan", "pro", "--workspace", "banana"}, 2, "--workspace must be"},
		{"stray argument", []string{"grant", "--user", "user_ada", "--plan", "pro", "extra"}, 2, "unexpected argument"},
		{"unknown command", []string{"upgrade"}, 2, "unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := r.run(tc.args...)
			if code != tc.code || !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("exit %d, want %d, stderr should contain %q\n%s%s", code, tc.code, tc.wantErr, out, errOut)
			}
		})
	}
	if r.active("Fit") {
		t.Fatal("a refused command granted a plan")
	}
	if code, _, _ := cmdPlanNoRig(); code != 2 {
		t.Fatalf("plan with no command = %d", code)
	}
}

func cmdPlanNoRig() (int, string, string) {
	var out, errb bytes.Buffer
	code := cmdPlan(nil, &out, &errb, fakeProcessEnv(nil))
	return code, out.String(), errb.String()
}

// planGuardCase runs `cfctl plan args...` against a database that does not exist (a closed port), so a run that gets
// past the guard fails at the connection with exit 1 and a run the guard refuses exits 2.
func planGuard(t *testing.T, cfFile, cfProc, host string, urlInEnv bool, args ...string) (code int, out, errOut string) {
	t.Helper()
	proc := map[string]string{}
	var body strings.Builder
	if cfFile != "" {
		body.WriteString("CF_ENV=" + cfFile + "\n")
	}
	if cfProc != "" {
		proc["CF_ENV"] = cfProc
	}
	if urlInEnv {
		proc["DATABASE_URL"] = guardURL(host)
	} else {
		body.WriteString("DATABASE_URL=" + guardURL(host) + "\n")
	}
	envFile := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(envFile, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	code = cmdPlan(append(args[:1:1], append([]string{"--env-file", envFile}, args[1:]...)...), &o, &e, fakeProcessEnv(proc))
	return code, o.String(), e.String()
}

// grant and revoke fail closed like migrate down: production needs --allow-production and --yes, anything that is not
// plainly this machine needs --yes, and show is never guarded.
func TestPlanWriteGuard(t *testing.T) {
	grant := []string{"grant", "--user", "a@example.com", "--plan", "pro"}
	with := func(base []string, extra ...string) []string { return append(append([]string{}, base...), extra...) }
	revoke := []string{"revoke", "--user", "a@example.com"}
	show := []string{"show", "--user", "a@example.com"}

	tests := []struct {
		name     string
		cfFile   string
		cfProc   string
		host     string
		inEnv    bool
		args     []string
		wantCode int
		wantErr  string
	}{
		// Allowed without --yes: explicit local, this machine, one source. (They fail at the closed port: exit 1.)
		{name: "local grant", cfFile: "local", host: "localhost", args: grant, wantCode: 1},
		{name: "local revoke", cfFile: "local", host: "127.0.0.1", args: revoke, wantCode: 1},
		{name: "ci grant", cfFile: "ci", host: "127.0.0.1", args: grant, wantCode: 1},

		// Refused without --yes.
		{name: "CF_ENV missing", host: "localhost", args: grant, wantCode: 2, wantErr: "CF_ENV is not set"},
		{name: "staging grant", cfFile: "staging", host: "db.invalid", args: grant, wantCode: 2, wantErr: "CF_ENV=staging is not local or ci"},
		{name: "staging revoke", cfFile: "staging", host: "db.invalid", args: revoke, wantCode: 2, wantErr: "without --yes"},
		{name: "local but a remote host", cfFile: "local", host: "db.invalid", args: grant, wantCode: 2, wantErr: "database host is not this machine"},
		{name: "mixed sources", cfFile: "local", host: "localhost", inEnv: true, args: grant, wantCode: 2, wantErr: "CF_ENV comes from the env file but DATABASE_URL from the process environment"},
		{name: "staging with --yes", cfFile: "staging", host: "db.invalid", args: with(grant, "--yes"), wantCode: 1},

		// Production.
		{name: "production", cfFile: "production", host: "db.invalid", args: grant, wantCode: 2, wantErr: "--allow-production"},
		{name: "production revoke", cfFile: "production", host: "db.invalid", args: revoke, wantCode: 2, wantErr: "--allow-production"},
		{name: "production --yes only", cfFile: "production", host: "localhost", args: with(grant, "--yes"), wantCode: 2, wantErr: "--allow-production"},
		{name: "production --allow-production only", cfFile: "production", host: "db.invalid", args: with(grant, "--allow-production"), wantCode: 2, wantErr: "--yes"},
		{name: "production from the environment", cfProc: "production", host: "db.invalid", inEnv: true, args: with(revoke, "--yes"), wantCode: 2, wantErr: "--allow-production"},
		{name: "production with both", cfFile: "production", host: "db.invalid", args: with(grant, "--yes", "--allow-production"), wantCode: 1},

		// show reads only.
		{name: "show on production", cfFile: "production", host: "db.invalid", args: show, wantCode: 1},
		{name: "show without CF_ENV", host: "db.invalid", args: show, wantCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := planGuard(t, tc.cfFile, tc.cfProc, tc.host, tc.inEnv, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s%s", code, tc.wantCode, out, errOut)
			}
			if tc.wantErr != "" && !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr %q does not contain %q", errOut, tc.wantErr)
			}
			if strings.Contains(out+errOut, guardPassword) || strings.Contains(out+errOut, "postgres://") {
				t.Errorf("the password or the URL was printed:\n%s%s", out, errOut)
			}
		})
	}
}

// Nothing cfctl plan prints may contain a secret or a connection URL, whether it succeeds or fails.
func TestPlanPrintsNoSecrets(t *testing.T) {
	r := newPlanRig(t)
	email := r.person("user_ada", "Fit")
	r.proc["CLERK_SECRET_KEY"] = "sk_test_SENTINEL_clerk_9f2c"
	r.proc["ANTHROPIC_API_KEY"] = "sk-ant-SENTINEL_9f2c"
	r.writeEnv("CF_ENV=local\nDATABASE_URL=" + r.dsn + "\nCLERK_SECRET_KEY=sk_test_SENTINEL_clerk_9f2c\n")
	for _, args := range [][]string{
		{"grant", "--user", email, "--plan", "pro", "--yes"},
		{"show", "--user", email},
		{"revoke", "--user", email, "--yes"},
		{"grant", "--user", "nobody@example.com", "--plan", "pro", "--yes"},
		{"grant", "--user", email, "--plan", "pro"}, // maybe refused by the guard, maybe not: either way, no secret
	} {
		code, out, errOut := r.run(args...)
		all := out + errOut
		for _, secret := range []string{"SENTINEL", "postgres://", "postgres:postgres", "sk_test", "sk-ant"} {
			if strings.Contains(all, secret) {
				t.Errorf("%v (exit %d) printed %q:\n%s", args, code, secret, all)
			}
		}
		// The dsn's password is the container's, "postgres": the connection error of a bad run would quote it.
		if strings.Contains(all, "@127.0.0.1") || strings.Contains(all, "@localhost") {
			t.Errorf("%v printed a URL host with credentials:\n%s", args, all)
		}
	}
}

// A workspace name is text a user typed. In the candidate list it is quoted, so a control character or a fake line in it
// cannot move the cursor, clear the screen or pass for another candidate.
func TestPlanCandidateNamesAreQuoted(t *testing.T) {
	r := newPlanRig(t)
	evil := "Evil\x1b[2J\r\nwks_01fakefakefakefakefakefakef  \"Trusted\""
	both := r.person("user_evil", evil, "Plain")
	code, _, errOut := r.run("grant", "--user", both, "--plan", "pro", "--yes")
	if code != 2 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	if strings.ContainsAny(errOut, "\x1b\r") {
		t.Errorf("a control character reached the terminal: %q", errOut)
	}
	// One line per candidate: the header line plus two.
	if lines := strings.Split(strings.TrimSpace(errOut), "\n"); len(lines) != 3 {
		t.Errorf("%d lines, want 3 (a name must not add lines):\n%q", len(lines), errOut)
	}
	if !strings.Contains(errOut, `\x1b[2J`) {
		t.Errorf("the name is not shown quoted:\n%q", errOut)
	}
	// The same for the result of a grant.
	code, out, errOut := r.run("grant", "--user", both, "--plan", "pro", "--workspace", r.workspace(evil).ID.String(), "--yes")
	if code != 0 || strings.ContainsAny(out, "\x1b\r") || len(strings.Split(strings.TrimSpace(out), "\n")) != 6 {
		t.Fatalf("grant for the evil name = %d\n%q%s", code, out, errOut)
	}
}

// An e-mail address is not unique to one account: nothing is granted to "whoever matched first".
func TestPlanSharedEmailNeedsAClerkUserID(t *testing.T) {
	r := newPlanRig(t)
	ctx := context.Background()
	for _, id := range []string{"user_twin_a", "user_twin_b"} {
		u, err := r.st.UpsertUserByClerkID(ctx, id, "twin@example.com")
		if err != nil {
			t.Fatal(err)
		}
		w, err := r.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_" + id, Name: id + " studio"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.AddMember(ctx, w.ID, u.ID, store.RoleOwner); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := r.run("grant", "--user", "twin@example.com", "--plan", "pro", "--yes")
	if code != 2 || !strings.Contains(errOut, "belongs to 2 users") || !strings.Contains(errOut, "user_twin_a") || !strings.Contains(errOut, "user_twin_b") {
		t.Fatalf("shared e-mail = %d\n%s%s", code, out, errOut)
	}
	for _, org := range []string{"user_twin_a", "user_twin_b"} {
		if e, _ := r.st.GetEntitlements(ctx, r.workspace(org).ID); e.Active {
			t.Fatal("a workspace was granted although the e-mail was ambiguous")
		}
	}
	code, out, errOut = r.run("grant", "--user", "user_twin_b", "--plan", "pro", "--yes")
	if code != 0 || !strings.Contains(out, "user_twin_b studio") {
		t.Fatalf("grant by Clerk user id = %d\n%s%s", code, out, errOut)
	}
}

// A plan the merchant of record manages is not replaced by hand without --force.
func TestPlanRefusesToOverwriteAMerchantPlan(t *testing.T) {
	r := newPlanRig(t)
	ctx := context.Background()
	email := r.person("user_paying", "Paying")
	ws := r.workspace("Paying")
	if _, err := r.st.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, Source: store.SourceMerchant}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"grant", "--user", email, "--plan", "trial", "--yes"},
		{"revoke", "--user", email, "--yes"},
	} {
		code, out, errOut := r.run(args...)
		if code != 2 || !strings.Contains(errOut, "merchant of record") || !strings.Contains(errOut, "--force") {
			t.Fatalf("%v = %d\n%s%s", args, code, out, errOut)
		}
		if e, _ := r.st.GetEntitlements(ctx, ws.ID); !e.Active || e.Plan != plans.Pro || e.Source != store.SourceMerchant {
			t.Fatalf("%v changed the plan: %+v", args, e)
		}
	}
	// show is never blocked, and --force replaces the record, which is manual afterwards.
	if code, out, _ := r.run("show", "--user", email); code != 0 || !strings.Contains(out, "plan        pro") {
		t.Fatalf("show = %d\n%s", code, out)
	}
	code, out, errOut := r.run("revoke", "--user", email, "--yes", "--force")
	if code != 0 || !strings.Contains(out, "plan        none") {
		t.Fatalf("forced revoke = %d\n%s%s", code, out, errOut)
	}
	if e, _ := r.st.GetEntitlements(ctx, ws.ID); e.Source != store.SourceManual {
		t.Fatalf("source after a forced revoke = %q", e.Source)
	}
}

// --until DATE keeps the plan through that whole day: the stored end is the start of the next day, UTC.
func TestPlanUntilDateIsTheStartOfTheNextDay(t *testing.T) {
	got, err := parseUntil("2026-12-31")
	if err != nil || !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseUntil(2026-12-31) = %v, %v", got, err)
	}
	if got, err = parseUntil("2026-02-28"); err != nil || !got.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseUntil(2026-02-28) = %v, %v", got, err)
	}
	if got, err = parseUntil("2026-12-31T18:00:00+02:00"); err != nil || !got.Equal(time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseUntil(RFC 3339) = %v, %v", got, err)
	}
	for _, bad := range []string{"", "tomorrow", "2026-13-01", "31-12-2026"} {
		if _, err := parseUntil(bad); err == nil {
			t.Errorf("parseUntil(%q) succeeded", bad)
		}
	}
}
