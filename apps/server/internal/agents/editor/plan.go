package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// plan_edit: the understand step and the creative plan (D-28, docs/design/edit-style-v1.md section 2). Before the first edit the
// editor says what the footage is and what it will make of it. The plan is validated (an enum for the type, bounded lengths), kept
// for the run (check_edit judges the edit against it), recorded as an artifact event for the run feed, and answered with the
// playbook of the chosen genre and the beat arithmetic of the plan.

// PlanArtifact is the kind of the artifact event a plan is recorded as. Its payload is
//
//	{"kind": "creative_plan", "tool_use_id": "toolu_...", "data": <Plan>}
//
// and Plan is the shape the web run feed reads. A later plan_edit call writes another event; the last one is the plan.
const PlanArtifact = "creative_plan"

// PlanVersion is the version of the Plan shape (Plan.Version). A reader checks it before it reads the rest.
const PlanVersion = 1

// Bounds of a plan. The strict tool schema cannot hold ranges, maxLength or maxItems, so the tool checks them and says which one is
// broken. They are tight on purpose: the plan is a short working note, and a plan_edit turn that thinks and then writes an essay runs
// out of output room (the first live Opus run's plan turn used all 16000 tokens and was cut off).
const (
	MaxPlanStoryChars  = 140
	MaxPlanHookChars   = 140
	MaxPlanArcChars    = 240
	MaxPlanEndingChars = 140
	MaxPlanWhatChars   = 80
	MinPlanTargetS     = 5
	MaxPlanTargetS     = 120
	MinTempoBPM        = 60
	MaxTempoBPM        = 200
	MaxPlanShots       = 16
	MaxShotBeats       = 64
	// MaxChapter is the highest chapter number of a listicle shot.
	MaxChapter = 9
	// nominalBPM counts the beats of a plan without a tempo (tempo_bpm 0): half a second each.
	nominalBPM = 120
)

// The shot roles, the paces and the effects a plan may name.
var (
	PlanRoles   = []string{"hook", "build", "peak", "detail", "step", "reveal", "payoff", "broll", "ending"}
	PlanPaces   = []string{"steady", "building", "fast"}
	PlanEffects = []string{"none", "punch_in", "push_in", "pull_back", "shake", "speed_ramp", "whip", "zoom_through", "flash", "punch", "handheld"}
)

// effectFeature is the feature each effect of a plan needs: a plan never promises an effect the build cannot make.
var effectFeature = map[string]string{
	"punch_in": "motion:punch_in", "push_in": "motion:push_in", "pull_back": "motion:pull_back", "shake": "motion:shake",
	"speed_ramp": "op:set_speed_ramp", "whip": "transition:whip_left", "zoom_through": "transition:zoom_through", "flash": "transition:flash",
	"punch": "transition:punch", "handheld": "motion:handheld",
}

// PlanShot is one shot of the shot list.
type PlanShot struct {
	// Role is what the shot is for: hook, build, peak, detail, step, reveal, payoff, broll or ending.
	Role string `json:"role"`
	// What is the shot in a few words, for the creator to read.
	What string `json:"what"`
	// AssetID is the clip the shot comes from, when the plan names it.
	AssetID string `json:"asset_id,omitempty"`
	// Beats is the shot's length in beats.
	Beats int `json:"beats"`
	// Effect is the effect the shot gets (none, punch_in, push_in, pull_back, shake, speed_ramp, whip, zoom_through, flash).
	Effect string `json:"effect,omitempty"`
	// Chapter is the exercise or step of a listicle the shot belongs to (1 to 9), 0 for none: the first shot of a chapter gets a numbered
	// chapter title (P6-06).
	Chapter int `json:"chapter,omitempty"`
	// LengthMs is Beats in milliseconds at the plan's tempo, worked out by the tool.
	LengthMs int64 `json:"length_ms"`
}

// Plan is the creative plan: the payload (data) of the creative_plan artifact. The tool fills Version, PlaybookVersion, BeatMs,
// EstimatedLengthMs, each shot's LengthMs and PromptVersion; everything else is the model's.
type Plan struct {
	Version int `json:"version"`
	// ContentType is one of talking, unboxing_product, workout, vlog, tutorial and general.
	ContentType string `json:"content_type"`
	// Story is what the video is, in one line.
	Story string `json:"story"`
	Hook  string `json:"hook"`
	Arc   string `json:"arc"`
	// Ending says how the video ends.
	Ending string `json:"ending"`
	// Playbook is the playbook the editor follows (usually the content type, or general), and PlaybookVersion its version.
	Playbook        string `json:"playbook"`
	PlaybookVersion string `json:"playbook_version"`
	// TargetLengthS is the length the editor aims for, in seconds.
	TargetLengthS int `json:"target_length_s"`
	// TempoBPM is the beat grid's tempo, 0 for a video that follows speech and has none. Pace is steady, building or fast.
	TempoBPM int    `json:"tempo_bpm"`
	Pace     string `json:"pace"`
	// BeatMs is the length of one beat (at 120 BPM when there is no tempo), EstimatedLengthMs the shot list's total.
	BeatMs            int64      `json:"beat_ms"`
	EstimatedLengthMs int64      `json:"estimated_length_ms"`
	Shots             []PlanShot `json:"shots"`
	// Vibe is the energy of the edit the plan was written for (calm, lively or hype; P6-06).
	Vibe string `json:"vibe,omitempty"`
	// PromptVersion is the editor prompt the plan was written under.
	PromptVersion string `json:"prompt_version"`
}

type planArgs struct {
	ContentType   string         `json:"content_type"`
	Story         string         `json:"story"`
	Hook          string         `json:"hook"`
	Arc           string         `json:"arc"`
	Ending        string         `json:"ending"`
	Playbook      string         `json:"playbook"`
	TargetLengthS int            `json:"target_length_s"`
	TempoBPM      int            `json:"tempo_bpm"`
	Pace          string         `json:"pace"`
	Shots         []planArgsShot `json:"shots"`
}

type planArgsShot struct {
	Role    string `json:"role"`
	What    string `json:"what"`
	AssetID string `json:"asset_id,omitempty"`
	Beats   int    `json:"beats"`
	Effect  string `json:"effect,omitempty"`
	Chapter int    `json:"chapter,omitempty"`
}

// BuildPlan checks a plan_edit call and works out its numbers. footage is the set of the project's clips (an asset id the plan names
// must be one of them); f is what the build can do. The error is written for the model.
func BuildPlan(in planArgs, footage map[string]bool, f Features, style *styleprofile.Profile) (Plan, error) {
	fail := func(format string, a ...any) (Plan, error) {
		return Plan{}, runtime.UserErrorf("the plan is not valid: "+format, a...)
	}
	text := func(name, s string, limit int) (string, error) {
		s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
		switch {
		case s == "":
			return "", fmt.Errorf("%s is empty: say it in a sentence", name)
		case !utf8.ValidString(s):
			return "", fmt.Errorf("%s is not valid text", name)
		case utf8.RuneCountInString(s) > limit:
			return "", fmt.Errorf("%s is %d characters; at most %d", name, utf8.RuneCountInString(s), limit)
		}
		return s, nil
	}
	p := Plan{Version: PlanVersion, PromptVersion: PromptVersion}
	if !ValidContentType(in.ContentType) {
		return fail("content_type %q is not one of %s", in.ContentType, strings.Join(ContentTypes, ", "))
	}
	if in.Playbook == "" {
		in.Playbook = in.ContentType
	}
	if !ValidContentType(in.Playbook) {
		return fail("playbook %q is not one of %s", in.Playbook, strings.Join(ContentTypes, ", "))
	}
	p.ContentType, p.Playbook, p.PlaybookVersion = in.ContentType, in.Playbook, PlaybookVersion(in.Playbook)
	var err error
	for _, fld := range []struct {
		name  string
		in    string
		limit int
		out   *string
	}{
		{"story", in.Story, MaxPlanStoryChars, &p.Story}, {"hook", in.Hook, MaxPlanHookChars, &p.Hook},
		{"arc", in.Arc, MaxPlanArcChars, &p.Arc}, {"ending", in.Ending, MaxPlanEndingChars, &p.Ending},
	} {
		if *fld.out, err = text(fld.name, fld.in, fld.limit); err != nil {
			return fail("%v", err)
		}
	}
	if in.TargetLengthS < MinPlanTargetS || in.TargetLengthS > MaxPlanTargetS {
		return fail("target_length_s is %d; it is a whole number of seconds from %d to %d", in.TargetLengthS, MinPlanTargetS, MaxPlanTargetS)
	}
	p.TargetLengthS = in.TargetLengthS
	if in.TempoBPM != 0 && (in.TempoBPM < MinTempoBPM || in.TempoBPM > MaxTempoBPM) {
		return fail("tempo_bpm is %d; use 0 for a video with no beat grid, or %d to %d (120 to 128 is the default for a short video)", in.TempoBPM, MinTempoBPM, MaxTempoBPM)
	}
	// A creator whose cuts sit loosely near the music's onsets (not on a grid) needs no beat grid, whatever the content type.
	if in.TempoBPM == 0 && typeLimits[p.ContentType].gridRequired && (!style.Firm() || !style.CutsOffBeat()) {
		return fail("tempo_bpm 0 means no beat grid, which a %s video needs: use 120 to 128 so the cuts land on the beat", p.ContentType)
	}
	p.TempoBPM = in.TempoBPM
	if !slices.Contains(PlanPaces, in.Pace) {
		return fail("pace %q is not one of %s", in.Pace, strings.Join(PlanPaces, ", "))
	}
	p.Pace = in.Pace
	if n := len(in.Shots); n < 1 || n > MaxPlanShots {
		return fail("the shot list has %d shots; it needs 1 to %d", n, MaxPlanShots)
	}
	bpm := p.TempoBPM
	if bpm == 0 {
		bpm = nominalBPM
	}
	p.BeatMs = BeatMs(bpm)
	for i, s := range in.Shots {
		n := i + 1
		if !slices.Contains(PlanRoles, s.Role) {
			return fail("shot %d: role %q is not one of %s", n, s.Role, strings.Join(PlanRoles, ", "))
		}
		what, err := text(fmt.Sprintf("shot %d: what", n), s.What, MaxPlanWhatChars)
		if err != nil {
			return fail("%v", err)
		}
		if s.Beats < 1 || s.Beats > MaxShotBeats {
			return fail("shot %d: beats is %d; a shot is 1 to %d beats long", n, s.Beats, MaxShotBeats)
		}
		effect := s.Effect
		if effect == "" {
			effect = "none"
		}
		if !slices.Contains(PlanEffects, effect) {
			return fail("shot %d: effect %q is not one of %s", n, s.Effect, strings.Join(PlanEffects, ", "))
		}
		if need := effectFeature[effect]; need != "" && !f.Has(need) {
			return fail("shot %d: the effect %s is not available yet, plan another or none", n, effect)
		}
		if s.AssetID != "" && !footage[s.AssetID] {
			return fail("shot %d: %s is not one of this project's clips; list_footage shows the clips you can use", n, s.AssetID)
		}
		length := int64(s.Beats) * p.BeatMs
		p.EstimatedLengthMs += length
		if s.Chapter < 0 || s.Chapter > MaxChapter {
			return fail("shot %d: chapter is %d; a chapter is 1 to %d, or leave it out", n, s.Chapter, MaxChapter)
		}
		p.Shots = append(p.Shots, PlanShot{Role: s.Role, What: what, AssetID: s.AssetID, Beats: s.Beats, Effect: effect, Chapter: s.Chapter, LengthMs: length})
	}
	return p, nil
}

// BeatMs is the length of one beat at bpm, in whole milliseconds (rounded half up, as the beat utilities round).
func BeatMs(bpm int) int64 { return (60000*2 + int64(bpm)) / (2 * int64(bpm)) }

// transitionEffects are the plan's effects that are transitions; the others (but none) move the camera or the speed.
var transitionEffects = map[string]bool{"whip": true, "zoom_through": true, "flash": true}

// planNotes are the things about a valid plan that the editor should hear now rather than at check_edit. set is the creator's
// effective settings (nil for the defaults).
func planNotes(p Plan, style *styleprofile.Profile, set *styleprofile.Resolved) []string {
	var notes []string
	target := int64(p.TargetLengthS) * 1000
	if p.EstimatedLengthMs < target*durationLoPct/100 || p.EstimatedLengthMs > target*durationHiPct/100 {
		notes = append(notes, fmt.Sprintf("The shot list adds up to %s but your target is %d s. Change one of them so they agree (a short video: fewer, shorter shots; too short: more shots).", secs(p.EstimatedLengthMs), p.TargetLengthS))
	}
	run, start := 1, 0
	for i := 1; i <= len(p.Shots); i++ {
		if i < len(p.Shots) && p.Shots[i].Beats == p.Shots[i-1].Beats {
			run++
			continue
		}
		if run > 2 {
			notes = append(notes, fmt.Sprintf("Shots %d to %d are all %d beats long. Never more than two shots of the same length in a row: vary them.", start+1, i, p.Shots[start].Beats))
		}
		run, start = 1, i
	}
	// What the creator's settings turn off (a measured "no effects" turns nothing off by itself).
	if lim := applyStyle(limits{}, style, set); lim.transitionsOff || lim.cameraOff {
		trans, moves := 0, 0
		for _, s := range p.Shots {
			switch {
			case s.Effect == "none":
			case transitionEffects[s.Effect]:
				trans++
			default:
				moves++
			}
		}
		if lim.transitionsOff && trans > 0 {
			notes = append(notes, fmt.Sprintf("%d shots of the plan have a transition. The creator's settings turn transitions off: make those cuts hard cuts.", trans))
		}
		if lim.cameraOff && moves > 0 {
			notes = append(notes, fmt.Sprintf("%d shots of the plan have a motion or a speed ramp. The creator's settings turn camera motion off: set those effects to none.", moves))
		}
	}
	energy := energetic(p.Vibe)
	// A measured "no effects" with nothing the creator turned off: no gimmicks at lively and hype, restraint at calm.
	if lim := applyStyle(limits{}, style, set); style != nil && style.Effects.Verdict == styleprofile.EffectsNone && !lim.transitionsOff && !lim.cameraOff {
		n := 0
		for _, s := range p.Shots {
			// At lively and hype the transitions, the punch-ins on beats and the living camera are the vibe's, not gimmicks: only speed
			// ramps and shakes go against a creator of no effects. The living camera is camera motion at every vibe.
			if s.Effect != "none" && s.Effect != "handheld" && (!energy || s.Effect == "speed_ramp" || s.Effect == "shake") {
				n++
			}
		}
		if n > 0 && energy {
			notes = append(notes, fmt.Sprintf("%d shots of the plan have a speed ramp or a shake. This creator uses no gimmicks: keep the transitions, punch-ins and the living camera of the %s vibe, drop those.", n, VibeOr(p.Vibe)))
		} else if n > 0 {
			notes = append(notes, fmt.Sprintf("%d shots of the plan have an effect. This creator's videos have none and the vibe is calm: keep at most one transition (on the drop) and the living camera (handheld), set the other effects to none.", n))
		}
	}
	if energy {
		transitions := 0
		for _, s := range p.Shots[1:] {
			switch s.Effect {
			case "whip", "zoom_through", "flash", "punch":
				transitions++
			}
		}
		if len(p.Shots) >= 3 && transitions == 0 {
			notes = append(notes, fmt.Sprintf("The vibe is %s and the plan has no transition: put whip, zoom_through, punch or flash on the cut into the drop and on the strong beats (phrase starts), 250 to 400 ms, never the same one three times in a row.", VibeOr(p.Vibe)))
		}
	}
	var chapters []int
	for _, s := range p.Shots {
		if s.Chapter > 0 && !slices.Contains(chapters, s.Chapter) {
			chapters = append(chapters, s.Chapter)
		}
	}
	if len(chapters) >= 2 && (set == nil || set.ChapterTitles != styleprofile.LevelOff) {
		notes = append(notes, fmt.Sprintf("The plan has %d chapters: give the first shot of each a numbered chapter title (\"1. <EXERCISE>\" in the creator's title style, in the top band above the head) that enters with the shot and leaves before the next chapter; inside a chapter switch the angle every 2 to 5 s at a rep boundary instead of one long take.", len(chapters)))
	}
	if hookMax := applyStyle(limits{}, style, set).hookMaxMs; p.Shots[0].LengthMs > hookMax {
		if style != nil {
			notes = append(notes, fmt.Sprintf("The hook shot is %s long. The creator's first cut comes by %s.", secs(p.Shots[0].LengthMs), secs(hookMax)))
		} else {
			notes = append(notes, fmt.Sprintf("The hook shot is %s long. The first shot should be 1 to 2 seconds.", secs(p.Shots[0].LengthMs)))
		}
	}
	return notes
}

func (p Plan) describe() string {
	var b strings.Builder
	if p.TempoBPM > 0 {
		fmt.Fprintf(&b, "Tempo: %d BPM, one beat is %d ms (2 beats %s, 4 beats %s, 8 beats %s). Write the grid as {\"bpm\":%d,\"first_beat_ms\":0} in snap_cuts_to_beats with write_markers true.\n",
			p.TempoBPM, p.BeatMs, secs2(2*p.BeatMs), secs2(4*p.BeatMs), secs2(8*p.BeatMs), p.TempoBPM)
	} else {
		fmt.Fprintf(&b, "No beat grid: shots are counted in half seconds (%d ms a beat). Do not write markers or call snap_cuts_to_beats.\n", p.BeatMs)
	}
	fmt.Fprintf(&b, "Shot list, %d shots, %s in all (target %d s):\n", len(p.Shots), secs(p.EstimatedLengthMs), p.TargetLengthS)
	for i, s := range p.Shots {
		line := fmt.Sprintf("%d. %s: %d beats = %s", i+1, s.Role, s.Beats, secs2(s.LengthMs))
		if s.Chapter > 0 {
			line += fmt.Sprintf(", chapter %d", s.Chapter)
		}
		if s.Effect != "none" {
			line += ", " + s.Effect
		}
		fmt.Fprintln(&b, line)
	}
	return b.String()
}

// planEdit is the plan_edit tool.
func (d Deps) planEdit(ctx context.Context, rc runtime.RunContext, in planArgs) (runtime.Result, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return runtime.Result{}, err
	}
	videos, err := d.projectVideos(ctx, ws, pid)
	if err != nil {
		return runtime.Result{}, err
	}
	footage := make(map[string]bool, len(videos))
	for _, v := range videos {
		footage[v.AssetID.String()] = true
	}
	feat := d.features()
	st := d.styleFor(ctx, ws, rc.Input)
	style := profileOf(st)
	plan, err := BuildPlan(in, footage, feat, style)
	if err != nil {
		return runtime.Result{}, err
	}
	if st != nil {
		plan.Vibe = st.Resolved.Vibe // this run's, else the creator's style's (P6-05b)
	} else if ri, ierr := parseInput(rc.Input); ierr == nil {
		plan.Vibe = VibeOr(ri.Vibe)
	} else {
		plan.Vibe = DefaultVibe
	}
	book, err := Playbook(plan.Playbook, feat)
	if err != nil {
		return runtime.Result{}, err
	}
	if err := rc.Artifact(ctx, PlanArtifact, plan); err != nil {
		return runtime.Result{}, fmt.Errorf("record the plan: %w", err)
	}
	d.previews.setPlan(rc.RunID, plan)

	var b strings.Builder
	fmt.Fprintf(&b, "Plan saved (it is shown to the creator). Content type %s, playbook %s.\n", plan.ContentType, plan.PlaybookVersion)
	b.WriteString(plan.describe())
	for _, n := range planNotes(plan, firmStyle(style), settingsOf(st)) {
		b.WriteString("Note: " + n + "\n")
	}
	b.WriteString("\nThe playbook for this kind of video. Follow it; it is the craft of the genre, and check_edit will measure your edit against it.\n\n")
	// The profile block itself is in the run context already; the note carries the points that decide the plan.
	return runtime.Result{Content: []llm.Block{llm.TextBlock(b.String() + book + styleNote(st))}}, nil
}

// features is what the build offers (the tests of the gate replace it).
func (d Deps) features() Features {
	if d.Features != nil {
		return *d.Features
	}
	return AvailableFeatures()
}

// ---- finding the run's plan ------------------------------------------------------------------------------------------

// planOf returns the run's latest plan: from memory, or from the artifact events of the run when this worker has no record (a
// resumed run). ok is false when the run has made none.
func (d Deps) planOf(ctx context.Context, rc runtime.RunContext) (Plan, bool) {
	if p, ok := d.previews.plan(rc.RunID); ok {
		return p, true
	}
	p, ok := PlanFromEvents(ctx, d.Store, rc.WorkspaceID, rc.RunID)
	if ok {
		d.previews.setPlan(rc.RunID, p)
	}
	return p, ok
}

// EventSource reads a run's artifact events of one kind. *store.Store is the real one.
type EventSource interface {
	AgentArtifactEvents(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID, kind string) ([]store.AgentEvent, error)
}

// PlanFromEvents reads the last creative_plan artifact of a run. The payload is the artifact's data, so a plan that cannot be read
// (an older shape, a payload from elsewhere) is none.
func PlanFromEvents(ctx context.Context, st EventSource, ws ids.WorkspaceID, run uuid.UUID) (Plan, bool) {
	var plan Plan
	var found bool
	for _, art := range runArtifacts(ctx, st, ws, run, PlanArtifact) {
		var got Plan
		if json.Unmarshal(art.Data, &got) == nil && got.Version == PlanVersion {
			plan, found = got, true
		}
	}
	return plan, found
}
