package editor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
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
	d = d.withState()
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
		EndTurn: d.endTurn,
		OnEnd:   d.previews.forget,
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

// The run context is stored as one event, which may hold 64 KB, and the creator's text is up to 4,000 characters of brief and
// 255 of title and a description of any length per clip, up to 50 clips. It is built under a budget instead: maxContextBytes of
// encoded text, well below the event limit, with the creator's text cut harder step by step until it fits. The tools
// (get_brief, list_footage) return all of it, so nothing is lost, only moved out of the first message.
const maxContextBytes = 40 * 1024

// contextCaps are one step of that: how much of the brief, and of the first few clips' titles and descriptions, is kept.
type contextCaps struct{ briefChars, titleChars, descChars, detailed int }

var contextSteps = []contextCaps{
	{briefChars: 1500, titleChars: 60, descChars: 160, detailed: 20},
	{briefChars: 800, titleChars: 40, descChars: 80, detailed: 10},
	{briefChars: 400, titleChars: 30, descChars: 0, detailed: 5},
	{briefChars: 200, titleChars: 0, descChars: 0, detailed: 0},
}

// runContext is the run context: the project's format and brief, the footage and the timeline as they are when the run starts.
// The runtime stores it once and sends the same text on every resume, so the prompt cache stays valid.
func (d Deps) runContext(ctx context.Context, rc runtime.RunContext) ([]llm.Block, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return nil, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return nil, projectErr(perr)
	}
	rows, err := d.Store.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return nil, fmt.Errorf("list footage: %w", err)
	}
	sums, err := d.analysisSummaries(ctx, ws, rows)
	if err != nil {
		return nil, err
	}
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
	for _, caps := range contextSteps {
		blocks := contextBlocks(det.Project.Format, det.Project.Brief, rows, sums, status, caps)
		if encodedSize(blocks) <= maxContextBytes {
			return blocks, nil
		}
	}
	return nil, runtime.UserErrorf("the project's text is too large for the run context even when cut hard")
}

func contextBlocks(format, brief string, rows []store.ProjectFootageRow, sums map[ids.AssetID]analysis.Summary, status string, caps contextCaps) []llm.Block {
	f := timeline.Format(format)
	w, h, _ := f.Size()
	blocks := []llm.Block{llm.TextBlock(fmt.Sprintf("Project context, as it was when the run started. Output format: %s (%dx%d).", f, w, h))}
	if strings.TrimSpace(brief) == "" {
		blocks = append(blocks, llm.TextBlock("Brief: the creator wrote none."))
	} else {
		excerpt := cutRunes(brief, caps.briefChars)
		intro := "Brief (the creator's words):"
		if excerpt != brief {
			intro = "Brief (the creator's words, cut short here; get_brief has all of it):"
		}
		blocks = append(blocks, llm.TextBlock(intro), runtime.UntrustedBlock("brief", excerpt))
	}
	blocks = append(blocks, llm.TextBlock(footageText(rows, footageOpts{Detailed: caps.detailed, TitleChars: caps.titleChars, DescChars: caps.descChars, Analysis: sums})))
	return append(blocks, llm.TextBlock(status))
}

// encodedSize is the size of the blocks as the run's event will hold them: JSON, without HTML escaping.
func encodedSize(blocks []llm.Block) int {
	type wire struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	list := make([]wire, len(blocks))
	for i, b := range blocks {
		list[i] = wire{Type: string(b.Type), Text: b.Text}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(list); err != nil {
		return math.MaxInt
	}
	return buf.Len()
}
