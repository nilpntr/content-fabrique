package editor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The per-edit spend ceiling (the cost guard) on a real editor run, in replay mode over the committed cassettes: no API, no key, no cost.
//
// A replayed run cannot simply be given a lower ceiling from the start: the tool answers tell the model how much budget is left, so a
// different budget is a different request and a replay miss (and a call the budget shortens has no cassette either). So the run is
// replayed as recorded until its worker stops before the 7th model call (after the first version was made, previewed and checked), and
// a worker whose ceiling is below what the run has spent takes it over, as one does after a deploy that lowered the ceiling. The
// runtime stops the run there, mid-run, before another call is paid for, and the editor ends it succeeded on its newest version with a
// note that says the ceiling is why.

// stopAt is a model client factory that stops the worker at the nth model call: it cancels the run's context instead of sending it.
type stopAt struct {
	inner  llm.ClientFactory
	n      int
	cancel context.CancelFunc

	mu    sync.Mutex
	calls int
}

func (s *stopAt) For(ctx context.Context, ws ids.WorkspaceID, stage string) (llm.Client, llm.Billing, error) {
	c, b, err := s.inner.For(ctx, ws, stage)
	if err != nil {
		return nil, "", err
	}
	return stopClient{Client: c, s: s}, b, nil
}

type stopClient struct {
	llm.Client
	s *stopAt
}

func (c stopClient) Messages(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.s.mu.Lock()
	c.s.calls++
	stop := c.s.calls == c.s.n
	c.s.mu.Unlock()
	if stop {
		c.s.cancel()
		return llm.Response{}, ctx.Err()
	}
	return c.Client.Messages(ctx, req)
}

func TestSpendCeilingEndsAReplayedEditOnItsNewestVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &stopAt{n: 7, cancel: cancel}
	x := newE2E(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil, func(c *e2eConfig) {
		c.wrap = func(f llm.ClientFactory) llm.ClientFactory { stop.inner = f; return stop }
	})
	if _, err := x.runner.Execute(ctx, x.wsA.ID, x.run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("the first worker: %v, want it stopped at the 7th model call", err)
	}
	before := x.get()
	if before.FinishedAt != nil || before.ToolCalls != 6 || before.CostMicros <= 0 { // given back to the queue, unfinished
		t.Fatalf("before the takeover: %s, %d tool calls, cost %d", before.Status, before.ToolCalls, before.CostMicros)
	}
	if x.artifacts(PreviewArtifact) == 0 || x.artifacts(CheckArtifact) == 0 {
		t.Fatalf("previews %d, checks %d: the run must have a previewed, checked version before the ceiling", x.artifacts(PreviewArtifact), x.artifacts(CheckArtifact))
	}

	// The creator plan's ceiling is now below what the run has spent.
	ceiling := before.CostMicros - 1
	deps := x.rdeps
	deps.Ceilings = spend.Ceilings{Default: spend.DefaultEditCeilingMicros, Plans: map[plans.Plan]int64{plans.Creator: ceiling}}
	taker, err := runtime.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := taker.Execute(context.Background(), x.wsA.ID, x.run.ID); err != nil || out != runtime.OutcomeFinished {
		t.Fatalf("the takeover: %s, %v", out, err)
	}

	r := x.get()
	if r.Status != store.AgentSucceeded || r.FailureReason != nil {
		t.Fatalf("the run %s (%v): %s; it must end gracefully at the ceiling", r.Status, r.FailureReason, r.Output)
	}
	if r.CostMicros != before.CostMicros || r.ToolCalls != before.ToolCalls || stop.calls != 7 {
		t.Fatalf("the ceiling let more through: cost %d (was %d), tool calls %d (was %d), model calls %d", r.CostMicros, before.CostMicros, r.ToolCalls, before.ToolCalls, stop.calls)
	}
	out := outputOf(t, r)
	summary := fmt.Sprint(out["summary"])
	if out["limit"] != "dollars" || out["implicit_finish"] != true || out["ceiling_micros"] != float64(ceiling) ||
		!strings.Contains(summary, "spend ceiling of "+spend.USD(ceiling)+" for one edit") {
		t.Fatalf("the run did not say the ceiling stopped it: %s", r.Output)
	}
	made, err := x.st.GetTimelineVersionByAuthor(context.Background(), x.run.WorkspaceID, x.prj, "agent", ids.Encode(ids.PrefixRun, x.run.ID))
	if err != nil || made.Version < 1 {
		t.Fatalf("the run made no version: %v", err)
	}
	if v, ok := out["timeline_version"].(float64); !ok || int32(v) != made.Version || !strings.Contains(summary, fmt.Sprintf("version %d", made.Version)) {
		t.Fatalf("the run ended on version %v (%q), want its newest, %d", out["timeline_version"], summary, made.Version)
	}
}
