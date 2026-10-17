// Package renderjobs is the render service and the render job (P3-06, docs/design/timeline-v1.md section 6): it renders a
// version of a project's timeline with the preview profile (from the 720p proxies) or the final profile (from the originals),
// and keeps the three outputs of a render next to each other in object storage: the MP4, the time map (JSON) and the contact
// sheet (a JPEG).
//
// Two halves share the renders table:
//
//   - Service is what the API and the agent's render_preview tool call. Request validates the version against the project's
//     footage as it is now, reuses a render of the same version and profile that is waiting, running or ready (dedupe), and
//     otherwise inserts a queued render and enqueues the River job in the same transaction. Get, List and Files read them.
//   - Pipeline is what the worker runs (jobs.Renderer). Run claims the render, stages the footage of the timeline into a job
//     directory, compiles the spec with render.Compile, runs it through the sandbox with render.Execute (progress goes to
//     renders.progress), makes the contact sheet from the rendered MP4 (also in the sandbox), uploads the three files and marks
//     the render ready.
//
// Tenancy: the workspace comes from the verified token (the Handler) or from the job's arguments; the project, the version and
// every asset are looked up with it, so another workspace's project or render is as unknown as one that never existed (404). A
// render reads only the footage of its own project (render inputs are the project's ready assets), every key it reads is
// checked against the ids of the row it came from, and the keys it writes are built from the render's ids (storage.RenderKey).
//
// Failures are of two kinds, as in the ingest. A fileError is the timeline's or the footage's fault (the timeline no longer
// validates, an asset is gone, ffmpeg cannot make the video): the render is marked failed with a reason code that is safe to
// show and Run returns normally, so the job ends and is not retried. Anything else (the sandbox is down, storage did not
// answer, the worker is shutting down, a timeout) is returned as an error and the job runs again; its last attempt gives up
// through Fail.
package renderjobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// Failure reasons of a render (renders.failure_reason). They are codes that are safe to show.
const (
	ReasonInvalidTimeline = "invalid_timeline"
	// ReasonGraphTooLong is an invalid_timeline whose filter graph is too long for one process argument (render.ErrGraphTooLong):
	// too many sound effects or clips. It is a code with a short cause so that an editor run can be told what to remove; the full
	// compile message is not stored (the failure reason is at most 64 characters).
	ReasonGraphTooLong      = ReasonInvalidTimeline + ": filter graph too long"
	ReasonSourceUnavailable = "source_unavailable"
	ReasonInputsTooLarge    = "inputs_too_large"
	ReasonRenderFailed      = "render_failed"
	ReasonProjectDeleted    = "project_deleted"
	ReasonTimeout           = "timeout"
	ReasonInterrupted       = "interrupted"
	ReasonProcessing        = "processing_failed"
	// ReasonJobLost is set by the sweeper on a render whose job is gone (jobs.RenderJobLost).
	ReasonJobLost = jobs.RenderJobLost
)

// Limits of the renders.
const (
	// MaxInFlight is how many renders one workspace may have waiting or running at once.
	MaxInFlight = 5
	// MaxInputBytes is the most footage one render stages: the sum of the files it reads. A final render reads originals.
	MaxInputBytes = 8 << 30
	// MaxOutputBytes caps the rendered MP4 that is uploaded.
	MaxOutputBytes = 4 << 30
	// MaxTiles is the most tiles on a contact sheet. A timeline has at most 500 items per track; a 180 s edit has far fewer
	// shots than this.
	MaxTiles = 60
)

// The time the render allows itself. A sandboxed run is killed at its timeout (render.DefaultLimits: 30 minutes per run; the
// final profile makes two runs, the loudness measurement and the encode); the job as a whole is cut off at jobs.RenderTimeout
// (River's per-job timeout), and River only hands a running job to another worker after jobs.JobRescueAfter, so an attempt
// never overlaps its own retry.
const (
	// ExecuteTimeout is the limit of one sandbox run of render.Execute.
	ExecuteTimeout = 30 * time.Minute
	// SheetTimeout is the limit of the frame extraction of the contact sheet.
	SheetTimeout = 5 * time.Minute
	// transferAllowance is the time reserved for moving the footage down and the outputs up.
	transferAllowance = 30 * time.Minute
)

// WorstCase is the longest one attempt can take if every sandbox run goes to its timeout: both transfers, the two runs of a
// final render and the contact sheet. New checks it against jobs.RenderTimeout.
func WorstCase() time.Duration {
	return 2*transferAllowance + 2*ExecuteTimeout + SheetTimeout
}

// fileError is a failure caused by the timeline or the footage, not by us or the infrastructure: retrying cannot help, the
// render is marked failed with reason and the job ends.
type fileError struct {
	reason string
	err    error
}

func (e *fileError) Error() string { return "renderjobs: " + e.reason + ": " + e.err.Error() }
func (e *fileError) Unwrap() error { return e.err }

func fileErr(reason string, err error) *fileError { return &fileError{reason: reason, err: err} }

// reasonFor is the failure reason of a render whose last attempt ended with err: a timeout (the job's or a tool's), an
// interruption (the worker was stopped), or processing_failed for anything else.
func reasonFor(err error) string {
	var fe *fileError
	switch {
	case errors.As(err, &fe):
		return fe.reason
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, sandbox.ErrTimeout):
		return ReasonTimeout
	case errors.Is(err, context.Canceled):
		return ReasonInterrupted
	}
	return ReasonProcessing
}

// ErrRenderFailed is wrapped by the error RenderAndWait returns for a render that ended failed.
var ErrRenderFailed = errors.New("renderjobs: the render failed")

// ErrWaitTimeout is wrapped by the error RenderAndWait returns when the render is still waiting or running at the deadline.
// The render goes on; the caller may ask again for the same version and profile, which returns it.
var ErrWaitTimeout = errors.New("renderjobs: the render is not finished yet")

// FailedError is the error of a render that failed; it carries the reason code.
type FailedError struct{ Reason string }

func (e *FailedError) Error() string { return fmt.Sprintf("%v (%s)", ErrRenderFailed, e.Reason) }

// Unwrap lets errors.Is(err, ErrRenderFailed) match.
func (e *FailedError) Unwrap() error { return ErrRenderFailed }
