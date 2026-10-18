package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestContentAllowedOnlyLocally(t *testing.T) {
	for _, c := range []struct {
		env, val string
		want     bool
	}{
		{"local", "1", true}, {"local", " 1 ", true}, {"local", "", false}, {"local", "0", false}, {"local", "true", false},
		{"ci", "1", false}, {"bench", "1", false}, {"staging", "1", false}, {"production", "1", false}, {"", "1", false},
	} {
		if got := ContentAllowed(c.env, c.val); got != c.want {
			t.Errorf("ContentAllowed(%q, %q) = %v", c.env, c.val, got)
		}
	}
}

func TestRedactRemovesSecrets(t *testing.T) {
	in := `get https://bucket.r2.cloudflarestorage.com/ws/a/b.mp4?X-Amz-Algorithm=AWS4&X-Amz-Credential=AKIA%2F2026&X-Amz-Signature=abcdef0123 now
and http://localhost:9000/cf-media-dev/x.jpg?token=s3cr3t-value plus Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig
key sk-ant-api03-ABCDEFGHIJKLMNOP and sk_test_abcdefgh12345 and whsec_abcdefghijk; plain https://example.com/page?q=shoes stays`
	out := Redact(in)
	for _, leak := range []string{"X-Amz-Signature", "abcdef0123", "AKIA", "s3cr3t-value", "eyJhbGciOiJIUzI1NiJ9", "sk-ant-api03", "sk_test_abcdefgh", "whsec_abcdefghijk"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived: %s", leak, out)
		}
	}
	for _, keep := range []string{"https://bucket.r2.cloudflarestorage.com/ws/a/b.mp4?[redacted]", "http://localhost:9000/cf-media-dev/x.jpg?[redacted]", "https://example.com/page?q=shoes"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q missing: %s", keep, out)
		}
	}
}

func TestCapKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("é", 100) // two bytes a rune
	got := Cap(s, 51)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, strings.Repeat("é", 25)) || !strings.Contains(got, "cut 150 bytes") {
		t.Errorf("Cap = %q", got)
	}
	if Cap("short", 10) != "short" {
		t.Error("a short string was changed")
	}
}

func TestIDGeneratorHonoursTheContext(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithIDGenerator(IDGenerator{}))
	run := uuid.New()
	ctx := WithTraceID(context.Background(), RunTraceID(run))
	ctx, root := tp.Tracer("t").Start(ctx, "root")
	_, child := tp.Tracer("t").Start(WithSpanID(ctx, DerivedSpanID("x")), "child")
	child.End()
	root.End()
	_, free := tp.Tracer("t").Start(context.Background(), "free")
	free.End()
	spans := sr.Ended()
	if got := spans[1].SpanContext().TraceID(); got != trace.TraceID(run) {
		t.Errorf("root trace id = %s, want the run's %s", got, run)
	}
	if spans[0].SpanContext().TraceID() != trace.TraceID(run) || spans[0].SpanContext().SpanID() != DerivedSpanID("x") {
		t.Errorf("child ids = %s/%s", spans[0].SpanContext().TraceID(), spans[0].SpanContext().SpanID())
	}
	if spans[2].SpanContext().TraceID() == trace.TraceID(run) || !spans[2].SpanContext().IsValid() {
		t.Errorf("a span without a chosen id got %s", spans[2].SpanContext().TraceID())
	}
	if a1, a2 := DerivedTraceID("a"), DerivedTraceID("a"); a1 != a2 || a1 == DerivedTraceID("b") {
		t.Error("derived ids are not stable or not distinct")
	}
}

func TestThumbnailIsSmall(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 384, 683))
	for y := range 683 {
		for x := range 384 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	url, err := Thumbnail(buf.Bytes(), ThumbLongSide, MaxThumbnailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") || len(url) > MaxThumbnailBytes {
		t.Fatalf("thumbnail %d bytes: %.40s", len(url), url)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Height != ThumbLongSide || cfg.Width != 384*ThumbLongSide/683 {
		t.Errorf("thumbnail is %dx%d (%v)", cfg.Width, cfg.Height, err)
	}
	if _, err := Thumbnail([]byte("not an image"), ThumbLongSide, MaxThumbnailBytes); err == nil {
		t.Error("garbage made a thumbnail")
	}
}

func TestMessagesFitTheSpan(t *testing.T) {
	big := strings.Repeat("x", MaxTextBytes)
	var msgs []Message
	msgs = append(msgs, Message{Role: "system", Parts: []Part{{Text: "system prompt"}}}, Message{Role: "user", Parts: []Part{{Text: "context"}}})
	for range 60 {
		msgs = append(msgs, Message{Role: "assistant", Parts: []Part{{Text: big}}})
	}
	msgs = append(msgs, Message{Role: "tool", ToolCallID: "toolu_1", Parts: []Part{{Text: "newest"}}})
	fit := fitMessages(msgs)
	size := 0
	for _, m := range fit {
		size += messageSize(m)
	}
	if size > MaxSpanContentBytes+1024 {
		t.Errorf("%d bytes kept", size)
	}
	if fit[0].Role != "system" || fit[1].Parts[0].Text != "context" || fit[len(fit)-1].Parts[0].Text != "newest" {
		t.Errorf("head or tail lost: %+v / %+v", fit[0], fit[len(fit)-1])
	}
	if !strings.Contains(fit[2].Parts[0].Text, "earlier messages left out") {
		t.Errorf("no note of what was left out: %q", fit[2].Parts[0].Text)
	}
	attrs := Messages("llm.input_messages", msgs)
	found := false
	for _, a := range attrs {
		if string(a.Key) == "llm.input_messages.0.message.role" && a.Value.AsString() == "system" {
			found = true
		}
	}
	if !found {
		t.Error("flattened messages lack the system role")
	}
}

func TestParseHeaders(t *testing.T) {
	h := ParseHeaders("Authorization=Bearer%20abc, X-Axiom-Dataset=cf-local,bad,=x")
	if h["Authorization"] != "Bearer abc" || h["X-Axiom-Dataset"] != "cf-local" || len(h) != 2 {
		t.Errorf("headers = %v", h)
	}
	if TracesURL("http://localhost:6006/") != "http://localhost:6006/v1/traces" {
		t.Error(TracesURL("http://localhost:6006/"))
	}
}
