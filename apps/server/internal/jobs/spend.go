package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"
)

// The sweep of expired spend holds (P7-09a, docs/design/spend-control-v1.md section 6).
//
// A paid call reserves its worst case in spend_holds before it is sent and settles it afterwards. A caller that dies in between leaves
// a hold. It stops counting against the caps the moment it expires (every cap check ignores holds past their expiry), so a dead
// worker never blocks budget for longer than the call could have run; this job only deletes the rows, to keep the table small and to
// log what crashed callers left behind.
//
// The expiry of a hold is chosen by its caller from how long the call can take: a model call's request timeout plus the retry wait and
// a margin (capped at the run's wall clock), a hosted transcription's chunks times the client timeout plus a margin, a local one's tool
// timeout plus a margin.

// SpendHoldsSweepInterval is how often the sweep runs.
const SpendHoldsSweepInterval = time.Minute

const spendHoldsSweepID = "spend_holds_sweep"

// SpendHoldsSweepArgs is the periodic job that deletes expired spend holds.
type SpendHoldsSweepArgs struct{}

// Kind implements river.JobArgs.
func (SpendHoldsSweepArgs) Kind() string { return "spend_holds_sweep" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (SpendHoldsSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: SpendHoldsSweepInterval / 2}}
}

// SpendHoldsSweepWorker works SpendHoldsSweepArgs.
type SpendHoldsSweepWorker struct {
	river.WorkerDefaults[SpendHoldsSweepArgs]
	deps Deps
}

// SpendHoldsSweepOutput is what a finished run records.
type SpendHoldsSweepOutput struct {
	Holds   int64 `json:"holds"`
	Micros  int64 `json:"micros"`
	AudioMs int64 `json:"audio_ms"`
}

// Work deletes the holds past their expiry. Safe to run twice and from several workers.
func (w *SpendHoldsSweepWorker) Work(ctx context.Context, _ *river.Job[SpendHoldsSweepArgs]) error {
	res, err := w.deps.Store.SweepExpiredSpendHolds(ctx, w.deps.now())
	if err != nil {
		return fmt.Errorf("sweep spend holds: %w", err)
	}
	if res.Holds > 0 {
		// A hold that expired was not settled or released: its caller crashed, or could not write its ledger row.
		w.deps.logger().WarnContext(ctx, "swept expired spend holds", "holds", res.Holds, "micros", res.Micros, "audio_ms", res.AudioMs)
	}
	return river.RecordOutput(ctx, SpendHoldsSweepOutput{Holds: res.Holds, Micros: res.Micros, AudioMs: res.AudioMs})
}
