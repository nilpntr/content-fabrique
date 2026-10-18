package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func (r *planRig) credits(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "credits-test", MaxConns: 2})
	}}
	code = cmdCredits(append(args[:1:1], append([]string{"--env-file", r.envFile}, args[1:]...)...), &out, &errb, fakeProcessEnv(r.proc), deps)
	return code, out.String(), errb.String()
}

func (r *planRig) balance(name string) store.CreditBalance {
	r.t.Helper()
	b, err := r.st.CreditBalance(context.Background(), r.workspace(name).ID, time.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func TestCreditsGrantAdjustShow(t *testing.T) {
	r := newPlanRig(t)
	r.person("user_ada", "Fit Studio")
	ws := r.workspace("Fit Studio").ID.String()

	// show on a workspace that has nothing.
	code, out, errOut := r.credits("show", "--workspace", ws)
	if code != 0 || !strings.Contains(out, "available   0 credits") || !strings.Contains(out, "ledger (newest 0)") {
		t.Fatalf("show = %d\n%s%s", code, out, errOut)
	}

	// grant: a reason is required, and so is a positive number; nothing is written by a refused command.
	for _, args := range [][]string{
		{"grant", "--workspace", ws, "--credits", "30", "--yes"},
		{"grant", "--workspace", ws, "--reason", "x", "--yes"},
		{"grant", "--workspace", ws, "--credits", "-3", "--reason", "x", "--yes"},
		{"grant", "--workspace", ws, "--credits", "3", "--reason", "x", "--expires", "2020-01-01", "--yes"},
		{"grant", "--workspace", "nope", "--credits", "3", "--reason", "x", "--yes"},
		{"adjust", "--workspace", ws, "--credits", "0", "--reason", "x", "--yes"},
		{"adjust", "--workspace", ws, "--credits", "-3", "--yes"},
		{"adjust", "--workspace", ws, "--credits", "-3", "--reason", "x", "--expires", "2999-01-01", "--yes"},
		{"show", "--workspace", ws, "--limit", "0"},
		{"explode"},
	} {
		if code, _, _ := r.credits(args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
	if b := r.balance("Fit Studio"); b.Available != 0 {
		t.Fatalf("refused commands changed the balance: %+v", b)
	}

	code, out, errOut = r.credits("grant", "--workspace", ws, "--credits", "90", "--reason", "founder testing", "--yes")
	if code != 0 || !strings.Contains(out, "granted     90 credits, expiring ") || !strings.Contains(out, "available   90 credits (3.0 videos)") ||
		!strings.Contains(out, "grant_topup") || !strings.Contains(out, `by "cfctl:sam"`) || !strings.Contains(out, `"founder testing"`) {
		t.Fatalf("grant = %d\n%s%s", code, out, errOut)
	}
	b := r.balance("Fit Studio")
	if b.Available != 90 || len(b.TopUps) != 1 || b.TopUps[0].ExpiresAt.Before(time.Now().AddDate(0, 11, 0)) {
		t.Fatalf("balance = %+v; a top-up lasts 12 months", b)
	}

	// adjust, by the Clerk org id as well.
	code, out, errOut = r.credits("adjust", "--workspace", "org_Fit Studio", "--credits", "-10", "--reason", "refund of a broken render", "--yes")
	if code != 0 || !strings.Contains(out, "adjusted    -10 credits") || !strings.Contains(out, "available   80 credits") || !strings.Contains(out, "adjust") {
		t.Fatalf("adjust = %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = r.credits("adjust", "--workspace", ws, "--credits", "+5", "--reason", "goodwill", "--expires", "2999-01-01", "--yes")
	if code != 0 || !strings.Contains(out, "available   85 credits") {
		t.Fatalf("adjust + = %d\n%s%s", code, out, errOut)
	}
	// Taking away more than there is fails and changes nothing.
	code, out, errOut = r.credits("adjust", "--workspace", ws, "--credits", "-86", "--reason", "too much", "--yes")
	if code != 1 || !strings.Contains(errOut, "Nothing was changed") {
		t.Fatalf("adjust too much = %d\n%s%s", code, out, errOut)
	}
	if b := r.balance("Fit Studio"); b.Available != 85 {
		t.Fatalf("balance = %+v", b)
	}
	// Every ledger row says who and why.
	rows, err := r.st.CreditLedger(context.Background(), r.workspace("Fit Studio").ID, 20)
	if err != nil || len(rows) != 3 {
		t.Fatalf("ledger = %d rows, %v", len(rows), err)
	}
	for _, row := range rows {
		if row.Actor == nil || *row.Actor != "cfctl:sam" || row.Reason == nil || *row.Reason == "" {
			t.Errorf("a ledger row without actor and reason: %+v", row)
		}
	}

	// A plan grant puts the allowance on top, and show lists it.
	if code, out, errOut := r.run("grant", "--user", "user_ada@example.com", "--plan", "creator", "--yes"); code != 0 || !strings.Contains(out, "credits     150 granted") {
		t.Fatalf("plan grant = %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = r.credits("show", "--workspace", ws)
	if code != 0 || !strings.Contains(out, "available   235 credits") || !strings.Contains(out, "allowance   150 of 150 left, expires ") || !strings.Contains(out, "top-up      ") ||
		!strings.Contains(out, "plan        creator (active)") {
		t.Fatalf("show = %d\n%s%s", code, out, errOut)
	}
}

// The guards of grant and adjust are those of cfctl plan: production needs --allow-production and --yes, staging needs --yes; show asks
// for nothing. Nothing the command prints holds the database URL.
func TestCreditsGuards(t *testing.T) {
	r := newPlanRig(t)
	r.person("user_ada", "Fit Studio")
	ws := r.workspace("Fit Studio").ID.String()
	grant := []string{"grant", "--workspace", ws, "--credits", "5", "--reason", "x"}

	r.writeEnv("CF_ENV=production\nDATABASE_URL=" + r.dsn + "\n")
	if code, _, errOut := r.credits(append(grant, "--yes")...); code != 2 || !strings.Contains(errOut, "--allow-production") {
		t.Fatalf("production with --yes only = %d %s", code, errOut)
	}
	if code, _, errOut := r.credits(append(grant, "--allow-production")...); code != 2 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("production with --allow-production only = %d %s", code, errOut)
	}
	r.writeEnv("CF_ENV=staging\nDATABASE_URL=" + r.dsn + "\n")
	if code, _, errOut := r.credits(grant...); code != 2 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("staging without --yes = %d %s", code, errOut)
	}
	if b := r.balance("Fit Studio"); b.Available != 0 {
		t.Fatalf("a refused grant moved credits: %+v", b)
	}
	// show reads: no flag needed anywhere, and it never prints the URL (which has the password in it).
	code, out, errOut := r.credits("show", "--workspace", ws)
	if code != 0 {
		t.Fatalf("show on staging = %d %s", code, errOut)
	}
	if strings.Contains(out+errOut, r.dsn) || strings.Contains(out+errOut, "postgres://") {
		t.Fatalf("the database URL was printed:\n%s%s", out, errOut)
	}
	// With the flags, production works.
	r.writeEnv("CF_ENV=production\nDATABASE_URL=" + r.dsn + "\n")
	if code, out, errOut := r.credits(append(grant, "--allow-production", "--yes")...); code != 0 {
		t.Fatalf("production with both = %d\n%s%s", code, out, errOut)
	}
	if b := r.balance("Fit Studio"); b.Available != 5 {
		t.Fatalf("balance = %+v", b)
	}
}

// A reason or an actor is text a person typed: it is shown quoted, so it cannot forge lines or move the cursor.
func TestCreditsShowQuotesFreeText(t *testing.T) {
	r := newPlanRig(t)
	r.person("user_ada", "Fit Studio")
	ws := r.workspace("Fit Studio").ID.String()
	evil := "ok\x1b[2J\r\nwks_01fake  fake 999 credits"
	if code, _, errOut := r.credits("grant", "--workspace", ws, "--credits", "5", "--reason", evil, "--yes"); code != 0 {
		t.Fatalf("grant = %d %s", code, errOut)
	}
	code, out, _ := r.credits("show", "--workspace", ws)
	if code != 0 || strings.ContainsAny(out, "\x1b\r") {
		t.Fatalf("a control character reached the terminal: %q", out)
	}
	if !strings.Contains(out, `\x1b[2J`) {
		t.Fatalf("the reason is not shown quoted:\n%q", out)
	}
	// The ledger section has one line for the one row.
	section := out[strings.Index(out, "ledger (newest"):]
	if lines := strings.Split(strings.TrimSpace(section), "\n"); len(lines) != 2 {
		t.Fatalf("%d lines in the ledger section, want the heading and one row:\n%q", len(lines), section)
	}
}

func TestCreditsUnknownWorkspace(t *testing.T) {
	r := newPlanRig(t)
	code, _, errOut := r.credits("show", "--workspace", "wks_01m55grwj8fdfvv0xj8rpan4tw")
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("unknown workspace = %d %s", code, errOut)
	}
	_ = plans.Creator
}

// --no-credits is for local and ci only. The command as a whole is refused on staging and production; an environment nobody named is
// refused for this flag as well.
func TestAgentRunNoCreditsIsLocalAndCiOnly(t *testing.T) {
	srv := newPlanRig(t)
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "agent-test", MaxConns: 2})
	}}
	ws := srv.workspaceNamed("x").ID.String()
	prj := "prj_01m55grwj8fdfvv0xj8rpan4tw"
	for env, want := range map[string]string{
		"CF_ENV=staging\n":    "development only",
		"CF_ENV=production\n": "development only",
		"":                    "--no-credits is for CF_ENV=local or ci only",
		"CF_ENV=preview\n":    "--no-credits is for CF_ENV=local or ci only",
	} {
		path := pingEnv(t, env+"LLM_MODE=replay\nDATABASE_URL="+srv.dsn+"\n")
		code, _, errOut := runAgent(t, deps, "run", "--agent", "editor", "--workspace", ws, "--project", prj, "--no-credits", "--yes", "--env-file", path)
		if code != 2 || !strings.Contains(errOut, want) {
			t.Errorf("%q: exit %d, %s", env, code, errOut)
		}
	}
	// In local and ci the flag gets past its own guard (the run then stops at what the editor needs: object storage).
	for _, env := range []string{"CF_ENV=local\n", "CF_ENV=ci\n"} {
		path := pingEnv(t, env+"LLM_MODE=replay\nDATABASE_URL="+srv.dsn+"\n")
		_, _, errOut := runAgent(t, deps, "run", "--agent", "editor", "--workspace", ws, "--project", prj, "--no-credits", "--yes", "--env-file", path)
		if strings.Contains(errOut, "--no-credits") {
			t.Errorf("%q: the flag was refused: %s", env, errOut)
		}
	}
}

func (r *planRig) workspaceNamed(name string) store.Workspace {
	r.t.Helper()
	w, err := r.st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_" + name, Name: name})
	if err != nil {
		r.t.Fatal(err)
	}
	return w
}
