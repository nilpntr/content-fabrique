package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Name is the agent's name in agent_runs.agent, and Stage its routing stage (config/routing): v1-start runs it on Opus 5.5,
// dev-cheap on Haiku 5.5.
const (
	Name  = "editor"
	Stage = "editor"
)

// DefaultBudgets are the editor's budgets for a run that does not name its own: $5, 60 tool calls and 15 minutes. The token
// budget counts cache reads too, which dominate a long run (the tool definitions and the conversation are read again on every
// call), so it is generous; the dollar budget is what really limits a run.
var DefaultBudgets = runtime.Budgets{Micros: 5_000_000, Tokens: 4_000_000, ToolCalls: 60, WallClock: 15 * time.Minute}

// NewAgent defines the editor agent: its prompt, its tools, and the context it starts every run with.
func NewAgent(d Deps) (runtime.Agent, error) {
	tools, err := Tools(d)
	if err != nil {
		return runtime.Agent{}, err
	}
	return runtime.Agent{
		Name:    Name,
		Stage:   Stage,
		System:  SystemPrompt(),
		Tools:   tools,
		Opening: opening,
		Context: d.runContext,
		Budgets: DefaultBudgets,
	}, nil
}

// opening is the first user message: what to do, and the creator's extra instructions as data.
func opening(raw json.RawMessage) ([]llm.Block, error) {
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	blocks := []llm.Block{llm.TextBlock("Make the video for this project now. The brief, the clips and the state of the timeline follow.")}
	if in.Instructions != "" {
		blocks = append(blocks,
			llm.TextBlock("The creator added these instructions for this run:"),
			runtime.UntrustedBlock("instructions", in.Instructions))
	}
	return blocks, nil
}

// runContext is the run context: the project's format and brief, the footage and the timeline as they are when the run starts.
// The runtime stores it once and sends the same text on every resume, so the prompt cache stays valid. The creator's text is
// cut short here; the tools return all of it.
func (d Deps) runContext(ctx context.Context, rc runtime.RunContext) ([]llm.Block, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return nil, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return nil, projectErr(perr)
	}
	rows, err := d.Footage.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return nil, fmt.Errorf("list footage: %w", err)
	}
	f := timeline.Format(det.Project.Format)
	w, h, _ := f.Size()
	blocks := []llm.Block{llm.TextBlock(fmt.Sprintf("Project context, as it was when the run started. Output format: %s (%dx%d).", f, w, h))}
	if strings.TrimSpace(det.Project.Brief) == "" {
		blocks = append(blocks, llm.TextBlock("Brief: the creator wrote none."))
	} else {
		blocks = append(blocks, llm.TextBlock("Brief (the creator's words):"), runtime.UntrustedBlock("brief", det.Project.Brief))
	}
	blocks = append(blocks, llm.TextBlock(footageText(rows, contextTitleChars, contextDescriptionChars)))

	status := "Timeline: none yet (version 0)."
	if det.Project.CurrentTimelineVersion != nil {
		v := int(*det.Project.CurrentTimelineVersion)
		vd, perr := d.Projects.Version(ctx, actor(ws), pid, v)
		if perr != nil {
			return nil, projectErr(perr)
		}
		spec, err := timeline.Parse(vd.Version.Spec)
		if err != nil {
			return nil, fmt.Errorf("parse stored spec: %w", err)
		}
		status = "Timeline: " + timelineLine(v, spec) + ". Call get_timeline to read it."
	}
	return append(blocks, llm.TextBlock(status)), nil
}
