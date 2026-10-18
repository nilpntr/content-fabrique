package runtime

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Service is what the API and the CLI call: start a run, answer an ask_user, cancel a run. The loop itself is Runner.
type Service struct {
	Store    *store.Store
	Enqueuer *jobs.Enqueuer
	Router   *llm.Router
	Factory  llm.ClientFactory
	Agents   *Agents
	// Credits prices the runs of the agents that cost credits (the editor): it decides first edit or revision, sets the credits aside in
	// the transaction that creates the run, and gives the run its budget. Nil refuses to start such a run (ErrNoCredits): no editor run
	// is ever started without a hold.
	Credits *credits.Service
	// Inline is for the dev CLI, which runs the loop itself: Reply then does not enqueue a job.
	Inline bool
}

// StartParams describe a run to start.
type StartParams struct {
	Agent       string
	WorkspaceID ids.WorkspaceID
	// ProjectID and ParentRunID are uuid.Nil when there is none.
	ProjectID   uuid.UUID
	ParentRunID uuid.UUID
	Input       json.RawMessage
	// Profile overrides the environment's routing profile for this run (a test can use v1-start while the default stays
	// dev-cheap). Empty uses ROUTING_PROFILE.
	Profile string
	// Budgets override the agent's defaults field by field; zero fields keep them.
	Budgets Budgets
	// NoCredits starts an editor run that costs no credits (cfctl agent run --no-credits, development and ci only; the callers refuse it
	// elsewhere). The run still gets a hold, of zero credits, and its budget is the caller's.
	NoCredits bool
	// Quality is how carefully an editor run works (credits.Standard, the default when empty, or credits.Max, the creator's choice for one
	// edit): it picks the price, the budget and the routing entry (`editor_max`, `revision_max`).
	Quality credits.Quality
	// StartedBy is the member who started the run, for the usage ledger's member_user_id; the zero value when nobody did (cfctl).
	StartedBy ids.UserID
}

// Errors of the service.
var (
	// ErrUnknownAgent is returned for an agent name nobody defined.
	ErrUnknownAgent = errors.New("runtime: unknown agent")
	// ErrInvalidInput is returned, wrapped, when the run's input does not suit the agent or a budget is out of range.
	ErrInvalidInput = errors.New("runtime: invalid run input")
	// ErrBYOKKeyRequired is returned when the workspace is on a plan whose model calls its own Anthropic key must pay for and the run would
	// be billed to another key (ours): the API answers 402 byok_key_required.
	ErrBYOKKeyRequired = errors.New("runtime: this plan uses the workspace's own Anthropic key, and the run would not be billed to it")
	// ErrNoCredits is returned when a run that costs credits is started by a Service that has no credits service.
	ErrNoCredits = errors.New("runtime: this service has no credits service, so a run that costs credits cannot be started")
	// ErrEmptyReply and ErrReplyTooLong are returned by Reply.
	ErrEmptyReply   = errors.New("runtime: the reply is empty")
	ErrReplyTooLong = errors.New("runtime: the reply is too long")
)

// RevisionStage is the routing stage that a revision of an editor run resolves its model and limits from (config/routing).
const RevisionStage = "revision"

// MaxSuffix is added to the routing stage of an edit of quality max: `editor_max`, `revision_max`.
const MaxSuffix = "_max"

// MaxReplyBytes is the longest reply to an ask_user.
const MaxReplyBytes = 16 * 1024

// Create inserts a queued run into tx, which is a Store from InTx (or the pool Store for the CLI, which runs the loop
// itself). The routing entry (model, effort, max_tokens) is resolved now and kept on the row. The caller enqueues the job
// in the same transaction: Start does both.
func (s *Service) Create(ctx context.Context, tx *store.Store, p StartParams) (store.AgentRun, error) {
	agent, ok := s.Agents.Get(p.Agent)
	if !ok {
		return store.AgentRun{}, fmt.Errorf("%w: %q (the agents are %s)", ErrUnknownAgent, p.Agent, strings.Join(s.Agents.Names(), ", "))
	}
	if _, err := agent.opening(p.Input); err != nil {
		return store.AgentRun{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	// An editor run is an edit that costs credits: the server decides which kind (first or revision) from the project, takes the price and
	// the budget from the plan's price list, never from the caller, and sets the credits aside below, in this transaction.
	var quote *credits.Quote
	if credits.BillsEdits(agent.Name) {
		if s.Credits == nil {
			return store.AgentRun{}, ErrNoCredits
		}
		if p.ProjectID == uuid.Nil {
			return store.AgentRun{}, fmt.Errorf("%w: an editor run needs a project", ErrInvalidInput)
		}
		q, err := s.Credits.QuoteEdit(ctx, tx, p.WorkspaceID, ids.ProjectID{UUID: p.ProjectID}, p.NoCredits, p.Quality)
		if err != nil {
			return store.AgentRun{}, err
		}
		quote = &q
		if q.BudgetMicros > 0 {
			p.Budgets.Micros = q.BudgetMicros
		}
	}
	budgets := DefaultBudgets.over(agent.Budgets).over(p.Budgets)
	if err := budgets.validate(); err != nil {
		return store.AgentRun{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	// An extra-careful edit (quality max) is routed on the stage's `_max` entry; a profile without it refuses the edit loudly rather than
	// running it as a standard one the creator paid more credits for.
	suffix := ""
	if quote != nil && quote.Quality == credits.Max {
		suffix = MaxSuffix
	}
	route, err := s.Router.Resolve(p.Profile, agent.Stage+suffix)
	if err != nil {
		return store.AgentRun{}, err
	}
	// A revision is cheaper than a first edit, so it runs on the profile's `revision` entry (a smaller max_tokens: its budget is a third of
	// a first edit's). The run keeps the agent's stage for the ledger and the cassettes; only the model, effort and limits come from the
	// revision entry. A profile without one keeps the editor's.
	if quote != nil && (quote.Action == credits.EditRevision || quote.Action == credits.EditRevisionMax) {
		rev, err := s.Router.Resolve(p.Profile, RevisionStage+suffix)
		var noStage *routing.NoStageError
		switch {
		case err == nil:
			route = rev
		case errors.As(err, &noStage):
			// the profile has no revision entry: the editor's stands
		default:
			return store.AgentRun{}, err // a revision entry that is there and cannot be used (a typo'd model, a disabled stage) is loud
		}
	}
	// Asking the factory now fails fast when there is no key for the workspace (a missing key would otherwise show up
	// in the worker), and says who pays.
	_, billing, err := s.Factory.For(ctx, p.WorkspaceID, agent.Stage)
	if err != nil {
		return store.AgentRun{}, err
	}
	// A BYOK plan's run budget ($2) is not counted as our cost because the creator's own key pays for it. Until a workspace's own key can be
	// used (P4-00b) the factory bills our key, and such a run would cost us up to its budget for a fraction of its price: refuse it.
	if quote != nil {
		if want := credits.RequiredBilling(quote.BillingMode); want != "" && string(billing) != want {
			return store.AgentRun{}, ErrBYOKKeyRequired
		}
	}
	input := p.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	run, err := tx.CreateAgentRun(ctx, p.WorkspaceID, store.NewAgentRun{
		Agent: agent.Name, Stage: agent.Stage, ParentRunID: p.ParentRunID, ProjectID: p.ProjectID, Input: input,
		RoutingProfile: route.Profile, Model: route.Model,
		// Effort is always set explicitly: a stage whose profile says nothing runs at medium.
		Effort: cmp.Or(route.Effort, "medium"), MaxTokens: route.MaxTokens, MinOutputTokens: route.MinOutputTokens,
		BudgetMicros: budgets.Micros, BudgetTokens: budgets.Tokens, BudgetToolCalls: budgets.ToolCalls,
		BudgetWallClockMs: budgets.WallClock.Milliseconds(), BilledTo: string(billing), StartedBy: p.StartedBy,
	})
	if err != nil {
		return store.AgentRun{}, err
	}
	if quote != nil {
		if _, err := s.Credits.Hold(ctx, tx, *quote, run); err != nil {
			return store.AgentRun{}, err
		}
		// After the hold, so that "no credits" (402) wins over "too many runs that ended without a charge today" (429).
		if err := s.Credits.CheckUncharged(ctx, tx, p.WorkspaceID, *quote); err != nil {
			return store.AgentRun{}, err
		}
	}
	return run, nil
}

// Start creates a run and enqueues its agent_run job in one transaction.
func (s *Service) Start(ctx context.Context, p StartParams) (store.AgentRun, error) {
	return s.StartGuarded(ctx, p, nil)
}

// StartGuarded is Start with a check that runs first, inside the same transaction: guard receives the transaction's Store and
// may refuse the run by returning an error, which is returned as it is and creates nothing. A guard that locks a row (the
// project, say) makes "no other run is active" and "create this one" a single step, so two requests cannot both pass it.
func (s *Service) StartGuarded(ctx context.Context, p StartParams, guard func(ctx context.Context, tx *store.Store) error) (store.AgentRun, error) {
	var run store.AgentRun
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		if guard != nil {
			if err := guard(ctx, tx); err != nil {
				return err
			}
		}
		var err error
		if run, err = s.Create(ctx, tx, p); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, run)
	})
	return run, err
}

func (s *Service) enqueue(ctx context.Context, tx *store.Store, run store.AgentRun) error {
	if s.Inline {
		return nil
	}
	if s.Enqueuer == nil {
		return errors.New("runtime: no job enqueuer is configured")
	}
	_, err := s.Enqueuer.InsertInStore(ctx, tx, jobs.AgentRunArgs{WorkspaceID: run.WorkspaceID, RunID: run.ID}, nil)
	return err
}

// Get returns the run and its events after afterSeq.
func (s *Service) Get(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID, afterSeq int32) (store.AgentRun, []store.AgentEvent, error) {
	run, err := s.Store.GetAgentRun(ctx, ws, id)
	if err != nil {
		return store.AgentRun{}, nil, err
	}
	events, err := s.Store.AgentEvents(ctx, ws, id, afterSeq, 0)
	return run, events, err
}

// Reply answers the question of a run that is waiting_input: the text becomes the result of the ask_user call (inside an
// untrusted-data block), the run goes back to queued, and its job is enqueued again, all in one transaction. It returns
// store.ErrNotWaiting when the run is not waiting for input.
func (s *Service) Reply(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID, text string) error {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	switch {
	case text == "":
		return ErrEmptyReply
	case len(text) > MaxReplyBytes:
		return ErrReplyTooLong
	}
	return s.Store.InTx(ctx, func(tx *store.Store) error {
		run, err := tx.QueueAgentRunAfterReply(ctx, ws, id)
		if err != nil {
			return err
		}
		last, err := tx.LastAgentEvent(ctx, ws, id)
		if err != nil {
			return err
		}
		var wait statusPayload
		if last.Type != store.EventStatus || json.Unmarshal(last.Payload, &wait) != nil || wait.State != stateWaitingInput || wait.ToolUseID == "" {
			return fmt.Errorf("runtime: run %s is waiting for input but its last event is not the question", id)
		}
		answer := toolResultPayload{
			ToolUseID: wait.ToolUseID, Name: wait.Tool,
			Content: []wireBlock{{Type: "text", Text: Untrusted("user_reply", text)}},
		}
		encoded := mustJSON(answer)
		if len(encoded) > store.MaxEventPayloadBytes { // escaped control characters can grow a short reply
			return ErrReplyTooLong
		}
		if _, err := tx.AppendAgentEventUnfenced(ctx, ws, id, store.EventToolResult, encoded); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, run)
	})
}

// Cancel asks for the run to end. A run that is queued or waiting for input is cancelled at once; a running one ends
// between steps. It returns the run as it is after the request.
func (s *Service) Cancel(ctx context.Context, ws ids.WorkspaceID, id uuid.UUID) (store.AgentRun, error) {
	return s.Store.RequestAgentRunCancel(ctx, ws, id,
		mustJSON(map[string]any{"stopped": "cancelled", "summary": "Cancelled before finishing."}),
		mustJSON(statusPayload{State: stateCancelled}))
}
