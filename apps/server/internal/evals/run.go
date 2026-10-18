package evals

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Target is the version a case is scored on.
type Target struct {
	Workspace ids.WorkspaceID
	Project   ids.ProjectID
	Version   int
	// RunID is the editor run that made the version, when the suite made it live (uuid.Nil otherwise).
	RunID uuid.UUID
	// EditMicros is what that run cost.
	EditMicros int64
}

// Measurement is what the deterministic part of a case reads and finds.
type Measurement struct {
	Report editor.Report
	Spec   *timeline.Spec
	Plan   *editor.Plan
	Assets map[string]editor.AssetAnalysis
	Brief  string
	// StyleEvidence is the creator's style profile in words, and StyleNames the names of their reference videos (the evidence lines).
	StyleEvidence string
	StyleNames    []string
}

// JudgeScorer is the judge. *Judge is the real one.
type JudgeScorer interface {
	Score(ctx context.Context, ws ids.WorkspaceID, runID uuid.UUID, in JudgeInput, budget int64) (Verdict, error)
	Lookup(in JudgeInput) (Verdict, bool, error)
}

// Deps are what a suite run needs from the world; cfctl builds the real ones, tests fakes.
type Deps struct {
	// Edit runs the editor live on a case, with a dollar budget, and returns the version it made. Nil: no live runs.
	Edit func(ctx context.Context, c Case, budgetMicros int64) (Target, error)
	// Stored finds a case's stored version.
	Stored func(ctx context.Context, c Case) (Target, error)
	// Measure runs the checks and reads what the judge is told.
	Measure func(ctx context.Context, t Target) (Measurement, error)
	// Visuals cuts the render's contact sheet and the strips around its cuts; References the contact sheets of the creator's videos.
	// Either may be nil: the judge then does not run.
	Visuals    func(ctx context.Context, t Target, m Measurement) (*Picture, []Picture, error)
	References func(ctx context.Context, c Case, m Measurement) ([]Picture, error)
	Judge      JudgeScorer
	// Phoenix, when set, receives the suite as a dataset and the run as an experiment.
	Phoenix *Phoenix
	// Log receives progress lines.
	Log io.Writer
}

// Options of a suite run.
type Options struct {
	// Live runs the editor on the cases that have footage (spending); without it, the stored versions are scored.
	Live bool
	// MaxMicros is the hard total the run may spend, editor runs and judge calls together; 0 spends nothing (the judge answers from its
	// cache only).
	MaxMicros int64
	// NoJudge skips the judge.
	NoJudge bool
	// Only, when set, keeps the cases with these ids.
	Only []string
	// OutDir is where the JSONL goes (eval-results); "" writes none.
	OutDir string
	// Label names the run in Phoenix (the improvement loop says "baseline" or the change).
	Label string
	Now   func() time.Time
}

// CaseResult is one line of the results.
type CaseResult struct {
	Suite       string             `json:"suite"`
	RunID       string             `json:"suite_run"`
	Case        string             `json:"case"`
	Title       string             `json:"title,omitempty"`
	HeldOut     bool               `json:"held_out,omitempty"`
	Calibration *CalibrationResult `json:"calibration,omitempty"`
	Mode        string             `json:"mode"` // live, stored or skipped
	Skipped     string             `json:"skipped,omitempty"`
	Error       string             `json:"error,omitempty"`
	Workspace   string             `json:"workspace,omitempty"`
	Project     string             `json:"project,omitempty"`
	Version     int                `json:"version,omitempty"`
	EditorRun   string             `json:"editor_run,omitempty"`
	Checks      struct {
		Passed  int      `json:"passed"`
		Failed  int      `json:"failed"`
		Skipped int      `json:"skipped"`
		Failing []string `json:"failing,omitempty"`
	} `json:"checks"`
	StyleScore *float64         `json:"style_score,omitempty"`
	Feedback   []FeedbackResult `json:"feedback,omitempty"`
	Judge      *Verdict         `json:"judge,omitempty"`
	JudgeNote  string           `json:"judge_note,omitempty"`
	MusicSync  *Score           `json:"music_sync,omitempty"`
	Parts      []Part           `json:"parts,omitempty"`
	Score      *float64         `json:"score,omitempty"`
	CostMicros int64            `json:"cost_micros"`
	TraceID    string           `json:"trace_id,omitempty"`
	Started    time.Time        `json:"started"`
	Ended      time.Time        `json:"ended"`
}

// RunResult is a whole suite run.
type RunResult struct {
	Suite      string       `json:"suite"`
	RunID      string       `json:"suite_run"`
	Label      string       `json:"label,omitempty"`
	Cases      []CaseResult `json:"-"`
	Mean       *float64     `json:"mean"`
	HeldOut    *float64     `json:"held_out_mean,omitempty"`
	SpentMicro int64        `json:"spent_micros"`
	JSONL      string       `json:"jsonl,omitempty"`
	Experiment string       `json:"experiment_url,omitempty"`
}

// ByCase returns the result of a case.
func (r *RunResult) ByCase(id string) (CaseResult, bool) {
	for _, c := range r.Cases {
		if c.Case == id {
			return c, true
		}
	}
	return CaseResult{}, false
}

// minEditMicros is the least a live editor run is started with: below it a run cannot make an edit.
const minEditMicros = 300_000

// minJudgeMicros is the least a judge call is started with.
const minJudgeMicros = 30_000

func tracer() trace.Tracer { return otel.Tracer("github.com/nilpntr/content-fabrique/internal/evals") }

// Run runs the suite.
func Run(ctx context.Context, s *Suite, d Deps, o Options) (*RunResult, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	logf := func(format string, a ...any) {
		if d.Log != nil {
			_, _ = fmt.Fprintf(d.Log, format+"\n", a...)
		}
	}
	runID := o.Now().UTC().Format("20060102-150405") + "-" + uuid.NewString()[:8]
	res := &RunResult{Suite: s.Name, RunID: runID, Label: o.Label}
	var out *os.File
	if o.OutDir != "" {
		dir := filepath.Join(o.OutDir, s.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		res.JSONL = filepath.Join(dir, runID+".jsonl")
		f, err := os.OpenFile(res.JSONL, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- our results directory
		if err != nil {
			return nil, err
		}
		defer f.Close() //nolint:errcheck // written line by line, closed on exit
		out = f
	}
	only := map[string]bool{}
	for _, id := range o.Only {
		only[id] = true
	}
	for _, raw := range s.Cases {
		if len(only) > 0 && !only[raw.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		c := s.Resolved(raw)
		cr := runCase(ctx, s, c, d, o, runID, res.SpentMicro)
		res.SpentMicro += cr.CostMicros
		res.Cases = append(res.Cases, cr)
		line := "skipped: " + cr.Skipped
		switch {
		case cr.Error != "":
			line = "error: " + cr.Error
		case cr.Score != nil:
			line = fmt.Sprintf("score %.1f (checks %d/%d, cost $%.3f)", *cr.Score, cr.Checks.Passed, cr.Checks.Passed+cr.Checks.Failed, float64(cr.CostMicros)/1e6)
		}
		logf("  %-28s %s", c.ID, line)
		if out != nil {
			if err := writeLine(out, cr); err != nil {
				return res, err
			}
		}
	}
	res.Mean, res.HeldOut = means(res.Cases)
	if d.Phoenix != nil {
		if url, err := logExperiment(ctx, d.Phoenix, s, res); err != nil {
			logf("phoenix: the experiment was not logged: %v", err)
		} else {
			res.Experiment = url
		}
	}
	if out != nil {
		if err := writeLine(out, map[string]any{"summary": res}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// LoadResults reads a results file back: the cases and the summary line.
func LoadResults(path string) (*RunResult, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a results file of ours
	if err != nil {
		return nil, err
	}
	var res *RunResult
	var cases []CaseResult
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, `{"summary":`) {
			var s struct {
				Summary RunResult `json:"summary"`
			}
			if err := json.Unmarshal([]byte(line), &s); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			res = &s.Summary
			continue
		}
		var c CaseResult
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		cases = append(cases, c)
	}
	if res == nil {
		return nil, fmt.Errorf("%s has no summary line: the run did not finish", path)
	}
	res.Cases, res.JSONL = cases, path
	return res, nil
}

func writeLine(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// means are the mean score of the cases that count (scored, not calibration, not held out) and of the held-out ones.
func means(cases []CaseResult) (*float64, *float64) {
	var sum, n, hsum, hn float64
	for _, c := range cases {
		if c.Score == nil || c.Calibration != nil {
			continue
		}
		if c.HeldOut {
			hsum, hn = hsum+*c.Score, hn+1
			continue
		}
		sum, n = sum+*c.Score, n+1
	}
	var m, h *float64
	if n > 0 {
		v := math.Round(10*sum/n) / 10
		m = &v
	}
	if hn > 0 {
		v := math.Round(10*hsum/hn) / 10
		h = &v
	}
	return m, h
}

func runCase(ctx context.Context, s *Suite, c Case, d Deps, o Options, runID string, spent int64) (cr CaseResult) {
	cr = CaseResult{Suite: s.Name, RunID: runID, Case: c.ID, Title: c.Title, HeldOut: c.HeldOut, Started: o.Now()}
	ctx = telemetry.WithTraceID(ctx, telemetry.DerivedTraceID("eval/"+runID+"/"+c.ID))
	ctx, span := tracer().Start(ctx, "eval.case "+c.ID, trace.WithNewRoot(), trace.WithAttributes(
		telemetry.Kind(telemetry.KindChain), attribute.String("cf.suite", s.Name), attribute.String("cf.suite_run", runID),
		attribute.String("cf.case", c.ID), attribute.Bool("cf.held_out", c.HeldOut), attribute.Bool("cf.calibration", c.Calibration != nil)))
	if sc := span.SpanContext(); sc.IsValid() {
		cr.TraceID = sc.TraceID().String()
	}
	defer func() {
		cr.Ended = o.Now()
		span.SetAttributes(attribute.String("cf.mode", cr.Mode), attribute.Int64("cf.cost_micros", cr.CostMicros))
		if cr.Score != nil {
			span.SetAttributes(attribute.Float64("cf.score", *cr.Score))
		}
		for _, p := range cr.Parts {
			span.SetAttributes(attribute.Float64("cf.part."+p.Name, p.Value))
		}
		if raw, err := json.Marshal(cr); err == nil {
			span.SetAttributes(telemetry.JSONValue(telemetry.OutputValue, json.RawMessage(raw))...)
		}
		if cr.Error != "" {
			span.SetStatus(codes.Error, telemetry.Cap(cr.Error, 200))
		}
		span.End()
	}()
	left := func() int64 { return o.MaxMicros - spent - cr.CostMicros }
	judgeOn := !o.NoJudge && d.Judge != nil && d.Visuals != nil

	// The version: made now (live), or the stored one.
	var t Target
	live := o.Live && d.Edit != nil && c.Calibration == nil && len(c.Missing(true)) == 0
	switch {
	case live:
		budget := min(int64(c.BudgetUSD*1e6), left())
		if judgeOn {
			budget = min(budget, left()-MaxJudgeMicros)
		}
		if budget < minEditMicros {
			cr.Mode, cr.Skipped = "skipped", fmt.Sprintf("the --max-usd budget has $%.2f left, too little for an editor run", float64(max(left(), 0))/1e6)
			return cr
		}
		var err error
		t, err = d.Edit(ctx, c, budget)
		cr.CostMicros += t.EditMicros
		if err != nil {
			cr.Mode, cr.Error = "live", err.Error()
			return cr
		}
		cr.Mode, cr.EditorRun = "live", ids.Encode(ids.PrefixRun, t.RunID)
	default:
		if miss := c.Missing(false); len(miss) > 0 {
			cr.Mode = "skipped"
			cr.Skipped = "no stored version to score (" + strings.Join(miss, ", ") + " still a placeholder: fill the local suite file), and not run live"
			if o.Live && d.Edit != nil && c.Calibration == nil {
				cr.Skipped = "the case's " + strings.Join(c.Missing(true), ", ") + " are placeholders: fill the local suite file"
			}
			return cr
		}
		var err error
		if t, err = d.Stored(ctx, c); err != nil {
			cr.Mode, cr.Error = "stored", err.Error()
			return cr
		}
		cr.Mode = "stored"
	}
	cr.Workspace, cr.Project, cr.Version = t.Workspace.String(), t.Project.String(), t.Version
	span.SetAttributes(attribute.String("cf.project_id", cr.Project), attribute.Int("cf.version", t.Version), attribute.String("cf.run_id", cr.EditorRun))

	// The deterministic part: the checks, the style distance, the measured feedback and the music sync.
	m, err := d.Measure(ctx, t)
	if err != nil {
		cr.Error = "measure: " + err.Error()
		return cr
	}
	pass, fail, skip := m.Report.Counts()
	cr.Checks.Passed, cr.Checks.Failed, cr.Checks.Skipped, cr.Checks.Failing = pass, fail, skip, m.Report.Failed()
	if m.Report.Style != nil {
		v := m.Report.Style.Score
		cr.StyleScore = &v
	}
	cr.Feedback = MeasureFeedback(s.Feedback, Measured{Spec: m.Spec, Report: m.Report, Assets: m.Assets})
	if ms, ok := MusicSync(m.Report); ok {
		cr.MusicSync = &ms
	}

	// The judge.
	if judgeOn {
		in, err := judgeInput(ctx, c, t, m, s, d)
		switch {
		case err != nil:
			cr.JudgeNote = "no judge: " + err.Error()
		case o.MaxMicros <= 0:
			if v, ok, _ := d.Judge.Lookup(in); ok {
				cr.Judge = &v
			} else {
				cr.JudgeNote = "no judge: not judged before, and --max-usd is 0 (a judge call costs up to $0.10)"
			}
		case left() < minJudgeMicros:
			if v, ok, _ := d.Judge.Lookup(in); ok {
				cr.Judge = &v
			} else {
				cr.JudgeNote = "no judge: the --max-usd budget is used up"
			}
		default:
			v, err := d.Judge.Score(ctx, t.Workspace, uuid.New(), in, min(MaxJudgeMicros, left()))
			cr.CostMicros += v.CostMicros
			switch {
			case err != nil:
				cr.JudgeNote = "judge failed: " + err.Error()
			case v.Reason != "":
				cr.JudgeNote = "judge gave no verdict: " + v.Reason
			default:
				cr.Judge = &v
			}
		}
	} else {
		cr.JudgeNote = "judge off"
	}
	var verdict *Verdict
	if cr.Judge != nil {
		verdict = cr.Judge
	}
	parts, score := Compose(m.Report, verdict, cr.MusicSync, c.Expectations, cr.Feedback)
	cr.Parts = parts
	if !math.IsNaN(score) {
		cr.Score = &score
	}
	cr.Calibration = Calibrate(c.Calibration, verdict, cr.MusicSync)
	return cr
}

func judgeInput(ctx context.Context, c Case, t Target, m Measurement, s *Suite, d Deps) (JudgeInput, error) {
	render, strips, err := d.Visuals(ctx, t, m)
	if err != nil {
		return JudgeInput{}, err
	}
	in := JudgeInput{CaseID: c.ID, Render: render, Strips: strips, Brief: m.Brief, Expectations: c.Expectations, Feedback: s.Feedback,
		StyleEvidence: m.StyleEvidence, Timeline: describeTimeline(m.Spec)}
	if m.Plan != nil {
		plan := map[string]any{"content_type": m.Plan.ContentType, "story": m.Plan.Story, "hook": m.Plan.Hook, "arc": m.Plan.Arc,
			"ending": m.Plan.Ending, "playbook": m.Plan.PlaybookVersion, "target_length_s": m.Plan.TargetLengthS, "tempo_bpm": m.Plan.TempoBPM, "pace": m.Plan.Pace}
		if f := m.Plan.Format; f != nil {
			plan["format"] = map[string]any{"id": f.ID, "why": f.Why, "mix": f.Mix, "new_to_creator": f.NewToCreator, "new_idea": f.NewIdea}
			in.Format = formatWords(f)
		}
		if raw, err := json.Marshal(plan); err == nil {
			in.Plan = string(raw)
		}
	}
	if ms, ok := MusicSync(m.Report); ok {
		in.Music = ms.Rationale
	} else {
		in.Music = "no beat grid was measured for this edit"
	}
	if d.References != nil {
		refs, err := d.References(ctx, c, m)
		if err != nil {
			return JudgeInput{}, fmt.Errorf("references: %w", err)
		}
		in.References = refs
	}
	if in.Render == nil {
		return JudgeInput{}, errors.New("the version has no ready preview render to look at")
	}
	return in, nil
}

// describeTimeline is a few lines of where the clips, transitions and texts are, for the judge: every text item and every graphics item
// (titles, story captions, chapter cards and the other motion-graphics templates), with its words, time, place and placement rule.
// formatWords is the plan's format for the judge: the card's name, pitch and beats from the library (our words), and the device mixed in.
func formatWords(f *editor.PlanFormat) string {
	card, ok := formats.Default().Card(f.ID)
	if !ok {
		return f.ID + " (not in this build's library)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): %s\n", card.Name, card.ID, card.Pitch)
	for _, beat := range card.Beats {
		fmt.Fprintf(&b, "- %s (%s): %s\n", beat.Name, beat.Timing, beat.What)
	}
	if mix, ok := formats.Default().Card(f.Mix); ok {
		fmt.Fprintf(&b, "mixed in: %s: %s\n", mix.Name, mix.Pitch)
	}
	if f.NewToCreator {
		b.WriteString("The editor marked it as new to this creator.\n")
	}
	if f.NewIdea {
		b.WriteString("The creator asked for a new idea: this version had to use another format than the project's earlier versions.\n")
	}
	return b.String()
}

func describeTimeline(spec *timeline.Spec) string {
	if spec == nil {
		return ""
	}
	var b strings.Builder
	if main, _, ok := timeline.MainTrack(spec); ok {
		for _, it := range main.Items {
			fmt.Fprintf(&b, "clip %s at %.1f s for %.1f s", it.ID, float64(it.AtMs)/1000, float64(timeline.OutputDuration(it))/1000)
			if it.TransitionIn != nil && it.TransitionIn.Type != timeline.TransitionCut {
				fmt.Fprintf(&b, ", transition in: %s", it.TransitionIn.Type)
			}
			if it.Motion.Active() {
				fmt.Fprintf(&b, ", motion %s", it.Motion.Preset)
			}
			fmt.Fprintf(&b, ", own sound %s\n", timeline.ClipAudioMode(it.Audio))
		}
	}
	text := func(kind string, t timeline.TextItem) {
		keep := "keeps off the body"
		if t.AvoidsFaceOnly() {
			keep = "keeps off the face only"
		}
		place := cmp.Or(t.Anchor, "its style's place")
		if t.Position != nil {
			place = fmt.Sprintf("at %.2f, %.2f", t.Position.X, t.Position.Y)
		}
		fmt.Fprintf(&b, "%s %s at %.1f s for %.1f s, style %s, %s, %s: %q\n", kind, t.ID, float64(t.AtMs)/1000, float64(t.DurationMs)/1000,
			t.Style, place, keep, t.Text)
	}
	for _, tr := range spec.Tracks {
		switch tr := tr.(type) {
		case *timeline.TextTrack:
			for _, it := range tr.Items {
				text("text", it)
			}
		case *timeline.GraphicsTrack:
			for _, g := range tr.Items {
				kind := "graphic " + g.Template
				if g.Template == timeline.TemplateChapterCard {
					kind = "chapter card"
				} else if g.IsTitle() {
					kind = "title graphic " + g.Template
				}
				text(kind, g.AsText())
			}
		}
	}
	fmt.Fprintf(&b, "total %.1f s, music: %v", float64(timeline.TotalDuration(spec))/1000, timeline.HasMusic(spec))
	return b.String()
}

// logExperiment sends the suite to Phoenix as a dataset and this run as an experiment, one run per scored case with its evaluations.
func logExperiment(ctx context.Context, p *Phoenix, s *Suite, res *RunResult) (string, error) {
	var examples []Example
	for _, raw := range s.Cases {
		c := s.Resolved(raw)
		var rubric []map[string]any
		for _, e := range c.Expectations {
			rubric = append(rubric, map[string]any{"id": e.ID, "text": e.Text, "weight": e.Weight})
		}
		meta := map[string]any{"held_out": c.HeldOut, "calibration": c.Calibration != nil}
		if c.Calibration != nil {
			meta["human_verdict"] = c.Calibration.Note
		}
		examples = append(examples, Example{ID: c.ID, Input: map[string]any{"case": c.ID, "title": c.Title, "brief": c.Brief, "vibe": c.Vibe, "profile": c.Profile},
			Output: map[string]any{"rubric": rubric}, Metadata: meta})
	}
	datasetID, nodes, err := p.UpsertDataset(ctx, "suite-"+s.Name, s.Description, examples)
	if err != nil {
		return "", err
	}
	name := res.RunID
	if res.Label != "" {
		name = res.Label + " " + res.RunID
	}
	exp, err := p.CreateExperiment(ctx, datasetID, name, "cfctl eval suite "+s.Name, map[string]any{"suite_run": res.RunID, "label": res.Label, "mean": res.Mean, "spent_micros": res.SpentMicro})
	if err != nil {
		return "", err
	}
	for _, c := range res.Cases {
		node := nodes[c.Case]
		if node == "" {
			continue
		}
		out := map[string]any{"score": c.Score, "mode": c.Mode, "version": c.Version, "project": c.Project, "editor_run": c.EditorRun,
			"checks_failing": c.Checks.Failing, "judge_note": c.JudgeNote, "trace": p.TraceURL(ctx, telemetry.PhoenixProject, c.TraceID)}
		errText := c.Error
		if c.Skipped != "" {
			errText = "skipped: " + c.Skipped
		}
		rid, err := p.AddRun(ctx, exp.ID, node, out, c.Started, c.Ended, c.TraceID, errText)
		if err != nil {
			return "", err
		}
		for _, e := range evaluations(c) {
			if err := p.AddEvaluation(ctx, rid, e, c.Started, c.Ended); err != nil {
				return "", err
			}
		}
	}
	return p.ExperimentURL(datasetID, exp.ID), nil
}

// evaluations are a case's scores as Phoenix evaluations: the total, the checks and style (code), each judge dimension (LLM), the
// music sync (code) and each feedback item (human).
func evaluations(c CaseResult) []Evaluation {
	var out []Evaluation
	if c.Score != nil {
		out = append(out, Evaluation{Name: "score", Kind: AnnotatorCode, Score: *c.Score / 100, Label: fmt.Sprintf("%.1f", *c.Score)})
	}
	if n := c.Checks.Passed + c.Checks.Failed; n > 0 {
		out = append(out, Evaluation{Name: "checks", Kind: AnnotatorCode, Score: float64(c.Checks.Passed) / float64(n),
			Label: fmt.Sprintf("%d/%d", c.Checks.Passed, n), Explanation: "failing: " + strings.Join(c.Checks.Failing, ", ")})
	}
	if c.StyleScore != nil {
		out = append(out, Evaluation{Name: "style_distance", Kind: AnnotatorCode, Score: *c.StyleScore})
	}
	if c.Judge != nil {
		for _, d := range judged {
			if s, ok := c.Judge.Scores[d]; ok {
				out = append(out, Evaluation{Name: d, Kind: AnnotatorLLM, Score: float64(s.Score), Label: fmt.Sprintf("%d/5", s.Score), Explanation: s.Rationale})
			}
		}
		for id, s := range c.Judge.Expectations {
			out = append(out, Evaluation{Name: "rubric." + id, Kind: AnnotatorLLM, Score: float64(s.Score), Label: fmt.Sprintf("%d/5", s.Score), Explanation: s.Rationale})
		}
	}
	if c.MusicSync != nil {
		out = append(out, Evaluation{Name: DimMusicSync, Kind: AnnotatorCode, Score: float64(c.MusicSync.Score), Label: fmt.Sprintf("%d/5", c.MusicSync.Score), Explanation: c.MusicSync.Rationale})
	}
	for _, f := range c.Feedback {
		if f.Status == Skip {
			continue
		}
		v := 0.0
		if f.Status == Pass {
			v = 1
		}
		out = append(out, Evaluation{Name: "feedback." + f.ID, Kind: AnnotatorHuman, Score: v, Label: f.Status, Explanation: f.Detail})
	}
	if c.Calibration != nil {
		v := 0.0
		if c.Calibration.Agrees {
			v = 1
		}
		out = append(out, Evaluation{Name: "calibration", Kind: AnnotatorHuman, Score: v, Label: fmt.Sprint(c.Calibration.Agrees), Explanation: c.Calibration.Note + " " + strings.Join(c.Calibration.Off, "; ")})
	}
	return out
}
