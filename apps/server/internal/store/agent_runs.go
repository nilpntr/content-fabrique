package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Agent runs and their events (P4-01, docs/design/agent-runtime-v1.md section 3). internal/agents/runtime is the only
// caller. Every function takes the workspace, and the queries filter by it.

// Rows as the repository returns them.
type (
	AgentRun   = db.AgentRun
	AgentEvent = db.AgentEvent
)

// The statuses agent_runs.status allows.
const (
	AgentQueued       = "queued"
	AgentRunning      = "running"
	AgentWaitingInput = "waiting_input"
	AgentSucceeded    = "succeeded"
	AgentFailed       = "failed"
	AgentCancelled    = "cancelled"
)

// The event types agent_events.type allows.
const (
	EventStatus           = "status"
	EventAssistantMessage = "assistant_message"
	EventToolCall         = "tool_call"
	EventToolResult       = "tool_result"
	EventError            = "error"
	EventArtifact         = "artifact"
)

// MaxEventPayloadBytes is the largest payload of an event: 64 KB of compact JSON. Images are never inline, only R2 keys.
const MaxEventPayloadBytes = 64 * 1024

var (
	// ErrPayloadTooLarge is returned, wrapping ErrInvalid, for an event payload over MaxEventPayloadBytes.
	ErrPayloadTooLarge = fmt.Errorf("%w: event payload over %d bytes", ErrInvalid, MaxEventPayloadBytes)
	// ErrRunFenced is returned when a write of the running worker is refused: the run is not running under the worker's
	// epoch any more (it was claimed by another worker, cancelled, or finished). The worker must stop and write nothing.
	ErrRunFenced = errors.New("store: the run is no longer held by this worker")
	// ErrNotWaiting is returned by QueueAgentRunAfterReply when the run is not waiting for input.
	ErrNotWaiting = errors.New("store: the run is not waiting for input")
)

// NewAgentRun is the input of CreateAgentRun. The runtime fills it: the routing entry is resolved once and kept.
type NewAgentRun struct {
	Agent          string
	Stage          string
	ParentRunID    uuid.UUID // uuid.Nil: none
	ProjectID      uuid.UUID // uuid.Nil: none
	Input          json.RawMessage
	RoutingProfile string
	Model          string
	Effort         string
	MaxTokens      int

	BudgetMicros      int64
	BudgetTokens      int64
	BudgetToolCalls   int
	BudgetWallClockMs int64
	BilledTo          string
}

// CreateAgentRun inserts a queued run. The caller enqueues the agent_run job in the same transaction.
func (s *Store) CreateAgentRun(ctx context.Context, ws ids.WorkspaceID, in NewAgentRun) (AgentRun, error) {
	if len(in.Input) == 0 {
		in.Input = json.RawMessage("{}")
	}
	r, err := s.q.CreateAgentRun(ctx, db.CreateAgentRunParams{
		WorkspaceID: ws, Agent: in.Agent, Stage: in.Stage, ParentRunID: pgUUID(in.ParentRunID), ProjectID: in.ProjectID,
		Input: in.Input, RoutingProfile: in.RoutingProfile, Model: in.Model, Effort: in.Effort, MaxTokens: int32(in.MaxTokens), //nolint:gosec // checked by the caller and the table
		BudgetMicros: in.BudgetMicros, BudgetTokens: in.BudgetTokens, BudgetToolCalls: int32(in.BudgetToolCalls), //nolint:gosec // checked by the caller and the table
		BudgetWallClockMs: in.BudgetWallClockMs, BilledTo: in.BilledTo,
	})
	return r, mapErr(err)
}

// GetAgentRun returns the run, or ErrNotFound (also for a run of another workspace).
func (s *Store) GetAgentRun(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) (AgentRun, error) {
	r, err := s.q.GetAgentRun(ctx, db.GetAgentRunParams{WorkspaceID: ws, ID: id})
	return r, mapErr(err)
}

// ActiveAgentRunForProject returns the unfinished (queued, running or waiting) run of the agent on the project, or
// ErrNotFound when there is none.
func (s *Store) ActiveAgentRunForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, agent string) (AgentRun, error) {
	r, err := s.q.GetActiveAgentRunForProject(ctx, db.GetActiveAgentRunForProjectParams{WorkspaceID: ws, ProjectID: project, Agent: agent})
	return r, mapErr(err)
}

// ClaimAgentRun is what a worker does first: a queued run, or a running one whose worker died (no heartbeat for
// staleAfter), becomes running under a new epoch, which fences every older worker out. ErrNotFound: the run is waiting for
// input, finished, held by a live worker, or not this workspace's (GetAgentRun tells which).
func (s *Store) ClaimAgentRun(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID, staleAfter time.Duration) (AgentRun, error) {
	r, err := s.q.ClaimAgentRun(ctx, db.ClaimAgentRunParams{WorkspaceID: ws, ID: id, StaleSeconds: int32(staleAfter.Seconds())}) //nolint:gosec // seconds
	return r, mapErr(err)
}

// ReleaseAgentRun puts a running run back to queued, for a worker that stops without finishing it. countResume says it
// stopped because something went wrong, so the retry counts toward the cap on resumes (a worker that was only told to
// stop does not count). It returns ErrRunFenced when the worker has lost the run already.
func (s *Store) ReleaseAgentRun(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, epoch int64, countResume bool) error {
	n, err := s.q.ReleaseAgentRun(ctx, db.ReleaseAgentRunParams{WorkspaceID: ws, ID: run, Epoch: epoch, CountResume: countResume})
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrRunFenced
	}
	return nil
}

// EventDelta is what an event adds to the run's totals, in the same statement that appends it.
type EventDelta struct {
	InputTokens, CacheWriteTokens, CacheReadTokens, OutputTokens int64
	CostMicros                                                   int64
	ToolCalls                                                    int
	ActiveMs                                                     int64
}

// AppendAgentEvent appends the next event of a run, fenced by the epoch ClaimAgentRun returned: it returns
// ErrRunFenced when the run is not running under that epoch. delta is added to the run's totals in the same statement.
func (s *Store) AppendAgentEvent(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, epoch int64, typ string, payload json.RawMessage, delta EventDelta) (AgentEvent, error) {
	if epoch < 1 {
		return AgentEvent{}, errors.New("store: AppendAgentEvent needs the epoch of ClaimAgentRun")
	}
	return s.appendAgentEvent(ctx, ws, run, epoch, typ, payload, delta)
}

// AppendAgentEventUnfenced appends an event for a writer that is not the running worker: the reply to an ask_user and a
// cancel, which hold their own guard (they have just changed the status in the same transaction).
func (s *Store) AppendAgentEventUnfenced(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, typ string, payload json.RawMessage) (AgentEvent, error) {
	return s.appendAgentEvent(ctx, ws, run, 0, typ, payload, EventDelta{})
}

func (s *Store) appendAgentEvent(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, epoch int64, typ string, payload json.RawMessage, d EventDelta) (AgentEvent, error) {
	if len(payload) > MaxEventPayloadBytes {
		return AgentEvent{}, ErrPayloadTooLarge
	}
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	e, err := s.q.AppendAgentEvent(ctx, db.AppendAgentEventParams{
		WorkspaceID: ws, RunID: run, Type: typ, Payload: payload,
		DInputTokens: d.InputTokens, DCacheWriteTokens: d.CacheWriteTokens, DCacheReadTokens: d.CacheReadTokens,
		DOutputTokens: d.OutputTokens, DCostMicros: d.CostMicros, DToolCalls: int32(d.ToolCalls), //nolint:gosec // a handful per event
		DActiveMs: d.ActiveMs, Epoch: epoch,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if epoch == 0 {
			return AgentEvent{}, ErrNotFound
		}
		return AgentEvent{}, ErrRunFenced
	}
	return e, mapErr(err)
}

// RunEnd is how a running worker ends or parks its run.
type RunEnd struct {
	// Status is succeeded, failed, cancelled or waiting_input.
	Status string
	// Output is the run's result (agent_runs.output); empty keeps the old one.
	Output json.RawMessage
	// FailureReason is required for failed and must be empty otherwise.
	FailureReason string
	// Event is the status event that records the transition; it is appended in the same transaction.
	Event json.RawMessage
	// ActiveMs is the running time since the last write, added to the run's total.
	ActiveMs int64
}

// EndAgentRun appends the status event and changes the run's status in one transaction, fenced by the epoch. It returns
// ErrRunFenced when the run is not running under that epoch.
func (s *Store) EndAgentRun(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, epoch int64, end RunEnd) (AgentRun, error) {
	switch end.Status {
	case AgentSucceeded, AgentCancelled, AgentWaitingInput:
		if end.FailureReason != "" {
			return AgentRun{}, fmt.Errorf("%w: only a failed run has a failure reason", ErrInvalid)
		}
	case AgentFailed:
		if end.FailureReason == "" {
			return AgentRun{}, fmt.Errorf("%w: a failed run needs a failure reason", ErrInvalid)
		}
	default:
		return AgentRun{}, fmt.Errorf("%w: status %q", ErrInvalid, end.Status)
	}
	var out AgentRun
	err := s.inTx(ctx, func(tx *Store) error {
		if _, err := tx.AppendAgentEvent(ctx, ws, run, epoch, EventStatus, end.Event, EventDelta{ActiveMs: end.ActiveMs}); err != nil {
			return err
		}
		var reason *string
		if end.FailureReason != "" {
			reason = &end.FailureReason
		}
		var output []byte
		if len(end.Output) > 0 {
			output = end.Output
		}
		r, err := tx.q.SetAgentRunState(ctx, db.SetAgentRunStateParams{
			WorkspaceID: ws, ID: run, Epoch: epoch, Status: end.Status, Output: output, FailureReason: reason,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunFenced
		}
		out = r
		if err != nil {
			return mapErr(err)
		}
		if end.Status == AgentWaitingInput && r.Status == AgentCancelled {
			// A cancel was requested while the run was asking: the cancel wins (the query did it).
			_, err = tx.AppendAgentEventUnfenced(ctx, ws, run, EventStatus, json.RawMessage(`{"state":"cancelled"}`))
		}
		return err
	})
	return out, err
}

// RequestAgentRunCancel asks for the run to end. A queued run or one waiting for input is cancelled at once (with a
// status event carrying event, and output as its result); a running run only gets cancel_requested_at, and its loop ends
// it between steps. A run that has finished already is returned as it is. Cancelling twice is fine.
func (s *Store) RequestAgentRunCancel(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID, output, event json.RawMessage) (AgentRun, error) {
	var out AgentRun
	err := s.inTx(ctx, func(tx *Store) error {
		r, err := tx.q.RequestAgentRunCancel(ctx, db.RequestAgentRunCancelParams{WorkspaceID: ws, ID: id, Output: output})
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = tx.GetAgentRun(ctx, ws, id) // finished already, or not this workspace's
			return err
		}
		if err != nil {
			return mapErr(err)
		}
		out = r
		if r.Status == AgentCancelled {
			_, err = tx.AppendAgentEventUnfenced(ctx, ws, id, EventStatus, event)
		}
		return err
	})
	return out, err
}

// QueueAgentRunAfterReply moves a run from waiting_input to queued. It returns ErrNotWaiting when the run is in any
// other state. The caller appends the answer and enqueues the agent_run job in the same transaction.
func (s *Store) QueueAgentRunAfterReply(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) (AgentRun, error) {
	r, err := s.q.QueueAgentRunAfterReply(ctx, db.QueueAgentRunAfterReplyParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.GetAgentRun(ctx, ws, id); gerr != nil {
			return AgentRun{}, gerr // not found
		}
		return AgentRun{}, ErrNotWaiting
	}
	return r, mapErr(err)
}

// LastAgentEvent returns the newest event of the run, or ErrNotFound when it has none.
func (s *Store) LastAgentEvent(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID) (AgentEvent, error) {
	e, err := s.q.GetLastAgentEvent(ctx, db.GetLastAgentEventParams{WorkspaceID: ws, RunID: run})
	return e, mapErr(err)
}

// agentEventPage is how many events one query reads.
const agentEventPage = 500

// AgentEvents returns the events of a run after afterSeq (0: from the start), oldest first, at most limit (0: all).
func (s *Store) AgentEvents(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, afterSeq int32, limit int) ([]AgentEvent, error) {
	var out []AgentEvent
	for limit == 0 || len(out) < limit {
		page := int32(agentEventPage)
		if limit > 0 {
			page = int32(min(agentEventPage, limit-len(out))) //nolint:gosec // at most agentEventPage
		}
		rows, err := s.q.ListAgentEvents(ctx, db.ListAgentEventsParams{WorkspaceID: ws, RunID: run, AfterSeq: afterSeq, RowLimit: page})
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, rows...)
		if len(rows) < int(page) {
			break
		}
		afterSeq = rows[len(rows)-1].Seq
	}
	return out, nil
}

// AgentArtifactEvents returns the artifact events of a run whose payload has the kind, oldest first.
func (s *Store) AgentArtifactEvents(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, kind string) ([]AgentEvent, error) {
	rows, err := s.q.ListAgentArtifactEvents(ctx, db.ListAgentArtifactEventsParams{WorkspaceID: ws, RunID: run, Kind: kind})
	return rows, mapErr(err)
}

// AgentRunLedgerCostMicros is what the usage ledger holds for the run, at list price.
func (s *Store) AgentRunLedgerCostMicros(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID) (int64, error) {
	n, err := s.q.SumAgentRunLedgerCost(ctx, db.SumAgentRunLedgerCostParams{WorkspaceID: ws, RunID: pgUUID(run)})
	return n, mapErr(err)
}

// TouchAgentRun is the running worker's heartbeat; activeMs is the running time since its last write, added to the run's
// total. It returns ErrRunFenced when the worker has lost the run.
func (s *Store) TouchAgentRun(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, epoch, activeMs int64) error {
	n, err := s.q.TouchAgentRun(ctx, db.TouchAgentRunParams{WorkspaceID: ws, ID: run, Epoch: epoch, DActiveMs: activeMs})
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrRunFenced
	}
	return nil
}

// MarkAgentRunForResume is the sweeper's claim on a stale run: true means this call marked it (so the caller enqueues the
// job, in the same transaction), false that it is not stale any more or a job was enqueued for it lately.
func (s *Store) MarkAgentRunForResume(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, staleAfter time.Duration) (bool, error) {
	n, err := s.q.MarkAgentRunForResume(ctx, db.MarkAgentRunForResumeParams{WorkspaceID: ws, ID: run, StaleSeconds: int32(staleAfter.Seconds())}) //nolint:gosec // seconds
	return n > 0, mapErr(err)
}

// StaleAgentRun names a running run whose worker stopped touching it.
type StaleAgentRun struct {
	WorkspaceID ids.WorkspaceID
	ID          uuid.UUID
}

// StaleAgentRuns lists up to limit running runs, across workspaces, whose heartbeat is older than staleAfter. It is for the
// resume sweeper, which acts on each row with the row's own workspace.
func (s *Store) StaleAgentRuns(ctx context.Context, staleAfter time.Duration, limit int) ([]StaleAgentRun, error) {
	rows, err := s.q.ListStaleAgentRuns(ctx, db.ListStaleAgentRunsParams{StaleSeconds: int32(staleAfter.Seconds()), RowLimit: int32(limit)}) //nolint:gosec // seconds and a page size
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]StaleAgentRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, StaleAgentRun{WorkspaceID: r.WorkspaceID, ID: r.ID})
	}
	return out, nil
}
