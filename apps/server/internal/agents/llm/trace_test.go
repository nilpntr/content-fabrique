package llm

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// recordSpans installs a recording tracer provider for the test, and turns content capture on or off.
func recordSpans(t *testing.T, content bool) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithRawSpanLimits(telemetry.SpanLimits()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	telemetry.SetContent(content)
	t.Cleanup(func() { otel.SetTracerProvider(prev); telemetry.SetContent(false) })
	return sr
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value
	}
	return m
}

func llmSpanOf(t *testing.T, sr *tracetest.SpanRecorder, run uuid.UUID) map[string]attribute.Value {
	t.Helper()
	for _, s := range sr.Ended() {
		a := spanAttrs(s)
		if a["cf.run_id"].AsString() == run.String() && a["openinference.span.kind"].AsString() == "LLM" {
			return a
		}
	}
	t.Fatalf("no LLM span for run %s among %d spans", run, len(sr.Ended()))
	return nil
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 384, 216))
	for y := range 216 {
		for x := range 384 {
			img.Set(x, y, color.RGBA{uint8(x), 80, uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fixedImages struct{ data []byte }

func (f fixedImages) ResolveImage(context.Context, string) (string, []byte, error) {
	return "image/jpeg", f.data, nil
}

func TestModelCallSpanWithoutContentIsMetadataOnly(t *testing.T) {
	sr := recordSpans(t, false)
	rg := newRig(t, nil)
	rg.api.messages = func(int, recordedCall) fakeReply {
		return okReply("the secret plan of the creator", apiUsage{Input: 1000, CacheRead: 5000, Output: 200})
	}
	run := uuid.New()
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Messages[0].Content = []Block{TextBlock("BRIEF: my private brief")}
	req.Run = RunInfo{ID: run, Budget: NewRunBudget(1_000_000)}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	a := llmSpanOf(t, sr, run)
	if a["llm.model_name"].AsString() != "claude-haiku-5-5" || a["llm.token_count.prompt"].AsInt64() != 6000 ||
		a["llm.token_count.completion"].AsInt64() != 200 || a["llm.token_count.prompt_details.cache_read"].AsInt64() != 5000 {
		t.Errorf("usage attributes: %v", a)
	}
	if a["cf.cost_micros"].AsInt64() <= 0 || a["cf.reserved_micros"].AsInt64() < a["cf.cost_micros"].AsInt64() || a["cf.max_tokens_sent"].AsInt64() != 1000 {
		t.Errorf("cost %d, reserved %d, max_tokens_sent %d", a["cf.cost_micros"].AsInt64(), a["cf.reserved_micros"].AsInt64(), a["cf.max_tokens_sent"].AsInt64())
	}
	if a["cf.stop_reason"].AsString() != "end_turn" || !strings.Contains(a["llm.invocation_parameters"].AsString(), `"max_tokens":1000`) {
		t.Errorf("stop reason %q, parameters %q", a["cf.stop_reason"].AsString(), a["llm.invocation_parameters"].AsString())
	}
	for k, v := range a {
		if strings.HasPrefix(k, "llm.input_messages") || strings.HasPrefix(k, "llm.output_messages") || k == "input.value" || k == "output.value" {
			t.Errorf("content attribute %s without content capture", k)
		}
		if s := v.String(); strings.Contains(s, "private brief") || strings.Contains(s, "secret plan") || strings.Contains(s, testKey) {
			t.Errorf("%s leaks content or the key: %s", k, s)
		}
	}
}

func TestModelCallSpanWithContent(t *testing.T) {
	sr := recordSpans(t, true)
	rg := newRig(t, nil, func(o *FactoryOptions) { o.Images = fixedImages{data: testJPEG(t)} })
	run := uuid.New()
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.System = []Block{TextBlock("You edit videos.")}
	req.Messages = []Message{
		{Role: RoleUser, Content: []Block{TextBlock("look"), {Type: BlockImage, Image: &Image{MediaType: "image/jpeg", Ref: "ws/x/old.jpg"}}}},
		{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "toolu_1", Name: "view_clip", Input: []byte(`{"asset_id":"ast_1"}`)}}},
		{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "toolu_1", Content: []Block{
			TextBlock("frames, see https://r2.example/x.jpg?X-Amz-Signature=deadbeef"), {Type: BlockImage, Image: &Image{MediaType: "image/jpeg", Ref: "ws/x/new.jpg"}},
		}}}},
	}
	req.Run = RunInfo{ID: run}
	if _, err := c.Messages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	a := llmSpanOf(t, sr, run)
	if a["llm.input_messages.0.message.role"].AsString() != "system" || a["llm.input_messages.0.message.contents.0.message_content.text"].AsString() != "You edit videos." {
		t.Errorf("system message: %v", a)
	}
	if got := a["llm.input_messages.1.message.contents.1.message_content.text"].AsString(); got != "[image ws/x/old.jpg]" {
		t.Errorf("an older image is %q, want its reference", got)
	}
	if a["llm.input_messages.2.message.tool_calls.0.tool_call.function.name"].AsString() != "view_clip" {
		t.Errorf("tool call: %v", a["llm.input_messages.2.message.tool_calls.0.tool_call.function.name"])
	}
	if a["llm.input_messages.3.message.role"].AsString() != "tool" || a["llm.input_messages.3.message.tool_call_id"].AsString() != "toolu_1" {
		t.Errorf("tool result message: %v / %v", a["llm.input_messages.3.message.role"], a["llm.input_messages.3.message.tool_call_id"])
	}
	if url := a["llm.input_messages.3.message.contents.1.message_content.image.image.url"].AsString(); !strings.HasPrefix(url, "data:image/jpeg;base64,") || len(url) > telemetry.MaxThumbnailBytes {
		t.Errorf("the newest image is not a small thumbnail: %.60s (%d bytes)", url, len(url))
	}
	if txt := a["llm.input_messages.3.message.contents.0.message_content.text"].AsString(); strings.Contains(txt, "deadbeef") || !strings.Contains(txt, "?[redacted]") {
		t.Errorf("a presigned URL was not redacted: %q", txt)
	}
	if a["llm.output_messages.0.message.contents.0.message_content.text"].AsString() != "hello" || a["output.value"].AsString() != "hello" {
		t.Errorf("output: %v / %v", a["llm.output_messages.0.message.contents.0.message_content.text"], a["output.value"])
	}
}

func TestRefusedCallSpanSaysWhichCap(t *testing.T) {
	sr := recordSpans(t, false)
	rg := newRig(t, nil)
	run := uuid.New()
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	req := simpleRequest()
	req.Model = "claude-opus-5-5"
	req.MaxTokens = 20000
	req.Run = RunInfo{ID: run, Budget: NewRunBudget(1000)}
	if _, err := c.Messages(context.Background(), req); err == nil {
		t.Fatal("the call was not refused")
	}
	a := llmSpanOf(t, sr, run)
	if a["cf.refused_by"].AsString() != string(CapRun) {
		t.Errorf("refused_by = %q", a["cf.refused_by"].AsString())
	}
	for _, s := range sr.Ended() {
		if spanAttrs(s)["cf.run_id"].AsString() == run.String() && s.Status().Code.String() != "Error" {
			t.Errorf("status = %v", s.Status())
		}
	}
}
