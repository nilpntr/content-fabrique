package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// agentRunsSuite is part of repositorySuite (so it also runs as the app role, with DML rights only). It covers the
// claim and the epoch fence, the event log (sequence numbers, totals, the payload cap), the state changes, and the
// tenancy rule: workspace B can neither read nor change workspace A's runs.
func agentRunsSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) store.Workspace {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_runs_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	newRun := func(ws ids.WorkspaceID) store.AgentRun {
		t.Helper()
		r, err := st.CreateAgentRun(ctx, ws, store.NewAgentRun{
			Agent: "echo", Stage: "ideation", Input: json.RawMessage(`{"message":"hi"}`), RoutingProfile: "dev-cheap",
			Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 1000,
			BudgetMicros: 100_000, BudgetTokens: 10_000, BudgetToolCalls: 5, BudgetWallClockMs: 60_000, BilledTo: store.BilledToTest,
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	const stale = 90 * time.Second
	wa, wb := newWorkspace("a"), newWorkspace("b")

	t.Run("create and claim", func(t *testing.T) {
		r := newRun(wa.ID)
		if r.Status != store.AgentQueued || r.Epoch != 0 || r.LastSeq != 0 || r.CostMicros != 0 || r.StartedAt != nil || r.Output != nil || r.FailureReason != nil {
			t.Fatalf("a new run: %+v", r)
		}
		if r.ID.Version() != 7 {
			t.Fatalf("id %v is not a UUIDv7", r.ID)
		}
		c, err := st.ClaimAgentRun(ctx, wa.ID, r.ID, stale)
		if err != nil || c.Status != store.AgentRunning || c.Epoch != 1 || c.StartedAt == nil || c.HeartbeatAt == nil {
			t.Fatalf("claim = %+v, %v", c, err)
		}
		// A live worker holds it: a second claim is refused, whatever job asks.
		if _, err := st.ClaimAgentRun(ctx, wa.ID, r.ID, stale); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second claim = %v, want ErrNotFound", err)
		}
		// Once the heartbeat is stale (here: a zero threshold), another worker takes over under a new epoch.
		c2, err := st.ClaimAgentRun(ctx, wa.ID, r.ID, 0)
		if err != nil || c2.Epoch != 2 {
			t.Fatalf("takeover = %+v, %v", c2, err)
		}
		// The old worker is fenced out of every write.
		if _, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventStatus, json.RawMessage(`{}`), store.EventDelta{}); !errors.Is(err, store.ErrRunFenced) {
			t.Fatalf("append under the old epoch = %v, want ErrRunFenced", err)
		}
		if err := st.TouchAgentRun(ctx, wa.ID, r.ID, 1); !errors.Is(err, store.ErrRunFenced) {
			t.Fatalf("touch under the old epoch = %v", err)
		}
		if _, err := st.EndAgentRun(ctx, wa.ID, r.ID, 1, store.RunEnd{Status: store.AgentSucceeded}); !errors.Is(err, store.ErrRunFenced) {
			t.Fatalf("end under the old epoch = %v", err)
		}
		if err := st.TouchAgentRun(ctx, wa.ID, r.ID, 2); err != nil {
			t.Fatal(err)
		}
		// A worker that releases the run puts it back for the next job.
		if err := st.ReleaseAgentRun(ctx, wa.ID, r.ID, 2); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetAgentRun(ctx, wa.ID, r.ID); got.Status != store.AgentQueued {
			t.Fatalf("released run is %s", got.Status)
		}
	})

	t.Run("events: sequence, totals, payload cap", func(t *testing.T) {
		r := newRun(wa.ID)
		if _, err := st.ClaimAgentRun(ctx, wa.ID, r.ID, stale); err != nil {
			t.Fatal(err)
		}
		e, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventAssistantMessage, json.RawMessage(`{"a":1}`), store.EventDelta{
			InputTokens: 100, CacheWriteTokens: 10, CacheReadTokens: 5, OutputTokens: 20, CostMicros: 700, ToolCalls: 1, ActiveMs: 30,
		})
		if err != nil || e.Seq != 1 {
			t.Fatalf("first event = %+v, %v", e, err)
		}
		// Concurrent appends take distinct, gapless sequence numbers.
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if _, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventStatus, json.RawMessage(`{}`), store.EventDelta{CostMicros: 1}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		events, err := st.AgentEvents(ctx, wa.ID, r.ID, 0, 0)
		if err != nil || len(events) != 21 {
			t.Fatalf("events = %d, %v", len(events), err)
		}
		for i, ev := range events {
			if int(ev.Seq) != i+1 {
				t.Fatalf("event %d has seq %d", i, ev.Seq)
			}
		}
		got, _ := st.GetAgentRun(ctx, wa.ID, r.ID)
		if got.LastSeq != 21 || got.InputTokens != 100 || got.CacheWriteTokens != 10 || got.CacheReadTokens != 5 || got.OutputTokens != 20 ||
			got.CostMicros != 720 || got.ToolCalls != 1 || got.ActiveMs != 30 {
			t.Fatalf("totals: %+v", got)
		}
		if page, err := st.AgentEvents(ctx, wa.ID, r.ID, 18, 2); err != nil || len(page) != 2 || page[0].Seq != 19 {
			t.Fatalf("page = %+v, %v", page, err)
		}
		last, err := st.LastAgentEvent(ctx, wa.ID, r.ID)
		if err != nil || last.Seq != 21 {
			t.Fatalf("last = %+v, %v", last, err)
		}

		// Over 64 KB is refused, and nothing is half-written: the sequence did not move.
		big := json.RawMessage(`{"t":"` + strings.Repeat("x", store.MaxEventPayloadBytes) + `"}`)
		if _, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventToolResult, big, store.EventDelta{CostMicros: 5}); !errors.Is(err, store.ErrPayloadTooLarge) || !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("big payload = %v", err)
		}
		// Just under the cap is fine.
		ok := json.RawMessage(`{"t":"` + strings.Repeat("x", store.MaxEventPayloadBytes-20) + `"}`)
		if e, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventToolResult, ok, store.EventDelta{}); err != nil || e.Seq != 22 {
			t.Fatalf("payload under the cap = %+v, %v", e, err)
		}
		if _, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, "nonsense", json.RawMessage(`{}`), store.EventDelta{}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown type = %v", err)
		}
		if _, err := st.AppendAgentEvent(ctx, wa.ID, r.ID, 1, store.EventStatus, json.RawMessage(`not json`), store.EventDelta{}); err == nil {
			t.Fatal("a payload that is not JSON was accepted")
		}
		if after, _ := st.GetAgentRun(ctx, wa.ID, r.ID); after.LastSeq != 22 || after.CostMicros != 720 {
			t.Fatalf("a refused append changed the run: last_seq %d, cost %d", after.LastSeq, after.CostMicros)
		}
	})

	t.Run("end, park, reply and cancel", func(t *testing.T) {
		// Succeeded: the status event and the state change commit together.
		r := newRun(wa.ID)
		_, _ = st.ClaimAgentRun(ctx, wa.ID, r.ID, stale)
		done, err := st.EndAgentRun(ctx, wa.ID, r.ID, 1, store.RunEnd{
			Status: store.AgentSucceeded, Output: json.RawMessage(`{"summary":"ok"}`), Event: json.RawMessage(`{"state":"succeeded"}`), ActiveMs: 12,
		})
		if err != nil || done.Status != store.AgentSucceeded || done.FinishedAt == nil || string(done.Output) != `{"summary": "ok"}` || done.ActiveMs != 12 {
			t.Fatalf("end = %+v, %v", done, err)
		}
		if _, err := st.ClaimAgentRun(ctx, wa.ID, r.ID, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a finished run was claimed: %v", err)
		}
		if _, err := st.EndAgentRun(ctx, wa.ID, r.ID, 1, store.RunEnd{Status: store.AgentFailed, FailureReason: "x"}); !errors.Is(err, store.ErrRunFenced) {
			t.Fatalf("a finished run was ended again: %v", err)
		}

		// Failed needs a reason; the others must not have one.
		r2 := newRun(wa.ID)
		_, _ = st.ClaimAgentRun(ctx, wa.ID, r2.ID, stale)
		if _, err := st.EndAgentRun(ctx, wa.ID, r2.ID, 1, store.RunEnd{Status: store.AgentFailed}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("failed without a reason = %v", err)
		}
		if _, err := st.EndAgentRun(ctx, wa.ID, r2.ID, 1, store.RunEnd{Status: store.AgentSucceeded, FailureReason: "x"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("succeeded with a reason = %v", err)
		}
		failed, err := st.EndAgentRun(ctx, wa.ID, r2.ID, 1, store.RunEnd{Status: store.AgentFailed, FailureReason: "budget", Event: json.RawMessage(`{"state":"failed"}`)})
		if err != nil || failed.FailureReason == nil || *failed.FailureReason != "budget" {
			t.Fatalf("failed = %+v, %v", failed, err)
		}

		// Park, then reply: waiting_input -> queued, and only once.
		r3 := newRun(wa.ID)
		_, _ = st.ClaimAgentRun(ctx, wa.ID, r3.ID, stale)
		if _, err := st.QueueAgentRunAfterReply(ctx, wa.ID, r3.ID); !errors.Is(err, store.ErrNotWaiting) {
			t.Fatalf("reply to a running run = %v", err)
		}
		parked, err := st.EndAgentRun(ctx, wa.ID, r3.ID, 1, store.RunEnd{Status: store.AgentWaitingInput, Event: json.RawMessage(`{"state":"waiting_input"}`)})
		if err != nil || parked.Status != store.AgentWaitingInput || parked.FinishedAt != nil {
			t.Fatalf("park = %+v, %v", parked, err)
		}
		if _, err := st.ClaimAgentRun(ctx, wa.ID, r3.ID, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a run waiting for input was claimed: %v", err)
		}
		if _, err := st.QueueAgentRunAfterReply(ctx, wb.ID, r3.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B replied to A's run: %v", err)
		}
		q, err := st.QueueAgentRunAfterReply(ctx, wa.ID, r3.ID)
		if err != nil || q.Status != store.AgentQueued {
			t.Fatalf("reply = %+v, %v", q, err)
		}
		if _, err := st.QueueAgentRunAfterReply(ctx, wa.ID, r3.ID); !errors.Is(err, store.ErrNotWaiting) {
			t.Fatalf("second reply = %v", err)
		}

		// Cancel: a queued run is cancelled at once, a running one only gets the flag, a finished one is left alone.
		cancelOut, cancelEvent := json.RawMessage(`{"stopped":"cancelled"}`), json.RawMessage(`{"state":"cancelled"}`)
		queued := newRun(wa.ID)
		c, err := st.RequestAgentRunCancel(ctx, wa.ID, queued.ID, cancelOut, cancelEvent)
		if err != nil || c.Status != store.AgentCancelled || c.FinishedAt == nil || c.CancelRequestedAt == nil {
			t.Fatalf("cancel queued = %+v, %v", c, err)
		}
		if last, _ := st.LastAgentEvent(ctx, wa.ID, queued.ID); last.Type != store.EventStatus {
			t.Fatalf("cancel left no status event: %+v", last)
		}
		running := newRun(wa.ID)
		_, _ = st.ClaimAgentRun(ctx, wa.ID, running.ID, stale)
		c, err = st.RequestAgentRunCancel(ctx, wa.ID, running.ID, cancelOut, cancelEvent)
		if err != nil || c.Status != store.AgentRunning || c.CancelRequestedAt == nil {
			t.Fatalf("cancel running = %+v, %v", c, err)
		}
		c, err = st.RequestAgentRunCancel(ctx, wa.ID, r.ID, cancelOut, cancelEvent) // r succeeded above
		if err != nil || c.Status != store.AgentSucceeded {
			t.Fatalf("cancel finished = %+v, %v", c, err)
		}
		if _, err := st.RequestAgentRunCancel(ctx, wb.ID, running.ID, cancelOut, cancelEvent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B cancelled A's run: %v", err)
		}
	})

	t.Run("tenancy and the sweeper", func(t *testing.T) {
		r := newRun(wa.ID)
		if _, err := st.GetAgentRun(ctx, wb.ID, r.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B read A's run: %v", err)
		}
		if _, err := st.ClaimAgentRun(ctx, wb.ID, r.ID, stale); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace B claimed A's run: %v", err)
		}
		_, _ = st.ClaimAgentRun(ctx, wa.ID, r.ID, stale)
		if _, err := st.AppendAgentEvent(ctx, wb.ID, r.ID, 1, store.EventStatus, json.RawMessage(`{}`), store.EventDelta{}); !errors.Is(err, store.ErrRunFenced) {
			t.Fatalf("workspace B appended to A's run: %v", err)
		}
		if events, err := st.AgentEvents(ctx, wb.ID, r.ID, 0, 0); err != nil || len(events) != 0 {
			t.Fatalf("workspace B read A's events: %v, %v", events, err)
		}
		// Parent runs stay inside the workspace.
		if _, err := st.CreateAgentRun(ctx, wb.ID, store.NewAgentRun{
			Agent: "echo", Stage: "ideation", ParentRunID: r.ID, RoutingProfile: "p", Model: "m", MaxTokens: 1,
			BudgetMicros: 1, BudgetTokens: 1, BudgetToolCalls: 1, BudgetWallClockMs: 1, BilledTo: store.BilledToTest,
		}); err == nil {
			t.Fatal("a run in workspace B took a parent from workspace A")
		}
		if _, err := st.CreateAgentRun(ctx, wa.ID, store.NewAgentRun{
			Agent: "echo", Stage: "ideation", ParentRunID: r.ID, RoutingProfile: "p", Model: "m", MaxTokens: 1,
			BudgetMicros: 1, BudgetTokens: 0, BudgetToolCalls: 1, BudgetWallClockMs: 1, BilledTo: store.BilledToTest,
		}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("a zero token budget = %v", err)
		}

		// No project is a null, not a zero uuid; a project that does not exist is refused.
		var nullProject bool
		if err := st.Pool().QueryRow(ctx, `SELECT project_id IS NULL FROM agent_runs WHERE id = $1`, r.ID).Scan(&nullProject); err != nil || !nullProject {
			t.Fatalf("project_id of a run without a project: null=%v, %v", nullProject, err)
		}
		if _, err := st.CreateAgentRun(ctx, wa.ID, store.NewAgentRun{
			Agent: "echo", Stage: "ideation", ProjectID: uuid.New(), RoutingProfile: "p", Model: "m", MaxTokens: 1,
			BudgetMicros: 1, BudgetTokens: 1, BudgetToolCalls: 1, BudgetWallClockMs: 1, BilledTo: store.BilledToTest,
		}); err == nil {
			t.Fatal("a run took a project that does not exist")
		}

		// A fresh heartbeat is not stale; a zero threshold lists every running run, ours included.
		fresh, err := st.StaleAgentRuns(ctx, time.Hour, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range fresh {
			if s.ID == r.ID {
				t.Fatal("a run with a fresh heartbeat was listed as stale")
			}
		}
		all, err := st.StaleAgentRuns(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range all {
			found = found || (s.ID == r.ID && s.WorkspaceID == wa.ID)
		}
		if !found {
			t.Fatal("the sweeper does not see a running run past the threshold")
		}
	})

	t.Run("the ledger cost of a run", func(t *testing.T) {
		r := newRun(wa.ID)
		run := r.ID
		for _, cost := range []int64{300, 450} {
			if err := st.InsertUsage(ctx, store.UsageEntry{
				WorkspaceID: wa.ID, RunID: run, Stage: "ideation", Model: "claude-haiku-5-5", InputTokens: 1, OutputTokens: 1,
				ListCostMicros: cost, PriceTableVersion: "t", BilledTo: store.BilledToTest,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := st.AgentRunLedgerCostMicros(ctx, wa.ID, run); err != nil || n != 750 {
			t.Fatalf("ledger cost = %d, %v", n, err)
		}
		if n, err := st.AgentRunLedgerCostMicros(ctx, wb.ID, run); err != nil || n != 0 {
			t.Fatalf("workspace B sees A's ledger: %d, %v", n, err)
		}
		if n, err := st.AgentRunLedgerCostMicros(ctx, wa.ID, uuid.New()); err != nil || n != 0 {
			t.Fatalf("unknown run: %d, %v", n, err)
		}
	})
}
