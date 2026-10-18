package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
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

	// creditsBatch is how many workspaces one page of the periods job lists and how many sources it expires; creditsMaxPages pages are read
	// per run, and the next run (a minute later) takes the rest. The pages go by workspace id, so a workspace that fails every time cannot
	// keep the ones behind it from their allowance.
	creditsBatch    = 500
	creditsMaxPages = 20
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
	Workspaces       int `json:"workspaces"`
	Granted          int `json:"granted_credits"`
	Replaced         int `json:"replaced_credits"`
	ListSwitchTopUps int `json:"list_switch_credits"`
	ExpiredSources   int `json:"expired_sources"`
	ExpiredCredits   int `json:"expired_credits"`
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

	failed := 0
	var after ids.WorkspaceID
	for page := 0; page < creditsMaxPages; page++ {
		todo, err := w.deps.Store.WorkspacesNeedingAllowance(ctx, now, after, creditsBatch)
		if err != nil {
			return fmt.Errorf("credits periods: list workspaces: %w", err)
		}
		for _, ws := range todo {
			after = ws
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
		if len(todo) < creditsBatch {
			break
		}
	}
	// A price list that gives a plan a larger allowance than the one its workspaces were granted (a trial's, once; a period's, until the
	// next refill) tops those allowances up, once.
	after = ids.WorkspaceID{}
	specs := list.AllowanceSpecs()
	for page := 0; page < creditsMaxPages; page++ {
		behind, err := w.deps.Store.WorkspacesBehindTheList(ctx, specs, now, after, creditsBatch)
		if err != nil {
			return fmt.Errorf("credits periods: list workspaces behind the price list: %w", err)
		}
		for _, ws := range behind {
			after = ws
			added, err := svc.SyncListSwitch(ctx, ws)
			if err != nil {
				failed++
				log.ErrorContext(ctx, "credits: topping up the allowance to the price list failed", "workspace_id", ws.String(), "error", err)
				continue
			}
			out.ListSwitchTopUps += added
			if added > 0 {
				log.InfoContext(ctx, "credits: allowance topped up to the price list", "workspace_id", ws.String(), "list", list.Version, "credits", added)
			}
		}
		if len(behind) < creditsBatch {
			break
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
