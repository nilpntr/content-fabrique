package editor

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// check_edit: the objective checks of an edit (docs/design/edit-style-v1.md, step 5). Everything here is computed in Go from the
// stored timeline version and the stored clip analysis: no model, no picture. The editor runs it after render_preview and revises
// once when a check fails; `cfctl eval editor` runs the same function over any stored version, so two runs can be compared with
// the same numbers.
//
// A check passes, fails (with a one-line reason that says what to change) or is skipped (it does not apply, or the data it needs is
// not there). The thresholds are constants and a per-genre table: they are a bar, not a taste.

// CheckStatus is the outcome of one check.
type CheckStatus string

// The outcomes.
const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

// The check ids, in the order a report lists them.
const (
	CheckHookLength    = "hook_length"
	CheckHookTitle     = "hook_title"
	CheckShotPacing    = "shot_pacing"
	CheckShotVariety   = "shot_variety"
	CheckPaceBuilds    = "pace_builds"
	CheckRepBoundaries = "rep_boundaries"
	CheckDeadTime      = "dead_time"
	CheckEffectDensity = "effect_density"
	CheckTextSafe      = "text_safe_zone"
	CheckDuration      = "duration"
	CheckBrightness    = "brightness_jump"
	CheckCrushed       = "crushed"
	CheckBeatSync      = "beat_sync"
	CheckStyleMatch    = "style_match"
	// CheckTextOverSubject is the check for a title that covers the head of the person in the footage (the quick look's subject boxes).
	CheckTextOverSubject = "text_over_subject"
	// CheckTitleCopied fails a text that is, word for word, the title of one of the creator's reference videos.
	CheckTitleCopied = "title_copied"
	// CheckLongShot and CheckHeldEnding hold the shots to the pace targets (pace.go): no shot but the hook and the ending over the longest,
	// and the ending held no longer than the creator holds theirs.
	CheckLongShot   = "long_shot"
	CheckHeldEnding = "held_ending"
)

// The thresholds that do not depend on the kind of video.
const (
	hookMaxMs     = 2000
	hookTitleByMs = 500
	// repToleranceMs is how far a shot edge may be from a rep's start, end or turning point. It is the measurement, not a taste: half a
	// sample of the analysis (50 ms at 10 Hz), the largest move of snap_cuts_to_beats (80 ms) and half a frame (17 ms at 30 fps) add up to
	// 147 ms. The snap and the frame are output time, so at a speed over 1 they are longer in the source: 50 + 100 x speed (repSnapMs).
	// A slow rep is still at its turn for longer: within 5 % of its length of a turning point the movement has covered under 5 % of its
	// range (1 - cos(2 pi x 0.05) = 0.049), so a rep of 4 s accepts 200 ms (repTurnShare, at most repTurnMaxMs).
	repToleranceMs    = 150
	repAnalysisHalfMs = 50
	repSnapMs         = 100
	repTurnShare      = 0.05
	repTurnMaxMs      = 250
	repEdgesShare     = 0.85
	deadShareMax      = 0.05
	effectsMax        = 0.5
	rampsMax          = 4
	brightnessJumpMax = 0.25
	// brightnessRatioMin and brightnessRatioDelta: in low-key footage the p90 luma of every shot is small, so an absolute jump of 0.25
	// never happens; a cut is also a jump when the lit part of one shot is 1.8 times (0.85 stops) the other's and they differ by at
	// least 0.10 (the posing clip of the founder's footage is 1.9 times the gym clips).
	brightnessRatioMax   = 1.8
	brightnessRatioDelta = 0.10
	// crushedRatio is how much of its source's brightness a rendered shot must keep (the p90 or the mean of the luma): the look that
	// crushed the founder's footage left 0.24 to 0.40 of the p90 and none of the mean, ungraded renders keep all of it. crushedBlack is
	// a rendered p90 under which a shot is near black whatever its source. minSourceP90 and minSourceMean keep the ratios to shots
	// whose source is bright enough for a ratio to mean something.
	crushedRatio        = 0.7
	crushedBlack        = 0.04
	minSourceP90        = 0.06
	minSourceMean       = 0.03
	beatToleranceFrames = 2
	beatShareMin        = 0.7
	// durationLoPct and durationHiPct are the share of the plan's target length that a video may be: the same tolerance as plan_edit's
	// note about a shot list that does not match its target.
	durationLoPct        = 75
	durationHiPct        = 125
	defaultMinDurationMs = 15_000
	defaultMaxDurationMs = 45_000
	paceBuildsMinShots   = 6
	paceBuildsSlack      = 1.15
	varietyMinCV         = 0.12
)

// Check is one result.
type Check struct {
	ID     string      `json:"id"`
	Status CheckStatus `json:"status"`
	// Detail is one line: the numbers, and what to change when the check fails.
	Detail string `json:"detail"`
}

// Span is a stretch of a clip, in the clip's own milliseconds. TurnMs is, for a rep, its measured turning point (the opposite end of the
// movement between its start and its end), 0 when the analysis has none: then the midpoint stands in for it.
type Span struct{ StartMs, EndMs, TurnMs int64 }

// AssetAnalysis is what check_edit knows about one clip from its stored analysis.
type AssetAnalysis struct {
	// Analysed says the clip has an analysis at all (its moments are there, even when there are none of a kind).
	Analysed bool
	// Reps, Holds and Dead are the moments of those kinds.
	Reps, Holds, Dead []Span
	// Series holds the luma and 90th percentile luma of the clip, when the stored analysis has them.
	Series    analysis.Series
	HasSeries bool
	// Camera is static or handheld when the analysis measured it (P6-06): footage that moves by itself is not a static shot.
	Camera string
}

// CheckInput is what Check reads.
type CheckInput struct {
	Version int
	Spec    *timeline.Spec
	// Plan is the run's creative plan: it names the content type (which sets the thresholds) and the target length. Nil is a general
	// video with the default length range.
	Plan *Plan
	// Assets holds the analysis of the clips, by asset id. A clip that is not in it, or not analysed, is skipped by the checks that
	// need the analysis.
	Assets map[string]AssetAnalysis
	// Issues are the validation issues of the version (the safe zone of the texts is the validator's).
	Issues []timeline.Issue
	// Subjects is where the person is in the clips (the quick look's subject boxes), by asset id; nil or without an asset's entry, the
	// subject check does not apply to the clip.
	Subjects subject.Index
	// Style is the creator's style profile (P6-05), nil for a workspace that has none: the thresholds then are the defaults of the
	// content type, and style_match does not apply.
	Style *styleprofile.Profile
	// Settings are the creator's effective settings for this edit (P6-05b: their own, the AI's suggestion, the vibe), nil for the
	// defaults (a lively vibe). They decide what may move (transitions, camera motion, text animation), the pace and the music; a
	// measured "no effects" in Style decides none of that.
	Settings *styleprofile.Resolved
	// Rendered is the luma of the version's preview render (a mean in Luma and a 90th percentile in High per sample, as the analysis
	// series), nil when there is no preview of the version yet: the checks that look at the rendered picture do not apply then.
	Rendered *analysis.Series
	// Audio is the ears' report of the version's preview (P3-12); nil when it could not be measured, and then AudioSkip says why (the
	// audio checks do not apply).
	Audio     *AudioResult
	AudioSkip string
	// Music is the edit's song on the timeline (its beats, hits and drop), nil when the edit has none or it was not analysed: the music
	// checks do not apply then (P6-06).
	Music *TimelineMusic
	// Vibe is the energy of the edit (calm, lively or hype; P6-06); empty takes the plan's, else the default (lively).
	Vibe string
	// Overlay is the luma of the preview behind each motion-graphics overlay, by item id (D-35), nil when it was not measured; then
	// OverlaySkip says why and overlay_legibility does not apply.
	Overlay     map[string]RegionLuma
	OverlaySkip string
	// PaceSetting is the creator's own pace setting (PaceSettings: "slower", "like_mine", "faster"; "" takes the Settings' pace), which
	// scales the pace targets after the vibe.
	PaceSetting string
}

// Metrics are the numbers behind the checks, for the run feed and for comparing runs.
type Metrics struct {
	DurationMs int64 `json:"duration_ms"`
	Shots      int   `json:"shots"`
	// ShotMs are the shortest, the median and the longest shot, in output milliseconds.
	ShotMinMs    int64 `json:"shot_min_ms"`
	ShotMedianMs int64 `json:"shot_median_ms"`
	ShotMaxMs    int64 `json:"shot_max_ms"`
	// LongestEqualRun is the most shots of (nearly) the same length in a row.
	LongestEqualRun int `json:"longest_equal_run"`
	// RepEdges is how many of the shot edges on clips with reps are on a rep boundary, out of how many there are.
	RepEdgesOn    int `json:"rep_edges_on"`
	RepEdgesTotal int `json:"rep_edges_total"`
	// DeadShare is the share of the screen time that is dead time (0 to 1), -1 when it could not be measured.
	DeadShare float64 `json:"dead_share"`
	// Effects counts the motion presets, the transitions that are not cuts and the speed ramps; Ramps the speed ramps alone.
	Effects         int     `json:"effects"`
	Ramps           int     `json:"ramps"`
	EffectsPerCut   float64 `json:"effects_per_cut"`
	CutsOnBeat      int     `json:"cuts_on_beat"`
	Cuts            int     `json:"cuts"`
	BrightnessJump  float64 `json:"brightness_max_jump"`
	BrightnessRange float64 `json:"brightness_range"`
	// RenderedKept is the lowest share of its source's p90 luma that a rendered shot keeps (1 is as bright as the source), 0 when the
	// preview was not measured.
	RenderedKept float64 `json:"rendered_kept"`
	// StyleScore is the share of the style dimensions that are in the creator's range (0 when there is no profile).
	StyleScore float64 `json:"style_score,omitempty"`
	// PaceMedianMs and PaceMaxShotMs are the pace targets the shots were held to (0 when none applies).
	PaceMedianMs  int64 `json:"pace_median_ms,omitempty"`
	PaceMaxShotMs int64 `json:"pace_max_shot_ms,omitempty"`
	// The ears (P3-12), when the preview was listened to: the profile ("programme" or "sound_ready"), the integrated loudness and the
	// bed level (LUFS; the bed only in the sound-ready profile), what was measured ("stem" or "preview") and how long listening took.
	AudioProfile string  `json:"audio_profile,omitempty"`
	AudioLUFS    float64 `json:"audio_lufs,omitempty"`
	AudioBedLUFS float64 `json:"audio_bed_lufs,omitempty"`
	AudioSource  string  `json:"audio_source,omitempty"`
	AudioMs      int64   `json:"audio_ms,omitempty"`
}

// Report is the result of Check.
type Report struct {
	Version     int     `json:"version"`
	ContentType string  `json:"content_type"`
	TargetS     int     `json:"target_length_s,omitempty"`
	Checks      []Check `json:"checks"`
	Metrics     Metrics `json:"metrics"`
	// Style is the distance of the edit from the creator's style profile, per dimension (nil without a profile).
	Style *styleprofile.Distance `json:"style,omitempty"`
}

// Counts returns how many checks passed, failed and were skipped.
func (r Report) Counts() (pass, fail, skip int) {
	for _, c := range r.Checks {
		switch c.Status {
		case CheckPass:
			pass++
		case CheckFail:
			fail++
		default:
			skip++
		}
	}
	return pass, fail, skip
}

// Failed lists the ids of the failing checks.
func (r Report) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			out = append(out, c.ID)
		}
	}
	return out
}

// limits are the thresholds that depend on the kind of video.
type limits struct {
	medianShotMs int64   // the median shot may not be longer
	minEffects   float64 // effects per cut, at least (0: no lower bound)
	beatGrid     bool    // the video follows a beat grid
	gridRequired bool    // a plan for this type may not have tempo 0
	paceBuilds   bool    // the cuts speed up toward the end
	// hookMaxMs is the longest first shot (the default is hookMaxMs), and the rest come from the creator's style profile and settings
	// (applyStyle): transitionsOff and cameraOff, no transition, or no motion preset or speed ramp, because the creator's settings
	// say so; effectsLots, the settings ask for lots of motion (a higher ceiling); noTitle, no title is expected; durLo and durHi, the
	// length range when the plan names no target; styled, a profile is in force.
	hookMaxMs                          int64
	transitionsOff, cameraOff, noTitle bool
	effectsLots                        bool
	durLo, durHi                       int64
	styled                             bool
	// looseBeat says the creator's cuts are known not to sit on a beat grid, so beat_sync does not apply.
	looseBeat bool
	// effectsMax is the most effects a cut (0.5, more at lively and hype).
	effectsMax float64
}

// applyStyle sets the thresholds from the creator's style profile and settings: a creator who lets takes run, uses no effects, cuts
// loosely near the music and has a first cut after two seconds is not held to the defaults of a fast edit. The profile loosens the
// checks that assume a montage (shot pacing, the pace building, the effect minimum, the beat grid, the hook length) and the style_match
// check measures the edit against the profile instead. What may move is the settings' (set, nil for the defaults): a measured "no
// effects" never forbids transitions or camera motion on its own (P6-05b).
func applyStyle(l limits, p *styleprofile.Profile, set *styleprofile.Resolved) limits {
	l.hookMaxMs = hookMaxMs
	r := styleprofile.Resolve(p, nil, styleprofile.Settings{}, "")
	if set != nil {
		r = *set
	}
	l.transitionsOff = r.Transitions == styleprofile.LevelOff
	l.cameraOff = r.CameraMotion == styleprofile.LevelOff
	l.effectsLots = r.Transitions == styleprofile.LevelLots || r.CameraMotion == styleprofile.LevelLots
	if r.Music == styleprofile.MusicNever {
		l.beatGrid, l.gridRequired = false, false // no music: the cuts follow the clips' own sound and action
	}
	if !p.Firm() {
		return l // a profile of one or two videos is a hint for the prompt, not a rule for the checks
	}
	l.styled = true
	pc, th := p.Pacing, p.Thresholds()
	// The median shot: what the creator's videos have, with room, from quartiles and capped (thresholds.go), never under the default. A
	// creator who asks for a faster pace than their videos keeps the default of a fast edit; a slower one gets half as much room again.
	switch r.Pace {
	case styleprofile.PaceFaster:
	case styleprofile.PaceSlower:
		l.medianShotMs = max(l.medianShotMs, int64(th.MedianShotMaxS*1500))
	default:
		l.medianShotMs = max(l.medianShotMs, int64(th.MedianShotMaxS*1000))
	}
	// The checks that assume a fast edit are loosened for a creator of long takes or of few effects; a montage or busy-effects profile
	// keeps the minimum effect rate and the building pace.
	if pc.Style == styleprofile.PaceLongTake || p.Effects.Verdict == styleprofile.EffectsNone || p.Effects.Verdict == styleprofile.EffectsLight {
		l.minEffects = 0
		l.paceBuilds = false
	}
	if p.CutsOffBeat() {
		l.beatGrid, l.gridRequired, l.looseBeat = false, false, true
	}
	l.hookMaxMs = max(hookMaxMs, int64(th.HookMaxS*1000))
	if p.Text != nil && p.Text.TitleShare == 0 {
		l.noTitle = true
	}
	if th.DurHiS > 0 {
		l.durLo, l.durHi = int64(th.DurLoS*1000), int64(th.DurHiS*1000)
	}
	return l
}

var typeLimits = map[string]limits{
	TypeWorkout:  {medianShotMs: 3000, minEffects: 0.15, beatGrid: true, gridRequired: true, paceBuilds: true},
	TypeUnboxing: {medianShotMs: 3500, minEffects: 0.15, beatGrid: true, paceBuilds: true},
	TypeVlog:     {medianShotMs: 4000, minEffects: 0.15, beatGrid: true, paceBuilds: true},
	TypeGeneral:  {medianShotMs: 3500, minEffects: 0.15, beatGrid: true, paceBuilds: true},
	TypeTutorial: {medianShotMs: 5000},
	TypeTalking:  {medianShotMs: 6000},
}

// RunChecks runs the checks.
func RunChecks(in CheckInput) Report {
	typ := TypeGeneral
	targetS := 0
	if in.Plan != nil && ValidContentType(in.Plan.ContentType) {
		typ, targetS = in.Plan.ContentType, in.Plan.TargetLengthS
	}
	r := Report{Version: in.Version, ContentType: typ, TargetS: targetS}
	main, _, ok := timeline.MainTrack(in.Spec)
	if !ok || len(main.Items) == 0 {
		r.Checks = []Check{{ID: CheckDuration, Status: CheckFail, Detail: "the timeline has no clips"}}
		return r
	}
	items := slices.Clone(main.Items)
	slices.SortStableFunc(items, func(a, b timeline.VideoItem) int { return cmp.Compare(a.AtMs, b.AtMs) })
	// The vibe (P6-06): this edit's, as the creator's settings resolve it (P6-05b), else the plan's.
	if in.PaceSetting == "" {
		in.PaceSetting = paceSetting(in.Settings)
	}
	if in.Vibe == "" && in.Settings != nil {
		in.Vibe = in.Settings.Vibe
	}
	if in.Vibe == "" && in.Plan != nil {
		in.Vibe = in.Plan.Vibe
	}
	// The plan's format decides the text pattern the text checks follow, over a measured or suggested one (never over the creator's own).
	in.Settings = formatSettings(in.Plan, in.Settings)
	lim := applyStyle(typeLimits[typ], in.Style, in.Settings)
	// A busier edit is allowed more effects a cut at lively and hype, and when the creator's settings ask for lots.
	vibe := VibeOr(in.Vibe)
	lim.effectsMax = effectsMax
	if energetic(vibe) {
		lim.effectsMax = map[string]float64{VibeLively: 0.6, VibeHype: 1.0}[vibe]
	}
	if lim.effectsLots {
		lim.effectsMax = max(lim.effectsMax, effectsMaxLots)
	}
	// Lines that keep off the face only (P6-05d) are the style's whatever the strength of the profile: the creator set them, or the lines
	// of their videos lie over the body.
	faceOnly := in.Settings != nil && (&styleprofile.Style{Profile: in.Style, Resolved: *in.Settings}).FaceOnly()
	weakStyle := ""
	if in.Style != nil && !in.Style.Firm() {
		weakStyle = fmt.Sprintf("the creator style profile rests on %d reference videos; it sets thresholds from %d", in.Style.References, styleprofile.MinReferences)
		in.Style = nil
	}
	if in.Plan != nil && in.Plan.TempoBPM == 0 {
		lim.beatGrid = false // the plan has no beat grid (a vlog or a general video that follows speech, say): the cuts are not judged against one
	}
	c := &checker{in: in, items: items, lim: lim, typ: typ, m: &r.Metrics, dist: &r.Style, weakStyle: weakStyle, faceOnly: faceOnly}
	c.pace = Pace(in.Style, typ, vibe, in.PaceSetting)
	if c.pace.Applies {
		r.Metrics.PaceMedianMs, r.Metrics.PaceMaxShotMs = c.pace.MedianMs, c.pace.MaxShotMs
	}
	c.total = timeline.TotalDuration(in.Spec)
	// A shot's length is what it is on screen: from where it starts to where the next one starts, so the overlap of a transition
	// into the next shot is not counted (a 1.95 s hook with a 250 ms whip out of it is still a 1.95 s hook). The last shot runs to
	// the end of the video.
	c.durs = make([]int64, len(items))
	for i, it := range items {
		if i+1 < len(items) {
			c.durs[i] = items[i+1].AtMs - it.AtMs
		} else {
			c.durs[i] = c.total - it.AtMs
		}
		c.durs[i] = max(c.durs[i], 0)
	}
	r.Metrics.DurationMs, r.Metrics.Shots = c.total, len(items)
	r.Checks = []Check{
		c.hookLength(), c.hookTitle(), c.shotPacing(), c.longShot(), c.heldEnding(), c.shotVariety(), c.paceBuilds(), c.repBoundaries(), c.deadTime(),
		c.effectDensity(), c.textSafe(), c.textOverSubject(), c.duration(targetS), c.brightness(), c.crushed(), c.beatSync(), c.styleMatch(), c.titleCopied(),
		c.offBeatCut(), c.hookMissesDrop(), c.staticShot(), c.transitionsMatch(), c.transitionVariety(), c.textWithoutEntrance(), c.chapterTitles(),
		c.formatFollowed(),
	}
	if c.linesOn() { // the text-pattern checks (checktext.go), only for a style told in lines
		r.Checks = append(r.Checks, c.storyCaptions(), c.captionReadable(), c.textOverFace())
	}
	if len(graphicsOf(in.Spec)) > 0 { // the motion-graphics checks (check_graphics.go), only for an edit that has some
		r.Checks = append(r.Checks, c.overlayReadable(), c.overlayOverSubject(), c.overlayLegibility(), c.graphicsOverlap())
	}
	if a := in.Audio; a != nil {
		r.Checks = append(r.Checks, audioChecks(*a)...)
		r.Metrics.AudioProfile, r.Metrics.AudioLUFS, r.Metrics.AudioSource, r.Metrics.AudioMs = a.Report.Profile, a.Report.Loudness.IntegratedLUFS, a.Source, a.Elapsed.Milliseconds()
		if a.Report.Bed != nil {
			r.Metrics.AudioBedLUFS = a.Report.Bed.LevelLUFS
		}
	} else {
		r.Checks = append(r.Checks, audioSkipped(cmp.Or(in.AudioSkip, "there is no preview render of this version to listen to"))...)
	}
	return r
}

type checker struct {
	in    CheckInput
	items []timeline.VideoItem
	durs  []int64
	total int64
	lim   limits
	typ   string
	m     *Metrics
	dist  **styleprofile.Distance
	// weakStyle is why a profile that is there is not used (too few references), for the style_match line.
	weakStyle string
	// pace are the shot-length targets (pace.go).
	pace PaceTargets
	// faceOnly says the style's lines may stand on the body and keep off the face only (styleprofile.Style.FaceOnly).
	faceOnly bool
	// laidOut is the graphics as the renderer lays them out (check_graphics.go), computed once.
	laidOut []motion.Laid
}

func (c *checker) pass(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckPass, Detail: fmt.Sprintf(format, a...)}
}
func (c *checker) fail(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckFail, Detail: fmt.Sprintf(format, a...)}
}
func (c *checker) skip(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckSkip, Detail: fmt.Sprintf(format, a...)}
}

func (c *checker) hookLength() Check {
	d := c.durs[0]
	limit := c.lim.hookMaxMs
	// With a song, the hook runs until its drop (P6-06: the references drop 2 to 4.5 s in): up to the drop, a transition into it included.
	if m := c.in.Music; m != nil && m.HasDrop && m.DropMs+maxDropHookSlackMs > limit {
		limit = min(m.DropMs+maxDropHookSlackMs, maxDropHookMs)
	}
	if d > limit {
		if c.lim.styled {
			return c.fail(CheckHookLength, "the first shot (%s) is %s long; the creator's first cut comes by %s", c.items[0].ID, secs2(d), secs2(limit))
		}
		return c.fail(CheckHookLength, "the first shot (%s) is %s long; the hook should be at most %s. Trim it to its strongest 1 to 2 seconds, or speed it up", c.items[0].ID, secs2(d), secs2(limit))
	}
	return c.pass(CheckHookLength, "the first shot is %s long (at most %s)", secs2(d), secs2(limit))
}

func (c *checker) hookTitle() Check {
	if c.lim.noTitle {
		return c.skip(CheckHookTitle, "the creator's videos have no title")
	}
	var first int64 = -1
	for _, t := range c.in.Spec.Tracks {
		switch t := t.(type) {
		case *timeline.CaptionsTrack:
			if t.Enabled {
				return c.pass(CheckHookTitle, "captions are on, so there is text from the first word")
			}
		case *timeline.TextTrack:
			for _, it := range t.Items {
				if first < 0 || it.AtMs < first {
					first = it.AtMs
				}
			}
		case *timeline.GraphicsTrack: // a motion-graphics title (D-35) is a title
			for _, it := range t.Items {
				if first < 0 || it.AtMs < first {
					first = it.AtMs
				}
			}
		}
	}
	switch {
	case first < 0:
		return c.fail(CheckHookTitle, "there is no title; add a few bold words that start by %s", secs2(hookTitleByMs))
	case first > hookTitleByMs:
		return c.fail(CheckHookTitle, "the first text starts at %s; the title should be on screen by %s", secs2(first), secs2(hookTitleByMs))
	}
	return c.pass(CheckHookTitle, "the title starts at %s (by %s)", secs2(first), secs2(hookTitleByMs))
}

func (c *checker) shotStats() (minMs, medMs, maxMs int64) {
	s := slices.Clone(c.durs)
	slices.Sort(s)
	return s[0], s[len(s)/2], s[len(s)-1]
}

func (c *checker) shotPacing() Check {
	lo, med, hi := c.shotStats()
	c.m.ShotMinMs, c.m.ShotMedianMs, c.m.ShotMaxMs = lo, med, hi
	// With the creator's measured shot lengths, the median aims at theirs (scaled by the vibe and their pace setting), within ±40 %.
	if p := c.pace; p.Applies && p.FromProfile {
		switch {
		case med > p.MedianHiMs:
			return c.fail(CheckShotPacing, "the median shot is %s long; the target is about %s (%s to %s, from %s). Cut the long shots shorter: a slow rep at its turning point (half a rep on the beat), or switch to another angle of the same exercise at the turning point; or speed a shot up",
				secs2(med), secs2(p.MedianMs), secs2(p.MedianLoMs), secs2(p.MedianHiMs), p.source())
		case med < p.MedianLoMs:
			return c.fail(CheckShotPacing, "the median shot is %s long; the target is about %s (%s to %s, from %s). Let the shots run longer: whole reps, fewer cuts",
				secs2(med), secs2(p.MedianMs), secs2(p.MedianLoMs), secs2(p.MedianHiMs), p.source())
		}
		return c.pass(CheckShotPacing, "the median shot is %s long (the target is %s to %s, from %s)", secs2(med), secs2(p.MedianLoMs), secs2(p.MedianHiMs), p.source())
	}
	if med > c.lim.medianShotMs {
		return c.fail(CheckShotPacing, "the median shot is %s long (at most %s for a %s video; shortest %s, longest %s). Make most shots 1 to 4 beats: take the strongest half of a long shot, or speed it up",
			secs2(med), secs2(c.lim.medianShotMs), c.typ, secs2(lo), secs2(hi))
	}
	return c.pass(CheckShotPacing, "the median shot is %s long (at most %s for a %s video; shortest %s, longest %s)", secs2(med), secs2(c.lim.medianShotMs), c.typ, secs2(lo), secs2(hi))
}

// longShot fails a shot that runs longer than the pace allows (the creator's measured 90th percentile shot, scaled by the vibe and their
// setting, or the default). The hook (held to the drop by hook_length) and the ending (held_ending) are left out.
func (c *checker) longShot() Check {
	p := c.pace
	if !p.Applies {
		return c.skip(CheckLongShot, "no pace target applies (a %s video, or a creator of long takes at the calm vibe)", c.typ)
	}
	if len(c.items) < 3 {
		return c.skip(CheckLongShot, "only the hook and the ending")
	}
	var long []string
	for i := 1; i < len(c.items)-1; i++ {
		if c.durs[i] > p.MaxShotMs {
			long = append(long, fmt.Sprintf("%s (%s)", c.items[i].ID, secs2(c.durs[i])))
		}
	}
	if len(long) > 0 {
		return c.fail(CheckLongShot, "%s %s longer than %s (from %s). Cut a slow rep at its turning point (half a rep, on the beat), switch to another angle of the same exercise at the turning point, or speed the shot up",
			strings.Join(long, ", "), map[bool]string{true: "is", false: "are"}[len(long) == 1], secs2(p.MaxShotMs), p.source())
	}
	return c.pass(CheckLongShot, "no shot but the hook and the ending is over %s (from %s)", secs2(p.MaxShotMs), p.source())
}

// heldEnding fails an ending held longer than the creator holds theirs (scaled by the vibe), unless it moves: a push (the living camera,
// push_in or pull_back) with a punch-in on a beat may hold up to 1.6 times as long.
func (c *checker) heldEnding() Check {
	p := c.pace
	if !p.Applies {
		return c.skip(CheckHeldEnding, "no pace target applies (a %s video, or a creator of long takes at the calm vibe)", c.typ)
	}
	n := len(c.items)
	if n < 2 {
		return c.skip(CheckHeldEnding, "there is one shot")
	}
	last, d := c.items[n-1], c.durs[n-1]
	if d <= p.EndingMs {
		return c.pass(CheckHeldEnding, "the ending is held %s (at most %s, from %s)", secs2(d), secs2(p.EndingMs), p.source())
	}
	moving := c.endingMoves(last)
	if moving && d <= p.EndingMovingMs {
		return c.pass(CheckHeldEnding, "the ending is held %s with a push and a punch-in on a beat (at most %s moving, %s still)", secs2(d), secs2(p.EndingMovingMs), secs2(p.EndingMs))
	}
	if moving {
		return c.fail(CheckHeldEnding, "the ending (%s) is held %s, longer than %s even with its push and punch-in (from %s). Trim it to about %s: it loops back to the hook",
			last.ID, secs2(d), secs2(p.EndingMovingMs), p.source(), secs2(p.EndingMs))
	}
	return c.fail(CheckHeldEnding, "the ending (%s) is held %s and sits still; at most %s (from %s). Trim it to about %s, or give it set_motion handheld with at_ms on a beat (a push with a punch-in on the beat) and keep it under %s",
		last.ID, secs2(d), secs2(p.EndingMs), p.source(), secs2(p.EndingMs), secs2(p.EndingMovingMs))
}

// endingMoves says the shot has a push (handheld, push_in or pull_back) and a punch-in that lands on a beat or a hit of the song (a
// handheld or punch_in with at_ms; without a grid any at_ms counts).
func (c *checker) endingMoves(it timeline.VideoItem) bool {
	m := it.Motion
	if !m.Active() {
		return false
	}
	switch m.Preset {
	case timeline.MotionHandheld, timeline.MotionPushIn, timeline.MotionPullBack:
	default:
		return false
	}
	if m.Preset != timeline.MotionHandheld || m.AtMs == nil {
		return false // a push_in or pull_back has no punch; a handheld without at_ms has none either
	}
	at := it.AtMs + *m.AtMs
	var beats []int64
	if c.in.Music != nil {
		beats = c.in.Music.hits()
	} else {
		for _, mk := range c.in.Spec.Markers {
			if mk.Kind == timeline.MarkerBeat || mk.Kind == timeline.MarkerDownbeat {
				beats = append(beats, mk.AtMs)
			}
		}
		slices.Sort(beats)
	}
	if len(beats) == 0 {
		return true
	}
	_, d, _ := music.Nearest(beats, at)
	return d <= int64(beatToleranceFrames)*1000/int64(max(c.in.Spec.Output.FPS, 1))
}

// nearlyEqual says two shot lengths are the same length: within 8 % or 100 ms.
func nearlyEqual(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= max(100, max(a, b)*8/100)
}

func (c *checker) shotVariety() Check {
	run, best, bestAt := 1, 1, 0
	for i := 1; i < len(c.durs); i++ {
		if nearlyEqual(c.durs[i], c.durs[i-1]) {
			run++
			if run > best {
				best, bestAt = run, i-run+1
			}
		} else {
			run = 1
		}
	}
	c.m.LongestEqualRun = best
	if best > 2 {
		return c.fail(CheckShotVariety, "%d shots in a row are about %s long (%s to %s); never more than two of the same length. Change the length of one of them", best, secs2(c.durs[bestAt]), c.items[bestAt].ID, c.items[bestAt+best-1].ID)
	}
	if len(c.durs) >= 5 {
		var sum, sq float64
		for _, d := range c.durs {
			sum += float64(d)
		}
		mean := sum / float64(len(c.durs))
		for _, d := range c.durs {
			sq += (float64(d) - mean) * (float64(d) - mean)
		}
		if cv := math.Sqrt(sq/float64(len(c.durs))) / mean; cv < varietyMinCV {
			return c.fail(CheckShotVariety, "the shots are all close to the same length (spread %.0f %%); mix 1 to 2 beat hype shots with a few longer ones", cv*100)
		}
	}
	return c.pass(CheckShotVariety, "the longest run of same-length shots is %d (at most 2)", best)
}

func medianOf(x []int64) int64 {
	s := slices.Clone(x)
	slices.Sort(s)
	return s[len(s)/2]
}

func (c *checker) paceBuilds() Check {
	n := len(c.durs)
	if !c.lim.paceBuilds {
		return c.skip(CheckPaceBuilds, "does not apply to a %s video", c.typ)
	}
	if n < paceBuildsMinShots {
		return c.skip(CheckPaceBuilds, "only %d shots", n)
	}
	first, second := medianOf(c.durs[:n/2]), medianOf(c.durs[n/2:n-1]) // the last shot is held longer on purpose
	if float64(second) > float64(first)*paceBuildsSlack {
		return c.fail(CheckPaceBuilds, "the cuts slow down: the median shot is %s in the first half and %s in the second. The cuts should speed up toward the end", secs2(first), secs2(second))
	}
	return c.pass(CheckPaceBuilds, "the median shot is %s in the first half and %s in the second", secs2(first), secs2(second))
}

func (c *checker) asset(it timeline.VideoItem) (AssetAnalysis, bool) {
	a, ok := c.in.Assets[it.AssetID]
	return a, ok && a.Analysed
}

func (c *checker) repBoundaries() Check {
	if s := c.in.Settings; s != nil && s.CutTiming == styleprofile.CutsMidRep {
		return c.skip(CheckRepBoundaries, "the creator's cuts fall in the middle of a rep (their style): the cuts follow the beat, not the end of a rep")
	}
	type miss struct {
		text string
		off  int64
	}
	var misses []miss
	on, total := 0, 0
	for _, it := range c.items {
		a, ok := c.asset(it)
		if !ok || len(a.Reps) == 0 {
			continue
		}
		for _, e := range []struct {
			name string
			at   int64
		}{{"starts", it.Source.InMs}, {"ends", it.Source.OutMs}} {
			total++
			near, d, repLen := nearestRepBoundary(a, e.at)
			if tol := repTolerance(it.SpeedOrDefault(), repLen); d <= tol || insideHold(a, e.at) {
				on++
			} else {
				misses = append(misses, miss{fmt.Sprintf("%s %s at %d ms (nearest rep boundary %d ms, %d ms off; within %d ms counts)", it.ID, e.name, e.at, near, d, tol), d})
			}
		}
	}
	c.m.RepEdgesOn, c.m.RepEdgesTotal = on, total
	if total == 0 {
		return c.skip(CheckRepBoundaries, "none of the clips used has reps")
	}
	share := float64(on) / float64(total)
	if share < repEdgesShare {
		slices.SortStableFunc(misses, func(a, b miss) int { return cmp.Compare(b.off, a.off) })
		var parts []string
		for _, m := range misses[:min(3, len(misses))] {
			parts = append(parts, m.text)
		}
		return c.fail(CheckRepBoundaries, "%d of %d shot edges on reps are within %d ms of a rep start, end or turning point; off: %s. Move those edges to a boundary from find_moments", on, total, repToleranceMs, strings.Join(parts, "; "))
	}
	return c.pass(CheckRepBoundaries, "%d of %d shot edges on reps are within %d ms of a rep start, end or turning point", on, total, repToleranceMs)
}

// nearestRepBoundary is the rep boundary (a rep's start, its end or its turning point: the measured one, else the midpoint) nearest to t,
// how far it is, and the length of the rep it belongs to.
func nearestRepBoundary(a AssetAnalysis, t int64) (at, dist, repLen int64) {
	dist = math.MaxInt64
	for _, r := range a.Reps {
		turn := (r.StartMs + r.EndMs) / 2
		if r.TurnMs > r.StartMs && r.TurnMs < r.EndMs {
			turn = r.TurnMs
		}
		for _, b := range []int64{r.StartMs, r.EndMs, turn} {
			if d := absI(b - t); d < dist {
				at, dist, repLen = b, d, r.EndMs-r.StartMs
			}
		}
	}
	return at, dist, repLen
}

// repTolerance is how far from a rep boundary an edge of a shot played at speed may be, for a rep repLen long (see repToleranceMs).
func repTolerance(speed float64, repLen int64) int64 {
	tol := float64(repToleranceMs)
	if speed > 1 {
		tol = repAnalysisHalfMs + repSnapMs*speed
	}
	turn := math.Min(repTurnShare*float64(repLen), repTurnMaxMs)
	return int64(math.Round(math.Max(tol, turn)))
}

// insideHold says t is within a hold (a steady pose): a cut there is not in the middle of a rep.
func insideHold(a AssetAnalysis, t int64) bool {
	for _, h := range a.Holds {
		if t >= h.StartMs-repToleranceMs && t <= h.EndMs+repToleranceMs {
			return true
		}
	}
	return false
}

func absI(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (c *checker) deadTime() Check {
	c.m.DeadShare = -1
	var deadOut, measured float64
	worst, worstMs := "", int64(0)
	for i, it := range c.items {
		a, ok := c.asset(it)
		if !ok {
			continue
		}
		span := it.Source.OutMs - it.Source.InMs
		if span <= 0 {
			continue
		}
		// The dead stretches are mapped through the clip's own time (its speed, or its speed ramp: a ramp that slows the peak and
		// rushes the rest plays a dead stretch at the rush for far less than its share of the source), and scaled to the part of the
		// clip that is on screen.
		var dead float64
		for _, d := range a.Dead {
			lo, hi := max(d.StartMs, it.Source.InMs), min(d.EndMs, it.Source.OutMs)
			if hi > lo {
				dead += it.OutOffsetMs(float64(hi-it.Source.InMs)) - it.OutOffsetMs(float64(lo-it.Source.InMs))
			}
		}
		full := float64(timeline.OutputDuration(it))
		if full <= 0 {
			continue
		}
		measured += float64(c.durs[i])
		out := dead * float64(c.durs[i]) / full
		deadOut += out
		if int64(out) > worstMs {
			worst, worstMs = it.ID, int64(out)
		}
	}
	if measured == 0 {
		return c.skip(CheckDeadTime, "none of the clips used has been analysed")
	}
	share := deadOut / measured
	c.m.DeadShare = math.Round(share*1000) / 1000
	if share > deadShareMax {
		return c.fail(CheckDeadTime, "%.0f %% of the screen time (%s) is dead time, the most in %s; cut it out (find_moments lists the dead time to skip)", share*100, secs2(int64(deadOut)), worst)
	}
	return c.pass(CheckDeadTime, "%.0f %% of the screen time is dead time (at most %.0f %%)", share*100, deadShareMax*100)
}

// effectKinds counts the effects of the main track by kind. The living camera (handheld) is camera motion and not an effect: it is
// counted apart.
type effectKinds struct{ transitions, motions, handheld, ramps int }

func countEffectKinds(items []timeline.VideoItem) effectKinds {
	var k effectKinds
	for i, it := range items {
		switch {
		case it.Motion.Active() && it.Motion.Preset == timeline.MotionHandheld:
			k.handheld++
		case it.Motion.Active():
			k.motions++
		}
		if i > 0 && it.TransitionIn != nil && it.TransitionIn.Type != timeline.TransitionCut && it.TransitionIn.Type != "" {
			k.transitions++
		}
		if it.HasRamp() {
			k.ramps++
		}
	}
	return k
}

// effectsMaxLots is the most effects per cut when the creator's settings ask for lots of transitions or camera motion.
const effectsMaxLots = 1.5

func (c *checker) effectDensity() Check {
	k := countEffectKinds(c.items)
	effects, ramps := k.transitions+k.motions+k.ramps, k.ramps
	cuts := len(c.items) - 1
	c.m.Effects, c.m.Ramps, c.m.Cuts = effects, ramps, cuts
	// The creator's settings first: what they turned off stays off, whatever the vibe.
	switch {
	case c.lim.transitionsOff && c.lim.cameraOff && effects > 0:
		return c.fail(CheckEffectDensity, "%d effects (transitions, motion presets, speed ramps); the creator's settings turn transitions and camera motion off: take them out", effects)
	case c.lim.transitionsOff && k.transitions > 0:
		return c.fail(CheckEffectDensity, "%d transitions; the style has transitions off: make every cut a plain cut on the beat", k.transitions)
	case c.lim.cameraOff && k.motions+k.handheld+k.ramps > 0:
		return c.fail(CheckEffectDensity, "%d motion presets and speed ramps; the creator's settings turn camera motion off: take them out", k.motions+k.handheld+k.ramps)
	case c.lim.transitionsOff && c.lim.cameraOff:
		return c.pass(CheckEffectDensity, "no effects, as the creator's settings ask")
	}
	if cuts < 1 {
		return c.skip(CheckEffectDensity, "there are no cuts")
	}
	per := float64(effects) / float64(cuts)
	c.m.EffectsPerCut = math.Round(per*100) / 100
	switch {
	case per > c.lim.effectsMax:
		return c.fail(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut) is too busy; use about one per 3 or 4 cuts and keep the ones on the best moments", effects, cuts, per)
	case ramps > rampsMax:
		return c.fail(CheckEffectDensity, "%d shots have a speed ramp; at most %d, on the shots that deserve it", ramps, rampsMax)
	case c.lim.minEffects > 0 && cuts >= 4 && per < c.lim.minEffects:
		if c.lim.transitionsOff {
			return c.fail(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut) is flat; add a punch_in or a push_in on a beat (about one per 3 or 4 cuts); the cuts stay plain", effects, cuts, per)
		}
		return c.fail(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut) is flat; add a punch_in on a beat, or a transition the style allows between scenes (about one per 3 or 4 cuts)", effects, cuts, per)
	}
	return c.pass(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut), %d speed ramps", effects, cuts, per, ramps)
}

func (c *checker) textSafe() Check {
	texts := len(screenTexts(c.in.Spec)) // a graphic's box is checked like a text's (timeline.checkGraphics)
	var bad []string
	for _, is := range c.in.Issues {
		if is.Code == timeline.CodeUnsafeText || is.Code == timeline.CodeTextOverlapsCaptions {
			bad = append(bad, fmt.Sprintf("%s at %s", is.Code, is.Path))
		}
	}
	switch {
	case len(bad) > 0:
		return c.fail(CheckTextSafe, "text outside the safe zone: %s", strings.Join(bad[:min(3, len(bad))], "; "))
	case texts == 0:
		return c.skip(CheckTextSafe, "there is no text")
	}
	return c.pass(CheckTextSafe, "%d texts, all inside the safe zones", texts)
}

// zoneTop is the top of the safe zone of the output, in pixels (0 for an unknown format).
func (c *checker) zoneTop() int {
	z, ok := c.in.Spec.Output.Zone()
	if !ok {
		return 0
	}
	return z.Inner.Top
}

func (c *checker) duration(targetS int) Check {
	lo, hi := int64(defaultMinDurationMs), int64(defaultMaxDurationMs)
	what := "the default range"
	if c.lim.durHi > 0 {
		lo, hi, what = c.lim.durLo, c.lim.durHi, "the creator's usual length"
	}
	if targetS > 0 {
		lo, hi = int64(targetS)*10*durationLoPct, int64(targetS)*10*durationHiPct
		what = fmt.Sprintf("the plan's %d s, give or take a quarter", targetS)
	}
	lo, hi = max(lo, timeline.MinTotalMs), min(hi, timeline.MaxTotalMs)
	switch {
	case c.total < lo:
		return c.fail(CheckDuration, "the video is %s long, under %s (%s). Add a shot or lengthen one", secs2(c.total), secs2(lo), what)
	case c.total > hi:
		return c.fail(CheckDuration, "the video is %s long, over %s (%s). Cut a shot or shorten some", secs2(c.total), secs2(hi), what)
	}
	return c.pass(CheckDuration, "the video is %s long (%s to %s: %s)", secs2(c.total), secs2(lo), secs2(hi), what)
}

// renderedShot is the exposure of the rendered picture of shot i, from the middle half of its time on screen (away from the
// transitions at its ends), at least one sample.
func (c *checker) renderedShot(i int) (analysis.Exposure, bool) {
	if c.in.Rendered == nil {
		return analysis.Exposure{}, false
	}
	start, d := c.items[i].AtMs, c.durs[i]
	lo, hi := start+d/4, start+d-d/4
	if hi-lo < int64(c.in.Rendered.StepMs) {
		mid := start + d/2
		lo, hi = mid-int64(c.in.Rendered.StepMs)/2, mid+int64(c.in.Rendered.StepMs)/2
	}
	return c.in.Rendered.ExposureBetween(int(lo), int(hi))
}

const noPreviewNote = "the preview of this version has not been rendered (or could not be read); call render_preview, then check_edit again"

// brightness judges the cuts by what the render shows (the source analysis says how bright the footage is, not the picture: a look
// changes it). A cut is a jump when the lit part of the two shots differs a lot in absolute terms, or by 1.8 times and at least 0.10:
// low-key footage has small numbers everywhere.
func (c *checker) brightness() Check {
	if c.in.Rendered == nil {
		return c.skip(CheckBrightness, "%s", noPreviewNote)
	}
	type lvl struct {
		e  analysis.Exposure
		ok bool
	}
	lv := make([]lvl, len(c.items))
	lo, hi := 2.0, -1.0
	for i := range c.items {
		lv[i].e, lv[i].ok = c.renderedShot(i)
		if lv[i].ok {
			lo, hi = math.Min(lo, lv[i].e.P90), math.Max(hi, lv[i].e.P90)
		}
	}
	worst, pairs := 0.0, 0
	jump := func(a, b analysis.Exposure) (float64, bool) {
		d := analysis.Jump(a, b)
		small, big := math.Min(a.P90, b.P90), math.Max(a.P90, b.P90)
		bad := d > brightnessJumpMax || (d >= brightnessRatioDelta && big >= brightnessRatioMax*math.Max(small, 0.01))
		return d, bad
	}
	badAt, worstBad := -1, 0.0
	for i := 1; i < len(lv); i++ {
		if !lv[i].ok || !lv[i-1].ok {
			continue
		}
		pairs++
		d, bad := jump(lv[i-1].e, lv[i].e)
		if bad && (badAt < 0 || d > worstBad) {
			badAt, worstBad = i, d
		}
		worst = math.Max(worst, d)
	}
	if pairs == 0 {
		return c.skip(CheckBrightness, "no shot of the preview could be measured")
	}
	c.m.BrightnessJump, c.m.BrightnessRange = math.Round(worst*100)/100, math.Round((hi-lo)*100)/100
	if badAt >= 0 {
		return c.fail(CheckBrightness, "the cut from %s to %s jumps %.2f in the rendered brightness of the lit part (p90 luma %.2f to %.2f; a jump is over %.2f, or %.1f times with at least %.2f). Call match_exposure to darken the brighter shot, or pick another moment for one of them",
			c.items[badAt-1].ID, c.items[badAt].ID, analysis.Jump(lv[badAt-1].e, lv[badAt].e), lv[badAt-1].e.P90, lv[badAt].e.P90,
			brightnessJumpMax, brightnessRatioMax, brightnessRatioDelta)
	}
	return c.pass(CheckBrightness, "the largest jump at a cut is %.2f in the rendered p90 luma (the shots range from %.2f to %.2f)", worst, lo, hi)
}

// expectedGain is the gain the clip's own exposure hint is meant to apply (look.match, or the auto preset, with a hint that is not
// too small to matter): the render is allowed to differ from the source by that much.
func expectedGain(it timeline.VideoItem) float64 {
	if it.ExposureEv == nil || it.Look == nil || !it.Look.AppliesExposure() {
		return 1
	}
	ev := *it.ExposureEv
	if math.Abs(ev) < 0.15 {
		return 1
	}
	return math.Exp2(-math.Max(-0.7, math.Min(0.7, ev)))
}

// crushed compares each shot's rendered brightness (the p90 and the mean of the luma) with its source's (the analysis of the part of
// the clip the shot plays), allowing for the exposure hint the clip carries. A look that darkens low-key footage, like the first
// gym_moody, fails it.
func (c *checker) crushed() Check {
	if c.in.Rendered == nil {
		return c.skip(CheckCrushed, "%s", noPreviewNote)
	}
	type bad struct {
		id   string
		text string
		kept float64
	}
	var misses []bad
	measured := 0
	kept := math.Inf(1)
	for i, it := range c.items {
		r, ok := c.renderedShot(i)
		if !ok {
			continue
		}
		measured++
		if r.P90 < crushedBlack {
			misses = append(misses, bad{it.ID, fmt.Sprintf("%s renders at p90 luma %.2f, near black", it.ID, r.P90), 0})
			kept = 0
			continue
		}
		a, has := c.asset(it)
		if !has || !a.HasSeries {
			continue
		}
		src, ok := a.Series.ExposureBetween(int(it.Source.InMs), int(it.Source.OutMs))
		if !ok {
			continue
		}
		g := expectedGain(it)
		if src.P90 >= minSourceP90 {
			ratio := r.P90 / (src.P90 * g)
			kept = math.Min(kept, ratio)
			if ratio < crushedRatio {
				misses = append(misses, bad{it.ID, fmt.Sprintf("%s keeps %.0f %% of its source's p90 luma (%.2f from %.2f)", it.ID, ratio*100, r.P90, src.P90*g), ratio})
				continue
			}
		}
		if src.Mean >= minSourceMean && r.Mean < crushedRatio*src.Mean*g {
			misses = append(misses, bad{it.ID, fmt.Sprintf("%s keeps %.0f %% of its source's mean luma (%.2f from %.2f)", it.ID, 100*r.Mean/(src.Mean*g), r.Mean, src.Mean*g), r.Mean / (src.Mean * g)})
		}
	}
	if measured == 0 {
		return c.skip(CheckCrushed, "no shot of the preview could be measured")
	}
	if !math.IsInf(kept, 1) {
		c.m.RenderedKept = math.Round(kept*100) / 100
	}
	if len(misses) > 0 {
		slices.SortStableFunc(misses, func(a, b bad) int { return cmp.Compare(a.kept, b.kept) })
		var parts []string
		for _, m := range misses[:min(3, len(misses))] {
			parts = append(parts, m.text)
		}
		return c.fail(CheckCrushed, "%d of %d shots render much darker than their footage: %s. The look is crushing the shadows; set_look none, or use auto (a lighter grade), and render_preview again", len(misses), measured, strings.Join(parts, "; "))
	}
	if math.IsInf(kept, 1) {
		return c.pass(CheckCrushed, "%d shots rendered, none near black (no source analysis to compare with)", measured)
	}
	return c.pass(CheckCrushed, "%d shots rendered; the darkest keeps %.0f %% of its source's p90 luma (at least %.0f %%)", measured, kept*100, crushedRatio*100)
}

// editStats measures the edit against the dimensions of the creator's profile.
func (c *checker) editStats() styleprofile.EditStats {
	e := styleprofile.EditStats{DurationS: float64(c.total) / 1000, FirstCutS: -1, Energetic: energetic(c.in.Vibe)}
	for _, d := range c.durs {
		e.ShotsS = append(e.ShotsS, float64(d)/1000)
	}
	if len(c.items) > 1 {
		e.FirstCutS = float64(c.items[1].AtMs) / 1000
	}
	k := countEffectKinds(c.items)
	e.Effects, e.Transitions, e.Motions, e.Ramps = k.transitions+k.motions+k.ramps, k.transitions, k.motions+k.handheld, k.ramps
	if energetic(c.in.Vibe) {
		e.Effects = countGimmicks(c.items) // at lively and hype the creator's "no effects" is held to no gimmicks
	}
	for _, it := range c.items {
		if it.Look != nil && it.Look.Preset != "" && it.Look.Preset != timeline.LookNone {
			e.Graded = true
		}
	}
	firstTitleAt := int64(math.MaxInt64)
	// The titles and labels: the text items and the motion graphics (a title_pop hook is the title), as the renderer draws them.
	for _, it := range screenTexts(c.in.Spec) {
		e.Texts++
		if it.Graphic != nil || it.AnimationOrDefault() != timeline.AnimationNone { // a graphic enters by its template
			e.Animated++
		}
		if it.IsChapter() { // a chapter card or "1. ..." text: the chapter-titles setting's, not the title's or a label
			e.Chapters++
			continue
		}
		if !it.IsTitle() {
			e.Labels++
			continue
		}
		e.Titles++
		if it.AtMs < firstTitleAt { // the case judged is the earliest title's, whatever track it is on
			firstTitleAt = it.AtMs
			e.TitleCase, e.TitleAtS, e.TitleText = styleprofile.CaseOf(it.Text), float64(it.AtMs)/1000, it.Text
			e.TitleFill, e.TitleLines, e.TitleHeightPct, e.TitleShadow = "", 0, 0, "" // not the look of a later title
			c.titleLook(&e, it.TextItem)
		}
	}
	for _, t := range c.in.Spec.Tracks {
		switch t := t.(type) {
		case *timeline.CaptionsTrack:
			e.Captions = e.Captions || t.Enabled
		case *timeline.AudioTrack:
			for _, a := range t.Items {
				if _, ok := a.(*timeline.SFX); ok {
					e.SFX++
				}
			}
		}
	}
	return e
}

// titleLook reads the look of a title as the renderer draws it (the style with the item's overrides): the fill, the lines it wraps to,
// the height of its capitals and its shadow.
func (c *checker) titleLook(e *styleprofile.EditStats, it timeline.TextItem) {
	out := c.in.Spec.Output
	pl, err := timeline.PlaceText(out, it)
	if err != nil {
		return
	}
	e.TitleFill, e.TitleLines = pl.Style.Fill.Hex(), len(pl.Layout.Lines)
	e.TitleChars = utf8.RuneCountInString(strings.Join(strings.Fields(it.Text), " "))
	e.TitleBreak = strings.Contains(it.Text, "\n")
	e.TitleKind, _ = styleprofile.TitleKind(it.Text)
	if e.TitleLines > 1 && !e.TitleBreak {
		// The largest scale (in steps of 0.05, down to the lowest allowed) at which the text fits one line.
		cur := 1.0
		if it.Scale != nil {
			cur = *it.Scale
		}
		for pct := int(math.Round(cur*100)) - 5; pct >= int(math.Round(style.MinScale*100)); pct -= 5 {
			try := it
			sc := float64(pct) / 100
			try.Scale = &sc
			if tp, err := timeline.PlaceText(out, try); err == nil && len(tp.Layout.Lines) == 1 {
				e.TitleFitScale = sc
				break
			}
		}
	}
	// The size is a share of a 9:16 frame's height (1920 px for a 1080 px short side), as the profile has it, whatever the format of the
	// output: text sizes scale with the short side, so the same scale is the same share on every format.
	e.TitleHeightPct = math.Round(pl.Style.CapHeightPct(out.ShortSide(), out.ShortSide()*1920/1080)*10) / 10
	e.TitleShadow = pl.Style.ShadowName()
	e.TitleScale = 1
	if it.Scale != nil {
		e.TitleScale = *it.Scale
	}
}

// styleMatch measures the edit against the creator's profile: one line per dimension, with the fix for the ones out of range.
func (c *checker) styleMatch() Check {
	if c.in.Style == nil {
		if c.weakStyle != "" {
			return c.skip(CheckStyleMatch, "%s", c.weakStyle)
		}
		return c.skip(CheckStyleMatch, "this workspace has no creator style profile")
	}
	d := c.in.Style.CompareWith(c.editStats(), c.in.Settings)
	c.m.StyleScore = d.Score
	*c.dist = &d
	soft := ""
	if b := d.Borderline(); len(b) > 0 {
		var ps []string
		for _, x := range b {
			ps = append(ps, fmt.Sprintf("%s: %s, expected %s (%s)", x.ID, x.Value, x.Expected, x.Fix))
		}
		soft = "; borderline, not a failure: " + strings.Join(ps, "; ")
	}
	if bad := d.Failed(); len(bad) > 0 {
		var parts []string
		for _, b := range bad[:min(4, len(bad))] {
			parts = append(parts, fmt.Sprintf("%s: %s, expected %s (%s)", b.ID, b.Value, b.Expected, b.Fix))
		}
		return c.fail(CheckStyleMatch, "%d of %d style dimensions are out of the creator's range: %s%s", len(bad), d.Hard(), strings.Join(parts, "; "), soft)
	}
	if soft != "" {
		return c.pass(CheckStyleMatch, "every style dimension that has to match is in the creator's range%s", soft)
	}
	return c.pass(CheckStyleMatch, "all %d style dimensions are in the creator's range", d.Hard())
}

// titleCopied fails a text that is the title of one of the creator's reference videos, compared without case or spaces: the style profile
// gives the form of their titles ("<TOPIC> 101"), and a title copied from another of their videos is about that video, not this one.
func (c *checker) titleCopied() Check {
	p := c.in.Style
	if p == nil {
		return c.skip(CheckTitleCopied, "there is no creator style profile to compare the titles with")
	}
	refs := p.ReferenceTitles()
	if len(refs) == 0 {
		return c.skip(CheckTitleCopied, "no title was read from the creator's reference videos")
	}
	var copied []string
	texts := 0
	for _, it := range screenTexts(c.in.Spec) { // a graphic's words too
		texts++
		if p.CopiesReferenceTitle(it.Text) {
			copied = append(copied, it.ID)
		}
	}
	if len(copied) > 0 {
		form := "their most common form"
		switch s := c.in.Settings; {
		case s != nil && s.TitleTemplate != "" && s.Sources.TitleTemplate == styleprofile.SourceYou:
			form = "the form the creator chose (the title_form block)"
		case p.Text != nil && len(p.Text.Patterns) > 0:
			form = p.Text.Patterns[0].Template()
		}
		return c.fail(CheckTitleCopied, "%s is word for word the title of one of the creator's reference videos: write a new title for this video in the creator's template, %s, with the topic of this footage",
			strings.Join(copied, ", "), form)
	}
	return c.pass(CheckTitleCopied, "%d %s, none the title of one of the creator's %d reference titles", texts, plural(texts, "text", "texts"), len(refs))
}

func (c *checker) beatSync() Check {
	var beats []int64
	for _, m := range c.in.Spec.Markers {
		if m.Kind == timeline.MarkerBeat || m.Kind == timeline.MarkerDownbeat {
			beats = append(beats, m.AtMs)
		}
	}
	cuts := len(c.items) - 1
	if cuts < 1 {
		return c.skip(CheckBeatSync, "there are no cuts")
	}
	if c.lim.looseBeat {
		return c.skip(CheckBeatSync, "the creator's cuts do not sit on a beat grid (loose near the music's onsets, or too few cuts): no beat grid is needed")
	}
	if len(beats) == 0 {
		if c.lim.beatGrid {
			return c.fail(CheckBeatSync, "there is no beat grid; write one with snap_cuts_to_beats (a bpm from 120 to 128, first_beat_ms 0, write_markers true) so the cuts land on the beat")
		}
		return c.skip(CheckBeatSync, "no beat grid is expected (a %s video, or a plan with tempo 0)", c.typ)
	}
	slices.Sort(beats)
	tol := int64(beatToleranceFrames) * 1000 / int64(max(c.in.Spec.Output.FPS, 1))
	on := 0
	var off []string
	for i := 1; i < len(c.items); i++ {
		at := c.items[i].AtMs
		_, d, _ := music.Nearest(beats, at)
		if it := c.items[i]; isTransition(it) {
			// A transition switches the picture at its middle (P6-06): that may be the moment on the beat.
			if _, md, _ := music.Nearest(beats, at+it.TransitionIn.DurationMs/2); md < d {
				d = md
			}
		}
		if d <= tol {
			on++
		} else if len(off) < 3 {
			off = append(off, fmt.Sprintf("%s at %d ms is %d ms off", c.items[i].ID, at, d))
		}
	}
	c.m.CutsOnBeat = on
	if float64(on)/float64(cuts) < beatShareMin {
		return c.fail(CheckBeatSync, "%d of %d cuts are on a beat (within %d ms); off: %s. Make each shot's length whole beats (set_speed to source ms / (beats x beat ms), a ramped shot by trim_item), then snap_cuts_to_beats once with max_shift_ms 80", on, cuts, tol, strings.Join(off, "; "))
	}
	return c.pass(CheckBeatSync, "%d of %d cuts are on a beat (within %d ms)", on, cuts, tol)
}

// Text writes the report as the editor reads it.
func (r Report) Text() string {
	pass, fail, skip := r.Counts()
	var b strings.Builder
	target := ""
	if r.TargetS > 0 {
		target = fmt.Sprintf(", target %d s", r.TargetS)
	}
	fmt.Fprintf(&b, "check_edit for version %d (%s%s): %d of %d checks pass, %d fail, %d do not apply.\n", r.Version, r.ContentType, target, pass, pass+fail, fail, skip)
	for _, c := range r.Checks {
		label := map[CheckStatus]string{CheckPass: "PASS", CheckFail: "FAIL", CheckSkip: "n/a "}[c.Status]
		fmt.Fprintf(&b, "%s %s: %s\n", label, c.ID, c.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}
