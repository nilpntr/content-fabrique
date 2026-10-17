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
}

// Errors of the service.
var (
	// ErrUnknownAgent is returned for an agent name nobody defined.
	ErrUnknownAgent = errors.New("runtime: unknown agent")
	// ErrInvalidInput is returned, wrapped, when the run's input does not suit the agent or a budget is out of range.
	ErrInvalidInput = errors.New("runtime: invalid run input")
	// ErrEmptyReply and ErrReplyTooLong are returned by Reply.
	ErrEmptyReply   = errors.New("runtime: the reply is empty")
	ErrReplyTooLong = errors.New("runtime: the reply is too long")
)

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
	budgets := DefaultBudgets.over(agent.Budgets).over(p.Budgets)
	if err := budgets.validate(); err != nil {
		return store.AgentRun{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	route, err := s.Router.Resolve(p.Profile, agent.Stage)
	if err != nil {
		return store.AgentRun{}, err
	}
	// Asking the factory now fails fast when there is no key for the workspace (a missing key would otherwise show up
	// in the worker), and says who pays.
	_, billing, err := s.Factory.For(ctx, p.WorkspaceID, agent.Stage)
	if err != nil {
		return store.AgentRun{}, err
	}
	input := p.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return tx.CreateAgentRun(ctx, p.WorkspaceID, store.NewAgentRun{
		Agent: agent.Name, Stage: agent.Stage, ParentRunID: p.ParentRunID, ProjectID: p.ProjectID, Input: input,
		RoutingProfile: route.Profile, Model: route.Model,
		// Effort is always set explicitly: a stage whose profile says nothing runs at medium.
		Effort: cmp.Or(route.Effort, "medium"), MaxTokens: route.MaxTokens,
		BudgetMicros: budgets.Micros, BudgetTokens: budgets.Tokens, BudgetToolCalls: budgets.ToolCalls,
		BudgetWallClockMs: budgets.WallClock.Milliseconds(), BilledTo: string(billing),
	})
}

// Start creates a run and enqueues its agent_run job in one transaction.
func (s *Service) Start(ctx context.Context, p StartParams) (store.AgentRun, error) {
	var run store.AgentRun
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
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
	text = strings.TrimSpace(text)
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
		if _, err := tx.AppendAgentEventUnfenced(ctx, ws, id, store.EventToolResult, mustJSON(answer)); err != nil {
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
