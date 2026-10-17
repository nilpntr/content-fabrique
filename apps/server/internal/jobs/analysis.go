package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The analyze job (P2-02, P2-03, P2-09): shots, motion, exposure and reps of a clip, from one ffmpeg pass over its proxy. The
// worker itself lives in internal/analysis (it needs the sandbox runner); its arguments, queue and options are here with the
// others.

// QueueAnalysis holds the analyses: a pass over the proxy is seconds of CPU for a minute of footage, on a queue of its own so a
// burst of uploads neither waits for the long ingests nor delays them.
const QueueAnalysis = "analysis"

// DefaultAnalysisConcurrency is how many analyses one worker runs at once.
const DefaultAnalysisConcurrency = 2

// AnalysisTimeout is how long one attempt may take: the proxy download and one ffmpeg pass (analysis.timeoutFor allows a
// 30 minute clip 17 minutes).
const AnalysisTimeout = 30 * time.Minute

// AnalyzeArgs is enqueued, in the transaction that marks a video ready, for that asset.
type AnalyzeArgs struct {
	WorkspaceID ids.WorkspaceID `json:"workspace_id"`
	AssetID     ids.AssetID     `json:"asset_id"`
	// Generation is the ingest the job was enqueued for (assets.ingested_at in microseconds). It is part of the job's unique key, so
	// an asset that is ingested again gets a new analysis of its new proxy instead of being deduplicated against the old job.
	Generation int64 `json:"generation"`
}

// Kind implements river.JobArgs.
func (AnalyzeArgs) Kind() string { return "analyze" }

// InsertOpts puts the job on its own queue and makes it unique by its arguments, which include the ingest generation: a double
// enqueue for one ingest runs once, and a re-ingest (a new proxy, a new generation) is never deduplicated away.
func (AnalyzeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueAnalysis, MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// generationOf is the ingest generation of an asset: the time its ingest finished, in microseconds (0 when it has none).
func generationOf(a store.Asset) int64 {
	if a.IngestedAt == nil {
		return 0
	}
	return a.IngestedAt.UnixMicro()
}

// EnqueueAnalysis is the store's analysis hook (Store.SetAnalysisHook): it inserts the job in the transaction that marks the
// video ready, so the job exists exactly when the ready asset does.
func (e *Enqueuer) EnqueueAnalysis(ctx context.Context, tx *store.Store, a store.Asset) error {
	if _, err := e.InsertInStore(ctx, tx, AnalyzeArgs{WorkspaceID: a.WorkspaceID, AssetID: a.ID, Generation: generationOf(a)}, nil); err != nil {
		return fmt.Errorf("enqueue analyze: %w", err)
	}
	return nil
}

// ErrAnalysisNotReady: the asset is being ingested again. The job looks again in a minute.
var ErrAnalysisNotReady = errors.New("jobs: the asset is not ready")

// Analyzer is the analysis (package analysis) as the worker sees it.
type Analyzer interface {
	// RunJob does the analysis. gone says the asset is not a ready video of the workspace any more (the job is cancelled);
	// output is what the job records. ErrAnalysisNotReady snoozes the job; other errors are retried.
	RunJob(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (output any, gone bool, err error)
}

// noAnalysisSnooze is how long an analysis waits when the worker cannot run it (no object storage or sandbox).
const noAnalysisSnooze = 5 * time.Minute

// AnalysisMaxAge is how long an analyze job may wait, snoozed, for a worker that can run it: after this long it is cancelled
// (`cfctl assets analyze` runs it again once the cause is fixed).
const AnalysisMaxAge = 24 * time.Hour

// AnalyzeWorker works AnalyzeArgs. It is always registered, so the ingest can enqueue the job on a worker that cannot run it
// (River refuses to insert a kind its bundle does not know): such a worker snoozes the job, and one that can picks it up.
type AnalyzeWorker struct {
	river.WorkerDefaults[AnalyzeArgs]
	deps Deps
}

// Timeout implements river.Worker.
func (w *AnalyzeWorker) Timeout(*river.Job[AnalyzeArgs]) time.Duration { return AnalysisTimeout }

// Work runs one attempt. An asset that is gone ends the job; a worker without a sandbox or an asset that is being ingested
// again snoozes it (no attempt is used) for at most AnalysisMaxAge; any other error is retried by River.
func (w *AnalyzeWorker) Work(ctx context.Context, job *river.Job[AnalyzeArgs]) error {
	ws, id := job.Args.WorkspaceID, job.Args.AssetID
	if w.deps.Analysis == nil {
		return w.snooze(ctx, job, noAnalysisSnooze, "this worker cannot run analyses (no object storage or sandbox)")
	}
	out, gone, err := w.deps.Analysis.RunJob(ctx, ws, id)
	switch {
	case errors.Is(err, ErrAnalysisNotReady):
		return w.snooze(ctx, job, notReadySnooze, err.Error())
	case err != nil:
		w.deps.logger().WarnContext(ctx, "analysis attempt failed", "workspace_id", ws.String(), "asset_id", id.String(), "job_id", job.ID, "attempt", job.Attempt, "error", err)
		return err
	case gone:
		return river.JobCancel(fmt.Errorf("asset %s is not a ready video of workspace %s", id, ws))
	}
	return river.RecordOutput(ctx, out)
}

func (w *AnalyzeWorker) snooze(ctx context.Context, job *river.Job[AnalyzeArgs], d time.Duration, why string) error {
	age := w.deps.now().Sub(job.CreatedAt)
	if age >= AnalysisMaxAge {
		w.deps.logger().ErrorContext(ctx, "analysis gives up after waiting", "workspace_id", job.Args.WorkspaceID.String(), "asset_id", job.Args.AssetID.String(), "job_id", job.ID, "age", age.Round(time.Minute), "detail", why)
		return river.JobCancel(fmt.Errorf("analysis gave up after %s: %s", age.Round(time.Minute), why))
	}
	w.deps.logger().WarnContext(ctx, "analysis waits", "workspace_id", job.Args.WorkspaceID.String(), "asset_id", job.Args.AssetID.String(), "job_id", job.ID, "retry_in", d, "age", age.Round(time.Minute), "detail", why)
	return river.JobSnooze(d)
}
