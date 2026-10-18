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
	"net/url"
	"strings"
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
	// TraceContent turns content capture on (SetContent): the agent spans then carry prompts, answers, tool arguments and results and
	// thumbnails. config.Load sets it from OTEL_TRACE_CONTENT, only in the local environment (ContentAllowed).
	TraceContent bool
	// ExplicitEndpoint, for a process whose settings do not come from its own environment (cfctl reads them from the env file), passes
	// OTLPEndpoint and Headers to the exporter instead of letting the SDK read OTEL_EXPORTER_OTLP_ENDPOINT and _HEADERS itself. The
	// api and the worker leave it false.
	ExplicitEndpoint bool
	// Headers are the OTLP headers when ExplicitEndpoint is set (OTEL_EXPORTER_OTLP_HEADERS, parsed by ParseHeaders).
	Headers map[string]string
}

// PhoenixProject is the project Phoenix files the spans of the local stack under (the openinference.project.name resource attribute,
// set only in the local environment).
const PhoenixProject = "content-fabrique"

// Setup installs the global logger and tracer provider. The returned shutdown function flushes pending spans
// and must be called before the process exits.
func Setup(ctx context.Context, opts Options) (*slog.Logger, func(context.Context) error, error) {
	logger := NewLogger(opts)
	slog.SetDefault(logger)
	shutdown, err := SetupTracing(ctx, opts, logger)
	if err != nil {
		return nil, nil, err
	}
	return logger, shutdown, nil
}

// SetupTracing installs the global tracer provider (and content capture) without touching the logger: cfctl, which prints to the
// terminal, uses it. logger receives exporter errors.
func SetupTracing(ctx context.Context, opts Options, logger *slog.Logger) (func(context.Context) error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	SetContent(opts.TraceContent)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// Exporter problems (an unreachable collector, say) go to the logger, at most once a minute.
	otel.SetErrorHandler(newThrottledErrorHandler(logger, time.Minute))

	attrs := []attribute.KeyValue{
		attribute.String("service.name", opts.Service),
		attribute.String("service.version", opts.Version),
		attribute.String("deployment.environment.name", opts.Env),
	}
	if opts.Env == "local" {
		attrs = append(attrs, attribute.String("openinference.project.name", PhoenixProject))
	}
	res, err := resource.New(ctx, resource.WithAttributes(attrs...), resource.WithTelemetrySDK())
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	// The id generator lets an agent run's spans use the run's id as their trace id (ids.go).
	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithIDGenerator(IDGenerator{}), sdktrace.WithRawSpanLimits(SpanLimits())}
	if opts.OTLPEndpoint != "" {
		var expOpts []otlptracehttp.Option
		if opts.ExplicitEndpoint {
			expOpts = append(expOpts, otlptracehttp.WithEndpointURL(TracesURL(opts.OTLPEndpoint)))
			if len(opts.Headers) > 0 {
				expOpts = append(expOpts, otlptracehttp.WithHeaders(opts.Headers))
			}
		}
		exp, err := otlptracehttp.New(ctx, expOpts...)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		var batch []sdktrace.BatchSpanProcessorOption
		if opts.TraceContent {
			// Spans with content are large (an LLM span can hold a few hundred KB of messages): smaller batches keep each request small.
			batch = append(batch, sdktrace.WithMaxExportBatchSize(16))
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exp, batch...))
		logger.Info("trace export enabled", "endpoint", opts.OTLPEndpoint, "content", opts.TraceContent)
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// SpanLimits are the tracer provider's limits. The SDK's default of 128 attributes a span would cut an LLM span's messages (each part is
// two attributes, flattened as OpenInference has them); the sizes are bounded by the caps of content.go instead (MaxSpanContentBytes).
func SpanLimits() sdktrace.SpanLimits {
	l := sdktrace.NewSpanLimits()
	l.AttributeCountLimit = 4096
	l.EventCountLimit = 512
	l.AttributePerEventCountLimit = 64
	return l
}

// TracesURL is the OTLP/HTTP traces URL of a base endpoint, as the SDK builds it from OTEL_EXPORTER_OTLP_ENDPOINT: the base with
// /v1/traces appended.
func TracesURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + "/v1/traces"
}

// ParseHeaders reads OTEL_EXPORTER_OTLP_HEADERS: comma-separated key=value pairs, values percent-encoded. A malformed pair is skipped.
func ParseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if dec, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = dec
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
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
