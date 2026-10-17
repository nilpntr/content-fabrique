package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/credits"
)

// The periodic work of credits (P7-01a, docs/design/spend-control-v1.md sections 3 and 5): the allowance of each period, the expiry of
// credits that ran out of time, and the sweep of holds whose run is gone.
//
// Both jobs are safe to run twice and from several workers: the allowance's ledger key names the workspace, the plan and the period, an
// expire row's key names its source, and every change takes the workspace's credit account lock. A late job loses nothing: a source past its
// expiry has stopped counting whether or not its expire row is written yet, and a workspace is granted the allowance of the period it is
// in when the job gets to it.

const (
	// CreditsPeriodsInterval is how often the allowance and expiry job runs. A period starts at most this long before its allowance is
	// there; the hold path does not grant lazily, so a creator who starts an edit in that window sees 402 credits_exhausted for a minute.
	CreditsPeriodsInterval = time.Minute
	// CreditHoldsSweepInterval is how often the sweep of expired credit holds runs.
	CreditHoldsSweepInterval = time.Minute

	creditsPeriodsID   = "credits_periods"
	creditHoldsSweepID = "credit_holds_sweep"

	// creditsBatch is how many workspaces and sources one run of the periods job handles; the next run (a minute later) takes the rest.
	creditsBatch = 500
)

// CreditsPeriodsArgs is the periodic job that grants the allowance of the period each workspace is in and expires the credits that ran
// out of time.
type CreditsPeriodsArgs struct{}

// Kind implements river.JobArgs.
func (CreditsPeriodsArgs) Kind() string { return "credits_periods" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (CreditsPeriodsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: CreditsPeriodsInterval / 2}}
}

// CreditsPeriodsWorker works CreditsPeriodsArgs.
type CreditsPeriodsWorker struct {
	river.WorkerDefaults[CreditsPeriodsArgs]
	deps Deps
}

// CreditsPeriodsOutput is what a finished run records.
type CreditsPeriodsOutput struct {
	Workspaces     int `json:"workspaces"`
	Granted        int `json:"granted_credits"`
	Replaced       int `json:"replaced_credits"`
	ExpiredSources int `json:"expired_sources"`
	ExpiredCredits int `json:"expired_credits"`
}

func (d Deps) priceList() (*credits.PriceList, error) {
	if d.Credits != nil {
		return d.Credits, nil
	}
	return credits.LoadDefault()
}

// Work grants what is missing and expires what is due. A workspace that fails is logged and left for the next run; the others go on.
func (w *CreditsPeriodsWorker) Work(ctx context.Context, _ *river.Job[CreditsPeriodsArgs]) error {
	list, err := w.deps.priceList()
	if err != nil {
		return fmt.Errorf("credits periods: price list: %w", err)
	}
	svc := credits.NewService(w.deps.Store, list)
	svc.Now = w.deps.now
	log := w.deps.logger()
	now := w.deps.now()
	var out CreditsPeriodsOutput

	todo, err := w.deps.Store.WorkspacesNeedingAllowance(ctx, now, creditsBatch)
	if err != nil {
		return fmt.Errorf("credits periods: list workspaces: %w", err)
	}
	failed := 0
	for _, ws := range todo {
		res, err := svc.SyncAllowance(ctx, ws)
		if err != nil {
			failed++
			log.ErrorContext(ctx, "credits: granting the allowance failed", "workspace_id", ws.String(), "error", err)
			continue
		}
		out.Workspaces++
		out.Granted += res.Granted
		out.Replaced += res.Replaced
		if res.Granted > 0 || res.Replaced > 0 {
			log.InfoContext(ctx, "credits: allowance granted", "workspace_id", ws.String(), "plan", string(res.Plan), "credits", res.Granted,
				"replaced", res.Replaced, "period_end", res.PeriodEnd)
		}
	}
	sources, expired, err := w.deps.Store.ExpireDueCredits(ctx, now, creditsBatch)
	out.ExpiredSources, out.ExpiredCredits = sources, expired
	if err != nil {
		return fmt.Errorf("credits periods: expire: %w", err)
	}
	if sources > 0 {
		log.InfoContext(ctx, "credits: expired", "sources", sources, "credits", expired)
	}
	if err := river.RecordOutput(ctx, out); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("credits periods: %d workspaces could not be granted (logged above)", failed)
	}
	return nil
}

// CreditHoldsSweepArgs is the periodic job that deletes expired credit holds.
type CreditHoldsSweepArgs struct{}

// Kind implements river.JobArgs.
func (CreditHoldsSweepArgs) Kind() string { return "credit_holds_sweep" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (CreditHoldsSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: CreditHoldsSweepInterval / 2}}
}

// CreditHoldsSweepWorker works CreditHoldsSweepArgs.
type CreditHoldsSweepWorker struct {
	river.WorkerDefaults[CreditHoldsSweepArgs]
	deps Deps
}

// CreditHoldsSweepOutput is what a finished run records.
type CreditHoldsSweepOutput struct {
	Holds   int64 `json:"holds"`
	Credits int64 `json:"credits"`
}

// Work deletes the holds past their expiry, which gives their credits back: a hold stops counting when it expires, and this removes the
// row. A hold follows its run's heartbeat, so one that expired belongs to a run that is gone.
func (w *CreditHoldsSweepWorker) Work(ctx context.Context, _ *river.Job[CreditHoldsSweepArgs]) error {
	res, err := w.deps.Store.SweepExpiredCreditHolds(ctx, w.deps.now())
	if err != nil {
		return fmt.Errorf("sweep credit holds: %w", err)
	}
	if res.Holds > 0 {
		w.deps.logger().WarnContext(ctx, "swept expired credit holds: their runs are gone, so the credits were released, not charged", "holds", res.Holds, "credits", res.Credits)
	}
	return river.RecordOutput(ctx, CreditHoldsSweepOutput{Holds: res.Holds, Credits: res.Credits})
}
