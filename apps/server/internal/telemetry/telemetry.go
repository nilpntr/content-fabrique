// Package telemetry sets up structured logging (slog) and OpenTelemetry tracing.
//
// Tracing exports over OTLP/HTTP only when OTEL_EXPORTER_OTLP_ENDPOINT is set. The SDK's exporter reads that
// variable and OTEL_EXPORTER_OTLP_HEADERS itself. When the endpoint is empty, spans are still created (so log
// lines carry trace_id and span_id) but nothing is exported and nothing is logged about it.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Service names, set in code rather than through OTEL_SERVICE_NAME.
const (
	ServiceAPI    = "cf-api"
	ServiceWorker = "cf-worker"
	ServiceCfctl  = "cf-cfctl"
)

// Options configures Setup.
type Options struct {
	// Service is the service.name resource attribute, e.g. ServiceAPI.
	Service string
	// Version is the service.version resource attribute.
	Version string
	// Env is the deployment environment (CF_ENV).
	Env string
	// OTLPEndpoint is OTEL_EXPORTER_OTLP_ENDPOINT. Empty means no export.
	OTLPEndpoint string
	// LogOutput receives log lines.
	LogOutput io.Writer
	// LogLevel is the minimum log level.
	LogLevel slog.Level
	// LogText selects the human-readable text format instead of JSON (used locally).
	LogText bool
}

// Setup installs the global logger and tracer provider. The returned shutdown function flushes pending spans
// and must be called before the process exits.
func Setup(ctx context.Context, opts Options) (*slog.Logger, func(context.Context) error, error) {
	logger := NewLogger(opts)
	slog.SetDefault(logger)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// Exporter problems (an unreachable collector, say) go to the logger, at most once a minute.
	otel.SetErrorHandler(newThrottledErrorHandler(logger, time.Minute))

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", opts.Service),
			attribute.String("service.version", opts.Version),
			attribute.String("deployment.environment.name", opts.Env),
		),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry resource: %w", err)
	}

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if opts.OTLPEndpoint != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exp))
		logger.Info("trace export enabled", "endpoint", opts.OTLPEndpoint)
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error { return tp.Shutdown(ctx) }
	return logger, shutdown, nil
}

// NewLogger builds a slog logger that adds trace_id and span_id to records logged with a context that has an
// active span (use the *Context methods: InfoContext and friends).
func NewLogger(opts Options) *slog.Logger {
	out := opts.LogOutput
	if out == nil {
		out = io.Discard
	}
	ho := &slog.HandlerOptions{Level: opts.LogLevel}
	var h slog.Handler
	if opts.LogText {
		h = slog.NewTextHandler(out, ho)
	} else {
		h = slog.NewJSONHandler(out, ho)
	}
	l := slog.New(&traceHandler{Handler: h})
	if opts.Service != "" {
		l = l.With("service", opts.Service)
	}
	return l
}

// traceHandler decorates records with the active span's identifiers.
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
}

type throttledErrorHandler struct {
	logger   *slog.Logger
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newThrottledErrorHandler(l *slog.Logger, interval time.Duration) otel.ErrorHandler {
	return &throttledErrorHandler{logger: l, interval: interval}
}

func (h *throttledErrorHandler) Handle(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if now := time.Now(); h.last.IsZero() || now.Sub(h.last) >= h.interval {
		h.last = now
		h.logger.Warn("opentelemetry error", "error", err)
	}
}
