package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// usageSuite is part of repositorySuite (superuser, then the app role), so the ledger queries are proven to work with
// DML rights only.
func usageSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) store.Workspace {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_usage_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	wa, wb := newWorkspace("a"), newWorkspace("b")
	run := uuid.New()
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	entry := func(w store.Workspace, cost int64, at time.Time, reqID string) store.UsageEntry {
		return store.UsageEntry{
			WorkspaceID: w.ID, RunID: run, Stage: "editor", Model: "claude-haiku-5-5", InputTokens: 100,
			CacheWriteTokens: 50, CacheWrite1hTokens: 20, CacheReadTokens: 10, OutputTokens: 5, ListCostMicros: cost,
			PriceTableVersion: "2026-10", BilledTo: store.BilledToTest, RequestID: reqID, CreatedAt: at,
		}
	}

	t.Run("insert, read back and sum per workspace and window", func(t *testing.T) {
		for _, e := range []store.UsageEntry{
			entry(wa, 1000, base, "req_"+suffix+"_1"),
			entry(wa, 2000, base.Add(time.Hour), "req_"+suffix+"_2"),
			entry(wa, 4000, base.Add(24*time.Hour), "req_"+suffix+"_3"), // the next day
			entry(wb, 8000, base, "req_"+suffix+"_4"),
		} {
			if err := st.InsertUsage(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		day := base.Truncate(24 * time.Hour)
		got, err := st.UsageCostMicros(ctx, wa.ID, day, day.Add(24*time.Hour))
		if err != nil || got != 3000 {
			t.Fatalf("day sum = %d, %v; want 3000", got, err)
		}
		if got, err = st.UsageCostMicros(ctx, wb.ID, day, day.Add(24*time.Hour)); err != nil || got != 8000 {
			t.Fatalf("other workspace sum = %d, %v; want 8000", got, err)
		}
		rows, err := st.UsageForRun(ctx, wa.ID, run)
		if err != nil || len(rows) != 3 {
			t.Fatalf("run rows = %d, %v", len(rows), err)
		}
		r := rows[0]
		if r.Kind != "llm" || r.Stage != "editor" || r.CacheWrite1hTokens != 20 || r.BilledTo != "test" || r.RunID != run || r.PriceTableVersion != "2026-10" {
			t.Fatalf("row = %+v", r)
		}
		// Another workspace never sees the rows.
		if rows, err = st.UsageForRun(ctx, wb.ID, run); err != nil || len(rows) != 1 {
			t.Fatalf("workspace b rows = %d, %v", len(rows), err)
		}
		recent, err := st.RecentUsage(ctx, wa.ID, 2)
		if err != nil || len(recent) != 2 || recent[0].ID.String() < recent[1].ID.String() {
			t.Fatalf("recent = %+v, %v", recent, err)
		}
	})

	t.Run("a repeated request id counts once", func(t *testing.T) {
		w := newWorkspace("dup")
		e := entry(w, 500, base, "req_"+suffix+"_dup")
		for range 2 {
			if err := st.InsertUsage(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		day := base.Truncate(24 * time.Hour)
		if got, _ := st.UsageCostMicros(ctx, w.ID, day, day.Add(24*time.Hour)); got != 500 {
			t.Fatalf("sum = %d, want 500", got)
		}
		// Rows without a request id are all kept.
		e.RequestID = ""
		for range 2 {
			if err := st.InsertUsage(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := st.UsageCostMicros(ctx, w.ID, day, day.Add(24*time.Hour)); got != 1500 {
			t.Fatalf("sum = %d, want 1500", got)
		}
	})

	t.Run("an estimated row is marked and counted", func(t *testing.T) {
		w := newWorkspace("estimated")
		e := entry(w, 9000, base, "")
		e.Estimated = true
		if err := st.InsertUsage(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertUsage(ctx, entry(w, 1000, base, "")); err != nil {
			t.Fatal(err)
		}
		rows, err := st.UsageForRun(ctx, w.ID, run)
		if err != nil || len(rows) != 2 || !rows[0].Estimated || rows[1].Estimated {
			t.Fatalf("rows = %+v, %v", rows, err)
		}
		day := base.Truncate(24 * time.Hour)
		if got, _ := st.UsageCostMicros(ctx, w.ID, day, day.Add(24*time.Hour)); got != 10_000 {
			t.Fatalf("sum = %d: an estimated row counts toward the cap", got)
		}
	})

	t.Run("a call outside a run has no run id", func(t *testing.T) {
		w := newWorkspace("norun")
		e := entry(w, 1, base, "")
		e.RunID = uuid.Nil
		if err := st.InsertUsage(ctx, e); err != nil {
			t.Fatal(err)
		}
		rows, err := st.RecentUsage(ctx, w.ID, 5)
		if err != nil || len(rows) != 1 || rows[0].RunID != uuid.Nil {
			t.Fatalf("rows = %+v, %v", rows, err)
		}
	})

	t.Run("constraints", func(t *testing.T) {
		bad := entry(wa, 1, base, "")
		bad.BilledTo = "somebody"
		if err := st.InsertUsage(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("billed_to somebody: %v", err)
		}
		bad = entry(wa, -1, base, "")
		if err := st.InsertUsage(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("negative cost: %v", err)
		}
		bad = entry(wa, 1, base, "")
		bad.CacheWrite1hTokens = bad.CacheWriteTokens + 1
		if err := st.InsertUsage(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("1h writes above total writes: %v", err)
		}
		if _, err := st.RecentUsage(ctx, wa.ID, 0); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("limit 0: %v", err)
		}
	})
}
