package renderjobs

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// A render job is a CHAIN span (docs/design/observability-v1.md): its profile, version, how long the video is, the wall time, the time
// ffmpeg ran (the sandbox) and how it ended, with the ffmpeg encode and the contact sheet as spans of their own. A render an agent run
// asked for is put in the run's trace (the trace id is the run's UUID), next to the tool call that waited for it.

// tracer is the global provider's tracer, looked up on every use so a test can install its own provider.
func tracer() trace.Tracer {
	return otel.Tracer("github.com/nilpntr/content-fabrique/internal/renderjobs")
}

func startRenderSpan(ctx context.Context, rr store.Render) (context.Context, trace.Span) {
	if rr.RequestedByKind == "agent" {
		if run, err := uuid.Parse(rr.RequestedByID); err == nil {
			ctx = telemetry.WithTraceID(ctx, telemetry.RunTraceID(run))
		}
	}
	return tracer().Start(ctx, "render "+rr.Profile, trace.WithNewRoot(), trace.WithAttributes(
		telemetry.Kind(telemetry.KindChain),
		attribute.String("cf.render_id", rr.ID.String()), attribute.String("cf.project_id", rr.ProjectID.String()),
		attribute.String("cf.workspace_id", rr.WorkspaceID.String()), attribute.String("cf.profile", rr.Profile),
		attribute.Int("cf.timeline_version", int(rr.TimelineVersion)), attribute.String("cf.requested_by", rr.RequestedByKind),
	))
}

func endRenderSpan(span trace.Span, out jobs.RenderOutcome, err error, done finished, wall, sandbox time.Duration, cpuMs int64) {
	defer span.End()
	span.SetAttributes(
		attribute.Int64("cf.wall_ms", wall.Milliseconds()), attribute.Int64("cf.ffmpeg_ms", sandbox.Milliseconds()),
		attribute.Int64("cf.cpu_ms", cpuMs), attribute.String("cf.status", string(out.Status)),
	)
	if done.durationMs > 0 {
		span.SetAttributes(attribute.Int64("cf.duration_ms", done.durationMs), attribute.Int64("cf.size_bytes", done.sizeBytes))
	}
	if out.Reason != "" {
		span.SetAttributes(attribute.String("cf.failure_reason", out.Reason))
	}
	switch {
	case err != nil:
		span.RecordError(err)
		span.SetStatus(codes.Error, telemetry.Cap(telemetry.Redact(err.Error()), 200))
	case out.Status == store.RenderFailed:
		span.SetStatus(codes.Error, out.Reason)
	default:
		span.SetStatus(codes.Ok, "")
	}
}

// stageSpan times one stage of a render (the encode, the contact sheet).
func stageSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, trace.WithAttributes(telemetry.Kind(telemetry.KindChain)))
}
