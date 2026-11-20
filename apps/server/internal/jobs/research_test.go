package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The research_sweep job deletes an observation past its retention (a YouTube row seen 31 days ago) and keeps one inside it.
func TestResearchSweepDeletesExpiredEvidence(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, err := e.api.UpsertResearchCreator(ctx, store.NewResearchCreator{Platform: research.YouTube, Handle: "sweep_job"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.api.UpsertResearchPost(ctx, store.NewResearchPost{URL: "https://www.youtube.com/shorts/abcDEF12345", CreatorID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	add := func(age time.Duration) {
		t.Helper()
		if _, err := e.api.AddResearchEvidence(ctx, store.NewResearchEvidence{
			PostID: p.ID, Source: research.SourceYouTubeAPI, Method: research.MethodAPI, ObservedAt: time.Now().Add(-age), Observer: "run:test",
			Metrics: map[string]float64{"views": 10},
		}); err != nil {
			t.Fatal(err)
		}
	}
	add(31 * research.Day)
	add(29 * research.Day)
	count := func() (n int) {
		if err := e.super.QueryRow(ctx, `SELECT count(*) FROM research_evidence`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("evidence = %d", count())
	}
	e.startWorker(t, jobs.WorkerOptions{})
	if err := e.api.InTx(ctx, func(tx *store.Store) error {
		_, err := e.enq.InsertInStore(ctx, tx, jobs.ResearchSweepArgs{}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the sweep to delete the expired observation", func() bool { return count() == 1 })
}
