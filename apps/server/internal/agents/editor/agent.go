package editor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
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
		OnLimit: d.onLimit,
		OnEnd: func(run uuid.UUID) {
			d.previews.forget(run) // what the run kept in memory
			d.endRun(run)          // and its comments, now that its result is stored
		},
		Budgets: DefaultBudgets,
	}, nil
}

// opening is the first user message: what to do, and the creator's extra instructions as data.
func opening(raw json.RawMessage) ([]llm.Block, error) {
	in, err := parseInput(raw)
	if err != nil {
		return nil, err
	}
	blocks := []llm.Block{llm.TextBlock("Make the video for this project now. The brief, the clips and the state of the timeline follow."), llm.TextBlock(vibeNote(in.Vibe))}
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
type contextCaps struct {
	briefChars, titleChars, descChars, detailed int
	// comments is how many open comments are written out in full, and commentChars how much of each one's text is kept; the
	// count and the guidance are always there, and get_comments has all of them.
	comments, commentChars int
	// subjects is where the person is in each clip, for the footage lines (the same in every step).
	subjects subject.Index
	// music is the music analysis of the clips whose sound is a song (the same in every step).
	music map[string]music.Analysis
}

var contextSteps = []contextCaps{
	{briefChars: 1500, titleChars: 60, descChars: 160, detailed: 20, comments: 10, commentChars: 300},
	{briefChars: 800, titleChars: 40, descChars: 80, detailed: 10, comments: 5, commentChars: 150},
	{briefChars: 400, titleChars: 30, descChars: 0, detailed: 5, comments: 3, commentChars: 60},
	{briefChars: 200, titleChars: 0, descChars: 0, detailed: 0, comments: 0},
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
	now := timelineItems{ids: map[string]bool{}}
	var chosen map[string]bool
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
		now = itemsOf(v, spec)
		chosen = chosenSounds(spec)
	}
	// The creator's open comments on earlier versions, when there are any (a first run has none, and its context is unchanged).
	comments, total, err := d.openComments(ctx, ws, pid, 0)
	if err != nil {
		return nil, err
	}
	hint := contentTypeHint(det.Project.Brief, rows, sums)
	styleBlk := styleBlocks(d.style(ctx, ws))
	subjects := d.subjects(ctx, ws, rows)
	songs := d.musicOf(ctx, ws, rows, chosen)
	for _, caps := range contextSteps {
		caps.subjects, caps.music = subjects, songs
		blocks := contextBlocks(det.Project.Format, det.Project.Brief, rows, sums, status, caps)
		if hint != "" {
			blocks = append(blocks, llm.TextBlock(hint))
		}
		blocks = append(blocks, styleBlk...)
		blocks = append(blocks, commentsIntro(comments, total, now, caps.comments, caps.commentChars)...)
		if encodedSize(blocks) <= maxContextBytes {
			// Only the comments this context shows (the rest are for get_comments to list) are the run's to deal with.
			if shown := min(caps.comments, len(comments)); shown > 0 {
				if err := d.Comments.RecordHandoff(ctx, ws, pid, rc.RunID, comments[:shown]); err != nil {
					return nil, err
				}
			}
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
	blocks = append(blocks, llm.TextBlock(footageText(rows, footageOpts{Detailed: caps.detailed, TitleChars: caps.titleChars, DescChars: caps.descChars, Analysis: sums, Subjects: caps.subjects, Music: caps.music})))
	return append(blocks, llm.TextBlock(status))
}

// contentTypeHint is a one-line guess at the kind of video, from the words of the brief and the clips' titles and descriptions and
// from what the analysis found (a clip with several reps is a workout). It is fixed for the run (the context is stored once), so it
// does not disturb the prompt cache, and the model decides in plan_edit; "" when there is no clear winner.
func contentTypeHint(brief string, rows []store.ProjectFootageRow, sums map[ids.AssetID]analysis.Summary) string {
	var b strings.Builder
	b.WriteString(brief)
	for _, r := range rows {
		// a new line between the fields: a phrase does not run from one clip's title into the next one's
		b.WriteString("\n" + r.Title + "\n" + r.Description)
	}
	reps := 0
	for _, s := range sums {
		reps += s.Reps
	}
	if reps >= 4 {
		b.WriteString(" workout workout")
	}
	t := LikelyContentType(b.String())
	if t == "" {
		return ""
	}
	return fmt.Sprintf("A guess at the kind of video, from the words of the brief and the clips and from the analysis (you decide: confirm or correct it in plan_edit): %s.", t)
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
