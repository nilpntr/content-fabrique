package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/research"
)

// The retention sweep of the creator research engine (docs/05-decisions.md D-41). Third-party data is kept for as long as the setting
// says and not a day longer: every observation carries its own retain_until (12 months, 30 days for YouTube, set when it was written), and
// this daily job deletes what is past it, with the findings, posts and idle candidates that rested on it (store.SweepResearch).
const (
	researchSweepID = "research_sweep"
	// The sweep runs at 03:40 UTC, after the webhook prune.
	researchSweepHour, researchSweepMinute = 3, 40
)

// ResearchSweepArgs is the daily periodic job that deletes expired research evidence.
type ResearchSweepArgs struct{}

// Kind implements river.JobArgs.
func (ResearchSweepArgs) Kind() string { return researchSweepID }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (ResearchSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: 12 * time.Hour}}
}

// ResearchSweepWorker works ResearchSweepArgs.
type ResearchSweepWorker struct {
	river.WorkerDefaults[ResearchSweepArgs]
	deps Deps
}

// ResearchSweepOutput is what a finished run records.
type ResearchSweepOutput struct {
	Evidence int64 `json:"evidence"`
	Findings int64 `json:"findings"`
	Posts    int64 `json:"posts"`
	Creators int64 `json:"creators"`
}

// Work deletes the research data past its retention. Safe to run twice and from several workers.
func (w *ResearchSweepWorker) Work(ctx context.Context, _ *river.Job[ResearchSweepArgs]) error {
	ret := w.deps.Research
	if ret == (research.Retention{}) {
		ret = research.DefaultRetention()
	}
	res, err := w.deps.Store.SweepResearch(ctx, w.deps.now(), ret.Default)
	if err != nil {
		return fmt.Errorf("sweep research: %w", err)
	}
	w.deps.logger().InfoContext(ctx, "swept research data", "evidence", res.Evidence, "findings", res.Findings, "posts", res.Posts, "creators", res.Creators)
	return river.RecordOutput(ctx, ResearchSweepOutput{Evidence: res.Evidence, Findings: res.Findings, Posts: res.Posts, Creators: res.Creators})
}
