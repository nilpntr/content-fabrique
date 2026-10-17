package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"
)

// Retention of the webhook delivery log (table webhook_events), written by the Clerk webhook handler.
//
// A row has two jobs. Its primary key makes a replayed delivery a no-op, which only matters while Svix can still retry
// it (its retry schedule ends after about a day; a manual resend of an older message is order-safe anyway, because
// every write carries the Clerk object's updated_at). WebhookEventsRetention covers that with a wide margin.
//
// An applied organizationMembership.deleted row is also a tombstone: SyncMember refuses to bring a membership back when
// a delete at or after the write's version is on record (store.MembershipDeletedSince). Its ordering effect outlives
// the replay protection, because a stale create can come from any session token issued before the delete, or from a
// webhook that lags. MembershipTombstoneRetention keeps those three times as long. Failed rows carry no ordering effect
// and go with the rest.
const (
	WebhookEventsRetention       = 30 * 24 * time.Hour
	MembershipTombstoneRetention = 90 * 24 * time.Hour

	webhookEventsPruneID = "webhook_events_prune"
	// The prune runs at 03:15 UTC.
	pruneHour, pruneMinute = 3, 15
)

// WebhookEventsPruneArgs is the daily periodic job that trims webhook_events.
type WebhookEventsPruneArgs struct{}

// Kind implements river.JobArgs.
func (WebhookEventsPruneArgs) Kind() string { return "webhook_events_prune" }

// InsertOpts runs it on the maintenance queue, once per period however it was enqueued.
func (WebhookEventsPruneArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueMaintenance,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByPeriod: 12 * time.Hour},
	}
}

// WebhookEventsPruneWorker works WebhookEventsPruneArgs.
type WebhookEventsPruneWorker struct {
	river.WorkerDefaults[WebhookEventsPruneArgs]
	deps Deps
}

// Work deletes the rows past their retention and records how many went.
func (w *WebhookEventsPruneWorker) Work(ctx context.Context, _ *river.Job[WebhookEventsPruneArgs]) error {
	now := w.deps.now()
	n, err := w.deps.Store.PruneWebhookEvents(ctx, now.Add(-WebhookEventsRetention), now.Add(-MembershipTombstoneRetention))
	if err != nil {
		return fmt.Errorf("prune webhook_events: %w", err)
	}
	w.deps.logger().InfoContext(ctx, "pruned webhook_events", "deleted", n)
	return river.RecordOutput(ctx, map[string]int64{"deleted": n})
}

// PeriodicJobs is the registry of periodic jobs: add a river.NewPeriodicJob here and the worker runs it. River runs
// the schedules on the elected leader only, so several workers do not multiply the work; a new leader starts the
// schedules over, so every job here must be safe to run twice.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			dailyAt{hour: pruneHour, minute: pruneMinute},
			func() (river.JobArgs, *river.InsertOpts) { return WebhookEventsPruneArgs{}, nil },
			&river.PeriodicJobOpts{ID: webhookEventsPruneID},
		),
	}
}

// PeriodicJobIDs lists the IDs of PeriodicJobs, for tests and docs.
func PeriodicJobIDs() []string { return []string{webhookEventsPruneID} }

// dailyAt is a river.PeriodicSchedule that fires once a day at a fixed UTC time. River's PeriodicInterval counts from
// the moment the leader starts, so a restart would move "daily" around the clock.
type dailyAt struct{ hour, minute int }

// Next implements river.PeriodicSchedule.
func (d dailyAt) Next(t time.Time) time.Time {
	t = t.UTC()
	next := time.Date(t.Year(), t.Month(), t.Day(), d.hour, d.minute, 0, 0, time.UTC)
	if !next.After(t) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
