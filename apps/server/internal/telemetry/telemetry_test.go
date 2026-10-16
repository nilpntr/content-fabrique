package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestLoggerAddsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(Options{Service: ServiceAPI, LogOutput: &buf})

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	logger.InfoContext(ctx, "inside span")
	logger.Info("no ctx")

	dec := json.NewDecoder(&buf)
	var inside, outside map[string]any
	if err := dec.Decode(&inside); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&outside); err != nil {
		t.Fatal(err)
	}
	if inside["trace_id"] != span.SpanContext().TraceID().String() || inside["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("missing or wrong ids: %v", inside)
	}
	if inside["service"] != ServiceAPI {
		t.Errorf("service = %v", inside["service"])
	}
	if _, ok := outside["trace_id"]; ok {
		t.Errorf("unexpected trace_id without span: %v", outside)
	}
}

func TestLoggerKeepsIDsThroughWith(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(Options{LogOutput: &buf}).With("k", "v").WithGroup("g")
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	logger.InfoContext(ctx, "x", "a", 1)
	if !bytes.Contains(buf.Bytes(), []byte(span.SpanContext().TraceID().String())) {
		t.Errorf("trace id missing: %s", buf.String())
	}
}

func TestSetupWithoutEndpoint(t *testing.T) {
	var buf bytes.Buffer
	logger, shutdown, err := Setup(context.Background(), Options{
		Service: ServiceCfctl, Env: "local", LogOutput: &buf, LogLevel: slog.LevelInfo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("trace export enabled")) {
		t.Errorf("export should be off: %s", buf.String())
	}
	_ = logger
}
