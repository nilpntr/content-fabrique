package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Trace and span ids that can be chosen. An agent run's spans all belong to the trace whose id is the run's UUID (RunTraceID), so a
// run found in the database is found in the trace viewer by its id, the render jobs the run asks for join that trace, and a replay of
// the run's stored events (cfctl trace export) uses ids derived from it, so exporting the same run twice adds nothing new.

type traceIDKey struct{}
type spanIDKey struct{}

// WithTraceID makes the next root span started with the returned context use id as its trace id. A span that has a parent keeps
// the parent's trace.
func WithTraceID(ctx context.Context, id trace.TraceID) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// WithSpanID makes the next span started with the returned context use id as its span id (a replay, which must be deterministic).
func WithSpanID(ctx context.Context, id trace.SpanID) context.Context {
	return context.WithValue(ctx, spanIDKey{}, id)
}

// RunTraceID is the trace id of an agent run: its UUID.
func RunTraceID(run uuid.UUID) trace.TraceID { return trace.TraceID(run) }

// DerivedTraceID is a trace id derived from a name (a replay of a run: "replay/<run id>").
func DerivedTraceID(name string) trace.TraceID {
	sum := sha256.Sum256([]byte(name))
	var id trace.TraceID
	copy(id[:], sum[:16])
	return id
}

// DerivedSpanID is a span id derived from a name.
func DerivedSpanID(name string) trace.SpanID {
	sum := sha256.Sum256([]byte(name))
	var id trace.SpanID
	copy(id[:], sum[:8])
	if !id.IsValid() {
		id[7] = 1
	}
	return id
}

// IDGenerator is the tracer provider's id generator: random ids, unless the context names them (WithTraceID, WithSpanID).
type IDGenerator struct{}

var _ sdktrace.IDGenerator = IDGenerator{}

// NewIDs implements sdktrace.IDGenerator (a root span).
func (IDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	tid, ok := ctx.Value(traceIDKey{}).(trace.TraceID)
	if !ok || !tid.IsValid() {
		_, _ = rand.Read(tid[:])
		for !tid.IsValid() {
			_, _ = rand.Read(tid[:])
		}
	}
	return tid, spanID(ctx)
}

// NewSpanID implements sdktrace.IDGenerator (a span with a parent).
func (IDGenerator) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID { return spanID(ctx) }

func spanID(ctx context.Context) trace.SpanID {
	if sid, ok := ctx.Value(spanIDKey{}).(trace.SpanID); ok && sid.IsValid() {
		return sid
	}
	var sid trace.SpanID
	for !sid.IsValid() {
		_, _ = rand.Read(sid[:])
	}
	return sid
}
