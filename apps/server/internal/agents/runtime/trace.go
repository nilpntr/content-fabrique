package runtime

import (
	"context"
	"encoding/json"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// The spans of a run (docs/design/observability-v1.md): one AGENT span per claim of the run (Execute), with the run's ids, routing,
// budgets and how it ended; under it the run context (CHAIN), every model call (LLM, from the llm package) and every tool call (TOOL),
// with the artifacts a tool made, the checks of check_edit and the budget notes as span events. Every span of a run shares the trace
// id of the run's UUID, so a run is found by its id, and a second claim (a resume, an answer to ask_user) adds to the same trace.
// Content (the run's input and output, tool arguments and results, the context text) is only put on spans when telemetry.Content().

// tracer is the global provider's tracer, looked up on every use so a test can install its own provider.
func tracer() trace.Tracer {
	return otel.Tracer("github.com/nilpntr/content-fabrique/internal/agents/runtime")
}

// Span attributes of our own.
const (
	AttrRunID     = attribute.Key("cf.run_id")
	AttrWorkspace = attribute.Key("cf.workspace_id")
	AttrProject   = attribute.Key("cf.project_id")
	AttrAgent     = attribute.Key("cf.agent")

	attrStage        = attribute.Key("cf.stage")
	attrProfile      = attribute.Key("cf.routing_profile")
	attrEffort       = attribute.Key("cf.effort")
	attrBilledTo     = attribute.Key("cf.billed_to")
	attrEpoch        = attribute.Key("cf.epoch")
	attrResumes      = attribute.Key("cf.resumes")
	attrBudgetMicros = attribute.Key("cf.budget.micros")
	attrBudgetTokens = attribute.Key("cf.budget.tokens")
	attrBudgetCalls  = attribute.Key("cf.budget.tool_calls")
	attrBudgetWallMs = attribute.Key("cf.budget.wall_clock_ms")
	attrStatus       = attribute.Key("cf.status")
	attrFailure      = attribute.Key("cf.failure_reason")
	attrOutcome      = attribute.Key("cf.outcome")
	attrCost         = attribute.Key("cf.cost_micros")
	attrToolCalls    = attribute.Key("cf.tool_call_count")
	attrActiveMs     = attribute.Key("cf.active_ms")
	attrToolUseID    = attribute.Key("cf.tool_use_id")
	attrMutates      = attribute.Key("cf.mutates")
	attrResumed      = attribute.Key("cf.resumed")
	attrIsError      = attribute.Key("cf.is_error")
	attrLimit        = attribute.Key("cf.limit")
)

type runSpanKey struct{}

// TraceRun sets attributes on the span of the run that ctx belongs to (an agent's tool names the playbook it chose, say). Outside a run
// it does nothing.
func TraceRun(ctx context.Context, attrs ...attribute.KeyValue) {
	if s, ok := ctx.Value(runSpanKey{}).(trace.Span); ok {
		s.SetAttributes(attrs...)
	}
}

// TraceRunEvent adds an event to the span of the run that ctx belongs to.
func TraceRunEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	if s, ok := ctx.Value(runSpanKey{}).(trace.Span); ok {
		s.AddEvent(name, trace.WithAttributes(attrs...))
	}
}

// TraceEvent adds an event to the innermost span of ctx: the tool span inside a tool, the run's span in the loop.
func TraceEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).AddEvent(name, trace.WithAttributes(attrs...))
}

// runAttrs are the attributes a run's span starts with: who, what, which model and which budgets.
func runAttrs(ws ids.WorkspaceID, run store.AgentRun, agent *Agent) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		telemetry.Kind(telemetry.KindAgent),
		AttrRunID.String(ids.Encode(ids.PrefixRun, run.ID)), AttrWorkspace.String(ws.String()), AttrAgent.String(run.Agent),
		attrStage.String(run.Stage), attrProfile.String(run.RoutingProfile), telemetry.LLMModelName.String(run.Model),
		attrEffort.String(run.Effort), attrBilledTo.String(run.BilledTo), attrEpoch.Int64(run.Epoch), attrResumes.Int(int(run.Resumes)),
		attrBudgetMicros.Int64(run.BudgetMicros), attrBudgetTokens.Int64(run.BudgetTokens), attrBudgetCalls.Int(int(run.BudgetToolCalls)),
		attrBudgetWallMs.Int64(run.BudgetWallClockMs),
	}
	session := ids.Encode(ids.PrefixRun, run.ID)
	if !run.ProjectID.IsZero() {
		attrs = append(attrs, AttrProject.String(run.ProjectID.String()))
		session = run.ProjectID.String() // Phoenix groups the runs of a project as one session
	}
	attrs = append(attrs, telemetry.SessionID.String(session))
	if agent != nil && agent.TraceAttrs != nil {
		attrs = append(attrs, agent.TraceAttrs(run.Input)...)
	}
	if telemetry.Content() && len(run.Input) > 0 {
		attrs = append(attrs, telemetry.JSONValue(telemetry.InputValue, json.RawMessage(run.Input))...)
	}
	return attrs
}

// startRunSpan starts the span of one claim of a run, in the run's trace.
func startRunSpan(ctx context.Context, ws ids.WorkspaceID, run store.AgentRun, agents *Agents) (context.Context, trace.Span) {
	agent, _ := agents.Get(run.Agent)
	ctx = telemetry.WithTraceID(ctx, telemetry.RunTraceID(run.ID))
	ctx, span := tracer().Start(ctx, "agent.run "+run.Agent, trace.WithNewRoot(), trace.WithAttributes(runAttrs(ws, run, agent)...))
	return context.WithValue(ctx, runSpanKey{}, span), span
}

// endRunSpan writes how the claim ended, from the run row as it is now.
func endRunSpan(ctx context.Context, st *store.Store, ws ids.WorkspaceID, run store.AgentRun, span trace.Span, out Outcome, err error) {
	defer span.End()
	if out != "" {
		span.SetAttributes(attrOutcome.String(string(out)))
	}
	rctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if cur, gerr := st.GetAgentRun(rctx, ws, run.ID); gerr == nil {
		prompt := cur.InputTokens + cur.CacheWriteTokens + cur.CacheReadTokens
		span.SetAttributes(
			attrStatus.String(cur.Status), attrCost.Int64(cur.CostMicros), telemetry.LLMCostTotal.Float64(float64(cur.CostMicros)/1e6),
			telemetry.LLMTokensPrompt.Int64(prompt), telemetry.LLMTokensCompletion.Int64(cur.OutputTokens),
			telemetry.LLMTokensTotal.Int64(prompt+cur.OutputTokens), telemetry.LLMTokensCacheRead.Int64(cur.CacheReadTokens),
			telemetry.LLMTokensCacheWrite.Int64(cur.CacheWriteTokens), attrToolCalls.Int(int(cur.ToolCalls)), attrActiveMs.Int64(cur.ActiveMs),
		)
		if cur.FailureReason != nil {
			span.SetAttributes(attrFailure.String(*cur.FailureReason))
		}
		if telemetry.Content() && len(cur.Output) > 0 && json.Valid(cur.Output) {
			span.SetAttributes(telemetry.JSONValue(telemetry.OutputValue, json.RawMessage(cur.Output))...)
		}
		if cur.Status == store.AgentFailed {
			reason := "failed"
			if cur.FailureReason != nil {
				reason = *cur.FailureReason
			}
			span.SetStatus(codes.Error, reason)
			return
		}
	}
	if err != nil {
		llm.RecordError(span, err)
		return
	}
	span.SetStatus(codes.Ok, "")
}

// startToolSpan starts the span of one tool call.
func startToolSpan(ctx context.Context, use llm.Block, mutates, resumed bool) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		telemetry.Kind(telemetry.KindTool), telemetry.ToolName.String(use.Name), attrToolUseID.String(use.ID),
		attrMutates.Bool(mutates), attrResumed.Bool(resumed),
	}
	if telemetry.Content() {
		input := use.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		attrs = append(attrs, telemetry.JSONString(telemetry.ToolParameters, input))
		attrs = append(attrs, telemetry.JSONValue(telemetry.InputValue, input)...)
	}
	return tracer().Start(ctx, "tool "+use.Name, trace.WithAttributes(attrs...))
}

// endToolSpan writes the result of a tool call.
func endToolSpan(span trace.Span, p toolResultPayload) {
	defer span.End()
	span.SetAttributes(attrIsError.Bool(p.IsError))
	if telemetry.Content() {
		span.SetAttributes(telemetry.Text(telemetry.OutputValue, textOf(p.Content))...)
	}
	if p.IsError {
		span.SetStatus(codes.Error, "the tool answered with an error")
		return
	}
	span.SetStatus(codes.Ok, "")
}

// traceArtifact adds an artifact a tool recorded to the tool's span: its kind and the ids it names (a timeline version, a render). The
// rest of its data (a creative plan's text) is content.
func traceArtifact(ctx context.Context, payload json.RawMessage) {
	var a struct {
		Kind      string          `json:"kind"`
		ToolUseID string          `json:"tool_use_id"`
		Data      json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &a) != nil {
		return
	}
	trace.SpanFromContext(ctx).AddEvent("artifact", trace.WithAttributes(artifactAttrs(a.Kind, a.Data)...))
}

// artifactAttrs are the attributes of an artifact event.
func artifactAttrs(kind string, data json.RawMessage) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String("cf.artifact.kind", kind)}
	var d map[string]json.RawMessage
	if json.Unmarshal(data, &d) == nil {
		for _, k := range []string{"version", "duration_ms", "render_id", "id", "nth", "passed", "failed", "skipped"} {
			if v, ok := d[k]; ok {
				var n json.Number
				var s string
				switch {
				case json.Unmarshal(v, &n) == nil:
					if i, err := n.Int64(); err == nil {
						attrs = append(attrs, attribute.Int64("cf.artifact."+k, i))
					}
				case json.Unmarshal(v, &s) == nil:
					attrs = append(attrs, attribute.String("cf.artifact."+k, telemetry.Cap(s, 200)))
				}
			}
		}
	}
	if telemetry.Content() {
		attrs = append(attrs, telemetry.JSONString("cf.artifact.data", data))
	}
	return attrs
}

// The budget events of a run's span.
func traceLimit(ctx context.Context, limit string, graceful bool) {
	TraceRunEvent(ctx, "budget.limit_reached", attrLimit.String(limit), attribute.Bool("cf.graceful", graceful))
}
