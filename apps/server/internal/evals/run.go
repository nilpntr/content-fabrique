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
	// Anchors, when set, gives the judge's anchors: the calibration cases' renders with their human verdicts (judge.go). Read once a run.
	Anchors func(ctx context.Context) ([]Anchor, error)
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
	// Repeat is how many times each live case is run (its samples); 0 or 1 is once. A stored case is scored once: its score is the same
	// every time.
	Repeat int
	// CodeHash names the code the run scored (baseline.go); it is written with the results.
	CodeHash string
	Now      func() time.Time
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
	// Score is the mean of the case's samples (a failed sample counts 0), SD their standard deviation (two or more), N how many counted.
	Score       *float64       `json:"score,omitempty"`
	SD          *float64       `json:"sd,omitempty"`
	N           int            `json:"n"`
	Samples     []SampleResult `json:"samples,omitempty"`
	Failures    []string       `json:"failures,omitempty"`
	FailureRate float64        `json:"failure_rate"`
	CostMicros  int64          `json:"cost_micros"`
	TraceID     string         `json:"trace_id,omitempty"`
	Started     time.Time      `json:"started"`
	Ended       time.Time      `json:"ended"`

	// Of one sample, while it runs.
	failed                  bool
	reason                  string
	editMicros, judgeMicros int64
	judgeWanted             bool
}

// RunResult is a whole suite run.
type RunResult struct {
	Suite     string       `json:"suite"`
	RunID     string       `json:"suite_run"`
	Label     string       `json:"label,omitempty"`
	Cases     []CaseResult `json:"-"`
	Mean      *float64     `json:"mean"`
	MeanSE    *float64     `json:"mean_se,omitempty"`
	HeldOut   *float64     `json:"held_out_mean,omitempty"`
	HeldOutSE *float64     `json:"held_out_se,omitempty"`
	Repeat    int          `json:"repeat"`
	Samples   int          `json:"samples"`
	Failures  int          `json:"failures"`
	// FailureRate is failed samples over samples.
	FailureRate float64 `json:"failure_rate"`
	// CalibrationError is the mean distance of the judge's scores from the human ranges on the calibration cases (0: it agrees).
	CalibrationError *float64 `json:"calibration_error,omitempty"`
	CodeHash         string   `json:"code_hash,omitempty"`
	SpentMicro       int64    `json:"spent_micros"`
	JSONL            string   `json:"jsonl,omitempty"`
	Experiment       string   `json:"experiment_url,omitempty"`
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
	res := &RunResult{Suite: s.Name, RunID: runID, Label: o.Label, Repeat: max(o.Repeat, 1), CodeHash: o.CodeHash}
	rs := &runState{runID: runID}
	if !o.NoJudge && d.Judge != nil && d.Anchors != nil {
		anchors, err := d.Anchors(ctx)
		if err != nil {
			logf("judge anchors: none (%v)", err)
		}
		rs.anchors = anchors
	}
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
		cr := runCase(ctx, s, c, d, o, rs, res.SpentMicro)
		res.SpentMicro += cr.CostMicros
		res.Cases = append(res.Cases, cr)
		line := "skipped: " + cr.Skipped
		switch {
		case cr.Score != nil:
			line = fmt.Sprintf("score %s (checks %d/%d, cost $%.3f)", ScoreText(cr), cr.Checks.Passed, cr.Checks.Passed+cr.Checks.Failed, float64(cr.CostMicros)/1e6)
			for _, f := range cr.Failures {
				line += "\n" + strings.Repeat(" ", 31) + "failed " + f
			}
		case cr.Error != "":
			line = "error: " + cr.Error
		}
		logf("  %-28s %s", c.ID, line)
		if out != nil {
			if err := writeLine(out, cr); err != nil {
				return res, err
			}
		}
	}
	summarise(res)
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

// runState is what the cases of one run share.
type runState struct {
	runID   string
	anchors []Anchor
}

// liveCase says the case is edited live in this run.
func liveCase(c Case, d Deps, o Options) bool {
	return o.Live && d.Edit != nil && c.Calibration == nil && len(c.Missing(true)) == 0
}

// runCase runs a case's samples (Repeat for a live case, one otherwise) and sets its mean, spread and failures. The case's details
// (checks, feedback, judge, parts) are those of its last scored sample; every sample is in Samples.
func runCase(ctx context.Context, s *Suite, c Case, d Deps, o Options, rs *runState, spent int64) CaseResult {
	n := 1
	if liveCase(c, d, o) {
		n = max(o.Repeat, 1)
	}
	var detail CaseResult
	var samples []SampleResult
	var cost int64
	var started time.Time
	for k := 1; k <= n; k++ {
		sr := runSample(ctx, s, c, d, o, rs, spent+cost, k)
		if k == 1 {
			started = sr.Started
		}
		cost += sr.CostMicros
		if sr.Skipped != "" {
			if k == 1 {
				return sr // nothing ran
			}
			detail.Skipped = fmt.Sprintf("only %d of %d samples ran: %s", k-1, n, sr.Skipped)
			break
		}
		smp := SampleResult{N: k, Score: sr.Score, Failed: sr.failed, Reason: sr.reason, EditorRun: sr.EditorRun, Project: sr.Project,
			Version: sr.Version, EditMicros: sr.editMicros, JudgeMicros: sr.judgeMicros, TraceID: sr.TraceID}
		smp.Unjudged = sr.judgeWanted && sr.Judge == nil && !sr.failed && sr.Score != nil
		if smp.Failed {
			smp.Score = nil
		}
		samples = append(samples, smp)
		if detail.Case == "" || (sr.Score != nil && !sr.failed) {
			skipped := detail.Skipped
			detail = sr
			detail.Skipped = skipped
		}
	}
	detail.Samples, detail.CostMicros, detail.Started, detail.Ended = samples, cost, started, o.Now()
	aggregate(&detail)
	return detail
}

// runSample runs and scores a case once.
func runSample(ctx context.Context, s *Suite, c Case, d Deps, o Options, rs *runState, spent int64, sample int) (cr CaseResult) {
	runID := rs.runID
	cr = CaseResult{Suite: s.Name, RunID: runID, Case: c.ID, Title: c.Title, HeldOut: c.HeldOut, Started: o.Now()}
	ctx = telemetry.WithTraceID(ctx, telemetry.DerivedTraceID(fmt.Sprintf("eval/%s/%s/%d", runID, c.ID, sample)))
	ctx, span := tracer().Start(ctx, fmt.Sprintf("eval.case %s #%d", c.ID, sample), trace.WithNewRoot(), trace.WithAttributes(
		telemetry.Kind(telemetry.KindChain), attribute.String("cf.suite", s.Name), attribute.String("cf.suite_run", runID),
		attribute.String("cf.case", c.ID), attribute.Int("cf.sample", sample), attribute.Bool("cf.held_out", c.HeldOut),
		attribute.Bool("cf.calibration", c.Calibration != nil)))
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
		if cr.failed {
			span.SetAttributes(attribute.Bool("cf.failed", true), attribute.String("cf.failure", cr.reason))
		}
		if cr.Error != "" {
			span.SetStatus(codes.Error, telemetry.Cap(cr.Error, 200))
		}
		span.End()
	}()
	left := func() int64 { return o.MaxMicros - spent - cr.CostMicros }
	judgeOn := !o.NoJudge && d.Judge != nil && d.Visuals != nil
	cr.judgeWanted = judgeOn

	// The version: made now (live), or the stored one.
	var t Target
	live := liveCase(c, d, o)
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
		cr.editMicros = t.EditMicros
		if t.RunID != uuid.Nil {
			cr.EditorRun = ids.Encode(ids.PrefixRun, t.RunID)
		}
		if err != nil {
			// A run that failed (its budget, an answer cut off at max_tokens, an error) is a bad outcome of this code, not a missing sample:
			// it scores 0 and is listed.
			cr.Mode, cr.Error, cr.failed, cr.reason = "live", err.Error(), true, err.Error()
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
		if live {
			cr.failed, cr.reason = true, cr.Error
		}
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
		in, err := judgeInput(ctx, c, t, m, s, d, rs.anchors)
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
			cr.judgeMicros = v.CostMicros
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

func judgeInput(ctx context.Context, c Case, t Target, m Measurement, s *Suite, d Deps, anchors []Anchor) (JudgeInput, error) {
	render, strips, err := d.Visuals(ctx, t, m)
	if err != nil {
		return JudgeInput{}, err
	}
	in := JudgeInput{CaseID: c.ID, Render: render, Strips: strips, Brief: m.Brief, Expectations: c.Expectations, Feedback: s.Feedback,
		StyleEvidence: m.StyleEvidence, Timeline: describeTimeline(m.Spec)}
	// The calibration anchors, but never a case's own (a calibration case shown itself would agree by construction).
	for _, a := range anchors {
		if a.CaseID != c.ID {
			in.Anchors = append(in.Anchors, a)
		}
	}
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
	exp, err := p.CreateExperiment(ctx, datasetID, name, "cfctl eval suite "+s.Name, map[string]any{"suite_run": res.RunID, "label": res.Label,
		"mean": res.Mean, "mean_se": res.MeanSE, "held_out": res.HeldOut, "failure_rate": res.FailureRate, "repeat": res.Repeat,
		"calibration_error": res.CalibrationError, "code_hash": res.CodeHash, "spent_micros": res.SpentMicro}, res.Repeat)
	if err != nil {
		return "", err
	}
	for _, c := range res.Cases {
		node := nodes[c.Case]
		if node == "" {
			continue
		}
		samples := c.Samples
		if len(samples) == 0 { // skipped: one run that says why
			samples = []SampleResult{{N: 1, TraceID: c.TraceID}}
		}
		for i, smp := range samples {
			out := map[string]any{"score": smp.Score, "failed": smp.Failed, "case_mean": c.Score, "case_sd": c.SD, "case_n": c.N,
				"failure_rate": c.FailureRate, "mode": c.Mode, "version": smp.Version, "project": smp.Project, "editor_run": smp.EditorRun,
				"checks_failing": c.Checks.Failing, "judge_note": c.JudgeNote, "trace": p.TraceURL(ctx, telemetry.PhoenixProject, smp.TraceID)}
			errText := c.Error
			switch {
			case c.Skipped != "" && len(c.Samples) == 0:
				errText = "skipped: " + c.Skipped
			case smp.Failed:
				errText = "failed (scored 0): " + smp.Reason
			case len(c.Samples) > 0:
				errText = ""
			}
			rid, err := p.AddRun(ctx, exp.ID, node, out, c.Started, c.Ended, smp.TraceID, errText, i+1)
			if err != nil {
				return "", err
			}
			evs := []Evaluation{{Name: "sample_score", Kind: AnnotatorCode, Score: sampleValue(smp) / 100, Label: sampleLabel(smp)}}
			if i == len(samples)-1 {
				evs = append(evs, evaluations(c)...) // the case's mean and details once, on its last sample
			}
			for _, e := range evs {
				if err := p.AddEvaluation(ctx, rid, e, c.Started, c.Ended); err != nil {
					return "", err
				}
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
		out = append(out, Evaluation{Name: "score", Kind: AnnotatorCode, Score: *c.Score / 100, Label: ScoreText(c), Explanation: strings.Join(c.Failures, "; ")})
	}
	if len(c.Samples) > 0 {
		out = append(out, Evaluation{Name: "failure_rate", Kind: AnnotatorCode, Score: c.FailureRate, Label: fmt.Sprintf("%d/%d", len(c.Failures), len(c.Samples))})
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

func sampleValue(s SampleResult) float64 {
	if v, ok := s.counted(false); ok {
		return v
	}
	return 0
}

func sampleLabel(s SampleResult) string {
	switch {
	case s.Failed:
		return "failed: 0"
	case s.Unjudged:
		return "unjudged"
	case s.Score == nil:
		return "-"
	}
	return fmt.Sprintf("%.1f", *s.Score)
}
