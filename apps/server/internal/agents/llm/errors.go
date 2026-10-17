package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNoKey is what ClientFactory.For returns, wrapped in a *NoKeyError, when no API key is configured for the
// workspace. Test with errors.Is.
var ErrNoKey = errors.New("llm: no API key is configured")

// NoKeyError says which variable was empty. It never carries a key.
type NoKeyError struct {
	// Variable is the setting that is missing (ANTHROPIC_API_KEY for a managed workspace).
	Variable string
	// Mode is the LLM_MODE that needed it.
	Mode Mode
}

func (e *NoKeyError) Error() string {
	return fmt.Sprintf("llm: %s is empty, so LLM_MODE=%s cannot call the API (use LLM_MODE=replay to serve recorded calls without a key)", e.Variable, e.Mode)
}

// Is makes errors.Is(err, ErrNoKey) true.
func (e *NoKeyError) Is(target error) bool { return target == ErrNoKey }

// ErrUnpriced is returned, wrapped in an *UnpricedError, when a live call names a model the price table has no
// complete price for. Set LLM_ALLOW_UNPRICED=1 to run anyway (the ledger then records cost 0).
var ErrUnpriced = errors.New("llm: model has no price in the price table")

// UnpricedError names the model and the table.
type UnpricedError struct {
	Model        string
	TableVersion string
}

func (e *UnpricedError) Error() string {
	return fmt.Sprintf("llm: model %q has no price in price table %s, so its cost cannot be metered (add the price from Anthropic's pricing page, or set LLM_ALLOW_UNPRICED=1)", e.Model, e.TableVersion)
}

// Is makes errors.Is(err, ErrUnpriced) true.
func (e *UnpricedError) Is(target error) bool { return target == ErrUnpriced }

// CapKind says which spend cap refused a call.
type CapKind string

// The caps, all enforced before the request is sent.
const (
	// CapRequest is LLM_MAX_REQUEST_MICROS: one call's worst-case cost.
	CapRequest CapKind = "request"
	// CapRun is the run's budget (Budget.Reserve).
	CapRun CapKind = "run"
	// CapDaily is DAILY_SPEND_CAP_MICROS: the workspace's spend today plus this call's worst case.
	CapDaily CapKind = "daily"
)

// ErrCapExceeded is wrapped by every *CapError.
var ErrCapExceeded = errors.New("llm: spend cap exceeded")

// CapError is a call that was refused, before anything was sent, because it could exceed a spend cap.
type CapError struct {
	Kind CapKind
	// EstimateMicros is the call's worst-case cost: counted input tokens at the input price plus max_tokens at the
	// output price.
	EstimateMicros int64
	// LimitMicros is the room that was left under the cap (the whole cap for CapRequest).
	LimitMicros int64
	// Cause is what a custom Budget said, when it refused with some other error.
	Cause error
}

// Unwrap returns Cause.
func (e *CapError) Unwrap() error { return e.Cause }

func (e *CapError) Error() string {
	return fmt.Sprintf("llm: refused before sending: the %s spend cap would be exceeded (worst case $%.4f, room $%.4f)",
		e.Kind, float64(e.EstimateMicros)/1e6, float64(e.LimitMicros)/1e6)
}

// Is makes errors.Is(err, ErrCapExceeded) true.
func (e *CapError) Is(target error) bool { return target == ErrCapExceeded }

// ErrRateLimited is wrapped by the error returned when 429 or 529 answers outlast the retry budget.
var ErrRateLimited = errors.New("llm: rate limited")

// ErrRetriesExhausted is wrapped by the error returned when retries of other failures (5xx, timeouts, resets) ran
// out of attempts or waiting time.
var ErrRetriesExhausted = errors.New("llm: retries exhausted")

// ErrReplayMiss is wrapped by *ReplayMissError.
var ErrReplayMiss = errors.New("llm: no recorded call for this request")

// ReplayMissError is what LLM_MODE=replay returns when no cassette holds the request. It never falls back to the API.
type ReplayMissError struct {
	Stage string
	// Key is the SHA-256 of the canonical request: the file name of the cassette that was looked for.
	Key string
	// Path is where the cassette was looked for.
	Path string
}

func (e *ReplayMissError) Error() string {
	return fmt.Sprintf("llm: replay miss: no cassette for stage %q key %s (looked for %s). Re-record it with LLM_MODE=record on dev-cheap if the request changed on purpose", e.Stage, e.Key, e.Path)
}

// Is makes errors.Is(err, ErrReplayMiss) true.
func (e *ReplayMissError) Is(target error) bool { return target == ErrReplayMiss }

// ErrLedger is wrapped by *LedgerError.
var ErrLedger = errors.New("llm: could not write the usage ledger row")

// LedgerError is returned alongside a valid Response when the call succeeded but its ledger row could not be
// written. The response is paid for, so it is returned; the caller should fail the run (billing integrity) rather
// than carry on unmetered. The row's content is logged at error level so it can be reconciled.
type LedgerError struct{ Err error }

func (e *LedgerError) Error() string {
	return "llm: could not write the usage ledger row: " + e.Err.Error()
}
func (e *LedgerError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrLedger) true.
func (e *LedgerError) Is(target error) bool { return target == ErrLedger }

// APIError is an error answer from the API (or the gateway). Its message has the API key removed.
type APIError struct {
	// Status is the HTTP status.
	Status int
	// Type is the API's error type (rate_limit_error, overloaded_error, invalid_request_error ...), or "".
	Type    string
	Message string
	// RequestID is the request-id header, to quote to Anthropic.
	RequestID string
	// RetryAfter is the parsed retry-after header, zero when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "llm: API error %d", e.Status)
	if e.Type != "" {
		fmt.Fprintf(&b, " %s", e.Type)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request-id %s)", e.RequestID)
	}
	return b.String()
}

// Retryable reports whether the answer means "the server could not take this now, try again": 429 and 529 (busy),
// overloaded and rate-limit errors, 408, and 500, 502, 503 and 504. An error answer means the request was not
// processed, so a retry cannot bill twice. Every other 4xx is the caller's mistake and is never retried.
func (e *APIError) Retryable() bool {
	switch e.Status {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return e.Type == "overloaded_error" || e.Type == "rate_limit_error"
}

// ErrOutcomeUnknown is wrapped by a *TransportError whose request was sent: the call may have run and been billed,
// but no usage came back. It is never retried here, and the runtime must not retry it blindly either (it can look at
// the run's events first). The ledger holds a row for it, marked estimated, at the reserved worst-case cost.
var ErrOutcomeUnknown = errors.New("llm: the request was sent and its outcome is unknown")

// TransportError is a failure below HTTP: a dial error, a reset, a timeout, a cancelled context, a body cut off.
type TransportError struct {
	// Sent says the request had been written to the connection, so the server may have acted on it.
	Sent bool
	Err  error
	msg  string
}

func (e *TransportError) Error() string { return e.msg }

// Unwrap keeps errors.Is(err, context.Canceled) and friends working.
func (e *TransportError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrOutcomeUnknown) true for a request that was sent.
func (e *TransportError) Is(target error) bool { return target == ErrOutcomeUnknown && e.Sent }

// IsAuth reports a 401 or 403: the key is wrong, revoked or lacks access. P4-00b marks a BYOK key invalid on it.
func (e *APIError) IsAuth() bool { return e.Status == 401 || e.Status == 403 }
