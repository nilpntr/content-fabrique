package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Why a run failed (agent_runs.failure_reason).
const (
	FailBudget              = "budget"
	FailModelOutcomeUnknown = "model_outcome_unknown"
	FailModelError          = "model_error"
	FailModelAccess         = "model_access"
	FailDailyCap            = "daily_cap"
	FailRequestCap          = "request_cap"
	FailReplayMiss          = "replay_miss"
	FailMaxTokens           = "max_tokens"
	FailRefusal             = "refusal"
	FailLedger              = "ledger_error"
	FailEventTooLarge       = "event_too_large"
	FailUnknownAgent        = "unknown_agent"
	FailBadInput            = "invalid_input"
	FailContext             = "context_failed"
	FailCorruptLog          = "corrupt_log"
	FailInternal            = "internal_error"
)

// The limits a budget failure names (the "limit" of the failed status event).
const (
	LimitDollars   = "dollars"
	LimitTokens    = "tokens"
	LimitToolCalls = "tool_calls"
	LimitWallClock = "wall_clock"
)

// Outcome says how an Execute ended when it returned no error.
type Outcome string

// The outcomes.
const (
	// OutcomeFinished: the run reached a final state (succeeded, failed or cancelled).
	OutcomeFinished Outcome = "finished"
	// OutcomeWaiting: the run asked the user something and is parked in waiting_input.
	OutcomeWaiting Outcome = "waiting_input"
	// OutcomeSkipped: there was nothing to do (the run was finished already, or waiting for its answer).
	OutcomeSkipped Outcome = "skipped"
	// OutcomeHeld: another live worker holds the run.
	OutcomeHeld Outcome = "held"
	// OutcomeLost: this worker lost the run in the middle (another worker took it over); it wrote nothing more.
	OutcomeLost Outcome = "lost"
	// OutcomeGone: the run does not exist in the workspace.
	OutcomeGone Outcome = "gone"
)

// Deps are what the loop needs.
type Deps struct {
	Store   *store.Store
	Factory llm.ClientFactory
	Agents  *Agents
	Logger  *slog.Logger
	// StaleAfter is how long a running run may go without a heartbeat before another worker may take it. Zero is
	// jobs.AgentRunStaleAfter.
	StaleAfter time.Duration
	// Heartbeat is how often the worker touches its run. Zero is jobs.AgentRunHeartbeat.
	Heartbeat time.Duration
	// Now replaces time.Now (tests).
	Now func() time.Time
}

// Runner executes agent runs: the agent_run job calls Execute.
type Runner struct {
	deps Deps
	// hook, set by tests, is called at named points of the loop. A test simulates a killed worker by panicking in it:
	// nothing after that point runs, and nothing is cleaned up, as when a process dies.
	hook func(point string)
}

// NewRunner checks the dependencies.
func NewRunner(deps Deps) (*Runner, error) {
	if deps.Store == nil || deps.Factory == nil || deps.Agents == nil {
		return nil, errors.New("runtime: a store, a model client factory and the agents are required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.StaleAfter == 0 {
		deps.StaleAfter = jobs.AgentRunStaleAfter
	}
	if deps.Heartbeat == 0 {
		deps.Heartbeat = jobs.AgentRunHeartbeat
	}
	return &Runner{deps: deps}, nil
}

// Execute works a run until it ends, parks or has to be retried. It claims the run first (so a second job for a run that a
// live worker holds does nothing), rebuilds the conversation from the events, and repeats the steps of the design note:
// check cancel and budgets, run the tool calls that have no result, call the model. A non-nil error means "retry the job":
// the run is put back to queued and resumes from its events on the next attempt.
func (r *Runner) Execute(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) (Outcome, error) {
	st := r.deps.Store
	run, err := st.ClaimAgentRun(ctx, ws, id, r.deps.StaleAfter)
	if errors.Is(err, store.ErrNotFound) {
		cur, gerr := st.GetAgentRun(ctx, ws, id)
		switch {
		case errors.Is(gerr, store.ErrNotFound):
			return OutcomeGone, nil
		case gerr != nil:
			return "", fmt.Errorf("get run: %w", gerr)
		case cur.Status == store.AgentRunning:
			return OutcomeHeld, nil
		}
		return OutcomeSkipped, nil
	}
	if err != nil {
		return "", fmt.Errorf("claim run: %w", err)
	}

	l := &loop{r: r, st: st, ws: ws, run: run, log: r.deps.Logger.With("run_id", run.ID.String(), "workspace_id", ws.String(), "agent", run.Agent, "epoch", run.Epoch)}
	loopCtx, cancel := context.WithCancelCause(ctx)
	out, err := func() (Outcome, error) {
		// The heartbeat stops however the loop ends, a panic included: a worker that dies must not keep its run fresh.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); l.heartbeat(loopCtx, cancel) }()
		defer func() { cancel(nil); wg.Wait() }()
		return l.work(loopCtx)
	}()

	switch {
	case errors.Is(err, store.ErrRunFenced), errors.Is(context.Cause(loopCtx), store.ErrRunFenced):
		l.log.Warn("the run was taken over by another worker; this worker stops")
		return OutcomeLost, nil
	case err != nil:
		rel, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if rerr := st.ReleaseAgentRun(rel, ws, id, run.Epoch); rerr != nil && !errors.Is(rerr, store.ErrRunFenced) {
			l.log.Error("could not put the run back to queued; it resumes when its heartbeat goes stale", "error", rerr)
		}
		return "", err
	}
	return out, nil
}

// GiveUp fails a run whose job has used its last attempt, so it does not stay queued for ever.
func (r *Runner) GiveUp(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID, cause error) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	run, err := r.deps.Store.ClaimAgentRun(ctx, ws, id, r.deps.StaleAfter)
	if err != nil {
		return // finished, waiting, held by a live worker, or gone
	}
	now := r.deps.Now()
	l := &loop{r: r, st: r.deps.Store, ws: ws, run: run, log: r.deps.Logger, segStart: now, flushedAt: now, baseMs: run.ActiveMs}
	if _, err := l.fail(ctx, FailInternal, "the run could not be completed: "+cause.Error(), nil); err != nil {
		r.deps.Logger.Error("could not fail a run whose job gave up", "run_id", id.String(), "error", err)
	}
}

// loop is one claim of a run.
type loop struct {
	r   *Runner
	st  *store.Store
	ws  ids.WorkspaceID
	run store.AgentRun
	log *slog.Logger

	agent  *Agent
	cv     *convo
	client llm.Client
	budget *budgetTracker
	// opening is the first user message, built from the run's input.
	opening []llm.Block

	// totals, kept in step with what the appended events add to the run row.
	tokens    int64
	toolCalls int
	costMicro int64

	// segStart is when this claim began; flushedAt is when the active time was last written.
	segStart  time.Time
	flushedAt time.Time
	baseMs    int64
}

func (l *loop) now() time.Time { return l.r.deps.Now() }

func (l *loop) hook(point string) {
	if l.r.hook != nil {
		l.r.hook(point)
	}
}

// heartbeat touches the run until ctx ends. If the run is no longer this worker's, it cancels the loop.
func (l *loop) heartbeat(ctx context.Context, cancel context.CancelCauseFunc) {
	t := time.NewTicker(l.r.deps.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := l.st.TouchAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch)
			switch {
			case errors.Is(err, store.ErrRunFenced):
				cancel(store.ErrRunFenced)
				return
			case err != nil && ctx.Err() == nil:
				l.log.Warn("agent run heartbeat failed", "error", err)
			}
		}
	}
}

// takeActive returns the running time since the last write, in milliseconds, and starts the next interval.
func (l *loop) takeActive() int64 {
	now := l.now()
	d := now.Sub(l.flushedAt).Milliseconds()
	l.flushedAt = now
	return max(d, 0)
}

// elapsed is the time the run has been running, this claim included.
func (l *loop) elapsed() time.Duration {
	return time.Duration(l.baseMs)*time.Millisecond + l.now().Sub(l.segStart)
}

// append writes an event under this claim's epoch with the time since the last write, and keeps the in-memory totals.
func (l *loop) append(ctx context.Context, typ string, payload json.RawMessage, d store.EventDelta) (store.AgentEvent, error) {
	d.ActiveMs = l.takeActive()
	e, err := l.st.AppendAgentEvent(ctx, l.ws, l.run.ID, l.run.Epoch, typ, payload, d)
	if err != nil {
		return e, err
	}
	l.tokens += d.InputTokens + d.CacheWriteTokens + d.CacheReadTokens + d.OutputTokens
	l.toolCalls += d.ToolCalls
	l.costMicro += d.CostMicros
	return e, nil
}

// work is the body of Execute after the claim.
func (l *loop) work(ctx context.Context) (Outcome, error) {
	run := l.run
	l.segStart = l.now()
	l.flushedAt = l.segStart
	l.baseMs = run.ActiveMs
	l.tokens = run.InputTokens + run.CacheWriteTokens + run.CacheReadTokens + run.OutputTokens
	l.toolCalls = int(run.ToolCalls)
	l.costMicro = run.CostMicros

	agent, ok := l.r.deps.Agents.Get(run.Agent)
	if !ok {
		return l.fail(ctx, FailUnknownAgent, fmt.Sprintf("this worker does not know the agent %q", run.Agent), nil)
	}
	l.agent = agent

	events, err := l.st.AgentEvents(ctx, l.ws, run.ID, 0, 0)
	if err != nil {
		return "", fmt.Errorf("read events: %w", err)
	}
	l.cv, err = rebuild(events)
	if err != nil {
		return l.fail(ctx, FailCorruptLog, err.Error(), nil)
	}
	l.opening, err = agent.opening(run.Input)
	if err != nil {
		return l.fail(ctx, FailBadInput, err.Error(), nil)
	}

	client, _, err := l.r.deps.Factory.For(ctx, l.ws, run.Stage)
	if err != nil {
		return l.fail(ctx, FailModelAccess, "no model client for this run: "+err.Error(), nil)
	}
	l.client = client

	// The dollar budget is what the run row says, less anything the ledger holds for the run that the row does not: a model
	// call that was paid for and then lost to a crash before its event was written still counts.
	ledger, err := l.st.AgentRunLedgerCostMicros(ctx, l.ws, run.ID)
	if err != nil {
		return "", fmt.Errorf("read the ledger: %w", err)
	}
	spent := l.costMicro
	if ledger > spent {
		if _, err := l.append(ctx, store.EventStatus, mustJSON(statusPayload{State: stateCostFixed, AddedCostMicros: ledger - spent}), store.EventDelta{CostMicros: ledger - spent}); err != nil {
			return "", err
		}
		spent = ledger
	}
	l.budget = newBudgetTracker(run.BudgetMicros - spent)

	if _, err := l.append(ctx, store.EventStatus, mustJSON(statusPayload{State: stateRunning, Epoch: run.Epoch, Resumed: l.cv.started}), store.EventDelta{}); err != nil {
		return "", err
	}
	l.hook("claimed")
	if !l.cv.hasContext && agent.Context != nil {
		if out, done, err := l.storeContext(ctx); done || err != nil {
			return out, err
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return "", context.Cause(ctx)
		}
		cur, err := l.st.GetAgentRun(ctx, l.ws, run.ID)
		if err != nil {
			return "", fmt.Errorf("read run: %w", err)
		}
		if cur.CancelRequestedAt != nil {
			return l.cancelled(ctx)
		}
		if limit := l.overBudget(); limit != "" {
			return l.stopBudget(ctx, limit)
		}
		if t := l.cv.last(); t != nil && !t.complete() {
			if out, done, err := l.callTool(ctx, t, t.pending()[0]); done || err != nil {
				return out, err
			}
			continue
		}
		if out, done, err := l.modelStep(ctx); done || err != nil {
			return out, err
		}
	}
}

// storeContext builds the run context and stores it as an event. done says the run ended.
func (l *loop) storeContext(ctx context.Context) (Outcome, bool, error) {
	blocks, err := l.agent.Context(ctx, l.toolContext("", false))
	if err != nil {
		if ctx.Err() != nil {
			return "", true, context.Cause(ctx)
		}
		var te *ToolError
		if errors.As(err, &te) {
			out, ferr := l.fail(ctx, FailContext, "the run context could not be built: "+te.Msg, nil)
			return out, true, ferr
		}
		return "", true, fmt.Errorf("build run context: %w", err) // retried; the last attempt fails the run
	}
	wire, err := toWire(blocks)
	if err != nil {
		out, ferr := l.fail(ctx, FailContext, "the run context is invalid: "+err.Error(), nil)
		return out, true, ferr
	}
	e, err := l.append(ctx, store.EventStatus, mustJSON(statusPayload{State: stateContext, Blocks: wire}), store.EventDelta{})
	switch {
	case errors.Is(err, store.ErrPayloadTooLarge):
		out, ferr := l.fail(ctx, FailEventTooLarge, "the run context is over 64 KB", nil)
		return out, true, ferr
	case err != nil:
		return "", true, err
	}
	return "", false, l.cv.apply(e)
}

func (l *loop) toolContext(toolUseID string, resumed bool) RunContext {
	var project uuid.UUID
	if l.run.ProjectID.Valid {
		project = l.run.ProjectID.Bytes
	}
	rc := RunContext{
		WorkspaceID: l.ws, RunID: l.run.ID, ProjectID: project, Agent: l.run.Agent,
		ToolUseID: toolUseID, Resumed: resumed, Input: l.run.Input,
		Logger: l.log.With("tool_use_id", toolUseID),
	}
	if toolUseID != "" {
		rc.IdempotencyKey = l.run.ID.String() + ":" + toolUseID
	}
	rc.artifact = func(ctx context.Context, payload json.RawMessage) error {
		_, err := l.append(ctx, store.EventArtifact, payload, store.EventDelta{})
		return err
	}
	return rc
}

// overBudget returns the name of a limit the run has reached, or "". The dollar budget is enforced by reservation inside
// the llm client; this is the early exit.
func (l *loop) overBudget() string {
	run := l.run
	switch {
	case l.budget.remaining() <= 0:
		return LimitDollars
	case l.tokens >= run.BudgetTokens:
		return LimitTokens
	case l.toolCalls >= int(run.BudgetToolCalls) && l.nextCallStarts():
		return LimitToolCalls
	case l.elapsed() >= time.Duration(run.BudgetWallClockMs)*time.Millisecond:
		return LimitWallClock
	}
	return ""
}

// nextCallStarts says the next thing the loop does is start a tool call it has not started before (a resumed call was
// counted already). When the model is to be called instead, the tool-call limit is not what stops the run.
func (l *loop) nextCallStarts() bool {
	t := l.cv.last()
	if t == nil || t.complete() {
		return false
	}
	return !t.called[t.pending()[0].ID]
}

// wallDeadline is when the run's wall-clock budget runs out.
func (l *loop) wallDeadline() time.Time {
	return l.segStart.Add(time.Duration(l.run.BudgetWallClockMs-l.baseMs) * time.Millisecond)
}

// ---- the model step ---------------------------------------------------------------------------------------------

// request builds the Messages request from the conversation: the system prompt (the preamble and the agent's own) with
// the first cache breakpoint, which also covers the tools that precede it in the prompt; the run context with the second;
// and the rolling breakpoint on the last block of the conversation.
func (l *loop) request() llm.Request {
	msgs := l.cv.messages(l.opening)
	if n := len(l.cv.context); n > 0 {
		msgs[0].Content[n-1].Cache = &llm.CacheControl{}
	}
	last := &msgs[len(msgs)-1]
	content := append([]llm.Block(nil), last.Content...)
	for i := len(content) - 1; i >= 0; i-- {
		switch content[i].Type {
		case llm.BlockText, llm.BlockImage, llm.BlockToolUse, llm.BlockToolResult:
			content[i].Cache = &llm.CacheControl{}
		default:
			continue
		}
		break
	}
	last.Content = content
	return llm.Request{
		Model:     l.run.Model,
		System:    []llm.Block{{Type: llm.BlockText, Text: systemPrompt(l.agent.System), Cache: &llm.CacheControl{}}},
		Messages:  msgs,
		Tools:     l.agent.registry.Definitions(),
		MaxTokens: int(l.run.MaxTokens),
		Effort:    l.run.Effort,
		Thinking:  l.agent.Thinking,
		Run:       llm.RunInfo{ID: l.run.ID, Profile: l.run.RoutingProfile, Budget: l.budget},
	}
}

// modelStep calls the model once and handles the answer. done says the run ended or parked.
func (l *loop) modelStep(ctx context.Context) (Outcome, bool, error) {
	req := l.request()
	callCtx, cancel := context.WithDeadline(ctx, l.wallDeadline())
	l.hook("before_model_call")
	resp, err := l.client.Messages(callCtx, req)
	wallExpired := callCtx.Err() != nil && ctx.Err() == nil
	cancel()
	cost := l.budget.takeSettled()

	if err != nil && (!errors.Is(err, llm.ErrLedger) || len(resp.Content) == 0) {
		out, ferr := l.modelFailed(ctx, err, wallExpired, resp.Usage, cost)
		return out, true, ferr
	}

	payload := assistantPayload{
		StopReason: resp.StopReason, Model: cmpOr(resp.Model, l.run.Model), RequestID: resp.RequestID, CostMicros: cost,
		Usage: usagePayload{InputTokens: resp.Usage.InputTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens, OutputTokens: resp.Usage.OutputTokens},
	}
	wire, werr := toWire(resp.Content)
	if werr != nil {
		out, ferr := l.failWithUsage(ctx, FailModelError, "the model's answer could not be stored: "+werr.Error(), resp.Usage, cost)
		return out, true, ferr
	}
	payload.Content = wire
	delta := store.EventDelta{
		InputTokens: resp.Usage.InputTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		OutputTokens: resp.Usage.OutputTokens, CostMicros: cost,
	}
	e, aerr := l.append(ctx, store.EventAssistantMessage, mustJSON(payload), delta)
	switch {
	case errors.Is(aerr, store.ErrPayloadTooLarge):
		out, ferr := l.failWithUsage(ctx, FailEventTooLarge, "the model's answer is over 64 KB and cannot be stored", resp.Usage, cost)
		return out, true, ferr
	case aerr != nil:
		return "", true, aerr
	}
	l.hook("assistant_persisted")
	if err := l.cv.apply(e); err != nil {
		out, ferr := l.fail(ctx, FailCorruptLog, err.Error(), nil)
		return out, true, ferr
	}
	if err != nil { // ErrLedger: the call is paid for and its answer stored, but the books are wrong
		out, ferr := l.fail(ctx, FailLedger, err.Error(), nil)
		return out, true, ferr
	}

	switch {
	case resp.StopReason == "max_tokens":
		out, ferr := l.fail(ctx, FailMaxTokens, "the model's answer was cut off at max_tokens", nil)
		return out, true, ferr
	case resp.StopReason == "refusal":
		out, ferr := l.fail(ctx, FailRefusal, "the model declined to continue", nil)
		return out, true, ferr
	case len(resp.ToolUses()) > 0, resp.StopReason == "pause_turn":
		return "", false, nil
	}
	text := resp.Text()
	out, ferr := l.succeed(ctx, mustJSON(map[string]any{"summary": text, "stop_reason": resp.StopReason}))
	return out, true, ferr
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// modelFailed ends the run for a model call that returned an error (or retries the job, when the worker is stopping).
func (l *loop) modelFailed(ctx context.Context, err error, wallExpired bool, usage llm.Usage, cost int64) (Outcome, error) {
	if ctx.Err() != nil {
		// The worker is stopping (or lost the run). The call may have been billed; the ledger has it, and the next attempt
		// counts it in the budget. Retry.
		return "", context.Cause(ctx)
	}
	var capErr *llm.CapError
	var apiErr *llm.APIError
	var miss *llm.ReplayMissError
	switch {
	case wallExpired:
		return l.stopBudgetWithUsage(ctx, LimitWallClock, usage, cost)
	case errors.As(err, &capErr):
		switch capErr.Kind {
		case llm.CapRun:
			return l.stopBudgetWithUsage(ctx, LimitDollars, usage, cost)
		case llm.CapDaily:
			return l.failWithUsage(ctx, FailDailyCap, err.Error(), usage, cost)
		default:
			return l.failWithUsage(ctx, FailRequestCap, err.Error(), usage, cost)
		}
	case errors.Is(err, llm.ErrOutcomeUnknown):
		// The request was sent and nothing came back: it may have run and been billed. It is never sent again. The ledger
		// holds an estimated row at the reserved worst case, and the budget was charged the same.
		return l.failWithUsage(ctx, FailModelOutcomeUnknown, err.Error(), usage, cost)
	case errors.As(err, &miss):
		return l.failWithUsage(ctx, FailReplayMiss, err.Error(), usage, cost)
	case errors.Is(err, llm.ErrNoKey):
		return l.failWithUsage(ctx, FailModelAccess, err.Error(), usage, cost)
	case errors.As(err, &apiErr) && apiErr.IsAuth():
		return l.failWithUsage(ctx, FailModelAccess, err.Error(), usage, cost)
	case errors.Is(err, llm.ErrLedger):
		return l.failWithUsage(ctx, FailLedger, err.Error(), usage, cost)
	}
	return l.failWithUsage(ctx, FailModelError, err.Error(), usage, cost)
}

// ---- tool calls -------------------------------------------------------------------------------------------------

// maxResultTextBytes caps the text of one tool result, so its event stays under the payload limit with room for images.
const maxResultTextBytes = 48 * 1024

// callTool runs one tool call of the turn that has no result. done says the run ended or parked.
//
// The order is what makes a crash safe: the tool_call event is written first (and counts against the budget); the tool runs
// with the idempotency key run_id:tool_use_id; its result event is written after. A worker that dies between the two
// leaves a tool_call without a result: the next worker runs the call again, and the tool's own use of the key makes its
// effect happen once. A call that has a result is never run again.
func (l *loop) callTool(ctx context.Context, t *turn, use llm.Block) (Outcome, bool, error) {
	resumed := t.called[use.ID]
	tool, known := l.agent.registry.Get(use.Name)
	if !resumed {
		call := toolCallPayload{ToolUseID: use.ID, Name: use.Name, Input: use.Input, Mutates: known && tool.Mutates()}
		if len(call.Input) == 0 {
			call.Input = json.RawMessage("{}")
		}
		e, err := l.append(ctx, store.EventToolCall, mustJSON(call), store.EventDelta{ToolCalls: 1})
		switch {
		case errors.Is(err, store.ErrPayloadTooLarge):
			out, ferr := l.fail(ctx, FailEventTooLarge, fmt.Sprintf("the arguments of the call to %s are over 64 KB", use.Name), nil)
			return out, true, ferr
		case err != nil:
			return "", true, err
		}
		if err := l.cv.apply(e); err != nil {
			out, ferr := l.fail(ctx, FailCorruptLog, err.Error(), nil)
			return out, true, ferr
		}
		l.hook("tool_call_persisted")
	}

	var res Result
	switch {
	case !known:
		res = Errorf("there is no tool named %q; the tools are %s", use.Name, strings.Join(l.agent.registry.Names(), ", "))
	default:
		if verr := l.agent.registry.Validate(use.Name, use.Input); verr != nil {
			res = Errorf("%v", verr)
		} else {
			var err error
			res, err = l.invoke(ctx, tool, use, resumed)
			if err != nil {
				return "", true, err
			}
		}
	}
	l.hook("tool_run_done")

	if res.Ask && !res.IsError {
		return l.park(ctx, use, res)
	}
	payload, perr := l.resultPayload(use, res)
	if perr != nil {
		l.log.Error("a tool returned an invalid result", "tool", use.Name, "error", perr)
		payload = toolResultPayload{ToolUseID: use.ID, Name: use.Name, IsError: true, Content: []wireBlock{{Type: "text", Text: "The tool returned a result the runtime cannot store. This is a fault in the tool, not in your arguments."}}}
		res = Result{IsError: true}
	}
	final := res.Final && !res.IsError
	payload.Final = final
	e, err := l.append(ctx, store.EventToolResult, mustJSON(payload), store.EventDelta{})
	switch {
	case errors.Is(err, store.ErrPayloadTooLarge):
		out, ferr := l.fail(ctx, FailEventTooLarge, fmt.Sprintf("the result of %s is over 64 KB", use.Name), nil)
		return out, true, ferr
	case err != nil:
		return "", true, err
	}
	if err := l.cv.apply(e); err != nil {
		out, ferr := l.fail(ctx, FailCorruptLog, err.Error(), nil)
		return out, true, ferr
	}
	if !final {
		return "", false, nil
	}
	output := res.Output
	if len(output) == 0 || len(output) > store.MaxEventPayloadBytes || !json.Valid(output) {
		output = mustJSON(map[string]any{"summary": textOf(payload.Content)})
	}
	out, ferr := l.succeed(ctx, output)
	return out, true, ferr
}

// invoke runs the tool, turning a panic or an error into a result the model can see.
func (l *loop) invoke(ctx context.Context, tool Tool, use llm.Block, resumed bool) (res Result, err error) {
	rc := l.toolContext(use.ID, resumed)
	toolCtx, cancel := context.WithDeadline(ctx, l.wallDeadline())
	defer cancel()
	func() {
		defer func() {
			if p := recover(); p != nil {
				if _, isCrash := p.(crashSignal); isCrash {
					panic(p)
				}
				l.log.Error("a tool panicked", "tool", use.Name, "panic", fmt.Sprint(p))
				res, err = Errorf("The tool failed with an internal error."), nil
			}
		}()
		res, err = tool.Run(toolCtx, rc, use.Input)
	}()
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, context.Cause(ctx)
	}
	if toolCtx.Err() != nil && errors.Is(err, toolCtx.Err()) {
		return Errorf("The tool did not finish before the run's time ran out."), nil
	}
	var te *ToolError
	if errors.As(err, &te) {
		return Errorf("%s", te.Msg), nil
	}
	l.log.Error("a tool failed", "tool", use.Name, "error", err)
	return Errorf("The tool failed with an internal error."), nil
}

// resultPayload checks a tool's result and shapes it for storage: text and image references only, the text cut to fit.
func (l *loop) resultPayload(use llm.Block, res Result) (toolResultPayload, error) {
	blocks := make([]llm.Block, 0, len(res.Content))
	room := maxResultTextBytes
	for _, b := range res.Content {
		switch b.Type {
		case llm.BlockText:
			text := capText(b.Text, max(room, 0))
			room -= len(text)
			blocks = append(blocks, llm.TextBlock(text))
		case llm.BlockImage:
			blocks = append(blocks, llm.Block{Type: llm.BlockImage, Image: b.Image})
		default:
			return toolResultPayload{}, fmt.Errorf("a tool result holds text and image blocks, not %q", b.Type)
		}
	}
	wire, err := toWire(blocks)
	if err != nil {
		return toolResultPayload{}, err
	}
	return toolResultPayload{ToolUseID: use.ID, Name: use.Name, Content: wire, IsError: res.IsError}, nil
}

func textOf(blocks []wireBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// park stores the question of an ask_user and moves the run to waiting_input. The call stays unanswered until Reply.
func (l *loop) park(ctx context.Context, use llm.Block, res Result) (Outcome, bool, error) {
	q, _ := l.resultPayload(use, res)
	question := capText(textOf(q.Content), 8*1024)
	_, err := l.st.EndAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch, store.RunEnd{
		Status:   store.AgentWaitingInput,
		Event:    mustJSON(statusPayload{State: stateWaitingInput, ToolUseID: use.ID, Tool: use.Name, Question: question}),
		ActiveMs: l.takeActive(),
	})
	if err != nil {
		return "", true, err
	}
	return OutcomeWaiting, true, nil
}

// ---- ending a run -----------------------------------------------------------------------------------------------

func (l *loop) succeed(ctx context.Context, output json.RawMessage) (Outcome, error) {
	_, err := l.st.EndAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch, store.RunEnd{
		Status: store.AgentSucceeded, Output: output, Event: mustJSON(statusPayload{State: stateSucceeded}), ActiveMs: l.takeActive(),
	})
	if err != nil {
		return "", err
	}
	return OutcomeFinished, nil
}

func (l *loop) cancelled(ctx context.Context) (Outcome, error) {
	_, err := l.st.EndAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch, store.RunEnd{
		Status: store.AgentCancelled, Output: l.summary("cancelled", ""), Event: mustJSON(statusPayload{State: stateCancelled}), ActiveMs: l.takeActive(),
	})
	if err != nil {
		return "", err
	}
	return OutcomeFinished, nil
}

// summary is the output of a run that did not finish its work: why it stopped, what it had done, and the last thing the
// model said.
func (l *loop) summary(stopped, limit string) json.RawMessage {
	var msg string
	switch stopped {
	case FailBudget:
		msg = fmt.Sprintf("Stopped before finishing: the %s budget was used up.", strings.ReplaceAll(limit, "_", " "))
	case "cancelled":
		msg = "Cancelled before finishing."
	default:
		msg = "Stopped before finishing: " + stopped + "."
	}
	out := map[string]any{
		"stopped": stopped, "summary": msg, "tool_calls": l.toolCalls, "cost_micros": l.costMicro, "tokens": l.tokens,
		"elapsed_ms": l.elapsed().Milliseconds(),
	}
	if limit != "" {
		out["limit"] = limit
	}
	if t := l.cv; t != nil {
		if text := capText(t.lastText(), 2000); text != "" {
			out["last_message"] = text
		}
	}
	return mustJSON(out)
}

// stopBudget fails the run, cleanly, because it reached a limit: an error event, a summary as the output, status failed with
// reason budget.
func (l *loop) stopBudget(ctx context.Context, limit string) (Outcome, error) {
	return l.stopBudgetWithUsage(ctx, limit, llm.Usage{}, 0)
}

func (l *loop) stopBudgetWithUsage(ctx context.Context, limit string, usage llm.Usage, cost int64) (Outcome, error) {
	msg := fmt.Sprintf("The run reached its %s limit and was stopped.", strings.ReplaceAll(limit, "_", " "))
	if err := l.recordSpend(ctx, FailBudget, msg, usage, cost); err != nil {
		return "", err
	}
	_, err := l.st.EndAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch, store.RunEnd{
		Status: store.AgentFailed, FailureReason: FailBudget, Output: l.summary(FailBudget, limit),
		Event: mustJSON(statusPayload{State: stateFailed, Reason: FailBudget, Limit: limit}), ActiveMs: l.takeActive(),
	})
	if err != nil {
		return "", err
	}
	return OutcomeFinished, nil
}

// fail ends the run as failed with a reason and a message (the error event). output may be nil.
func (l *loop) fail(ctx context.Context, reason, message string, output json.RawMessage) (Outcome, error) {
	return l.failWithUsage(ctx, reason, message, llm.Usage{}, 0, output)
}

func (l *loop) failWithUsage(ctx context.Context, reason, message string, usage llm.Usage, cost int64, output ...json.RawMessage) (Outcome, error) {
	if err := l.recordSpend(ctx, reason, message, usage, cost); err != nil {
		return "", err
	}
	var out json.RawMessage
	if len(output) > 0 {
		out = output[0]
	}
	if len(out) == 0 {
		out = l.summary(reason, "")
	}
	_, err := l.st.EndAgentRun(ctx, l.ws, l.run.ID, l.run.Epoch, store.RunEnd{
		Status: store.AgentFailed, FailureReason: reason, Output: out,
		Event: mustJSON(statusPayload{State: stateFailed, Reason: reason}), ActiveMs: l.takeActive(),
	})
	if err != nil {
		return "", err
	}
	return OutcomeFinished, nil
}

// recordSpend writes the error event that explains a failure, with whatever the failed call cost added to the run's totals.
func (l *loop) recordSpend(ctx context.Context, code, message string, usage llm.Usage, cost int64) error {
	_, err := l.append(ctx, store.EventError, mustJSON(errorPayload{Code: code, Message: capText(message, 2000)}), store.EventDelta{
		InputTokens: usage.InputTokens, CacheWriteTokens: usage.CacheWriteTokens, CacheReadTokens: usage.CacheReadTokens,
		OutputTokens: usage.OutputTokens, CostMicros: cost,
	})
	return err
}

// crashSignal is what a test panics with to simulate a killed worker; a tool's recover must not swallow it.
type crashSignal struct{ point string }

// ---- the dollar budget ------------------------------------------------------------------------------------------

// budgetTracker is the run's llm.Budget: an in-memory reservation budget over what is left of agent_runs.budget_micros,
// which also remembers what each call settled at, so the loop can add the call's cost to the run row.
type budgetTracker struct {
	inner *llm.RunBudget

	mu      sync.Mutex
	settled int64
}

func newBudgetTracker(limit int64) *budgetTracker {
	return &budgetTracker{inner: llm.NewRunBudget(max(limit, 0))}
}

// Reserve implements llm.Budget.
func (b *budgetTracker) Reserve(micros int64) (func(actual int64), error) {
	release, err := b.inner.Reserve(micros)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func(actual int64) {
		once.Do(func() {
			b.mu.Lock()
			b.settled += max(actual, 0)
			b.mu.Unlock()
			release(actual)
		})
	}, nil
}

func (b *budgetTracker) remaining() int64 { return b.inner.Remaining() }

// takeSettled returns what calls have settled at since the last time, and resets it.
func (b *budgetTracker) takeSettled() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.settled
	b.settled = 0
	return n
}
