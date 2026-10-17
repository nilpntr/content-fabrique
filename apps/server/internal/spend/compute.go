package spend

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ComputeMeter writes the ledger rows of our own compute (P7-09a): the CPU time of renders (kind render) and of ingest, quick look
// and analysis (kind media), priced from the price table's compute section in vCPU-milliseconds.
//
// A cost that is already incurred is recorded, never refused: the rows go to the counters (so the platform breaker sees them) without a
// hold. Metering must not fail the work that was done, so a row that cannot be written is retried and then logged in full for
// reconciliation, like the model client's.
//
// What is measured. Renders carry the CPU time the pipeline allotted (the wall time of its sandbox runs times the sandbox's CPU limit,
// stored in renders.cpu_ms). The other jobs have no CPU measurement: their cost is the job's wall time times the vCPUs of their
// sandbox limit (VCPUs), an upper bound that also counts the time spent waiting on storage and the model API. Both are estimates of
// what the capacity costs, priced at the table's estimate of the container price; neither is an invoice line.
type ComputeMeter struct {
	Gate *Gate
	// Cost prices vcpuMs milliseconds of vCPU time in micro-dollars; PriceVersion names the price table.
	Cost         func(vcpuMs int64) (int64, error)
	PriceVersion string
	// BilledTo is platform in staging and production, test elsewhere.
	BilledTo string
	// VCPUs is the vCPU count of each wall-time-metered stage ("ingest", "quick_look", "analysis").
	VCPUs map[string]float64
	Now   func() time.Time
	Log   *slog.Logger
}

// ComputeUse is one metered stretch of compute.
type ComputeUse struct {
	Workspace ids.WorkspaceID
	// Kind is store.KindRender or store.KindMedia; Stage names the work ("render", "ingest", "quick_look", "analysis").
	Kind  string
	Stage string
	// VCPUMs is the vCPU time in milliseconds; for a wall-time stage use VCPUMsOfWall.
	VCPUMs int64
	// Key is the row's idempotency key: the same work recorded twice is one row ("render:<render id>", "media:ingest:<job>:<attempt>").
	Key string
	// Project, Member and Action attribute the row; all optional.
	Project ids.ProjectID
	Member  ids.UserID
	Action  uuid.UUID
}

// VCPUMsOfWall is the vCPU time of wall milliseconds on the stage's vCPUs; 0 for a stage with none configured.
func (m *ComputeMeter) VCPUMsOfWall(stage string, wall time.Duration) int64 {
	if m == nil {
		return 0
	}
	return int64(float64(wall.Milliseconds())*m.VCPUs[stage] + 0.5)
}

// Record writes the row. It never returns an error: the work is done. A nil meter records nothing (a process without a spend gate).
func (m *ComputeMeter) Record(ctx context.Context, u ComputeUse) {
	if m == nil || m.Gate == nil || u.VCPUMs <= 0 {
		return
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	cost, version := int64(0), "unpriced"
	if m.Cost != nil {
		var err error
		if cost, err = m.Cost(u.VCPUMs); err != nil {
			// The table has no compute price: the cost is not known. The row is still written, at 0 and marked, so the quantity is not
			// lost and the unpriced version shows up in the reports.
			log.ErrorContext(ctx, "spend: compute is unpriced; recording its quantity at cost 0", "error", err, "kind", u.Kind, "stage", u.Stage)
			cost = 0
		} else {
			version = m.PriceVersion
		}
	}
	row := store.UsageEntry{
		WorkspaceID: u.Workspace, Stage: u.Stage, Kind: u.Kind, Model: u.Stage + "-vcpu", Quantity: u.VCPUMs, Unit: store.UnitVCPUMs,
		ListCostMicros: cost, PriceTableVersion: version, BilledTo: m.BilledTo, IdempotencyKey: u.Key,
		ProjectID: u.Project, MemberUserID: u.Member, ActionID: u.Action, CreatedAt: now().UTC(),
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = m.Gate.Record(rctx, u.Workspace, row); err == nil {
			return
		}
		select {
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		case <-rctx.Done():
		}
	}
	log.ErrorContext(ctx, "spend: usage ledger write failed; reconcile this row by hand", "error", err, "workspace_id", u.Workspace.String(),
		"kind", u.Kind, "stage", u.Stage, "vcpu_ms", u.VCPUMs, "list_cost_micros", cost, "price_table_version", version,
		"billed_to", m.BilledTo, "idempotency_key", u.Key)
}
