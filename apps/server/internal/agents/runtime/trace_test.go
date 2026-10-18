package runtime

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

func newRecorder() (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	sr := tracetest.NewSpanRecorder()
	return sr, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithIDGenerator(telemetry.IDGenerator{}),
		sdktrace.WithRawSpanLimits(telemetry.SpanLimits()))
}

// recordSpans installs a recording global tracer provider for the test, and sets content capture.
func recordSpans(t *testing.T, content bool) *tracetest.SpanRecorder {
	t.Helper()
	sr, tp := newRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	telemetry.SetContent(content)
	t.Cleanup(func() { otel.SetTracerProvider(prev); telemetry.SetContent(false) })
	return sr
}

func attrsOf(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value
	}
	return m
}

func spansIn(sr *tracetest.SpanRecorder, tid trace.TraceID) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.SpanContext().TraceID() == tid {
			out = append(out, s)
		}
	}
	return out
}

func byName(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

// A run is one trace whose id is the run's UUID: the AGENT span with the run's routing, budgets and end, and a TOOL span for each call
// under it. Content capture puts the arguments and results on the tool spans.
func TestRunSpans(t *testing.T) {
	for _, content := range []bool{false, true} {
		sr := recordSpans(t, content)
		e := newEnv(t, newScripted(noteAndFinish()...))
		run := e.create("echo", "hello", Budgets{})
		if got := e.execute(run); got != OutcomeFinished {
			t.Fatalf("outcome = %s", got)
		}
		spans := spansIn(sr, telemetry.RunTraceID(run.ID))
		roots := byName(spans, "agent.run echo")
		if len(roots) != 1 {
			t.Fatalf("content %v: %d run spans in the run's trace (%d spans)", content, len(roots), len(spans))
		}
		root := attrsOf(roots[0])
		if root["openinference.span.kind"].AsString() != "AGENT" || root["cf.status"].AsString() != "succeeded" || root["cf.cost_micros"].AsInt64() != 200 ||
			root["cf.run_id"].AsString() != ids.Encode(ids.PrefixRun, run.ID) || root["cf.budget.tool_calls"].AsInt64() != int64(run.BudgetToolCalls) || root["cf.outcome"].AsString() != "finished" {
			t.Errorf("content %v: run span attributes %v", content, root)
		}
		for _, name := range []string{"tool note", "tool finish"} {
			got := byName(spans, name)
			if len(got) != 1 || got[0].Parent().SpanID() != roots[0].SpanContext().SpanID() {
				t.Fatalf("content %v: %s spans %d, or not under the run", content, name, len(got))
			}
			a := attrsOf(got[0])
			if a["openinference.span.kind"].AsString() != "TOOL" || a["tool.name"].AsString() != strings.TrimPrefix(name, "tool ") {
				t.Errorf("%s attributes %v", name, a)
			}
			_, hasParams := a["tool.parameters"]
			_, hasOut := a["output.value"]
			if hasParams != content || hasOut != content {
				t.Errorf("content %v: %s has parameters %v, output %v", content, name, hasParams, hasOut)
			}
		}
		if content {
			note := attrsOf(byName(spans, "tool note")[0])
			if !strings.Contains(note["tool.parameters"].AsString(), "hello") || note["output.value"].AsString() != "noted" {
				t.Errorf("note span content: %v", note)
			}
			if !strings.Contains(root["input.value"].AsString(), "hello") {
				t.Errorf("run input not on the span: %v", root["input.value"])
			}
		} else if _, ok := root["input.value"]; ok {
			t.Error("the run's input is on its span without content capture")
		}
	}
}

// The replay of a stored run: the same tree from the events, with derived ids (the same on every export), at the events' times.
func TestExportTraceReplaysAStoredRun(t *testing.T) {
	telemetry.SetContent(true)
	t.Cleanup(func() { telemetry.SetContent(false) })
	e := newEnv(t, newScripted(noteAndFinish()...))
	run := e.create("echo", "hello", Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	stored := e.get(run)
	events := e.events(run)
	agent, _ := e.agents.Get("echo")

	export := func() []sdktrace.ReadOnlySpan {
		sr, tp := newRecorder()
		res, err := ExportTrace(context.Background(), tp, ExportInput{Workspace: e.ws.ID, Run: stored, Events: events, Agent: agent})
		if err != nil {
			t.Fatal(err)
		}
		if res.TraceID != ReplayTraceID(run.ID) || res.Spans != 5 {
			t.Fatalf("result %+v", res)
		}
		return sr.Ended()
	}
	spans := export()
	if len(spans) != 5 {
		t.Fatalf("%d spans", len(spans))
	}
	root := byName(spans, "agent.run echo")
	llms := byName(spans, "llm."+stored.Stage)
	if len(root) != 1 || len(llms) != 2 || len(byName(spans, "tool note")) != 1 || len(byName(spans, "tool finish")) != 1 {
		t.Fatalf("spans: %v", names(spans))
	}
	rootID := root[0].SpanContext().SpanID()
	for _, s := range spans {
		if s.SpanContext().TraceID() != ReplayTraceID(run.ID) {
			t.Errorf("%s is in trace %s", s.Name(), s.SpanContext().TraceID())
		}
		if s != root[0] && s.Parent().SpanID() != rootID {
			t.Errorf("%s is not under the run", s.Name())
		}
		if s.EndTime().Before(s.StartTime()) {
			t.Errorf("%s ends before it starts", s.Name())
		}
	}
	first := attrsOf(llms[0])
	if first["llm.token_count.completion"].AsInt64() == 0 || first["cf.reserved_micros"].AsInt64() == 0 || first["cf.stop_reason"].AsString() != "tool_use" {
		t.Errorf("first call: %v", first)
	}
	if !strings.Contains(first["llm.input_messages.1.message.contents.0.message_content.text"].AsString(), "hello") {
		t.Errorf("the first call's user message: %v", first)
	}
	if first["llm.output_messages.0.message.tool_calls.0.tool_call.function.name"].AsString() != "note" {
		t.Errorf("the first call's answer: %v", first)
	}
	if a := attrsOf(root[0]); a["cf.status"].AsString() != "succeeded" || !a["cf.replay"].AsBool() {
		t.Errorf("run span: %v", a)
	}
	// Again: the same ids.
	again := export()
	ids := map[trace.SpanID]bool{}
	for _, s := range spans {
		ids[s.SpanContext().SpanID()] = true
	}
	for _, s := range again {
		if !ids[s.SpanContext().SpanID()] {
			t.Errorf("a second export made a new span id for %s", s.Name())
		}
	}
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name())
	}
	return out
}
