// Package runapi is the HTTP side of agent runs (P4-02a): starting an editor run on a project, reading a run, cancelling it and
// answering its question. It implements the runs operations of the generated api.StrictServerInterface.
//
// Tenancy: the workspace of every call comes from the Caller (the verified token) and nowhere else. A run or a project of
// another workspace is exactly as unknown as one that never existed (404 not_found), and every store call it makes is scoped by
// the workspace. Starting a run needs an owner or an editor; reading needs any member.
package runapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Event paging of GET /v1/runs/{id}.
const (
	defaultEvents = 200
	maxEvents     = 500
)

// Profiles are the routing profiles a request may name.
var Profiles = []string{"dev-cheap", "dev-mid", "v1-start"}

// Runs implements the runs operations.
type Runs struct {
	svc    *runtime.Service
	caller httpapi.CallerFunc
	logger *slog.Logger
}

// New returns the handler. With a nil service (a server wired without model access) every operation answers 503 after the
// checks of authentication and role.
func New(svc *runtime.Service, caller httpapi.CallerFunc, logger *slog.Logger) *Runs {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runs{svc: svc, caller: caller, logger: logger}
}

// failure is an answer to give: the status, the code and a message that never contains request data.
type failure struct {
	status  int
	code    api.ErrorCode
	message string
}

func bad(msg string) *failure {
	return &failure{http.StatusBadRequest, api.ErrorCodeInvalidRequest, msg}
}

var (
	errRunNotFound     = &failure{http.StatusNotFound, api.ErrorCodeNotFound, "no such run"}
	errProjectNotFound = &failure{http.StatusNotFound, api.ErrorCodeNotFound, "no such project"}
	errInternal        = &failure{http.StatusInternalServerError, api.ErrorCodeInternalError, "something went wrong"}
)

func (h *Runs) internal(ctx context.Context, what string, err error) *failure {
	h.logger.ErrorContext(ctx, "runs: "+what+" failed", "error", err)
	return errInternal
}

// begin authorises a call: reads are open to every role, starting, cancelling and replying need an owner or an editor.
func (h *Runs) begin(ctx context.Context, write bool) (httpapi.Caller, *failure) {
	c, r := h.caller.Authorize(ctx, write, "runs")
	if r != nil {
		return httpapi.Caller{}, &failure{r.Status, r.Code, r.Message}
	}
	if h.svc == nil {
		return httpapi.Caller{}, &failure{http.StatusServiceUnavailable, api.ErrorCodeModelNotConfigured, "agent runs are not available on this server"}
	}
	return c, nil
}

func body(f *failure) api.Error { return httpapi.ErrorBody(f.code, f.message) }

func undocumented(op string, f *failure) error {
	return fmt.Errorf("%s failed with undocumented status %d (%s)", op, f.status, f.code)
}

// parseRunID reads the run id of a path: "run_..." TypeID. A malformed one is a bad request.
func parseRunID(raw string) (uuid.UUID, *failure) {
	u, err := ids.Decode(ids.PrefixRun, raw)
	if err != nil {
		return uuid.Nil, bad("the run id is not valid")
	}
	return u, nil
}

// ---- StartEdit --------------------------------------------------------------------------------------------------

// StartEdit is POST /v1/projects/{id}/edit.
func (h *Runs) StartEdit(ctx context.Context, req api.StartEditRequestObject) (api.StartEditResponseObject, error) {
	run, f := h.startEdit(ctx, req)
	if f != nil {
		switch f.status {
		case 400:
			return api.StartEdit400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(f))}, nil
		case 401:
			return api.StartEdit401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(f))}, nil
		case 403:
			return api.StartEdit403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(f))}, nil
		case 402:
			return api.StartEdit402JSONResponse{PlanRequiredJSONResponse: api.PlanRequiredJSONResponse(body(f))}, nil
		case 404:
			return api.StartEdit404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(f))}, nil
		case 409:
			return api.StartEdit409JSONResponse(body(f)), nil
		case 429:
			return api.StartEdit429JSONResponse(body(f)), nil
		case 422:
			return api.StartEdit422JSONResponse(body(f)), nil
		case 500:
			return api.StartEdit500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(f))}, nil
		case 503:
			return api.StartEdit503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(f))}, nil
		}
		return nil, undocumented("startEdit", f)
	}
	return api.StartEdit202JSONResponse(runOf(run, nil)), nil
}

func (h *Runs) startEdit(ctx context.Context, req api.StartEditRequestObject) (store.AgentRun, *failure) {
	who, f := h.begin(ctx, true)
	if f != nil {
		return store.AgentRun{}, f
	}
	project, err := ids.ParseProjectID(req.Id)
	if err != nil {
		return store.AgentRun{}, bad("the project id is not valid")
	}
	var in api.StartEditRequest
	if req.Body != nil {
		in = *req.Body
	}
	instructions := ""
	if in.Instructions != nil {
		instructions = *in.Instructions
	}
	input, err := editor.NewInput(instructions)
	if err != nil {
		return store.AgentRun{}, bad("instructions must be text of at most 2,000 characters")
	}
	profile := ""
	if in.Profile != nil {
		if profile = string(*in.Profile); !slices.Contains(Profiles, profile) {
			return store.AgentRun{}, bad("profile must be one of " + fmt.Sprint(Profiles))
		}
	}
	// The run's budget is not the caller's to set: the server takes it from the plan's price list for the action it decides this run is
	// (credits.QuoteEdit). budget_usd is still validated, so a client that sends nonsense is told, but its value is ignored.
	if in.BudgetUsd != nil {
		if usd := float64(*in.BudgetUsd); math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
			return store.AgentRun{}, bad("budget_usd must be a positive number")
		}
	}

	// One unfinished editor run per project, decided under the project's lock together with creating the run, so two requests
	// cannot both pass the check. A project of another workspace is not found by the scoped query.
	guard := func(ctx context.Context, tx *store.Store) error {
		if _, err := tx.GetProjectForUpdate(ctx, who.Workspace, project); err != nil {
			return err
		}
		footage, err := tx.ListProjectFootage(ctx, who.Workspace, project)
		if err != nil {
			return err
		}
		// A usable clip is a ready video (an audio file is footage of the project, but there is nothing to cut).
		if !slices.ContainsFunc(footage, func(r store.ProjectFootageRow) bool { return r.Kind == store.AssetKindVideo }) {
			return errNoFootage
		}
		_, err = tx.ActiveAgentRunForProject(ctx, who.Workspace, project, editor.Name)
		switch {
		case err == nil:
			return errActive
		case errors.Is(err, store.ErrNotFound):
			return nil
		}
		return err
	}
	run, err := h.svc.StartGuarded(ctx, runtime.StartParams{
		Agent: editor.Name, WorkspaceID: who.Workspace, ProjectID: project.UUID, Input: input, Profile: profile,
		StartedBy: who.User,
	}, guard)
	switch {
	case err == nil:
		return run, nil
	case errors.Is(err, store.ErrNotFound):
		return store.AgentRun{}, errProjectNotFound
	case errors.Is(err, errNoFootage):
		return store.AgentRun{}, &failure{http.StatusUnprocessableEntity, api.ErrorCodeAssetsNotUsable, "the project has no ready video clip to edit"}
	case errors.Is(err, errActive):
		return store.AgentRun{}, &failure{http.StatusConflict, api.ErrorCodeRunInProgress, "the project already has an editor run that has not finished"}
	case errors.Is(err, store.ErrInsufficientCredits):
		// The stable code the web app opens its upgrade dialog on. The message carries no numbers: the balance is in GET /v1/me.
		return store.AgentRun{}, &failure{http.StatusPaymentRequired, api.ErrorCodeCreditsExhausted, "this workspace does not have enough credits for this edit"}
	case errors.Is(err, credits.ErrTooManyUncharged):
		var tm *credits.TooManyUnchargedError
		msg := "too many editor runs today ended with nothing to keep"
		if errors.As(err, &tm) {
			msg = fmt.Sprintf("this workspace has %d editor runs in progress or ended today that were cancelled before they made anything, found nothing to change or failed, which is the most per day; try again after 00:00 UTC", tm.Cap)
		}
		return store.AgentRun{}, &failure{http.StatusTooManyRequests, api.ErrorCodeTooManyUnchargedRuns, msg}
	case errors.Is(err, runtime.ErrBYOKKeyRequired):
		return store.AgentRun{}, &failure{http.StatusPaymentRequired, api.ErrorCodeByokKeyRequired, "this plan uses your own Anthropic key; add it in settings"}
	case errors.Is(err, credits.ErrNoPlan):
		return store.AgentRun{}, &failure{http.StatusPaymentRequired, api.ErrorCodePlanRequired, "this workspace has no active plan"}
	case errors.Is(err, runtime.ErrInvalidInput):
		return store.AgentRun{}, bad("the run cannot be started with this input")
	case errors.Is(err, runtime.ErrUnknownAgent), errors.Is(err, llm.ErrNoKey), errors.Is(err, llm.ErrStageDisabled):
		h.logger.WarnContext(ctx, "runs: the editor agent cannot be started", "error", err)
		return store.AgentRun{}, &failure{http.StatusServiceUnavailable, api.ErrorCodeModelNotConfigured, "the editor agent is not available on this server"}
	}
	return store.AgentRun{}, h.internal(ctx, "start edit", err)
}

var (
	errNoFootage = errors.New("runapi: the project has no ready clip")
	errActive    = errors.New("runapi: the project has an unfinished editor run")
)

// ---- GetRun -----------------------------------------------------------------------------------------------------

// GetRun is GET /v1/runs/{id}.
func (h *Runs) GetRun(ctx context.Context, req api.GetRunRequestObject) (api.GetRunResponseObject, error) {
	run, f := h.getRun(ctx, req)
	if f != nil {
		switch f.status {
		case 400:
			return api.GetRun400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(f))}, nil
		case 401:
			return api.GetRun401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(f))}, nil
		case 403:
			return api.GetRun403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(f))}, nil
		case 404:
			return api.GetRun404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(f))}, nil
		case 500:
			return api.GetRun500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(f))}, nil
		case 503:
			return api.GetRun503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(f))}, nil
		}
		return nil, undocumented("getRun", f)
	}
	return api.GetRun200JSONResponse(run), nil
}

func (h *Runs) getRun(ctx context.Context, req api.GetRunRequestObject) (api.Run, *failure) {
	who, f := h.begin(ctx, false)
	if f != nil {
		return api.Run{}, f
	}
	id, f := parseRunID(req.Id)
	if f != nil {
		return api.Run{}, f
	}
	after, limit := 0, defaultEvents
	if req.Params.AfterSeq != nil {
		if after = *req.Params.AfterSeq; after < 0 || after > math.MaxInt32 {
			return api.Run{}, bad("after_seq must be 0 or a sequence number")
		}
	}
	if req.Params.Limit != nil {
		if limit = *req.Params.Limit; limit < 1 || limit > maxEvents {
			return api.Run{}, bad("limit must be between 1 and 500")
		}
	}
	run, err := h.svc.Store.GetAgentRun(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Run{}, errRunNotFound
	}
	if err != nil {
		return api.Run{}, h.internal(ctx, "get run", err)
	}
	events, err := h.svc.Store.AgentEvents(ctx, who.Workspace, id, int32(after), limit) //nolint:gosec // range checked above
	if err != nil {
		return api.Run{}, h.internal(ctx, "run events", err)
	}
	out := runOf(run, events)
	if run.Status == store.AgentWaitingInput {
		out.Question = h.question(ctx, who.Workspace, id)
	}
	return out, nil
}

// question is the unanswered question of a run that waits for input: the last event is the status that parked it.
func (h *Runs) question(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) *string {
	e, err := h.svc.Store.LastAgentEvent(ctx, ws, id)
	if err != nil || e.Type != store.EventStatus {
		return nil
	}
	if q := runtime.QuestionOf(e); q != "" {
		return &q
	}
	return nil
}

// ---- CancelRun --------------------------------------------------------------------------------------------------

// CancelRun is POST /v1/runs/{id}/cancel.
func (h *Runs) CancelRun(ctx context.Context, req api.CancelRunRequestObject) (api.CancelRunResponseObject, error) {
	run, f := h.cancelRun(ctx, req)
	if f != nil {
		switch f.status {
		case 400:
			return api.CancelRun400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(f))}, nil
		case 401:
			return api.CancelRun401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(f))}, nil
		case 403:
			return api.CancelRun403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(f))}, nil
		case 404:
			return api.CancelRun404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(f))}, nil
		case 500:
			return api.CancelRun500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(f))}, nil
		case 503:
			return api.CancelRun503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(f))}, nil
		}
		return nil, undocumented("cancelRun", f)
	}
	return api.CancelRun200JSONResponse(runOf(run, nil)), nil
}

func (h *Runs) cancelRun(ctx context.Context, req api.CancelRunRequestObject) (store.AgentRun, *failure) {
	who, f := h.begin(ctx, true)
	if f != nil {
		return store.AgentRun{}, f
	}
	id, f := parseRunID(req.Id)
	if f != nil {
		return store.AgentRun{}, f
	}
	run, err := h.svc.Cancel(ctx, who.Workspace, id)
	switch {
	case err == nil:
		return run, nil
	case errors.Is(err, store.ErrNotFound):
		return store.AgentRun{}, errRunNotFound
	}
	return store.AgentRun{}, h.internal(ctx, "cancel run", err)
}

// ---- ReplyToRun -------------------------------------------------------------------------------------------------

// ReplyToRun is POST /v1/runs/{id}/reply.
func (h *Runs) ReplyToRun(ctx context.Context, req api.ReplyToRunRequestObject) (api.ReplyToRunResponseObject, error) {
	run, f := h.replyToRun(ctx, req)
	if f != nil {
		switch f.status {
		case 400:
			return api.ReplyToRun400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse(body(f))}, nil
		case 401:
			return api.ReplyToRun401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(body(f))}, nil
		case 403:
			return api.ReplyToRun403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(body(f))}, nil
		case 404:
			return api.ReplyToRun404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(body(f))}, nil
		case 409:
			return api.ReplyToRun409JSONResponse(body(f)), nil
		case 500:
			return api.ReplyToRun500JSONResponse{InternalErrorJSONResponse: api.InternalErrorJSONResponse(body(f))}, nil
		case 503:
			return api.ReplyToRun503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse(body(f))}, nil
		}
		return nil, undocumented("replyToRun", f)
	}
	return api.ReplyToRun200JSONResponse(runOf(run, nil)), nil
}

func (h *Runs) replyToRun(ctx context.Context, req api.ReplyToRunRequestObject) (store.AgentRun, *failure) {
	who, f := h.begin(ctx, true)
	if f != nil {
		return store.AgentRun{}, f
	}
	id, f := parseRunID(req.Id)
	if f != nil {
		return store.AgentRun{}, f
	}
	if req.Body == nil {
		return store.AgentRun{}, bad("a request body is required")
	}
	err := h.svc.Reply(ctx, who.Workspace, id, req.Body.Text)
	switch {
	case err == nil:
	case errors.Is(err, runtime.ErrEmptyReply), errors.Is(err, runtime.ErrReplyTooLong):
		return store.AgentRun{}, bad("text must be 1 to 16,384 bytes of text")
	case errors.Is(err, store.ErrNotFound):
		return store.AgentRun{}, errRunNotFound
	case errors.Is(err, store.ErrNotWaiting):
		return store.AgentRun{}, &failure{http.StatusConflict, api.ErrorCodeRunNotWaiting, "the run is not waiting for an answer"}
	default:
		return store.AgentRun{}, h.internal(ctx, "reply to run", err)
	}
	run, err := h.svc.Store.GetAgentRun(ctx, who.Workspace, id)
	if err != nil {
		return store.AgentRun{}, h.internal(ctx, "get run", err)
	}
	return run, nil
}

// ---- Conversions ------------------------------------------------------------------------------------------------

// runOf converts a run row, with the summaries of the events given (none for the answers of start, cancel and reply).
func runOf(r store.AgentRun, events []store.AgentEvent) api.Run {
	tokens := api.RunTokens{Input: r.InputTokens, CacheWrite: r.CacheWriteTokens, CacheRead: r.CacheReadTokens, Output: r.OutputTokens}
	if prompt := r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens; prompt > 0 {
		rate := float32(float64(r.CacheReadTokens) / float64(prompt))
		tokens.CacheHitRate = &rate
	}
	out := api.Run{
		Id: ids.Encode(ids.PrefixRun, r.ID), Agent: r.Agent, Status: api.RunStatus(r.Status), FailureReason: r.FailureReason,
		RoutingProfile: r.RoutingProfile, Model: r.Model, Effort: r.Effort, BilledTo: r.BilledTo,
		CostMicros: r.CostMicros, BudgetMicros: r.BudgetMicros, Tokens: tokens,
		ToolCalls: int(r.ToolCalls), BudgetToolCalls: int(r.BudgetToolCalls), ActiveMs: r.ActiveMs, BudgetWallClockMs: r.BudgetWallClockMs,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Events: make([]api.RunEvent, 0, len(events)), LastSeq: int(r.LastSeq),
	}
	if !r.ProjectID.IsZero() {
		p := r.ProjectID.String()
		out.ProjectId = &p
	}
	if summary, version := runtime.OutputSummary(r.Output); summary != "" || version != nil {
		if summary != "" {
			out.Summary = &summary
		}
		out.TimelineVersion = version
	}
	for _, e := range events {
		s := runtime.SummarizeEvent(e)
		ev := api.RunEvent{Seq: int(s.Seq), Type: api.RunEventType(s.Type), Text: s.Text, CreatedAt: e.CreatedAt}
		if s.Tool != "" {
			t := s.Tool
			ev.Tool = &t
		}
		if s.IsError {
			yes := true
			ev.IsError = &yes
		}
		out.Events = append(out.Events, ev)
	}
	return out
}
