package evals

import (
	"errors"
	"fmt"
)

// Infrastructure stops a run; the candidate's failures are scored (docs/design/observability-v1.md section 9). A live candidate run once
// hit the workspace's daily spend cap after its first case: every later sample "failed" with daily_cap, scored 0, and the candidate was
// rejected for a 73 % failure rate it had nothing to do with. A failure that says nothing about the code (a spend cap, the model API, the
// database or storage) now stops the run, unscored and never cached as a baseline; a failure of the edit itself (its budget, an answer cut
// off at max_tokens, an editor error, no version) still scores 0.

// ErrInfrastructure is wrapped by every *InfraError.
var ErrInfrastructure = errors.New("evals: stopped by the infrastructure, nothing was decided")

// InfraError is a failure of the infrastructure, not of the code under test.
type InfraError struct {
	// Reason is the run's failure reason (daily_cap, ...) or a short code; Msg says what happened.
	Reason, Msg string
}

func (e *InfraError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrInfrastructure) true.
func (e *InfraError) Is(target error) bool { return target == ErrInfrastructure }

// infraReasons are the agent run failure reasons (runtime.Fail*) that are the infrastructure's, with what they mean.
var infraReasons = map[string]string{
	"daily_cap":             "the workspace's daily spend cap was reached",
	"platform_spend_paused": "the platform's daily spend breaker is open",
	"spend_cap_busy":        "the platform spend breaker had no room for a call",
	"request_cap":           "a single model call was over LLM_MAX_REQUEST_MICROS",
	"model_access":          "the model could not be reached (the key or the gateway)",
	"model_error":           "the model API failed (overloaded, rate-limited or an error answer)",
	"model_outcome_unknown": "a model call was sent and its answer was lost",
	"replay_miss":           "a replay cassette was missing",
	"ledger_error":          "the usage ledger could not be written",
	"context_failed":        "the run context could not be built (the database or storage)",
	"internal_error":        "the run hit an internal error",
	"too_many_resumes":      "the run's worker kept stopping",
	"corrupt_log":           "the run's event log is corrupt",
	"unknown_agent":         "the worker does not know the editor agent",
}

// ClassifyRunFailure says whether an editor run's failure reason is the infrastructure's (true, with what it means) or the edit's own
// (budget, max_tokens, refusal, context_window_exceeded, event_too_large, unexpected_stop_reason: false).
func ClassifyRunFailure(reason string) (bool, string) {
	msg, ok := infraReasons[reason]
	return ok, msg
}

// RunFailure is the error of an editor run that ended without an edit: an *InfraError when the reason is the infrastructure's, a plain
// error (the candidate's failure) otherwise.
func RunFailure(run, status, reason string) error {
	if infra, what := ClassifyRunFailure(reason); infra {
		return &InfraError{Reason: reason, Msg: fmt.Sprintf("%s (editor run %s %s: %s)", what, run, status, reason)}
	}
	if reason == "" {
		return fmt.Errorf("the editor run %s %s", run, status)
	}
	return fmt.Errorf("the editor run %s %s: %s", run, status, reason)
}

// SpendRoom is what the spend caps leave today, for a live run's check before it starts.
type SpendRoom struct {
	// Workspace is the workspace's daily cap less what it has spent today; WorkspaceCap the cap.
	Workspace, WorkspaceCap int64
	// PlatformBilled says the calls are billed to the platform, so the breaker applies; Platform is its room and PlatformTripped says
	// it is open.
	PlatformBilled  bool
	Platform        int64
	PlatformTripped bool
}

// CheckRoom refuses a run whose estimate the caps cannot cover: better refused up front than stopped half way.
func CheckRoom(estimate int64, r SpendRoom) error {
	switch {
	case r.PlatformBilled && r.PlatformTripped:
		return &InfraError{Reason: "platform_spend_paused", Msg: "the platform's daily spend breaker is open: no live run can start until the next UTC day"}
	case r.PlatformBilled && estimate > r.Platform:
		return &InfraError{Reason: "platform_spend_paused", Msg: fmt.Sprintf("the platform breaker has $%.2f left today, less than the estimated $%.2f", float64(r.Platform)/1e6, float64(estimate)/1e6)}
	case estimate > r.Workspace:
		return &InfraError{Reason: "daily_cap", Msg: fmt.Sprintf("the workspace's daily spend cap ($%.2f) has $%.2f left today, less than the estimated $%.2f: run it tomorrow (UTC), raise DAILY_SPEND_CAP_MICROS for local runs, or pick fewer --cases or a lower --repeat",
			float64(r.WorkspaceCap)/1e6, float64(max(r.Workspace, 0))/1e6, float64(estimate)/1e6)}
	}
	return nil
}
