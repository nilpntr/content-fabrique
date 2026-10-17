package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
)

// Budgets are the limits of one run. A run stops, as failed with reason "budget", before the call that would pass one.
type Budgets struct {
	// Micros is the dollar budget in micro-dollars, at list price. The llm client reserves each call's worst case against
	// it, so a run cannot overspend it.
	Micros int64
	// Tokens is the total of input, cache and output tokens the run may use. It is checked before each model call, so a
	// call may go over by its own size.
	Tokens int64
	// ToolCalls is the number of tool calls (counted when the call starts).
	ToolCalls int
	// WallClock is the time the run may spend running. Time waiting for the user does not count.
	WallClock time.Duration
}

// Limits of the budgets.
const (
	// DefaultWallClock is the wall-clock budget of a run that does not name one (design note, section 3).
	DefaultWallClock = 15 * time.Minute
	// MaxWallClock is the longest wall-clock budget a run may have; the River job timeout is longer.
	MaxWallClock = 30 * time.Minute
)

// DefaultBudgets are what a run gets for the budgets its agent and its caller leave unset: $1, a million tokens, 60 tool
// calls and 15 minutes.
var DefaultBudgets = Budgets{Micros: 1_000_000, Tokens: 1_000_000, ToolCalls: 60, WallClock: DefaultWallClock}

// over returns b with the fields of o that are set.
func (b Budgets) over(o Budgets) Budgets {
	if o.Micros > 0 {
		b.Micros = o.Micros
	}
	if o.Tokens > 0 {
		b.Tokens = o.Tokens
	}
	if o.ToolCalls > 0 {
		b.ToolCalls = o.ToolCalls
	}
	if o.WallClock > 0 {
		b.WallClock = o.WallClock
	}
	return b
}

func (b Budgets) validate() error {
	switch {
	case b.Micros <= 0:
		return errors.New("runtime: the dollar budget must be positive")
	case b.Tokens <= 0:
		return errors.New("runtime: the token budget must be positive")
	case b.ToolCalls <= 0:
		return errors.New("runtime: the tool-call budget must be positive")
	case b.WallClock <= 0 || b.WallClock > MaxWallClock:
		return fmt.Errorf("runtime: the wall-clock budget must be between 1ms and %s", MaxWallClock)
	}
	return nil
}

// EndTurn is an agent's answer to a turn that ended with text and no tool call.
type EndTurn struct {
	// Nudge, when not empty, is sent to the model as a user message and the run goes on. It is stored in the run's events, so a
	// resumed run sends the same one. The agent's hook is told how many nudges the run has had (nudges), and must stop nudging
	// at some point: the runtime does not limit them.
	Nudge string
	// Output, when set (and Nudge is empty), is the output the run succeeds with. Empty keeps the default: {"summary": the text,
	// "stop_reason": ...}.
	Output json.RawMessage
}

// Agent is a definition: a system prompt, tools and defaults. Runs of it are agent_runs rows.
type Agent struct {
	// Name identifies the agent in agent_runs.agent: lowercase letters, digits and underscores.
	Name string
	// Stage is the routing stage that picks its model and effort (config/routing): editor, ideation, ...
	Stage string
	// System is the agent's system prompt. The runtime puts SystemPreamble before it and marks the cache breakpoint after it.
	// It must be the same for every run, so the prompt cache works across runs.
	System string
	// Tools are the tools the model may call. A run ends when a tool returns a Final result.
	Tools []Tool
	// Opening turns the run's input into the first user message. Nil uses the input's "message" as untrusted user text.
	Opening func(input json.RawMessage) ([]llm.Block, error)
	// Context builds the run context that follows the system prompt (the brief, the footage list, the timeline summary): text
	// that is the same for the whole run. It runs once, when the run starts, and its blocks are stored in the run's events so
	// a resumed run sends exactly the same prefix. Text from users goes through Untrusted. Nil means no context.
	Context func(ctx context.Context, rc RunContext) ([]llm.Block, error)
	// EndTurn, when set, is asked when the model ends its turn with text and no tool call. It may answer with a message to send the
	// model instead of ending the run (a nudge), or with the output the run ends with. Nil ends the run with the text as its
	// summary. See EndTurn.
	EndTurn func(ctx context.Context, rc RunContext, text string, nudges int) (EndTurn, error)
	// OnLimit, when set, is asked when the run reaches a budget limit (limit is one of the Limit* names), before the run is failed
	// for it. The agent may end the run gracefully instead: it returns the output the run succeeds with and true, and the run ends
	// succeeded, with a status event that names the limit. False fails the run with reason budget, as it does without the hook. It
	// is for an agent whose work is already in a usable state when the limit comes (the editor, whose newest timeline version
	// has been previewed), and it must not make model calls or spend anything.
	OnLimit func(ctx context.Context, rc RunContext, limit string) (output json.RawMessage, ok bool)
	// OnEnd, when set, is called after a run of this agent has ended in any way (finished, failed, stopped, cancelled), with the
	// run's id, so the agent can drop what it kept for the run in memory. It runs in the worker that ended the run.
	OnEnd func(run uuid.UUID)
	// Thinking, when set, is sent with every request. Nil leaves the model's default. Effort always comes from the routing
	// entry.
	Thinking *llm.Thinking
	// Budgets are this agent's defaults for the fields a caller leaves unset.
	Budgets Budgets

	registry *Registry
}

var agentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Agents is the set of agents a worker can run.
type Agents struct{ by map[string]*Agent }

// NewAgents checks the definitions and builds the set.
func NewAgents(defs ...Agent) (*Agents, error) {
	a := &Agents{by: map[string]*Agent{}}
	for i := range defs {
		d := defs[i]
		if !agentNameRE.MatchString(d.Name) {
			return nil, fmt.Errorf("runtime: agent name %q must be lowercase letters, digits and underscores", d.Name)
		}
		if _, dup := a.by[d.Name]; dup {
			return nil, fmt.Errorf("runtime: agent %q is defined twice", d.Name)
		}
		if d.Stage == "" || d.System == "" {
			return nil, fmt.Errorf("runtime: agent %q needs a stage and a system prompt", d.Name)
		}
		reg, err := NewRegistry(d.Tools...)
		if err != nil {
			return nil, fmt.Errorf("runtime: agent %q: %w", d.Name, err)
		}
		d.registry = reg
		if err := DefaultBudgets.over(d.Budgets).validate(); err != nil {
			return nil, fmt.Errorf("runtime: agent %q: %w", d.Name, err)
		}
		a.by[d.Name] = &d
	}
	return a, nil
}

// Get returns the agent with this name.
func (a *Agents) Get(name string) (*Agent, bool) {
	d, ok := a.by[name]
	return d, ok
}

// Names lists the agents, sorted.
func (a *Agents) Names() []string {
	out := make([]string, 0, len(a.by))
	for n := range a.by {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Input is the usual input of a run: one message from the user.
type Input struct {
	Message string `json:"message"`
}

// opening is the first user message's blocks.
func (d *Agent) opening(input json.RawMessage) ([]llm.Block, error) {
	if d.Opening != nil {
		return d.Opening(input)
	}
	var in Input
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("runtime: the run's input is not {\"message\": ...}: %w", err)
		}
	}
	if in.Message == "" {
		return nil, errors.New("runtime: the run's input has no message")
	}
	return []llm.Block{UntrustedBlock("user_message", in.Message)}, nil
}
