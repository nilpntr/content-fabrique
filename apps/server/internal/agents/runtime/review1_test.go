package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// catchCrash runs fn and reports whether it ended in a simulated kill.
func catchCrash(fn func()) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crashSignal); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	fn()
	return false
}

func killInModelCall(int, llm.Request) (llm.Response, error) {
	panic(crashSignal{point: "in_model_call"})
}

// A worker dies while the request is on the wire. The reservation it wrote before sending is charged by the next worker, so
// the budget cannot be spent twice; and the run goes on from its last durable event.
func TestKillDuringAModelCallChargesTheReservation(t *testing.T) {
	t.Run("the budget cannot cover the lost call and another", func(t *testing.T) {
		model := newScripted(append([]scriptStep{killInModelCall}, noteAndFinish()...)...)
		e := newEnv(t, model)
		run := e.create("echo", "hello", Budgets{Micros: 250}) // a call reserves 150
		if !catchCrash(func() { _, _ = e.runner.Execute(context.Background(), e.ws.ID, run.ID) }) {
			t.Fatal("not killed")
		}
		if r := e.get(run); r.CostMicros != 0 {
			t.Fatalf("the dead worker wrote a cost: %d", r.CostMicros)
		}
		if got := kinds(e.events(run)); !equalStrings(got, []string{"status:running", "status:model_call"}) {
			t.Fatalf("events = %v", got)
		}
		e.ageHeartbeat(run)
		e.execute(run)
		assertBudgetStop(t, e, run, LimitDollars)
		r := e.get(run)
		if r.CostMicros != 150 {
			t.Fatalf("cost = %d, want the lost call's reserved 150", r.CostMicros)
		}
		var fixed []statusPayload
		for _, ev := range eventsOf(e.events(run), store.EventStatus) {
			var p statusPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if p.State == stateCostFixed {
				fixed = append(fixed, p)
			}
		}
		if len(fixed) != 1 || fixed[0].AddedCostMicros != 150 {
			t.Fatalf("cost_reconciled events = %+v", fixed)
		}
	})
	t.Run("with room the run asks again and finishes", func(t *testing.T) {
		model := newScripted(append([]scriptStep{killInModelCall}, noteAndFinish()...)...)
		e := newEnv(t, model)
		run := e.create("echo", "hello", Budgets{})
		_ = catchCrash(func() { _, _ = e.runner.Execute(context.Background(), e.ws.ID, run.ID) })
		e.ageHeartbeat(run)
		e.execute(run)
		r := e.get(run)
		if r.Status != store.AgentSucceeded || r.CostMicros != 150+200 || e.sink.effects() != 1 {
			t.Fatalf("run = %s cost %d effects %d", r.Status, r.CostMicros, e.sink.effects())
		}
	})
	t.Run("the ledger row of a call that did finish is not counted twice", func(t *testing.T) {
		model := newScripted(append([]scriptStep{killInModelCall}, noteAndFinish()...)...)
		e := newEnv(t, model)
		run := e.create("echo", "hello", Budgets{})
		_ = catchCrash(func() { _, _ = e.runner.Execute(context.Background(), e.ws.ID, run.ID) })
		// The call had finished and written its ledger row (100) before the worker died.
		if err := e.st.InsertUsage(context.Background(), store.UsageEntry{
			WorkspaceID: e.ws.ID, RunID: run.ID, Stage: "ideation", Model: "claude-haiku-5-5", InputTokens: 1, OutputTokens: 1,
			ListCostMicros: 100, PriceTableVersion: "t", BilledTo: store.BilledToTest,
		}); err != nil {
			t.Fatal(err)
		}
		e.ageHeartbeat(run)
		e.execute(run)
		// max(ledger 100, row 0 + in flight 150) = 150 charged, not 250.
		if r := e.get(run); r.CostMicros != 150+200 {
			t.Fatalf("cost = %d", r.CostMicros)
		}
	})
}

// A run that keeps killing its workers is failed instead of resumed for ever.
func TestTooManyResumesFailTheRun(t *testing.T) {
	model := newScripted(endTurn("never"))
	e := newEnv(t, model)
	run := e.create("echo", "hello", Budgets{})
	for i := 0; i <= MaxResumes; i++ { // claims 1 to MaxResumes+1: the last has MaxResumes resumes, which is still allowed
		if !e.crashAt(run, "claimed") {
			t.Fatalf("claim %d did not reach the crash point (run is %s)", i+1, e.get(run).Status)
		}
		e.ageHeartbeat(run)
	}
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	r := e.get(run)
	if r.Status != store.AgentFailed || *r.FailureReason != FailTooManyResumes || r.Resumes != MaxResumes+1 {
		t.Fatalf("run = %s %v resumes %d", r.Status, r.FailureReason, r.Resumes)
	}
	if model.calls() != 0 {
		t.Fatalf("the model was called %d times", model.calls())
	}
}

// A bug that panics inside the loop is recorded, retried, counted, and ends in a failed run.
func TestAPanicInTheLoopIsCountedAndFailsTheRun(t *testing.T) {
	e := newEnv(t, newScripted(endTurn("never")))
	run := e.create("echo", "hello", Budgets{})
	e.runner.hook = func(p string) {
		if p == "claimed" {
			panic("a bug")
		}
	}
	attempts := 0
	for ; attempts < 20 && e.get(run).Status != store.AgentFailed; attempts++ {
		if _, err := e.runner.Execute(context.Background(), e.ws.ID, run.ID); err == nil && e.get(run).Status != store.AgentFailed {
			t.Fatal("a panic reported success")
		}
		if st := e.get(run).Status; st != store.AgentQueued && st != store.AgentFailed {
			t.Fatalf("after a panic the run is %s, want queued (released)", st)
		}
	}
	r := e.get(run)
	if r.Status != store.AgentFailed || *r.FailureReason != FailTooManyResumes || attempts != MaxResumes+2 {
		t.Fatalf("run = %s %v after %d attempts", r.Status, r.FailureReason, attempts)
	}
	panics := 0
	for _, ev := range eventsOf(e.events(run), store.EventError) {
		if strings.Contains(string(ev.Payload), `"panic"`) {
			panics++
		}
	}
	if panics != MaxResumes+1 {
		t.Fatalf("panic events = %d", panics)
	}
}

// The sweeper enqueues one job for a stale run however often it looks.
func TestSweeperEnqueuesOneJobPerStaleRun(t *testing.T) {
	e := newEnv(t, newScripted(noteAndFinish()...))
	cfg := jobs.Config{Queues: []jobs.Queue{{Name: jobs.QueueMaintenance, MaxWorkers: 2}}} // no agents queue: the jobs stay put
	client, err := jobs.NewWorkerClient(e.st.Pool(), cfg, jobs.Deps{Store: e.st, Logger: quiet, Register: Register(nil)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stop)
	})
	dead := e.create("echo", "hello", Budgets{})
	if !e.crashAt(dead, "claimed") {
		t.Fatal("not reached")
	}
	e.ageHeartbeat(dead)

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.st.Pool().QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for pass := 1; pass <= 3; pass++ {
		if _, err := client.Insert(ctx, jobs.AgentRunsResumeArgs{}, nil); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for count(`SELECT count(*) FROM river_job WHERE kind = 'agent_runs_resume' AND state = 'completed'`) == 0 {
			if time.Now().After(deadline) {
				rows, _ := e.st.Pool().Query(ctx, `SELECT state, coalesce(errors::text, '') FROM river_job WHERE kind = 'agent_runs_resume'`)
				for rows.Next() {
					var st, errs string
					_ = rows.Scan(&st, &errs)
					t.Logf("sweeper job: %s %s", st, errs)
				}
				t.Fatalf("pass %d: the sweeper did not run", pass)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Forget the sweeper's own job (it is unique by period), keep what it enqueued.
		if _, err := e.st.Pool().Exec(ctx, `DELETE FROM river_job WHERE kind = 'agent_runs_resume'`); err != nil {
			t.Fatal(err)
		}
		if n := count(`SELECT count(*) FROM river_job WHERE kind = 'agent_run' AND args->>'run_id' = $1`, dead.ID.String()); n != 1 {
			t.Fatalf("pass %d: %d agent_run jobs for the stale run, want 1", pass, n)
		}
	}
}

// A cancel that arrives while the tool is asking wins: the run is cancelled, not parked.
func TestCancelWinsOverAskUser(t *testing.T) {
	type none struct{}
	var e *env
	cancelAndAsk := NewTool(Spec{Name: "cancel_and_ask", Description: "d", Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		func(ctx context.Context, rc RunContext, _ none) (Result, error) {
			if _, err := e.svc.Cancel(ctx, rc.WorkspaceID, rc.RunID); err != nil {
				return Result{}, err
			}
			res := Text("which?")
			res.Ask = true
			return res, nil
		})
	a := Agent{Name: "cancelask", Stage: "ideation", System: "test", Tools: []Tool{cancelAndAsk, FinishTool()}}
	e = newEnv(t, newScripted(use(toolUse("t1", "cancel_and_ask", `{}`))), func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("cancelask", "go", Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	r := e.get(run)
	if r.Status != store.AgentCancelled || r.FinishedAt == nil {
		t.Fatalf("run = %s", r.Status)
	}
	for _, k := range kinds(e.events(run)) {
		if k == "status:waiting_input" {
			t.Fatalf("the run was parked: %v", kinds(e.events(run)))
		}
	}
	if err := e.svc.Reply(context.Background(), e.ws.ID, run.ID, "x"); !errors.Is(err, store.ErrNotWaiting) {
		t.Fatalf("reply to a cancelled run = %v", err)
	}
}

// The encoded size of an event is what is capped: escapes cannot push 48 KB of text over 64 KB, and a reply that grows when
// encoded is refused.
func TestEncodedEventSizeIsCapped(t *testing.T) {
	type none struct{}
	schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	mk := func(name string, f func() Result) Tool {
		return NewTool(Spec{Name: name, Description: "d", Schema: schema}, func(context.Context, RunContext, none) (Result, error) { return f(), nil })
	}
	many := make([]llm.Block, 0, 100)
	for i := range 100 {
		many = append(many, llm.TextBlock(fmt.Sprintf("part %d", i)))
	}
	a := Agent{Name: "sizer", Stage: "ideation", System: "test", Tools: []Tool{
		mk("angles", func() Result { return Text(strings.Repeat("<&>", 15_000)) }),
		mk("control", func() Result { return Text(strings.Repeat("\x01", 45_000)) }),
		mk("quotes", func() Result { return Text(strings.Repeat(`"`, 45_000)) }),
		mk("nul", func() Result { return Text("a\x00b") }),
		mk("empties", func() Result {
			return Result{Content: []llm.Block{llm.TextBlock(""), llm.TextBlock("x"), llm.TextBlock("")}}
		}),
		mk("many", func() Result { return Result{Content: many} }),
		AskUserTool(), FinishTool(),
	}}
	model := newScripted(
		use(toolUse("t1", "angles", `{}`), toolUse("t2", "control", `{}`), toolUse("t3", "quotes", `{}`), toolUse("t4", "nul", `{}`),
			toolUse("t5", "empties", `{}`), toolUse("t6", "many", `{}`)),
		use(toolUse("t7", "ask_user", `{"question":"ok?"}`)),
		use(toolUse("t8", "finish", `{"summary":"done"}`)),
	)
	e := newEnv(t, model, func(c *envConfig) { c.extra = []Agent{a} })
	run := e.create("sizer", "go", Budgets{})
	if got := e.execute(run); got != OutcomeWaiting {
		t.Fatalf("outcome = %s (%s)", got, e.get(run).Status)
	}
	byID := map[string]toolResultPayload{}
	for _, ev := range eventsOf(e.events(run), store.EventToolResult) {
		if len(ev.Payload) > store.MaxEventPayloadBytes {
			t.Fatalf("event %d is %d bytes", ev.Seq, len(ev.Payload))
		}
		var p toolResultPayload
		_ = json.Unmarshal(ev.Payload, &p)
		byID[p.ToolUseID] = p
	}
	if text := textOf(byID["t1"].Content); len(text) != 45_000 || strings.Contains(text, "[truncated]") {
		t.Fatalf("t1: %d bytes", len(text))
	}
	for _, ev := range eventsOf(e.events(run), store.EventToolResult) {
		if strings.Contains(string(ev.Payload), "toolu") || !strings.Contains(string(ev.Payload), "t1") {
			continue
		}
		if strings.Contains(string(ev.Payload), `\u003c`) {
			t.Fatal("angle brackets were escaped")
		}
	}
	for _, id := range []string{"t2", "t3"} {
		if text := textOf(byID[id].Content); !strings.Contains(text, "[truncated]") || len(text) < 4000 {
			t.Fatalf("%s: text of %d bytes, truncated=%v", id, len(text), strings.Contains(text, "[truncated]"))
		}
	}
	if got := textOf(byID["t4"].Content); got != "ab" {
		t.Fatalf("NUL result = %q", got)
	}
	if c := byID["t5"].Content; len(c) != 1 || c[0].Text != "x" {
		t.Fatalf("empty text blocks were kept: %+v", c)
	}
	if c := byID["t6"].Content; len(c) != maxResultBlocks+1 || !strings.Contains(c[len(c)-1].Text, "omitted") {
		t.Fatalf("%d blocks kept, last %+v", len(c), c[len(c)-1])
	}
	// Short in bytes, long once escaped: refused rather than failing the append.
	if err := e.svc.Reply(context.Background(), e.ws.ID, run.ID, strings.Repeat("\x01", 15_000)); !errors.Is(err, ErrReplyTooLong) {
		t.Fatalf("escaped reply = %v", err)
	}
	if got := e.get(run).Status; got != store.AgentWaitingInput {
		t.Fatalf("a refused reply changed the run to %s", got)
	}
	if err := e.svc.Reply(context.Background(), e.ws.ID, run.ID, "fine <b>"); err != nil {
		t.Fatal(err)
	}
	e.execute(run)
	if e.get(run).Status != store.AgentSucceeded {
		t.Fatalf("run = %s", e.get(run).Status)
	}
}

func TestStopReasons(t *testing.T) {
	cases := map[string]struct {
		steps  []scriptStep
		status string
		reason string
	}{
		"end_turn":                       {[]scriptStep{endTurn("done")}, store.AgentSucceeded, ""},
		"stop_sequence":                  {[]scriptStep{say("stop_sequence", llm.TextBlock("done"))}, store.AgentSucceeded, ""},
		"pause_turn continues":           {[]scriptStep{say("pause_turn", llm.TextBlock("thinking")), endTurn("done")}, store.AgentSucceeded, ""},
		"context window":                 {[]scriptStep{say("model_context_window_exceeded", llm.TextBlock("..."))}, store.AgentFailed, FailContextWindow},
		"max_tokens":                     {[]scriptStep{say("max_tokens", llm.TextBlock("...")), say("max_tokens", llm.TextBlock("...")), say("max_tokens", llm.TextBlock("..."))}, store.AgentFailed, FailMaxTokens},
		"refusal":                        {[]scriptStep{say("refusal")}, store.AgentFailed, FailRefusal},
		"unknown":                        {[]scriptStep{say("something_new", llm.TextBlock("..."))}, store.AgentFailed, FailStopReason},
		"empty":                          {[]scriptStep{say("", llm.TextBlock("..."))}, store.AgentFailed, FailStopReason},
		"tool_use stop with no tool":     {[]scriptStep{say("tool_use", llm.TextBlock("..."))}, store.AgentFailed, FailStopReason},
		"unknown reason with a tool use": {[]scriptStep{say("something_new", toolUse("t1", "note", `{"text":"a"}`))}, store.AgentFailed, FailStopReason},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, newScripted(c.steps...))
			run := e.create("echo", "hello", Budgets{})
			e.execute(run)
			r := e.get(run)
			if r.Status != c.status || (c.reason != "" && (r.FailureReason == nil || *r.FailureReason != c.reason)) {
				t.Fatalf("run = %s %v, want %s %s", r.Status, r.FailureReason, c.status, c.reason)
			}
			if e.sink.effects() != 0 {
				t.Fatal("a tool ran for a stop reason that is not tool_use")
			}
		})
	}
	t.Run("an empty text block is not stored", func(t *testing.T) {
		e := newEnv(t, newScripted(say("end_turn", llm.TextBlock(""), llm.TextBlock("x"))))
		run := e.create("echo", "hello", Budgets{})
		e.execute(run)
		var p assistantPayload
		_ = json.Unmarshal(eventsOf(e.events(run), store.EventAssistantMessage)[0].Payload, &p)
		if len(p.Content) != 1 || p.Content[0].Text != "x" {
			t.Fatalf("content = %+v", p.Content)
		}
	})
}

// A worker that finishes its run is never reported as having lost it, however the heartbeat races the last write.
func TestFinishingIsNotReportedAsLost(t *testing.T) {
	var steps []scriptStep
	for range 12 {
		steps = append(steps, noteAndFinish()...)
	}
	e := newEnv(t, newScripted(steps...))
	e.runner.deps.Heartbeat = time.Millisecond
	e.sink.delay = 3 * time.Millisecond
	for i := range 12 {
		run := e.create("echo", fmt.Sprintf("hello %d", i), Budgets{})
		if got := e.execute(run); got != OutcomeFinished {
			t.Fatalf("run %d: outcome = %s", i, got)
		}
	}
}

// The failure summary of a run that gave up carries its real totals and last message.
func TestGiveUpSummaryHasTheRunsTotals(t *testing.T) {
	e := newEnv(t, newScripted(use(llm.TextBlock("working on it"), toolUse("toolu_1", "note", `{"text":"hello"}`))))
	run := e.create("echo", "hello", Budgets{})
	if !e.crashAt(run, "tool_call_persisted") {
		t.Fatal("not reached")
	}
	e.ageHeartbeat(run)
	e.runner.GiveUp(context.Background(), e.ws.ID, run.ID, errors.New("database is down"))
	r := e.get(run)
	out := outputOf(t, r)
	if r.Status != store.AgentFailed || *r.FailureReason != FailInternal || out["tool_calls"] != float64(1) || out["cost_micros"] != float64(100) || out["last_message"] != "working on it" {
		t.Fatalf("run = %s %v output %v", r.Status, r.FailureReason, out)
	}
}

func TestDefaultAgents(t *testing.T) {
	a, err := NewAgents(DefaultAgents()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Get("echo"); !ok {
		t.Fatal("no echo agent")
	}
}

// The heartbeat can see "fenced" at the moment this worker writes the end of its own run (its UPDATE waited for the row
// lock of that very transaction). That must not turn a finished run into an error or a lost run: the end write is not
// cancelled with the loop, and the outcome is the run's.
func TestTheEndWriteSurvivesAHeartbeatFence(t *testing.T) {
	e := newEnv(t, newScripted(noteAndFinish()...))
	fired := 0
	e.runner.hookCancel = func(point string, cancel context.CancelCauseFunc) {
		if point == "ending" {
			fired++
			cancel(store.ErrRunFenced) // what the heartbeat does when it sees the status change
		}
	}
	run := e.create("echo", "hello", Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	if r := e.get(run); r.Status != store.AgentSucceeded || fired != 1 {
		t.Fatalf("run = %s, hook fired %d times", r.Status, fired)
	}
}
