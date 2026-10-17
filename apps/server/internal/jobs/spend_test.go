package jobs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The sweep deletes the holds past their expiry and leaves the live ones: a crashed caller's reservation goes, a running call's stays.
func TestSpendHoldsSweepDeletesExpiredHolds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w := e.newWorkspace(t, "org_spend_sweep")
	caps := store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 1 << 40}
	hold := func(key string, started time.Time) {
		t.Helper()
		if _, _, err := e.api.ReserveSpend(ctx, store.ReserveSpendParams{
			Key: key, Workspace: w.ID, BilledTo: store.BilledToPlatform, Kind: store.KindLLM, Micros: 1_000, Caps: caps, Now: started, TTL: 10 * time.Minute,
		}); err != nil {
			t.Fatal(err)
		}
	}
	hold("crashed", time.Now().Add(-time.Hour)) // a worker died an hour ago; the hold expired 50 minutes ago
	hold("running", time.Now())
	count := func() (n int) {
		if err := e.super.QueryRow(ctx, `SELECT count(*) FROM spend_holds`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("holds = %d", count())
	}
	e.startWorker(t, jobs.WorkerOptions{})
	if err := e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.SpendHoldsSweepArgs{}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the sweep to delete the expired hold", func() bool { return count() == 1 })
	var key string
	if err := e.super.QueryRow(ctx, `SELECT idempotency_key FROM spend_holds`).Scan(&key); err != nil || key != "running" {
		t.Fatalf("left: %q, %v", key, err)
	}
}

// fakeIngest takes a moment and succeeds.
type fakeIngest struct{}

func (fakeIngest) Run(context.Context, ids.WorkspaceID, ids.AssetID) (ingest.Result, error) {
	time.Sleep(150 * time.Millisecond)
	return ingest.Result{}, nil
}

func (fakeIngest) Fail(context.Context, ids.WorkspaceID, ids.AssetID, string) error { return nil }

// An ingest's compute is metered: one ledger row of kind media with the job's wall time on the stage's vCPUs, attributed to the member
// who uploaded the asset and keyed by the job attempt.
func TestMediaJobsMeterTheirCompute(t *testing.T) {
	e := newAssetsEnv(t)
	w := e.newWorkspace(t, "org_media_meter")
	a := e.newUploadedAsset(t, w, store.AssetKindVideo, "clip.mp4", []byte("data"))
	mem := &spendtest.Memory{}
	meter := &spend.ComputeMeter{
		Gate: spend.NewGate(mem, spend.Options{Caps: store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: 1 << 40}}),
		Cost: func(ms int64) (int64, error) { return (ms*20 + 999) / 1000, nil }, PriceVersion: "test-prices", BilledTo: store.BilledToTest,
		VCPUs: map[string]float64{"ingest": 2},
	}
	e.startWorker(t, jobs.WorkerOptions{Objects: e.objs, Ingest: fakeIngest{}, Meter: meter})
	e.enqueueIngest(t, a, nil)
	eventually(t, "the ingest job to complete", e.jobIs(t, a, "completed"))
	rows := mem.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Kind != store.KindMedia || r.Stage != "ingest" || r.Unit != store.UnitVCPUMs || r.Quantity < 300 || r.Quantity > 5_000 || r.ListCostMicros != (r.Quantity*20+999)/1000 ||
		r.PriceTableVersion != "test-prices" || r.MemberUserID != e.user || r.ActionID != a.ID.UUID || r.WorkspaceID != w.ID ||
		!strings.HasPrefix(r.IdempotencyKey, "media:ingest:") || !strings.HasSuffix(r.IdempotencyKey, ":1") {
		t.Errorf("row = %+v", r)
	}
}
