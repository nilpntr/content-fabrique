package editor

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel/attribute"

	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// What the editor adds to the run's trace (docs/design/observability-v1.md): the prompt version and vibe on the run's span, the plan's
// playbook once plan_edit has chosen it, every check of check_edit as an event of its tool span, and a note when the budget runs low.

// traceAttrs are the run span's attributes from the run's input (runtime.Agent.TraceAttrs): metadata only.
func traceAttrs(raw json.RawMessage) []attribute.KeyValue {
	in, err := parseInput(raw)
	if err != nil {
		return nil
	}
	return []attribute.KeyValue{
		attribute.String("cf.prompt_version", in.PromptVersion),
		attribute.String("cf.vibe", VibeOr(in.Vibe)),
		attribute.Bool("cf.has_instructions", in.Instructions != ""),
	}
}

// tracePlan names the plan's choices on the run's span.
func tracePlan(ctx context.Context, p Plan) {
	runtime.TraceRun(ctx,
		attribute.String("cf.playbook", p.Playbook), attribute.String("cf.playbook_version", p.PlaybookVersion),
		attribute.String("cf.content_type", p.ContentType), attribute.Int("cf.target_length_s", p.TargetLengthS),
		attribute.Int("cf.tempo_bpm", p.TempoBPM), attribute.String("cf.pace", p.Pace), attribute.Int("cf.planned_shots", len(p.Shots)),
	)
}

// traceChecks adds the checks of a check_edit to its tool span: one event per check with its status, and its one-line reason when
// content capture is on (a reason can quote the creator's title).
func traceChecks(ctx context.Context, report Report, nth int) {
	pass, fail, skip := report.Counts()
	runtime.TraceEvent(ctx, "check_edit", attribute.Int("cf.check.version", report.Version), attribute.Int("cf.check.nth", nth),
		attribute.Int("cf.check.passed", pass), attribute.Int("cf.check.failed", fail), attribute.Int("cf.check.skipped", skip),
		attribute.StringSlice("cf.check.failing", report.Failed()))
	for _, c := range report.Checks {
		attrs := []attribute.KeyValue{attribute.String("cf.check.id", c.ID), attribute.String("cf.check.status", string(c.Status))}
		if telemetry.Content() {
			attrs = append(attrs, attribute.String("cf.check.detail", telemetry.Clean(c.Detail, 1000)))
		}
		runtime.TraceEvent(ctx, "check", attrs...)
	}
	if report.Style != nil {
		if raw, err := json.Marshal(report.Style); err == nil {
			runtime.TraceEvent(ctx, "style_distance", attribute.String("cf.style_distance", telemetry.Cap(string(raw), 4000)))
		}
	}
}

// traceBudgetLow notes, on the span of the tool whose answer says so, that the run told the model it is running out.
func traceBudgetLow(ctx context.Context, l runtime.BudgetLeft, perCall int64) {
	if !l.Known || !budgetLow(l, perCall) {
		return
	}
	runtime.TraceEvent(ctx, "budget.low", attribute.Int64("cf.left.micros", l.Micros), attribute.Int("cf.left.tool_calls", l.ToolCalls),
		attribute.Int64("cf.left.wall_ms", l.Wall.Milliseconds()), attribute.Int64("cf.per_call_micros", perCall))
}
