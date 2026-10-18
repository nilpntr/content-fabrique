package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// Every model call is an LLM span (OpenInference conventions, docs/design/observability-v1.md): the model, the parameters that were
// sent (max_tokens after the budget fitted it), what was reserved against the caps, the usage, the cost and the stop reason. With
// content capture on (telemetry.Content, local only) it also carries the messages: the system prompt, the conversation with every
// image as its storage reference and the new ones as small thumbnails, the thinking summary and the answer.

// tracer is the global provider's tracer, looked up on every use so a test can install its own provider.
func tracer() trace.Tracer {
	return otel.Tracer("github.com/nilpntr/content-fabrique/internal/agents/llm")
}

// Span attributes of our own, next to the OpenInference ones.
const (
	attrStage          = attribute.Key("cf.stage")
	attrWorkspace      = attribute.Key("cf.workspace_id")
	attrRun            = attribute.Key("cf.run_id")
	attrMode           = attribute.Key("cf.llm_mode")
	attrBilledTo       = attribute.Key("cf.billed_to")
	attrReserved       = attribute.Key("cf.reserved_micros")
	attrReservedBudget = attribute.Key("cf.reserved_budget_micros")
	attrMaxTokensSent  = attribute.Key("cf.max_tokens_sent")
	attrMaxTokensAsked = attribute.Key("cf.max_tokens_asked")
	attrShortened      = attribute.Key("cf.shortened")
	attrCost           = attribute.Key("cf.cost_micros")
	attrPriceVersion   = attribute.Key("cf.price_table_version")
	attrEstimated      = attribute.Key("cf.estimated")
	attrStopReason     = attribute.Key("cf.stop_reason")
	attrRequestID      = attribute.Key("cf.request_id")
	attrMessages       = attribute.Key("cf.messages")
	attrImages         = attribute.Key("cf.images")
	attrTools          = attribute.Key("cf.tools")
	attrToolCalls      = attribute.Key("cf.tool_calls")
	attrThinking       = attribute.Key("cf.thinking")
	attrRefusedBy      = attribute.Key("cf.refused_by")
)

// callTrace collects what the call learned on the way, for the span.
type callTrace struct {
	asked     int
	req       Request // as sent (after the image references were resolved and max_tokens fitted)
	sent      bool
	reserved  int64
	budgetEst int64
	shortened bool
	cost      int64
	version   string
	estimated bool
}

func (m *metered) startSpan(ctx context.Context, req Request) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		telemetry.Kind(telemetry.KindLLM),
		telemetry.LLMSystem.String("anthropic"), telemetry.LLMProvider.String("anthropic"), telemetry.LLMModelName.String(req.Model),
		attrStage.String(m.stage), attrWorkspace.String(m.ws.String()), attrMode.String(string(m.mode)), attrBilledTo.String(string(m.billing)),
		attrMessages.Int(len(req.Messages)), attrImages.Int(countImages(req)),
	}
	if req.Run.ID != uuid.Nil {
		attrs = append(attrs, attrRun.String(req.Run.ID.String()))
	}
	if len(req.Tools) > 0 {
		names := make([]string, len(req.Tools))
		for i, t := range req.Tools {
			names[i] = t.Name
		}
		attrs = append(attrs, attrTools.StringSlice(names))
	}
	return tracer().Start(ctx, "llm."+m.stage, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

// endSpan writes what the call came to.
func (m *metered) endSpan(span trace.Span, ct *callTrace, resp Response, err error) {
	if ct.sent {
		span.SetAttributes(
			attrReserved.Int64(ct.reserved), attrReservedBudget.Int64(ct.budgetEst), attrShortened.Bool(ct.shortened),
			attrMaxTokensSent.Int(ct.req.MaxTokens), attrMaxTokensAsked.Int(ct.asked),
			telemetry.JSONString(telemetry.LLMInvocationParameters, invocationParameters(ct.req)),
		)
		if telemetry.Content() {
			span.SetAttributes(telemetry.Messages("llm.input_messages", inputMessages(ct.req))...)
			if last := lastUserText(ct.req); last != "" {
				span.SetAttributes(telemetry.Text(telemetry.InputValue, last)...)
			}
		}
	}
	u := resp.Usage
	if !u.IsZero() || ct.estimated {
		prompt := u.InputTokens + u.CacheWriteTokens + u.CacheReadTokens
		span.SetAttributes(
			telemetry.LLMTokensPrompt.Int64(prompt), telemetry.LLMTokensCompletion.Int64(u.OutputTokens),
			telemetry.LLMTokensTotal.Int64(prompt+u.OutputTokens),
			telemetry.LLMTokensCacheRead.Int64(u.CacheReadTokens), telemetry.LLMTokensCacheWrite.Int64(u.CacheWriteTokens),
			attrCost.Int64(ct.cost), telemetry.LLMCostTotal.Float64(float64(ct.cost)/1e6),
			attrPriceVersion.String(ct.version), attrEstimated.Bool(ct.estimated),
		)
	}
	if resp.StopReason != "" {
		span.SetAttributes(attrStopReason.String(resp.StopReason))
	}
	if resp.RequestID != "" {
		span.SetAttributes(attrRequestID.String(resp.RequestID))
	}
	if resp.Model != "" {
		span.SetAttributes(telemetry.LLMModelName.String(resp.Model))
	}
	if uses := resp.ToolUses(); len(uses) > 0 {
		names := make([]string, len(uses))
		for i, u := range uses {
			names[i] = u.Name
		}
		span.SetAttributes(attrToolCalls.StringSlice(names))
	}
	if telemetry.Content() && len(resp.Content) > 0 {
		out, thinking := outputMessage(resp)
		span.SetAttributes(telemetry.Messages("llm.output_messages", []telemetry.Message{out})...)
		if thinking != "" {
			span.SetAttributes(attrThinking.String(telemetry.Clean(thinking, telemetry.MaxValueBytes)))
		}
		span.SetAttributes(telemetry.Text(telemetry.OutputValue, outputSummary(resp))...)
	}
	if err != nil {
		var ce *CapError
		if errors.As(err, &ce) {
			span.SetAttributes(attrRefusedBy.String(string(ce.Kind)))
			span.AddEvent("spend_cap_refused", trace.WithAttributes(attrRefusedBy.String(string(ce.Kind)),
				attribute.Int64("cf.estimate_micros", ce.EstimateMicros), attribute.Int64("cf.room_micros", ce.LimitMicros)))
		}
		RecordError(span, err)
		return
	}
	span.SetStatus(codes.Ok, "")
}

// TraceInputMessages is a request in OpenInference's message shape, as an LLM span shows it (the replay of a stored run builds its spans
// with it). Images with bytes in the newest message become thumbnails; the others are named by their reference.
func TraceInputMessages(req Request) []telemetry.Message { return inputMessages(req) }

// TraceOutputMessage is an answer's content in OpenInference's message shape, and its thinking summary on its own.
func TraceOutputMessage(content []Block) (telemetry.Message, string) {
	return outputMessage(Response{Content: content})
}

// TraceInvocationParameters are the parameters of a request, as an LLM span's llm.invocation_parameters shows them.
func TraceInvocationParameters(req Request) map[string]any { return invocationParameters(req) }

// RecordError marks a span failed with the error's text, redacted (an error can quote a URL).
func RecordError(span trace.Span, err error) {
	msg := telemetry.Clean(err.Error(), 2000)
	span.RecordError(errors.New(msg))
	span.SetStatus(codes.Error, telemetry.Cap(msg, 200))
}

func invocationParameters(req Request) map[string]any {
	p := map[string]any{"max_tokens": req.MaxTokens}
	if req.Effort != "" {
		p["effort"] = req.Effort
	}
	if req.MinOutputTokens > 0 {
		p["min_output_tokens"] = req.MinOutputTokens
	}
	if t := req.Thinking; t != nil {
		th := map[string]any{"type": string(t.Mode)}
		if t.BudgetTokens > 0 {
			th["budget_tokens"] = t.BudgetTokens
		}
		if t.Display != "" {
			th["display"] = t.Display
		}
		p["thinking"] = th
	}
	if tc := req.ToolChoice; tc != nil {
		p["tool_choice"] = map[string]any{"type": tc.Type, "name": tc.Name}
	}
	if len(req.StopSequences) > 0 {
		p["stop_sequences"] = req.StopSequences
	}
	return p
}

func countImages(req Request) int {
	n := 0
	var walk func([]Block)
	walk = func(bs []Block) {
		for _, b := range bs {
			if b.Type == BlockImage {
				n++
			}
			walk(b.Content)
		}
	}
	for _, msg := range req.Messages {
		walk(msg.Content)
	}
	return n
}

// inputMessages is the request in OpenInference's shape: the system prompt, then the messages. The images of the newest message (the
// ones this call is the first to send) are thumbnails, at most telemetry.MaxThumbnails; every other image is its storage reference.
func inputMessages(req Request) []telemetry.Message {
	var out []telemetry.Message
	if len(req.System) > 0 {
		var sys []string
		for _, b := range req.System {
			sys = append(sys, b.Text)
		}
		out = append(out, telemetry.Message{Role: "system", Parts: []telemetry.Part{{Text: strings.Join(sys, "\n\n")}}})
	}
	thumbs := 0
	for i, msg := range req.Messages {
		newest := i == len(req.Messages)-1
		img := func(b Block) telemetry.Part {
			if newest && thumbs < telemetry.MaxThumbnails && b.Image != nil && len(b.Image.Data) > 0 {
				if url, err := telemetry.Thumbnail(b.Image.Data, telemetry.ThumbLongSide, telemetry.MaxThumbnailBytes); err == nil {
					thumbs++
					return telemetry.Part{ImageURL: url}
				}
			}
			return telemetry.Part{Text: "[image " + imageName(b.Image) + "]"}
		}
		if msg.Role == RoleAssistant {
			m := telemetry.Message{Role: "assistant"}
			for _, b := range msg.Content {
				switch b.Type {
				case BlockText:
					m.Parts = append(m.Parts, telemetry.Part{Text: b.Text})
				case BlockThinking:
					if b.Thinking != "" {
						m.Parts = append(m.Parts, telemetry.Part{Text: "[thinking] " + b.Thinking})
					}
				case BlockToolUse:
					m.ToolCalls = append(m.ToolCalls, telemetry.ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
				}
			}
			out = append(out, m)
			continue
		}
		// A user message: text and images go in one message; each tool result is a message of its own (role tool).
		user := telemetry.Message{Role: "user"}
		for _, b := range msg.Content {
			switch b.Type {
			case BlockText:
				user.Parts = append(user.Parts, telemetry.Part{Text: b.Text})
			case BlockImage:
				user.Parts = append(user.Parts, img(b))
			case BlockToolResult:
				tm := telemetry.Message{Role: "tool", ToolCallID: b.ToolUseID}
				if b.IsError {
					tm.Parts = append(tm.Parts, telemetry.Part{Text: "[error]"})
				}
				for _, c := range b.Content {
					switch c.Type {
					case BlockText:
						tm.Parts = append(tm.Parts, telemetry.Part{Text: c.Text})
					case BlockImage:
						tm.Parts = append(tm.Parts, img(c))
					}
				}
				out = append(out, tm)
			}
		}
		if len(user.Parts) > 0 {
			out = append(out, user)
		}
	}
	return out
}

func imageName(im *Image) string {
	switch {
	case im == nil:
		return "?"
	case im.Ref != "":
		return im.Ref
	}
	return fmt.Sprintf("%s, %d bytes", im.MediaType, len(im.Data))
}

// lastUserText is the text of the newest message, for the span's input.value (what a trace list shows).
func lastUserText(req Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	var parts []string
	for _, b := range req.Messages[len(req.Messages)-1].Content {
		switch b.Type {
		case BlockText:
			parts = append(parts, b.Text)
		case BlockToolResult:
			for _, c := range b.Content {
				if c.Type == BlockText {
					parts = append(parts, c.Text)
				}
			}
		}
	}
	return telemetry.Cap(strings.Join(parts, "\n"), 4096)
}

// outputMessage is the answer, and the thinking summary on its own.
func outputMessage(resp Response) (telemetry.Message, string) {
	m := telemetry.Message{Role: "assistant"}
	var thinking []string
	for _, b := range resp.Content {
		switch b.Type {
		case BlockText:
			m.Parts = append(m.Parts, telemetry.Part{Text: b.Text})
		case BlockThinking:
			if b.Thinking != "" {
				thinking = append(thinking, b.Thinking)
			}
		case BlockToolUse:
			m.ToolCalls = append(m.ToolCalls, telemetry.ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
		}
	}
	return m, strings.Join(thinking, "\n\n")
}

// outputSummary is the answer as one text: what it said and which tools it called with what.
func outputSummary(resp Response) string {
	var parts []string
	if t := resp.Text(); t != "" {
		parts = append(parts, t)
	}
	for _, u := range resp.ToolUses() {
		args := string(u.Input)
		if !json.Valid(u.Input) {
			args = "{}"
		}
		parts = append(parts, u.Name+" "+args)
	}
	return strings.Join(parts, "\n")
}
