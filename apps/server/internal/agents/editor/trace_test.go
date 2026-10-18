package editor

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// Every check of a report is an event of check_edit's span, whatever check it is: the text, caption, face, overlay and music checks
// added after the spans (P6-07) included, with their detail only when content capture is on.
func TestTraceChecksCoversEveryCheck(t *testing.T) {
	ids := []string{CheckHookLength, CheckTextOverSubject, CheckTextOverFace, CheckStoryCaptions, CheckCaptionReadable, CheckOverlayReadable,
		CheckOverlayOverSubject, CheckOverlayLegibility, CheckGraphicsOverlap, CheckOffBeatCut, CheckHookMissesDrop, CheckBeatSync}
	var report Report
	for i, id := range ids {
		st := CheckPass
		if i%3 == 0 {
			st = CheckFail
		}
		report.Checks = append(report.Checks, Check{ID: id, Status: st, Detail: "the title covers the face at 0.4 s"})
	}
	for _, content := range []bool{false, true} {
		telemetry.SetContent(content)
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithRawSpanLimits(telemetry.SpanLimits()))
		ctx, span := tp.Tracer("t").Start(context.Background(), "tool check_edit")
		traceChecks(ctx, report, 1)
		span.End()
		seen := map[string]bool{}
		details := 0
		for _, e := range sr.Ended()[0].Events() {
			if e.Name != "check" {
				continue
			}
			for _, a := range e.Attributes {
				switch a.Key {
				case "cf.check.id":
					seen[a.Value.AsString()] = true
				case "cf.check.detail":
					details++
				}
			}
		}
		for _, id := range ids {
			if !seen[id] {
				t.Errorf("content %v: no event for %s", content, id)
			}
		}
		if (details > 0) != content {
			t.Errorf("content %v: %d details", content, details)
		}
	}
	telemetry.SetContent(false)
}
