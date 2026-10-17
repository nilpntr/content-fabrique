package llm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

// The factory over the real ledger: the row lands in Postgres, and the daily cap reads the sum back from SQL.
func TestFactoryWithTheRealLedger(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	mig, err := store.NewMigrator(srv.SuperDSN(database))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mig.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = mig.Close()
	st, err := store.Open(context.Background(), srv.SuperDSN(database), store.Options{AppName: "llm-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	ctx := context.Background()
	w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_llm_" + uuid.NewString(), Name: "LLM"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_llm_" + uuid.NewString(), Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	// Haiku, 1000 in / 200 out = 200 micro-dollars a call; the worst case of simpleRequest is 600.
	rg := newRig(t, map[string]string{"DAILY_SPEND_CAP_MICROS": "1000"}, func(o *FactoryOptions) { o.Ledger = st })
	run := uuid.New()
	c, _ := rg.client(t, w.ID, "editor")
	req := simpleRequest()
	req.Run = RunInfo{ID: run}

	if _, err := c.Messages(ctx, req); err != nil { // spent 0 + worst case 600 <= 1000
		t.Fatal(err)
	}
	rows, err := st.UsageForRun(ctx, w.ID, run)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	r := rows[0]
	if r.Stage != "editor" || r.Model != "claude-haiku-5-5" || r.InputTokens != 1000 || r.OutputTokens != 200 || r.ListCostMicros != 200 ||
		r.PriceTableVersion != "2026-10" || r.BilledTo != "test" || r.RequestID != "req_fake_2" || r.Kind != "llm" {
		t.Fatalf("row = %+v", r)
	}

	if _, err := c.Messages(ctx, req); err != nil { // spent 200 + 600 <= 1000
		t.Fatal(err)
	}
	if _, err := c.Messages(ctx, req); err != nil { // spent 400 + 600 <= 1000
		t.Fatal(err)
	}
	// Spent 600; 600 + 600 > 1000: refused before sending.
	sent := len(rg.api.messageCalls())
	_, err = c.Messages(ctx, req)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Kind != CapDaily || ce.LimitMicros != 400 {
		t.Fatalf("err = %#v", err)
	}
	if len(rg.api.messageCalls()) != sent {
		t.Error("the refused call was sent")
	}
	// The cap is per workspace.
	oc, _ := rg.client(t, other.ID, "editor")
	if _, err := oc.Messages(ctx, req); err != nil {
		t.Fatalf("other workspace: %v", err)
	}
}
