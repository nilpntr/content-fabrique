package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// The replay of a stored run as spans (cfctl trace export): the same tree the live run would have made, built from the run's events,
// its usage ledger rows and its artifacts, at the times the events were written, without calling a model or running a tool. Runs made
// before the spans existed, or runs whose live spans were not exported, can so be read in the trace viewer.
//
// The ids are derived from the run's id (telemetry.DerivedTraceID of "replay/<run>", a span id per event), so exporting a run twice
// sends the same spans again, which the viewer stores once. Content follows telemetry.Content(), as it does for live spans.

// ExportInput is a stored run.
type ExportInput struct {
	Workspace ids.WorkspaceID
	Run       store.AgentRun
	Events    []store.AgentEvent
	// Ledger are the run's usage_ledger rows: an answer is matched to its row by request id, for the price table version and whether
	// the cost was estimated; a row with no stored answer (a call that failed) is a span of its own.
	Ledger []store.UsageRow
	// Agent, when known, gives the first user message (its Opening) and the system prompt; the system prompt is shown when the run's
	// input names no prompt version, or the same one as SystemVersion.
	Agent         *Agent
	SystemVersion string
	// Images, when set, resolves the image references of each call's newest message, for thumbnails (content only).
	Images llm.ImageResolver
	// MaxThumbnailCalls bounds how many calls get thumbnails (each is a read from storage); 0 means all.
	MaxThumbnailCalls int
}

// ExportResult says what was exported.
type ExportResult struct {
	TraceID trace.TraceID
	Spans   int
}

// ReplayTraceID is the trace id of the replay of a run.
func ReplayTraceID(run fmt.Stringer) trace.TraceID {
	return telemetry.DerivedTraceID("replay/" + run.String())
}

type exporter struct {
	in     ExportInput
	tracer trace.Tracer
	root   trace.Span
	rootCx context.Context
	spans  int
}

// ExportTrace replays a stored run as spans of tp. tp must use telemetry.IDGenerator, so that the ids are the derived ones.
func ExportTrace(ctx context.Context, tp trace.TracerProvider, in ExportInput) (ExportResult, error) {
	if len(in.Events) == 0 {
		return ExportResult{}, fmt.Errorf("run %s has no events", ids.Encode(ids.PrefixRun, in.Run.ID))
	}
	x := &exporter{in: in, tracer: tp.Tracer("github.com/nilpntr/content-fabrique/internal/agents/runtime/replay")}
	tid := ReplayTraceID(in.Run.ID)
	start := in.Run.CreatedAt
	if in.Run.StartedAt != nil {
		start = *in.Run.StartedAt
	}
	start = minTime(start, in.Events[0].CreatedAt)
	end := in.Events[len(in.Events)-1].CreatedAt
	if in.Run.FinishedAt != nil && in.Run.FinishedAt.After(end) {
		end = *in.Run.FinishedAt
	}

	rctx := telemetry.WithSpanID(telemetry.WithTraceID(ctx, tid), x.spanID("root"))
	attrs := append(runAttrs(in.Workspace, in.Run, in.Agent), attribute.Bool("cf.replay", true))
	rctx, x.root = x.tracer.Start(rctx, "agent.run "+in.Run.Agent, trace.WithNewRoot(), trace.WithTimestamp(start), trace.WithAttributes(attrs...))
	x.rootCx = rctx
	x.spans++

	if err := x.events(); err != nil {
		x.root.End(trace.WithTimestamp(end))
		return ExportResult{}, err
	}
	x.finishRoot()
	x.root.End(trace.WithTimestamp(end))
	return ExportResult{TraceID: tid, Spans: x.spans}, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (x *exporter) spanID(name string) trace.SpanID {
	return telemetry.DerivedSpanID("replay/" + x.in.Run.ID.String() + "/" + name)
}

func (x *exporter) child(name string, id string, start time.Time, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	x.spans++
	ctx := telemetry.WithSpanID(x.rootCx, x.spanID(id))
	return x.tracer.Start(ctx, name, trace.WithTimestamp(start), trace.WithAttributes(attrs...))
}

func (x *exporter) finishRoot() {
	r := x.in.Run
	prompt := r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens
	x.root.SetAttributes(
		attrStatus.String(r.Status), attrCost.Int64(r.CostMicros), telemetry.LLMCostTotal.Float64(float64(r.CostMicros)/1e6),
		telemetry.LLMTokensPrompt.Int64(prompt), telemetry.LLMTokensCompletion.Int64(r.OutputTokens), telemetry.LLMTokensTotal.Int64(prompt+r.OutputTokens),
		telemetry.LLMTokensCacheRead.Int64(r.CacheReadTokens), telemetry.LLMTokensCacheWrite.Int64(r.CacheWriteTokens),
		attrToolCalls.Int(int(r.ToolCalls)), attrActiveMs.Int64(r.ActiveMs),
	)
	if telemetry.Content() && len(r.Output) > 0 && json.Valid(r.Output) {
		x.root.SetAttributes(telemetry.JSONValue(telemetry.OutputValue, json.RawMessage(r.Output))...)
	}
	if r.FailureReason != nil {
		x.root.SetAttributes(attrFailure.String(*r.FailureReason))
		x.root.SetStatus(codes.Error, *r.FailureReason)
		return
	}
	if r.Status == store.AgentSucceeded {
		x.root.SetStatus(codes.Ok, "")
	}
}

type openTool struct {
	span trace.Span
	ctx  context.Context
}

// events walks the run's events in order and makes the spans.
func (x *exporter) events() error {
	in := x.in
	var opening []llm.Block
	if in.Agent != nil {
		if o, err := in.Agent.opening(in.Run.Input); err == nil {
			opening = o
		}
	}
	// The system prompt is the agent's as this build has it: shown when the run names no prompt version (it has one text) or the same
	// version as this build.
	var system string
	if v := promptVersionOf(in.Run.Input); in.Agent != nil && (v == "" || v == in.SystemVersion) {
		system = systemPrompt(in.Agent.System)
	}
	byRequest := map[string]store.UsageRow{}
	for _, row := range in.Ledger {
		if row.RequestID != "" {
			byRequest[row.RequestID] = row
		}
	}
	matched := map[string]bool{}

	c := &convo{}
	tools := map[string]openTool{}
	prev := in.Events[0].CreatedAt
	var callStart *time.Time
	var reserved int64
	calls, thumbCalls := 0, 0
	for _, e := range in.Events {
		at := e.CreatedAt
		switch e.Type {
		case store.EventStatus:
			var p statusPayload
			_ = json.Unmarshal(e.Payload, &p)
			switch p.State {
			case stateContext:
				_, span := x.child("agent.context", fmt.Sprint(e.Seq), prev, telemetry.Kind(telemetry.KindChain))
				if telemetry.Content() {
					span.SetAttributes(telemetry.Text(telemetry.OutputValue, textOf(p.Blocks))...)
				}
				span.SetStatus(codes.Ok, "")
				span.End(trace.WithTimestamp(at))
			case stateModelCall:
				t := at
				callStart, reserved = &t, p.ReservedMicros
			default:
				x.statusEvent(p, at)
			}
		case store.EventAssistantMessage:
			var p assistantPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return fmt.Errorf("event %d: %w", e.Seq, err)
			}
			start := prev
			if callStart != nil {
				start = *callStart
			}
			calls++
			thumbs := in.Images != nil && telemetry.Content() && (in.MaxThumbnailCalls == 0 || thumbCalls < in.MaxThumbnailCalls)
			if thumbs {
				thumbCalls++
			}
			x.llmSpan(e, p, c.messages(opening), system, start, reserved, byRequest, matched, thumbs)
			callStart, reserved = nil, 0
		case store.EventToolCall:
			var p toolCallPayload
			_ = json.Unmarshal(e.Payload, &p)
			ctx, span := x.child("tool "+p.Name, fmt.Sprint(e.Seq), at,
				telemetry.Kind(telemetry.KindTool), telemetry.ToolName.String(p.Name), attrToolUseID.String(p.ToolUseID), attrMutates.Bool(p.Mutates))
			if telemetry.Content() {
				span.SetAttributes(telemetry.JSONString(telemetry.ToolParameters, p.Input))
				span.SetAttributes(telemetry.JSONValue(telemetry.InputValue, p.Input)...)
			}
			tools[p.ToolUseID] = openTool{span: span, ctx: ctx}
		case store.EventToolResult:
			var p toolResultPayload
			_ = json.Unmarshal(e.Payload, &p)
			ot, ok := tools[p.ToolUseID]
			if !ok { // a cut-off answer's calls get results without running
				_, span := x.child("tool "+p.Name, fmt.Sprint(e.Seq), at, telemetry.Kind(telemetry.KindTool), telemetry.ToolName.String(p.Name),
					attrToolUseID.String(p.ToolUseID), attribute.Bool("cf.not_run", true))
				ot = openTool{span: span}
			}
			delete(tools, p.ToolUseID)
			ot.span.SetAttributes(attrIsError.Bool(p.IsError))
			if telemetry.Content() {
				ot.span.SetAttributes(telemetry.Text(telemetry.OutputValue, textOf(p.Content))...)
			}
			if p.IsError {
				ot.span.SetStatus(codes.Error, "the tool answered with an error")
			} else {
				ot.span.SetStatus(codes.Ok, "")
			}
			ot.span.End(trace.WithTimestamp(at))
		case store.EventArtifact:
			var a struct {
				Kind      string          `json:"kind"`
				ToolUseID string          `json:"tool_use_id"`
				Data      json.RawMessage `json:"data"`
			}
			_ = json.Unmarshal(e.Payload, &a)
			span := x.root
			if ot, ok := tools[a.ToolUseID]; ok {
				span = ot.span
			}
			span.AddEvent("artifact", trace.WithTimestamp(at), trace.WithAttributes(artifactAttrs(a.Kind, a.Data)...))
			checkEvents(span, a.Kind, a.Data, at)
		case store.EventError:
			var p errorPayload
			_ = json.Unmarshal(e.Payload, &p)
			x.root.AddEvent("error", trace.WithTimestamp(at), trace.WithAttributes(
				attribute.String("cf.error.code", p.Code), attribute.String("cf.error.message", telemetry.Clean(p.Message, 2000))))
		}
		if err := c.apply(e); err != nil {
			return err
		}
		prev = at
	}
	// A call that was started and has no result: the worker stopped during it.
	for id, ot := range tools {
		ot.span.SetAttributes(attribute.Bool("cf.no_result", true))
		ot.span.SetStatus(codes.Error, "the call has no stored result")
		ot.span.End(trace.WithTimestamp(prev))
		delete(tools, id)
	}
	// Ledger rows of calls whose answer was not stored (refused after usage, outcome unknown).
	for _, row := range in.Ledger {
		if row.Kind != "" && row.Kind != store.KindLLM {
			continue
		}
		if row.RequestID != "" && matched[row.RequestID] {
			continue
		}
		_, span := x.child("llm."+row.Stage+" (no stored answer)", "ledger/"+row.IdempotencyKey, row.CreatedAt,
			telemetry.Kind(telemetry.KindLLM), telemetry.LLMModelName.String(row.Model), attribute.String("cf.stage", row.Stage))
		x.usageAttrs(span, row.InputTokens, row.CacheWriteTokens, row.CacheReadTokens, row.OutputTokens, row.ListCostMicros)
		span.SetAttributes(attribute.String("cf.price_table_version", row.PriceTableVersion), attribute.Bool("cf.estimated", row.Estimated))
		span.End(trace.WithTimestamp(row.CreatedAt))
	}
	x.root.SetAttributes(attribute.Int("cf.model_calls", calls))
	return nil
}

func (x *exporter) usageAttrs(span trace.Span, in, cw, cr, out, cost int64) {
	prompt := in + cw + cr
	span.SetAttributes(
		telemetry.LLMTokensPrompt.Int64(prompt), telemetry.LLMTokensCompletion.Int64(out), telemetry.LLMTokensTotal.Int64(prompt+out),
		telemetry.LLMTokensCacheRead.Int64(cr), telemetry.LLMTokensCacheWrite.Int64(cw),
		attrCost.Int64(cost), telemetry.LLMCostTotal.Float64(float64(cost)/1e6),
	)
}

// llmSpan is one stored model call: the conversation as it stood (msgs), the stored answer and its usage.
func (x *exporter) llmSpan(e store.AgentEvent, p assistantPayload, msgs []llm.Message, system string, start time.Time, reserved int64,
	byRequest map[string]store.UsageRow, matched map[string]bool, thumbs bool) {
	r := x.in.Run
	model := cmpOr(p.Model, r.Model)
	_, span := x.child("llm."+r.Stage, fmt.Sprint(e.Seq), start,
		telemetry.Kind(telemetry.KindLLM), telemetry.LLMSystem.String("anthropic"), telemetry.LLMProvider.String("anthropic"),
		telemetry.LLMModelName.String(model), attribute.String("cf.stage", r.Stage), attribute.String("cf.stop_reason", p.StopReason),
		attribute.Bool("cf.shortened", p.Shortened), attribute.Int64("cf.reserved_micros", reserved), attribute.Int("cf.messages", len(msgs)),
	)
	defer span.End(trace.WithTimestamp(e.CreatedAt))
	x.usageAttrs(span, p.Usage.InputTokens, p.Usage.CacheWriteTokens, p.Usage.CacheReadTokens, p.Usage.OutputTokens, p.CostMicros)
	req := llm.Request{Model: model, MaxTokens: int(r.MaxTokens), MinOutputTokens: int(r.MinOutputTokens), Effort: r.Effort, Messages: msgs}
	if x.in.Agent != nil {
		req.Thinking = x.in.Agent.Thinking
	}
	span.SetAttributes(telemetry.JSONString(telemetry.LLMInvocationParameters, llm.TraceInvocationParameters(req)),
		attribute.Int("cf.max_tokens_asked", int(r.MaxTokens)))
	if p.RequestID != "" {
		span.SetAttributes(attribute.String("cf.request_id", p.RequestID))
		if row, ok := byRequest[p.RequestID]; ok {
			matched[p.RequestID] = true
			span.SetAttributes(attribute.String("cf.price_table_version", row.PriceTableVersion), attribute.Bool("cf.estimated", row.Estimated),
				attribute.String("cf.billed_to", row.BilledTo), attribute.Int64("cf.ledger_cost_micros", row.ListCostMicros))
		}
	}
	content := fromWire(p.Content)
	var names []string
	for _, b := range content {
		if b.Type == llm.BlockToolUse {
			names = append(names, b.Name)
		}
	}
	if len(names) > 0 {
		span.SetAttributes(attribute.StringSlice("cf.tool_calls", names))
	}
	if !telemetry.Content() {
		span.SetStatus(codes.Ok, "")
		return
	}
	if system != "" {
		req.System = []llm.Block{llm.TextBlock(system)}
	} else if v := promptVersionOf(r.Input); v != "" {
		span.SetAttributes(attribute.String("cf.system_prompt", "not shown: the run used "+v+", this build has "+x.in.SystemVersion))
	}
	if thumbs && len(req.Messages) > 0 {
		x.resolveNewest(&req.Messages[len(req.Messages)-1])
	}
	span.SetAttributes(telemetry.Messages("llm.input_messages", llm.TraceInputMessages(req))...)
	if len(req.Messages) > 0 {
		var parts []string
		for _, b := range req.Messages[len(req.Messages)-1].Content {
			if b.Type == llm.BlockText {
				parts = append(parts, b.Text)
			}
			for _, c := range b.Content {
				if c.Type == llm.BlockText {
					parts = append(parts, c.Text)
				}
			}
		}
		span.SetAttributes(telemetry.Text(telemetry.InputValue, telemetry.Cap(strings.Join(parts, "\n"), 4096))...)
	}
	out, thinking := llm.TraceOutputMessage(content)
	span.SetAttributes(telemetry.Messages("llm.output_messages", []telemetry.Message{out})...)
	if thinking != "" {
		span.SetAttributes(attribute.String("cf.thinking", telemetry.Clean(thinking, telemetry.MaxValueBytes)))
	}
	var summary []string
	for _, part := range out.Parts {
		summary = append(summary, part.Text)
	}
	for _, tc := range out.ToolCalls {
		summary = append(summary, tc.Name+" "+tc.Arguments)
	}
	span.SetAttributes(telemetry.Text(telemetry.OutputValue, strings.Join(summary, "\n"))...)
	span.SetStatus(codes.Ok, "")
}

// resolveNewest reads the bytes of the images of a message (a copy), so the span can show them as thumbnails.
func (x *exporter) resolveNewest(m *llm.Message) {
	ctx := llm.WithWorkspace(context.Background(), x.in.Workspace)
	resolve := func(b llm.Block) llm.Block {
		if b.Type != llm.BlockImage || b.Image == nil || b.Image.Ref == "" || len(b.Image.Data) > 0 {
			return b
		}
		mt, data, err := x.in.Images.ResolveImage(ctx, b.Image.Ref)
		if err != nil {
			return b
		}
		b.Image = &llm.Image{MediaType: mt, Data: data, Ref: b.Image.Ref}
		return b
	}
	content := make([]llm.Block, len(m.Content))
	for i, b := range m.Content {
		b = resolve(b)
		if len(b.Content) > 0 {
			inner := make([]llm.Block, len(b.Content))
			for j, c := range b.Content {
				inner[j] = resolve(c)
			}
			b.Content = inner
		}
		content[i] = b
	}
	m.Content = content
}

// statusEvent is a status event on the run's span.
func (x *exporter) statusEvent(p statusPayload, at time.Time) {
	attrs := []attribute.KeyValue{attribute.String("cf.state", p.State)}
	name := "status"
	switch p.State {
	case stateRunning:
		name = "claimed"
		attrs = append(attrs, attrEpoch.Int64(p.Epoch), attrResumed.Bool(p.Resumed))
	case "limit_reached":
		name = "budget.limit_reached"
		attrs = append(attrs, attrLimit.String(p.Limit), attribute.Bool("cf.graceful", true))
	case stateFailed:
		name = "failed"
		attrs = append(attrs, attrFailure.String(p.Reason))
		if p.Limit != "" {
			attrs = append(attrs, attrLimit.String(p.Limit))
		}
	case stateSucceeded:
		name = "succeeded"
		if p.Limit != "" {
			attrs = append(attrs, attrLimit.String(p.Limit))
		}
	case stateCutOff:
		name = "cut_off"
	case stateNudge:
		name = "nudge"
		if telemetry.Content() {
			attrs = append(attrs, attribute.String("cf.nudge", telemetry.Clean(textOf(p.Blocks), telemetry.MaxTextBytes)))
		}
	case stateCostFixed:
		name = "cost_reconciled"
		attrs = append(attrs, attribute.Int64("cf.added_cost_micros", p.AddedCostMicros))
	case stateWaitingInput:
		name = "waiting_input"
		attrs = append(attrs, telemetry.ToolName.String(p.Tool))
		if telemetry.Content() {
			attrs = append(attrs, attribute.String("cf.question", telemetry.Clean(p.Question, telemetry.MaxTextBytes)))
		}
	case stateCancelled:
		name = "cancelled"
	}
	x.root.AddEvent(name, trace.WithTimestamp(at), trace.WithAttributes(attrs...))
}

// checkEvents adds the checks of an artifact that holds a check report ({"checks": [{"id", "status", "detail"}]}, the editor's
// edit_check) as events, as the live check_edit span has them.
func checkEvents(span trace.Span, kind string, data json.RawMessage, at time.Time) {
	var rec struct {
		Version int `json:"version"`
		Nth     int `json:"nth"`
		Checks  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if json.Unmarshal(data, &rec) != nil || len(rec.Checks) == 0 {
		return
	}
	var failing []string
	for _, c := range rec.Checks {
		attrs := []attribute.KeyValue{attribute.String("cf.check.id", c.ID), attribute.String("cf.check.status", c.Status)}
		if telemetry.Content() {
			attrs = append(attrs, attribute.String("cf.check.detail", telemetry.Clean(c.Detail, 1000)))
		}
		if c.Status == "fail" {
			failing = append(failing, c.ID)
		}
		span.AddEvent("check", trace.WithTimestamp(at), trace.WithAttributes(attrs...))
	}
	span.AddEvent(kind, trace.WithTimestamp(at), trace.WithAttributes(attribute.Int("cf.check.version", rec.Version),
		attribute.Int("cf.check.nth", rec.Nth), attribute.StringSlice("cf.check.failing", failing)))
}

// promptVersionOf reads "prompt_version" from a run's input, "" when it has none.
func promptVersionOf(input json.RawMessage) string {
	var in struct {
		PromptVersion string `json:"prompt_version"`
	}
	_ = json.Unmarshal(input, &in)
	return in.PromptVersion
}
