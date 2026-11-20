package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/evals"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// The cost guard's CLI side, in replay mode (LLM_MODE=replay, ROUTING_PROFILE=dev-cheap): cfctl spend summary reads the seeded ledger,
// and cfctl eval suite refuses a run whose worst case is more than the test cap has left, unless --over-test-cap is given. No model is
// called: the refusal comes before the first call.
func TestSpendSummaryAndTheTestCapRefusal(t *testing.T) {
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
	ctx := context.Background()
	st, err := store.Open(ctx, dsn, store.Options{AppName: "spend-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_spend", Name: "Spend"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_spend_2", Name: "Spend 2"})
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	day2 := day1.Add(48 * time.Hour)
	for _, e := range []store.UsageEntry{
		{WorkspaceID: w.ID, Stage: "editor", Model: "claude-opus-5-5", ListCostMicros: 60_000_000, BilledTo: store.BilledToTest, CreatedAt: day1},
		{WorkspaceID: w.ID, Stage: "judge_edit", Model: "claude-sonnet-5-5", ListCostMicros: 4_950_000, BilledTo: store.BilledToTest, CreatedAt: day2},
		{WorkspaceID: other.ID, Stage: "editor", Model: "claude-haiku-5-5", ListCostMicros: 10_000_000, BilledTo: store.BilledToTest, CreatedAt: day2},
		// Not test spend: it never counts against the test cap.
		{WorkspaceID: w.ID, Stage: "editor", Model: "claude-opus-5-5", ListCostMicros: 900_000_000, BilledTo: store.BilledToPlatform, CreatedAt: day2},
	} {
		e.PriceTableVersion, e.InputTokens, e.OutputTokens = "test", 1000, 100
		if err := st.InsertUsage(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "spend-test", MaxConns: 2})
	}}
	env := pingEnv(t, "CF_ENV=local\nROUTING_PROFILE=dev-cheap\nLLM_MODE=replay\nDATABASE_URL="+dsn+"\nTEST_SPEND_CAP_MICROS=75000000\n")
	spendCmd := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := cmdSpendAt(append(args, "--env-file", env), &o, &e, fakeProcessEnv(nil), deps, time.Now)
		return code, o.String(), e.String()
	}

	// The summary: $74.95 of $75, by day, by run type and by model; the platform row is not in it.
	code, out, errOut := spendCmd("summary")
	if code != 0 {
		t.Fatalf("summary: exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"$74.95 of the $75.00 test cap (100 %), $0.05 left", "rows      3 from 2026-11-18 to 2026-11-20",
		"by day", "2026-11-20", "$14.95", "2026-11-18", "$60.00", "by run type", "editor", "$70.00", "judge_edit", "$4.95",
		"by model", "claude-opus-5-5", "claude-haiku-5-5", "claude-sonnet-5-5"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$900") || strings.Contains(out, "$974") {
		t.Errorf("the summary counts platform spend:\n%s", out)
	}
	if days := out[strings.Index(out, "by day"):]; strings.Index(days, "2026-11-20") > strings.Index(days, "2026-11-18") {
		t.Errorf("the days are not newest first:\n%s", out)
	}
	code, out, _ = spendCmd("summary", "--json", "--days", "1")
	var js struct {
		Total int64 `json:"total_micros"`
		Left  int64 `json:"left_micros"`
		ByDay []struct {
			Key string `json:"key"`
		} `json:"by_day"`
	}
	jerr := json.Unmarshal([]byte(out), &js)
	if code != 0 || jerr != nil || js.Total != 74_950_000 || js.Left != 50_000 || len(js.ByDay) != 1 || js.ByDay[0].Key != "2026-11-20" {
		t.Fatalf("--json --days 1: exit %d, %v, %+v\n%q", code, jerr, js, out)
	}
	if code, out, _ := spendCmd("summary", "--cap-usd", "50"); code != 0 || !strings.Contains(out, "OVER THE CAP by $24.95") {
		t.Errorf("--cap-usd 50: exit %d\n%s", code, out)
	}
	if code, _, errOut := spendCmd("nope"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command: %d %s", code, errOut)
	}

	// A suite with one stored case: its judge call may cost up to $0.10, more than the $0.05 the cap has left.
	dir := t.TempDir()
	suite := filepath.Join(dir, "cap.yaml")
	prj := ids.Encode(ids.PrefixProject, uuid.Must(uuid.NewV7()))
	body := "suite: cap\ndefaults:\n  workspace: " + w.ID.String() + "\n  budget_usd: 1.00\ncases:\n  - id: stored-one\n    stored: {project: " + prj + ", version: 1}\n"
	if err := os.WriteFile(suite, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	evalCmd := func(extra ...string) (int, string, string) {
		var o, e bytes.Buffer
		args := append([]string{"--suite", suite, "--out", filepath.Join(dir, "results"), "--no-phoenix", "--max-usd", "1", "--env-file", env}, extra...)
		code := cmdEvalSuite(args, &o, &e, fakeProcessEnv(nil), deps)
		return code, o.String(), e.String()
	}
	code, out, errOut = evalCmd()
	if code != 2 || !strings.Contains(out, "this run may cost up to $0.10; test spend so far $74.95 of the $75.00 test cap, $0.05 left") ||
		!strings.Contains(errOut, "refused: this run may cost up to $0.10 and the test cap has $0.05 left") {
		t.Fatalf("over the cap: exit %d\n%s%s", code, out, errOut)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "results", "cap")); len(entries) != 0 {
		t.Fatalf("a refused run wrote results: %v", entries)
	}
	// The override runs it (the stored project is not there, so the case itself fails; the guard let it through).
	_, out, errOut = evalCmd("--over-test-cap")
	if strings.Contains(errOut, "refused") || !strings.Contains(out, "--over-test-cap: running although it may pass the test cap by $0.05") {
		t.Fatalf("--over-test-cap: \n%s%s", out, errOut)
	}
	// Without the judge nothing can be spent, so nothing is refused.
	if _, out, errOut := evalCmd("--no-judge"); strings.Contains(errOut, "refused") || strings.Contains(out, "may cost up to") {
		t.Errorf("--no-judge:\n%s%s", out, errOut)
	}
}

func TestTestCapMicros(t *testing.T) {
	if n, err := testCapMicros(map[string]string{}); err != nil || n != DefaultTestSpendCapMicros {
		t.Errorf("default: %d %v", n, err)
	}
	for _, v := range []string{"0", "-1", "x"} {
		if _, err := testCapMicros(map[string]string{"TEST_SPEND_CAP_MICROS": v}); err == nil {
			t.Errorf("%q was accepted", v)
		}
	}
}

func TestEstimateUpTo(t *testing.T) {
	e := evals.Estimate{Micros: 300_000, WorstMicros: 2_400_000}
	if e.UpTo(1_000_000) != 1_000_000 || e.UpTo(5_000_000) != 2_400_000 || e.UpTo(0) != 0 {
		t.Errorf("UpTo: %d %d %d", e.UpTo(1_000_000), e.UpTo(5_000_000), e.UpTo(0))
	}
}
