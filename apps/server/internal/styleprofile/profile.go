// Package styleprofile is the creator style profile (P6-05, docs/design/style-profile-v1.md): what a creator's finished videos look and
// sound like, measured from the videos themselves, so that the editor can follow the creator's style instead of a generic playbook.
//
// A reference video is measured by ffmpeg in the sandbox and plain Go (cuts and shot lengths, camera motion and zoom proxies, the
// light and the colour, the loudness, the shape of the music), and one small vision call reads the title text from a few frames of the
// hook. Every number is deterministic except the text read, and the text read is untrusted data. The measures of several references
// are aggregated into a Profile: medians and ranges per dimension, with a confidence that grows with the number of references.
//
// The profile is stored per workspace (store.CreatorStyle), shown to the editor as a compact block of untrusted data, and used by
// check_edit to set its thresholds and to measure how far an edit is from the creator's style (Distance).
package styleprofile

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/camshift"
)

// ProfileVersion is the version of the Profile shape. A reader checks it before it reads the rest. Version 2 (P6-05c) adds the title
// templates, the chapter titles, the shot-length tail and the ending hold, the transitions and the text entrance, the camera level and
// where the cuts sit against the movement; a version 1 profile is still read (those parts are then unknown).
const ProfileVersion = 2

// MinProfileVersion is the oldest Profile shape this code reads.
const MinProfileVersion = 1

// Range is a median with the smallest and the largest value seen, and the quartiles. The thresholds are made from the quartiles, not
// from the extremes (thresholds.go): the minimum and the maximum are for the reader.
type Range struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
	// P90 is the 90th percentile (0 in a version 1 profile).
	P90 float64 `json:"p90,omitempty"`
}

// Lower is the 25th percentile and Upper the 75th: what the thresholds are made from.
func (r Range) Lower() float64 { return r.P25 }

// Upper is the 75th percentile (see Lower).
func (r Range) Upper() float64 { return r.P75 }

// percentile is the linear-interpolated percentile (0 to 1) of sorted values.
func percentile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, n-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func (r Range) String() string {
	if r.Min == r.Max {
		return fmt.Sprintf("%.1f", r.Median)
	}
	return fmt.Sprintf("%.1f (%.1f to %.1f)", r.Median, r.Min, r.Max)
}

// RangeOf summarises values (a zero Range for none).
func RangeOf(values []float64) Range {
	if len(values) == 0 {
		return Range{}
	}
	s := slices.Clone(values)
	slices.Sort(s)
	return Range{Median: round2(median(s)), Min: round2(s[0]), Max: round2(s[len(s)-1]), P25: round2(percentile(s, 0.25)), P75: round2(percentile(s, 0.75)), P90: round2(percentile(s, 0.9))}
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// ---- one reference -----------------------------------------------------------------------------------------------------

// Measures are the measurements of one reference video.
type Measures struct {
	// Name is a label for the reference (the file's name), used in evidence lines. It is the creator's own text.
	Name      string  `json:"name"`
	DurationS float64 `json:"duration_s"`
	FPS       float64 `json:"fps"`
	Codec     string  `json:"codec"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	// CutsS are the times of the cuts, ShotsS the lengths of the shots between them (the first from the start, the last to the end).
	CutsS  []float64  `json:"cuts_s"`
	ShotsS []float64  `json:"shots_s"`
	Video  VideoStats `json:"video"`
	Audio  AudioStats `json:"audio"`
	// Text is what the vision call read from the hook, nil when it was not made or its answer was rejected.
	Text *TextStyle `json:"text,omitempty"`
	// Transitions is how the picture changes at the cuts and Movement where the cuts sit against the reps (P6-05c); nil in measures
	// made before they existed, or when the movement pass could not run.
	Transitions *TransitionStats `json:"transitions,omitempty"`
	Movement    *MovementStats   `json:"movement,omitempty"`
}

// FirstCutS is the time of the first cut, -1 for a video with none.
func (m Measures) FirstCutS() float64 {
	if len(m.CutsS) == 0 {
		return -1
	}
	return m.CutsS[0]
}

// VideoStats are the picture measures of a reference: proxies for camera motion and effects, and the light and the colour.
type VideoStats struct {
	// MotionMedian is the median, over the thumbnails, of the mean absolute difference from the previous one (0 to 255).
	MotionMedian float64 `json:"motion_median"`
	// ZoomEvents counts the quick zooms (a punch-in: 6 % of scale or more in half a second), PushInShare is the share of the time spent
	// in a slow steady zoom (a push-in or a pull-back), Flashes the brightness spikes that fall back within 0.4 s (a flash transition).
	ZoomEvents  int     `json:"zoom_events"`
	PushInShare float64 `json:"push_in_share"`
	Flashes     int     `json:"flashes"`
	// EffectsPerMinute is the estimate of the effect density: zoom events, flashes and the cuts that look like transitions (a dip or
	// a slide: a scene score between the two thresholds), per minute.
	EffectsPerMinute float64 `json:"effects_per_minute"`
	// LumaMean and LumaP90 are the medians of the frames' mean luma and 90th percentile luma (0 to 1, limited range), SatMean and
	// SatP90 those of the saturation (0 to 1), VignetteRatio the median of the corners' brightness over the centre's (1: no vignette).
	LumaMean      float64 `json:"luma_mean"`
	LumaP90       float64 `json:"luma_p90"`
	SatMean       float64 `json:"sat_mean"`
	SatP90        float64 `json:"sat_p90"`
	VignetteRatio float64 `json:"vignette_ratio"`
	// Camera is the global motion of the picture (package camshift): how much the camera itself moves, apart from the subject (P6-06).
	Camera camshift.Motion `json:"camera"`
}

// TextStyle is what the vision call read of the title of a reference: strict fields, and a text that is the creator's own words.
type TextStyle struct {
	TitlePresent bool `json:"title_present"`
	// Text is the title as read (untrusted data: a person wrote it, on a video that may say anything).
	Text string `json:"text,omitempty"`
	// Case is upper, lower or mixed; Colour a hex such as #F5F0DC; Position upper_third, center or lower_third; Alignment left, center or
	// right; Weight regular or bold; Condensed whether the letters are narrow; Lines how many lines the title has.
	Case      string `json:"case,omitempty"`
	Colour    string `json:"colour,omitempty"`
	Position  string `json:"position,omitempty"`
	Alignment string `json:"alignment,omitempty"`
	Weight    string `json:"weight,omitempty"`
	Condensed bool   `json:"condensed,omitempty"`
	Lines     int    `json:"lines,omitempty"`
	// TextHeightPct is the height of the capital letters as a percentage of the frame height, and Shadow is none, soft or strong (what
	// the vision read saw, an estimate).
	TextHeightPct float64 `json:"text_height_pct,omitempty"`
	Shadow        string  `json:"shadow,omitempty"`
	// ExerciseLabels says there are small labels (an exercise name, a tag) besides the title.
	ExerciseLabels bool `json:"exercise_labels,omitempty"`
	// Captions says there are spoken-word captions.
	Captions bool `json:"captions,omitempty"`
	// Entrance is how the title comes on (none, pop, slide, fade, typewriter; "" when not read or unknown), and Chapters the numbered
	// chapter titles read after the cuts (P6-05c, stylevision.v3): the creator's words, untrusted.
	Entrance string   `json:"entrance,omitempty"`
	Chapters []string `json:"chapters,omitempty"`
	// ChaptersRead says the later frames were read (a read of stylevision.v2 did not look for chapters).
	ChaptersRead bool `json:"chapters_read,omitempty"`
	// ShotTexts are the text in a frame of each shot (P6-05d, stylevision.v4): the lines are the creator's words, untrusted. ShotsRead says
	// the shots were read (a read of stylevision.v3 or older did not), and Language is the language of the text (ISO 639-1, "" unknown).
	ShotTexts []ShotText `json:"shot_texts,omitempty"`
	ShotsRead bool       `json:"shots_read,omitempty"`
	Language  string     `json:"language,omitempty"`
}

// AudioStats are the sound measures of a reference.
type AudioStats struct {
	HasAudio       bool    `json:"has_audio"`
	IntegratedLUFS float64 `json:"integrated_lufs"`
	RangeLU        float64 `json:"range_lu"`
	// IntroS is the time of the lift (the biggest rise of the loudness, smoothed over a second) and LiftLU its size; a video whose
	// loudness never rises by LiftMinLU has none (IntroS 0, LiftLU 0).
	IntroS float64 `json:"intro_s"`
	LiftLU float64 `json:"lift_lu"`
	// DropS is the drop of the music (package music: the biggest jump of the energy early on, P6-06), 0 when there is none; DropLU its size.
	DropS  float64 `json:"drop_s,omitempty"`
	DropLU float64 `json:"drop_lu,omitempty"`
	// QuarterLUFS is the mean loudness of each quarter of the video, Arc the shape of it: builds, flat or falls, and EndingLU the level
	// of the last three seconds against the integrated loudness (0: the music holds to the last frame).
	QuarterLUFS [4]float64 `json:"quarter_lufs"`
	Arc         string     `json:"arc"`
	EndingLU    float64    `json:"ending_lu"`
	// TempoBPM is the estimate from the onsets (0 when there is none) with its confidence 0 to 1.
	TempoBPM        float64 `json:"tempo_bpm"`
	TempoConfidence float64 `json:"tempo_confidence"`
	// CutOnsetMs is, for each cut, the distance to the nearest onset (the cut minus the onset is negative when the cut is early),
	// and CutBeatMs the distance to the nearest beat of the fitted tempo. CutsWithin100ms is the share of the cuts within 100 ms of an
	// onset. BeatLocked is the verdict: the cuts sit on the beat, or loosely near onsets.
	CutOnsetMs      []int64 `json:"cut_onset_ms,omitempty"`
	CutBeatMs       []int64 `json:"cut_beat_ms,omitempty"`
	CutsWithin100ms float64 `json:"cuts_within_100ms"`
	BeatLocked      bool    `json:"beat_locked"`
	// FirstCutOnsetMs is the first cut's distance to the nearest strong onset, and FirstCutAtLift whether the first cut lands within
	// 600 ms of the lift (the hook sits in the intro and the cut comes with the music).
	FirstCutOnsetMs int64 `json:"first_cut_onset_ms"`
	FirstCutAtLift  bool  `json:"first_cut_at_lift"`
	// MusicPresent says the sound is music (a steady tempo over a sound that is there). Flatness is the median spectral flatness (low:
	// tonal, musical; high: noise, a room, a crowd). HitsPer10s counts the very strong onsets (20 dB or more) away from the start and
	// the lift, for the record: music has them too, so they do not decide anything. Content is the reading: music_only (music with a
	// low flatness), music_with_ambience (music over noise or effects, which a model-free measure cannot tell apart), or unknown.
	MusicPresent bool    `json:"music_present"`
	Flatness     float64 `json:"flatness"`
	HitsPer10s   float64 `json:"hits_per_10s"`
	Content      string  `json:"content"`
	// Speech is "unknown": a voice activity detector is not part of this pass.
	Speech string `json:"speech"`
}

// FlatnessAmbience is the median spectral flatness above which the sound is called music over noise or effects. The five reference videos
// the thresholds were set on (one creator, trend music only) measure at most 0.1, so 0.2 leaves room for a brighter track without a
// room's noise being taken for music alone.
const FlatnessAmbience = 0.2

// ---- the profile ---------------------------------------------------------------------------------------------------------

// MinReferences is how many references a profile needs before the editor sets thresholds from it (fewer are a hint). MinCutsForBeat is
// how many measured cuts it takes to call the cuts loose against the beat.
const (
	MinReferences  = 3
	MinCutsForBeat = 3
)

// Firm says the profile rests on enough references to set thresholds from.
func (p *Profile) Firm() bool { return p != nil && p.References >= MinReferences }

// CutsOffBeat says the creator's cuts are known not to sit on a beat grid: measured loose, or too few to snap (a creator of long takes
// has almost no cuts, and a grid means nothing to them).
func (p *Profile) CutsOffBeat() bool {
	return p != nil && !p.Audio.BeatLocked && (p.Audio.CutsMeasured >= MinCutsForBeat || p.Pacing.Style == PaceLongTake)
}

// Profile is the aggregated creator style.
type Profile struct {
	Version    int `json:"version"`
	References int `json:"references"`
	// Confidence is 0 to 1 and grows with the number of references (0.9 x 0.2 a reference: one gives 0.2, three 0.54, five 0.9; ten or
	// more give 1). The editor treats a profile of fewer than MinReferences references as a hint and does not set thresholds from it.
	Confidence float64 `json:"confidence"`

	Format  FormatProfile  `json:"format"`
	Pacing  PacingProfile  `json:"pacing"`
	Hook    HookProfile    `json:"hook"`
	Effects EffectsProfile `json:"effects"`
	Look    LookProfile    `json:"look"`
	Text    *TextProfile   `json:"text,omitempty"`
	Audio   AudioProfile   `json:"audio"`
	// Camera is how the creator's camera moves, apart from any effect (P6-06): a hand-held creator's "no effects" still moves.
	Camera CameraProfile `json:"camera"`
	// Transitions and Movement (P6-05c): what happens at the cuts, and where they sit against the movement.
	Transitions TransitionsProfile `json:"transitions"`
	Movement    MovementProfile    `json:"movement"`

	// Evidence is one line per reference with its main numbers, for the creator to read and correct.
	Evidence []string `json:"evidence,omitempty"`
}

// FormatProfile is the size of the creator's videos.
type FormatProfile struct {
	DurationS Range   `json:"duration_s"`
	FPS       float64 `json:"fps"`
}

// Pacing styles.
const (
	PaceLongTake = "long_takes"
	PaceMontage  = "montage"
	PaceMixed    = "mixed"
)

// PacingProfile is how the creator cuts.
type PacingProfile struct {
	Style         string  `json:"style"`
	CutsPerVideo  Range   `json:"cuts_per_video"`
	CutsPerMinute Range   `json:"cuts_per_minute"`
	ShotMedianS   Range   `json:"shot_median_s"`
	LongestShotS  Range   `json:"longest_shot_s"`
	ShortestShotS Range   `json:"shortest_shot_s"`
	ZeroCutShare  float64 `json:"zero_cut_share"`
	LongTakeShare float64 `json:"long_take_share"` // the share of the screen time in shots of 6 s or more
	HasLongTakes  bool    `json:"has_long_takes"`
	// ShotS is the distribution of every shot length of the references that have a cut (P6-06): what a shot of this creator lasts.
	ShotS Range `json:"shot_s"`
	// ShotP90S is the 90th percentile of those shot lengths (how long the creator's longer shots run, a stray long take left out), and
	// EndingS the length of the last shot of each reference that has a cut (how long the creator holds the ending). 0 and empty in a
	// profile made before they were measured: the pace targets then fall back to ratios of the median (internal/agents/editor/pace.go).
	ShotP90S float64 `json:"shot_p90_s,omitempty"`
	EndingS  Range   `json:"ending_s"`
}

// P90 is the 90th percentile shot length: ShotP90S, or the shot range's own (both are the same measure; 0 when neither was measured).
func (pc PacingProfile) P90() float64 {
	if pc.ShotP90S > 0 {
		return pc.ShotP90S
	}
	return pc.ShotS.P90
}

// LongEndingHold says the creator holds the last shot clearly longer than their other shots: at least 2.5 s and 1.5 times the median
// shot.
func (pc PacingProfile) LongEndingHold() bool {
	return pc.EndingS.Median >= 2.5 && (pc.ShotS.Median == 0 || pc.EndingS.Median >= 1.5*pc.ShotS.Median)
}

// CameraProfile is the camera motion of the references (P6-06): Verdict is static or handheld (the majority), ShiftPctPerS the drift of
// the picture in percent of the frame a second, HandheldShare the share of the references that are hand-held.
type CameraProfile struct {
	Verdict       string  `json:"verdict,omitempty"`
	ShiftPctPerS  Range   `json:"shift_pct_per_s"`
	HandheldShare float64 `json:"handheld_share"`
	// Level is static, subtle or handheld (P6-05c): a picture that drifts a little but is not hand-held is subtle.
	Level string `json:"level,omitempty"`
}

// The camera levels.
const (
	CameraStatic   = "static"
	CameraSubtle   = "subtle"
	CameraHandheld = "handheld"
	// CameraSubtlePctPerS is the median drift from which a camera is not static (camshift.HandheldPctPerS is the one for hand-held).
	CameraSubtlePctPerS = 0.3
)

// TransitionsProfile is what happens at the creator's cuts (P6-05c). Used is none (the videos have no cuts), cut_only (every cut is a
// plain cut) or some; Kinds are the kinds seen (blend, flash, zoom), most used first; Share the share of the cuts with a transition.
type TransitionsProfile struct {
	Measured int      `json:"measured"`
	Cuts     int      `json:"cuts"`
	Used     string   `json:"used,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
	Share    float64  `json:"share"`
	// Videos is how many of the references have a transition at one of their cuts or more (0 in a profile made before it was counted).
	Videos int `json:"videos,omitempty"`
}

// The least evidence of transitions that makes them the creator's style: on a fifth of the cuts, in two videos or more. Fewer is a
// detector's stray reading or a one-off, and the style stays plain cuts.
const (
	TransitionsMinShare  = 0.2
	TransitionsMinVideos = 2
)

// Evident says the references measurably use transitions: some cuts have one, on at least TransitionsMinShare of the cuts, in at least
// TransitionsMinVideos videos.
func (tp TransitionsProfile) Evident() bool {
	return tp.Used == TransitionsSome && tp.Share >= TransitionsMinShare && tp.Videos >= TransitionsMinVideos
}

// Level is the transitions setting the creator's videos stand for: "" when they do not measurably use transitions (plain cuts, no cuts,
// or not measured), subtle when some of their cuts have one, lots when half of them or more do.
func (tp TransitionsProfile) Level() string {
	switch {
	case !tp.Evident():
		return ""
	case tp.Share >= 0.5:
		return LevelLots
	}
	return LevelSubtle
}

// The uses of transitions.
const (
	TransitionsNone    = "none"
	TransitionsCutOnly = "cut_only"
	TransitionsSome    = "some"
)

// MovementProfile is where the creator's cuts sit against the movement (P6-05c), over the cuts that could be measured (CutsMeasured;
// fewer than MinCutsForBeat means unknown).
type MovementProfile struct {
	CutsMeasured    int     `json:"cuts_measured"`
	OnBoundaryShare float64 `json:"on_boundary_share"`
	MidRepShare     float64 `json:"mid_rep_share"`
	InsideSetShare  float64 `json:"inside_set_share"`
}

// Known says enough cuts were measured to say anything.
func (m MovementProfile) Known() bool { return m.CutsMeasured >= MinCutsForBeat }

// HookProfile is how the videos open.
type HookProfile struct {
	// FirstCutS is the time of the first cut of the references that have one.
	FirstCutS Range `json:"first_cut_s"`
	// TitleShare is the share of the references with a title in the hook (of those read).
	TitleShare float64 `json:"title_share"`
}

// Effect verdicts.
const (
	EffectsNone  = "none"
	EffectsLight = "light"
	EffectsBusy  = "busy"
)

// EffectsProfile is how much the creator uses effects.
type EffectsProfile struct {
	Verdict          string  `json:"verdict"`
	PerMinute        Range   `json:"per_minute"`
	ZoomEventsPerMin float64 `json:"zoom_events_per_min"`
	PushInShare      Range   `json:"push_in_share"`
	FlashesPerMin    float64 `json:"flashes_per_min"`
}

// LookProfile is the light and the colour.
type LookProfile struct {
	LumaMean      Range `json:"luma_mean"`
	LumaP90       Range `json:"luma_p90"`
	Saturation    Range `json:"saturation"`
	VignetteRatio Range `json:"vignette_ratio"`
	// Grade is "none" for the natural picture of the camera (no vignette) and "graded" when a vignette or a heavy treatment shows.
	Grade string `json:"grade"`
	// LowKey says the footage is dark overall with a lit subject, as gym footage is.
	LowKey bool `json:"low_key"`
}

// TextProfile aggregates the title text styles read from the references.
type TextProfile struct {
	Read       int     `json:"read"` // how many references had a text read
	TitleShare float64 `json:"title_share"`
	Case       string  `json:"case,omitempty"`
	Colour     string  `json:"colour,omitempty"`
	Position   string  `json:"position,omitempty"`
	Alignment  string  `json:"alignment,omitempty"`
	Weight     string  `json:"weight,omitempty"`
	Condensed  bool    `json:"condensed,omitempty"`
	Lines      int     `json:"lines,omitempty"`
	// OneLineShare is the share of the titled references whose title is one line; HeightPct the median cap height as a percentage of the
	// frame height (0: not read); Shadow the majority shadow (none, soft or strong).
	OneLineShare float64 `json:"one_line_share,omitempty"`
	// OneLineChars is how long a title may be and still be one line for this creator: between the longest of their one-line titles and the
	// shortest of their wrapped ones (0: no preference known). Patterns are the forms their titles take, most common first.
	OneLineChars int            `json:"one_line_chars,omitempty"`
	Patterns     []TitlePattern `json:"patterns,omitempty"`
	HeightPct    float64        `json:"height_pct,omitempty"`
	Shadow       string         `json:"shadow,omitempty"`
	LabelShare   float64        `json:"label_share"`
	CaptionShare float64        `json:"caption_share"`
	// Examples are up to three titles as read: the creator's own words, untrusted.
	Examples []string `json:"examples,omitempty"`
	// Titles are every title read from the references (untrusted). They are never shown to the editor: check_edit compares an edit's
	// titles with them, so a reference's title is never reused word for word (title_copied).
	Titles []string `json:"titles,omitempty"`
	// Templates are the forms the titles take with the changing words as slots (P6-05c), most common first. Entrance is the most common
	// way the title comes on (of EntranceRead reads). ChapterRead is how many multi-shot references were read for chapter titles,
	// ChapterShare the share of those that have numbered chapter titles, ChapterTemplates their forms and ChapterExamples a couple of
	// them (the creator's words, untrusted).
	Templates        []TitleTemplate `json:"templates,omitempty"`
	Entrance         string          `json:"entrance,omitempty"`
	EntranceRead     int             `json:"entrance_read,omitempty"`
	ChapterRead      int             `json:"chapter_read,omitempty"`
	ChapterShare     float64         `json:"chapter_share,omitempty"`
	ChapterTemplates []TitleTemplate `json:"chapter_templates,omitempty"`
	ChapterExamples  []string        `json:"chapter_examples,omitempty"`
	// Pattern is the creator's text pattern (P6-05d: single_title, story_captions, dialogue or none; "" when no read saw the shots), the
	// majority of PatternRead reads, PatternShare its share; PatternsSeen the captions and dialogue patterns seen in any read; LineLook
	// the look of the pattern's lines (nil without any); Language the language of the text (ISO 639-1).
	Pattern      string    `json:"pattern,omitempty"`
	PatternShare float64   `json:"pattern_share,omitempty"`
	PatternRead  int       `json:"pattern_read,omitempty"`
	PatternsSeen []string  `json:"patterns_seen,omitempty"`
	LineLook     *TextLook `json:"line_look,omitempty"`
	Language     string    `json:"language,omitempty"`
}

// NumberedChapters says the creator's multi-exercise videos have numbered chapter titles (half of the ones read or more).
func (t *TextProfile) NumberedChapters() bool {
	return t != nil && t.ChapterRead > 0 && t.ChapterShare >= 0.5
}

// Audio arcs.
const (
	ArcBuilds = "builds"
	ArcFlat   = "flat"
	ArcFalls  = "falls"
	ArcMixed  = "mixed"
)

// Content readings of the sound.
const (
	ContentMusicOnly    = "music_only"
	ContentMusicEffects = "music_with_ambience"
	ContentUnknown      = "unknown"
)

// AudioProfile is how the creator's videos sound.
type AudioProfile struct {
	IntroS Range `json:"intro_s"`
	// DropS is where the music drops in the references that have a drop, DropShare the share that do (P6-06).
	DropS     Range   `json:"drop_s"`
	DropShare float64 `json:"drop_share"`
	LiftLU    Range   `json:"lift_lu"`
	LiftShare float64 `json:"lift_share"` // the share of the references with a clear lift
	Arc       string  `json:"arc"`
	EndingLU  Range   `json:"ending_lu"`
	NoFadeOut bool    `json:"no_fade_out"`
	TempoBPM  Range   `json:"tempo_bpm"`
	// TempoByPacing is the median tempo of the long-take references and of the montage ones (0 when there are none of that kind).
	TempoLongTakeBPM float64 `json:"tempo_long_take_bpm"`
	TempoMontageBPM  float64 `json:"tempo_montage_bpm"`
	CutOnsetMs       Range   `json:"cut_onset_ms"` // the absolute distance of a cut to the nearest onset, over all the cuts
	CutsWithin100ms  float64 `json:"cuts_within_100ms"`
	BeatLocked       bool    `json:"beat_locked"`
	// CutsMeasured is how many cuts had a strong onset to be measured against: the evidence behind BeatLocked (a creator of long takes has
	// few cuts, and few is not "loose").
	CutsMeasured        int     `json:"cuts_measured"`
	FirstCutAtLiftShare float64 `json:"first_cut_at_lift_share"`
	Content             string  `json:"content"`
	MusicShare          float64 `json:"music_share"`
	Speech              string  `json:"speech"`
	LUFS                Range   `json:"lufs"`
	RangeLU             Range   `json:"range_lu"`
}

// ---- aggregation ----------------------------------------------------------------------------------------------------------

// Aggregate builds the profile from the measures of the references. It returns nil for none.
func Aggregate(refs []Measures) *Profile {
	n := len(refs)
	if n == 0 {
		return nil
	}
	p := &Profile{Version: ProfileVersion, References: n}
	var dur, cutsPer, cutsMin, shotMed, longest, shortest, firstCut, fx, push []float64
	var zeroCuts, totalMin, zooms, flashes float64
	var longTime, allTime float64
	var luma, lumaP, sat, vig []float64
	var fps []float64
	for _, m := range refs {
		dur = append(dur, m.DurationS)
		fps = append(fps, m.FPS)
		cutsPer = append(cutsPer, float64(len(m.CutsS)))
		if m.DurationS > 0 {
			cutsMin = append(cutsMin, float64(len(m.CutsS))/m.DurationS*60)
		}
		if len(m.CutsS) == 0 {
			zeroCuts++
		} else {
			firstCut = append(firstCut, m.CutsS[0])
		}
		s := slices.Clone(m.ShotsS)
		slices.Sort(s)
		if len(s) > 0 {
			longest = append(longest, s[len(s)-1])
		}
		if len(s) > 0 && len(m.CutsS) > 0 { // shot-length statistics come from the references that have a cut: a video that is one take is no shot-length sample
			shotMed = append(shotMed, median(s))
			shortest = append(shortest, s[0])
		}
		for _, l := range m.ShotsS {
			allTime += l
			if l >= 6 {
				longTime += l
			}
		}
		fx = append(fx, m.Video.EffectsPerMinute)
		push = append(push, m.Video.PushInShare)
		totalMin += m.DurationS / 60
		zooms += float64(m.Video.ZoomEvents)
		flashes += float64(m.Video.Flashes)
		luma, lumaP, sat, vig = append(luma, m.Video.LumaMean), append(lumaP, m.Video.LumaP90), append(sat, m.Video.SatMean), append(vig, m.Video.VignetteRatio)
	}
	// The camera (P6-06): the drift of the picture, apart from effects; and every shot length of the references with a cut.
	var shift, allShots, endings []float64
	var verdicts []string
	handheld := 0.0
	for _, m := range refs {
		if v := m.Video.Camera.Verdict(); v != "" {
			verdicts = append(verdicts, v)
			shift = append(shift, m.Video.Camera.ShiftPctPerS)
			if v == camshift.Handheld {
				handheld++
			}
		}
		if len(m.CutsS) > 0 && len(m.ShotsS) > 0 {
			allShots = append(allShots, m.ShotsS...)
			endings = append(endings, m.ShotsS[len(m.ShotsS)-1])
		}
	}
	if len(verdicts) > 0 {
		p.Camera = CameraProfile{Verdict: majority(verdicts), ShiftPctPerS: RangeOf(shift), HandheldShare: round2(handheld / float64(len(verdicts)))}
		switch {
		case p.Camera.HandheldShare >= 0.5:
			p.Camera.Level = CameraHandheld
		case p.Camera.ShiftPctPerS.Median >= CameraSubtlePctPerS:
			p.Camera.Level = CameraSubtle
		default:
			p.Camera.Level = CameraStatic
		}
	}
	p.Format = FormatProfile{DurationS: RangeOf(dur), FPS: RangeOf(fps).Median}
	pace := PacingProfile{
		CutsPerVideo: RangeOf(cutsPer), CutsPerMinute: RangeOf(cutsMin), ShotMedianS: RangeOf(shotMed), LongestShotS: RangeOf(longest),
		ShortestShotS: RangeOf(shortest), ZeroCutShare: round2(zeroCuts / float64(n)), ShotS: RangeOf(allShots), EndingS: RangeOf(endings),
	}
	if len(allShots) > 0 {
		pace.ShotP90S = round2(percentile(sortedCopy(allShots), 0.9))
	}
	if allTime > 0 {
		pace.LongTakeShare = round2(longTime / allTime)
	}
	pace.HasLongTakes = pace.LongestShotS.Median >= 6
	// The classification is on purpose: the longest takes and the cut counts are those of every reference, so a creator whose videos are
	// mostly single takes is long takes however few references have a cut; only the shot-length criteria use the references with a cut.
	switch {
	case pace.LongestShotS.Median >= 6 && pace.CutsPerVideo.Median <= 5, pace.ShotMedianS.Median >= 6:
		pace.Style = PaceLongTake // every video lets a take run, and the cuts are few
	case pace.ShotMedianS.Median <= 3 && pace.CutsPerMinute.Median >= 12:
		pace.Style = PaceMontage
	default:
		pace.Style = PaceMixed
	}
	p.Pacing = pace

	p.Hook = HookProfile{FirstCutS: RangeOf(firstCut)}
	fxr := RangeOf(fx)
	verdict := EffectsNone
	switch {
	case fxr.Median >= 8:
		verdict = EffectsBusy
	case fxr.Median >= 1.5 || fxr.Upper() >= 6: // the quartile, so one odd reference does not make a creator of none "light"
		verdict = EffectsLight
	}
	p.Effects = EffectsProfile{Verdict: verdict, PerMinute: fxr, PushInShare: RangeOf(push)}
	if totalMin > 0 {
		p.Effects.ZoomEventsPerMin, p.Effects.FlashesPerMin = round2(zooms/totalMin), round2(flashes/totalMin)
	}
	look := LookProfile{LumaMean: RangeOf(luma), LumaP90: RangeOf(lumaP), Saturation: RangeOf(sat), VignetteRatio: RangeOf(vig), Grade: "none"}
	if look.VignetteRatio.Median < 0.8 {
		look.Grade = "graded"
	}
	look.LowKey = look.LumaMean.Median < 0.25
	p.Look = look
	p.Text = aggregateText(refs)
	if p.Text != nil {
		p.Hook.TitleShare = p.Text.TitleShare
	}
	p.Audio = aggregateAudio(refs)
	p.Transitions = aggregateTransitions(refs)
	p.Movement = aggregateMovement(refs)

	// Confidence: 0.9 x 0.2 a reference (five give 0.9), 1 for ten or more, 0.2 for one.
	conf := math.Min(1, 0.2*float64(n)) * 0.9
	if n >= 10 {
		conf = 1
	}
	if n == 1 {
		conf = 0.2
	}
	p.Confidence = round2(conf)
	for _, m := range refs {
		p.Evidence = append(p.Evidence, evidenceLine(m))
	}
	return p
}

func evidenceLine(m Measures) string {
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = "reference"
	}
	title := ""
	if m.Text != nil && m.Text.TitlePresent && m.Text.Text != "" {
		title = fmt.Sprintf(", title %q", m.Text.Text)
	}
	a := m.Audio
	sound := ""
	if a.HasAudio {
		lift := "no lift"
		if a.LiftLU > 0 {
			lift = fmt.Sprintf("lift %.1f s (+%.1f LU)", a.IntroS, a.LiftLU)
		}
		cuts := ""
		if len(a.CutOnsetMs) > 0 {
			abs := make([]float64, len(a.CutOnsetMs))
			for i, d := range a.CutOnsetMs {
				abs[i] = math.Abs(float64(d))
			}
			slices.Sort(abs)
			cuts = fmt.Sprintf(", cuts %.0f ms from an onset (median)", median(abs))
		}
		sound = fmt.Sprintf(", %s, arc %s, ends %+.1f LU, %.0f BPM, %.1f LUFS (range %.1f LU)%s", lift, a.Arc, a.EndingLU, a.TempoBPM, a.IntegratedLUFS, a.RangeLU, cuts)
	}
	return fmt.Sprintf("%s: %.1f s, %d cuts%s%s", name, m.DurationS, len(m.CutsS), title, sound)
}

func majority(values []string) string {
	count := map[string]int{}
	for _, v := range values {
		if v != "" {
			count[v]++
		}
	}
	best, bn := "", 0
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if count[k] > bn {
			best, bn = k, count[k]
		}
	}
	return best
}

// portraitFactor converts a share of the reference's own frame height to a share of a 9:16 frame's height (1.78 times the short side),
// the unit the profile and the style_match check use: a 16:9 reference whose capitals are 4 % of its 1080 px height has capitals of 43 px,
// 2.25 % of a 1920 px height. 1 when the dimensions are not known.
func portraitFactor(m Measures) float64 {
	if m.Width <= 0 || m.Height <= 0 {
		return 1
	}
	return float64(m.Height) / (float64(min(m.Width, m.Height)) * 16 / 9)
}

func aggregateText(refs []Measures) *TextProfile {
	var read []*TextStyle
	var readCuts []int
	portrait := map[*TextStyle]float64{} // each read's factor to a 9:16 frame's height
	for _, m := range refs {
		if m.Text != nil {
			read = append(read, m.Text)
			readCuts = append(readCuts, len(m.CutsS))
			portrait[m.Text] = portraitFactor(m)
		}
	}
	if len(read) == 0 {
		return nil
	}
	t := &TextProfile{Read: len(read)}
	aggregatePattern(t, read)
	var titled []*TextStyle
	var labels, captions float64
	for _, r := range read {
		if r.TitlePresent {
			titled = append(titled, r)
		}
		if r.ExerciseLabels {
			labels++
		}
		if r.Captions {
			captions++
		}
	}
	t.TitleShare = round2(float64(len(titled)) / float64(len(read)))
	// The entrance and the chapters (P6-05c) are known only from reads that looked for them.
	var entrances []string
	var chapterShapes []titleShape
	withChapters := 0
	for i, r := range read {
		if r.Entrance != "" && r.Entrance != "unknown" {
			entrances = append(entrances, r.Entrance)
		}
		if r.ChaptersRead && readCuts[i] >= 2 {
			t.ChapterRead++
			if len(r.Chapters) > 0 {
				withChapters++
				for _, c := range r.Chapters {
					chapterShapes = append(chapterShapes, titleShape{text: c})
					if len(t.ChapterExamples) < 2 {
						t.ChapterExamples = append(t.ChapterExamples, c)
					}
				}
			}
		}
	}
	t.Entrance, t.EntranceRead = majority(entrances), len(entrances)
	if t.ChapterRead > 0 {
		t.ChapterShare = round2(float64(withChapters) / float64(t.ChapterRead))
		t.ChapterTemplates = templatesOf(chapterShapes)
	}
	t.LabelShare, t.CaptionShare = round2(labels/float64(len(read))), round2(captions/float64(len(read)))
	if len(titled) == 0 {
		return t
	}
	var cases, colours, positions, aligns, weights, shadows []string
	var condensed, oneLine float64
	var lines, heights []float64
	var titleTexts []string
	oneMax, twoMin := 0, 0
	for _, r := range titled {
		if n := utf8.RuneCountInString(strings.Join(strings.Fields(r.Text), " ")); r.Text != "" && n > 0 {
			titleTexts = append(titleTexts, r.Text)
			if r.Lines == 1 {
				oneMax = max(oneMax, n)
			} else if r.Lines >= 2 && (twoMin == 0 || n < twoMin) {
				twoMin = n
			}
		}
		cases, colours, positions = append(cases, r.Case), append(colours, strings.ToUpper(r.Colour)), append(positions, r.Position)
		aligns, weights = append(aligns, r.Alignment), append(weights, r.Weight)
		shadows = append(shadows, r.Shadow)
		if r.Lines == 1 {
			oneLine++
		}
		if r.TextHeightPct > 0 {
			heights = append(heights, r.TextHeightPct*portrait[r])
		}
		if r.Condensed {
			condensed++
		}
		if r.Lines > 0 {
			lines = append(lines, float64(r.Lines))
		}
		if r.Text != "" && len(t.Examples) < 3 {
			t.Examples = append(t.Examples, r.Text)
		}
	}
	t.Case, t.Colour, t.Position, t.Alignment, t.Weight = majority(cases), medianColour(colours), majority(positions), majority(aligns), majority(weights)
	t.Shadow, t.OneLineShare = majority(shadows), round2(oneLine/float64(len(titled)))
	t.Patterns = patternsOf(titleTexts)
	var shapes []titleShape
	for _, r := range titled {
		if r.Text != "" {
			shapes = append(shapes, titleShape{text: r.Text, lines: r.Lines})
		}
	}
	t.Templates = templatesOf(shapes)
	t.Titles = titleTexts
	switch {
	case oneMax > 0 && twoMin > oneMax:
		t.OneLineChars = (oneMax + twoMin) / 2 // between the longest title that fitted one line and the shortest that wrapped
	case oneMax > 0 && twoMin > 0:
		t.OneLineChars = twoMin - 1 // a title that wrapped is no longer than one that did not: stay under the shortest that wrapped
	case oneMax > 0:
		t.OneLineChars = oneMax + 4 // no title of theirs wrapped: a little longer than the longest is still one line
	}
	if len(heights) > 0 {
		t.HeightPct = RangeOf(heights).Median
	}
	t.Condensed = condensed*2 > float64(len(titled))
	if len(lines) > 0 {
		t.Lines = int(math.Round(RangeOf(lines).Median))
	}
	return t
}

func aggregateAudio(refs []Measures) AudioProfile {
	var intro, lift, ending, tempo, lufs, lra, cutOnset []float64
	var within, within100n, liftN, firstAtLift, firstCuts, music float64
	var arcs []string
	var flats []float64
	var longTempos, montTempos []float64
	have := 0
	for _, m := range refs {
		a := m.Audio
		if !a.HasAudio {
			continue
		}
		have++
		lufs, lra = append(lufs, a.IntegratedLUFS), append(lra, a.RangeLU)
		ending = append(ending, a.EndingLU)
		arcs = append(arcs, a.Arc)
		if a.LiftLU > 0 {
			intro, lift = append(intro, a.IntroS), append(lift, a.LiftLU)
			liftN++
		}
		if a.TempoBPM > 0 {
			tempo = append(tempo, a.TempoBPM)
			if len(m.CutsS) <= 1 || median(sortedCopy(m.ShotsS)) >= 6 {
				longTempos = append(longTempos, a.TempoBPM)
			} else {
				montTempos = append(montTempos, a.TempoBPM)
			}
		}
		for _, d := range a.CutOnsetMs {
			cutOnset = append(cutOnset, math.Abs(float64(d)))
		}
		if len(a.CutOnsetMs) > 0 {
			within += a.CutsWithin100ms * float64(len(a.CutOnsetMs))
			within100n += float64(len(a.CutOnsetMs))
		}
		if len(m.CutsS) > 0 {
			firstCuts++
			if a.FirstCutAtLift {
				firstAtLift++
			}
		}
		if a.MusicPresent {
			music++
		}
		if a.Flatness > 0 {
			flats = append(flats, a.Flatness)
		}
	}
	out := AudioProfile{Speech: "unknown", Content: ContentUnknown}
	if have == 0 {
		return out
	}
	out.IntroS, out.LiftLU, out.EndingLU = RangeOf(intro), RangeOf(lift), RangeOf(ending)
	out.LiftShare = round2(liftN / float64(have))
	var drops []float64
	for _, m := range refs {
		if m.Audio.HasAudio && m.Audio.DropS > 0 {
			drops = append(drops, m.Audio.DropS)
		}
	}
	out.DropS, out.DropShare = RangeOf(drops), round2(float64(len(drops))/float64(have))
	out.TempoBPM, out.LUFS, out.RangeLU = RangeOf(tempo), RangeOf(lufs), RangeOf(lra)
	out.TempoLongTakeBPM, out.TempoMontageBPM = RangeOf(longTempos).Median, RangeOf(montTempos).Median
	out.CutOnsetMs = RangeOf(cutOnset)
	if within100n > 0 {
		out.CutsWithin100ms = round2(within / within100n)
	}
	// Beat-locked only when the cuts sit on the onsets: a median distance under 50 ms and nearly all within 100 ms.
	out.CutsMeasured = len(cutOnset)
	out.BeatLocked = len(cutOnset) >= 2 && out.CutOnsetMs.Median <= 50 && out.CutsWithin100ms >= 0.8
	if firstCuts > 0 {
		out.FirstCutAtLiftShare = round2(firstAtLift / firstCuts)
	}
	out.MusicShare = round2(music / float64(have))
	out.Arc = majority(arcs)
	if out.Arc == "" {
		out.Arc = ArcFlat
	}
	if distinct := countDistinct(arcs); distinct > 2 {
		out.Arc = ArcMixed
	}
	// No fade-out: every reference ends within 2 LU of its average (a fade ends far below it).
	out.NoFadeOut = out.EndingLU.Min >= -2
	if out.MusicShare >= 0.6 {
		out.Content = ContentMusicOnly
		if RangeOf(flats).Median > FlatnessAmbience {
			out.Content = ContentMusicEffects
		}
	}
	return out
}

func sortedCopy(v []float64) []float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s
}

func countDistinct(v []string) int {
	seen := map[string]bool{}
	for _, s := range v {
		seen[s] = true
	}
	return len(seen)
}

// ---- reading it ------------------------------------------------------------------------------------------------------------

// Describe is the profile in plain language, for the creator to read and for `cfctl style show`.
func (p *Profile) Describe() string { return p.describe(true) }

// describe is Describe; without words it leaves the title forms out (they hold the creator's fixed words).
func (p *Profile) describe(withWords bool) string {
	if p == nil {
		return "No style profile yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Creator style, from %d reference videos (confidence %.0f %%).\n", p.References, p.Confidence*100)
	fmt.Fprintf(&b, "Pacing: %s. %s cuts a video (%s a minute), shots %s s long at the median, the longest %s s", paceWords(p.Pacing.Style),
		p.Pacing.CutsPerVideo, p.Pacing.CutsPerMinute, p.Pacing.ShotMedianS, p.Pacing.LongestShotS)
	if p.Pacing.ZeroCutShare > 0 {
		fmt.Fprintf(&b, "; %.0f %% of the videos have no cut at all", p.Pacing.ZeroCutShare*100)
	}
	b.WriteString(".\n")
	if p.Hook.FirstCutS.Median > 0 {
		fmt.Fprintf(&b, "Hook: the first cut comes at %s s.\n", p.Hook.FirstCutS)
	}
	fmt.Fprintf(&b, "Effects: %s (an estimate of %.1f zoom or flash moments a minute). ", p.Effects.Verdict, p.Effects.PerMinute.Median)
	if c := p.Camera; c.Verdict != "" {
		fmt.Fprintf(&b, "Camera: %s (drifts %s %% of the frame a second). ", c.Verdict, c.ShiftPctPerS)
	}
	fmt.Fprintf(&b, "Look: %s grade, brightness (p90 luma) %s, %s.\n", p.Look.Grade, p.Look.LumaP90, map[bool]string{true: "low-key footage", false: "normally lit footage"}[p.Look.LowKey])
	if t := p.Text; t != nil {
		if t.TitleShare == 0 {
			b.WriteString("Text: no title.\n")
		} else {
			fmt.Fprintf(&b, "Text: a single title (%.0f %% of the videos), %s, %s, %s, %s", t.TitleShare*100, orDash(t.Case), orDash(t.Colour), orDash(t.Position), orDash(t.Alignment))
			if t.Condensed {
				b.WriteString(", condensed")
			}
			if t.HeightPct > 0 {
				fmt.Fprintf(&b, ", letters about %.1f %% of the frame height", t.HeightPct)
			}
			if t.Shadow != "" {
				fmt.Fprintf(&b, ", %s shadow", t.Shadow)
			}
			if t.OneLineChars > 0 && t.OneLineShare >= 0.5 {
				fmt.Fprintf(&b, ", every title one line (at most %d characters)", t.OneLineChars)
			} else if t.OneLineChars > 0 {
				fmt.Fprintf(&b, ", titles up to %d characters on one line", t.OneLineChars)
			} else if t.OneLineShare >= 0.5 {
				b.WriteString(", one line")
			}
			if t.LabelShare == 0 {
				b.WriteString(", no exercise labels")
			}
			b.WriteString(".\n")
		}
	} else {
		b.WriteString("Text: not read (the vision step was not run).\n")
	}
	b.WriteString("Sound: " + p.Audio.Describe() + "\n")
	for _, l := range p.structureLines(withWords) {
		b.WriteString(strings.ToUpper(l[:1]) + l[1:] + ".\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func paceWords(style string) string {
	switch style {
	case PaceLongTake:
		return "long takes"
	case PaceMontage:
		return "a fast montage"
	}
	return "a mix of long takes and quick cuts"
}

// Describe is the audio profile in plain language.
func (a AudioProfile) Describe() string {
	var parts []string
	if a.LiftShare >= 0.6 {
		parts = append(parts, fmt.Sprintf("quiet %.1f s intro, then the music lifts (by %.1f LU)", a.IntroS.Median, a.LiftLU.Median))
	}
	if a.BeatLocked {
		parts = append(parts, "cuts sit on the beat")
	} else if a.CutsMeasured < MinCutsForBeat {
		parts = append(parts, "too few cuts to say whether they sit on the beat")
	} else {
		parts = append(parts, "cuts sit loosely near onsets (not beat-locked)")
	}
	switch a.Arc {
	case ArcBuilds:
		parts = append(parts, "energy builds to the end")
	case ArcFalls:
		parts = append(parts, "energy falls toward the end")
	case ArcFlat:
		parts = append(parts, "energy stays flat")
	}
	if a.NoFadeOut {
		parts = append(parts, "no fade-out")
	}
	switch a.Content {
	case ContentMusicOnly:
		parts = append(parts, fmt.Sprintf("music only at about %.0f LUFS, no sound effects", a.LUFS.Median))
	case ContentMusicEffects:
		parts = append(parts, fmt.Sprintf("music over noise or effects at about %.0f LUFS", a.LUFS.Median))
	default:
		parts = append(parts, fmt.Sprintf("about %.0f LUFS", a.LUFS.Median))
	}
	if a.TempoBPM.Median > 0 {
		t := fmt.Sprintf("tempo %.0f to %.0f BPM", a.TempoBPM.Min, a.TempoBPM.Max)
		if a.TempoLongTakeBPM > 0 && a.TempoMontageBPM > 0 {
			t += fmt.Sprintf(" (about %.0f for the long takes, %.0f for the cut-up ones)", a.TempoLongTakeBPM, a.TempoMontageBPM)
		}
		parts = append(parts, t)
	}
	s := strings.Join(parts, "; ")
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// CompactBlock is the profile as the editor reads it: short lines, numbers and rules, with the creator's own words (the examples) left
// to the caller to wrap as untrusted data.
func (p *Profile) CompactBlock() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "references: %d, confidence %.0f %%\n", p.References, p.Confidence*100)
	fmt.Fprintf(&b, "pacing: %s; cuts per video %s; shot median %s s, longest %s s; long takes (6 s or more) are %.0f %% of the screen time; %.0f %% of the videos have no cut\n",
		p.Pacing.Style, p.Pacing.CutsPerVideo, p.Pacing.ShotMedianS, p.Pacing.LongestShotS, p.Pacing.LongTakeShare*100, p.Pacing.ZeroCutShare*100)
	if p.Hook.FirstCutS.Median > 0 {
		fmt.Fprintf(&b, "hook: first cut at %s s\n", p.Hook.FirstCutS)
	}
	fmt.Fprintf(&b, "effects: %s (applied punch-ins, flashes and speed ramps measured in the picture); labels: %s\n", p.Effects.Verdict, labelsWord(p.Text))
	if c := p.Camera; c.Verdict != "" {
		fmt.Fprintf(&b, "camera: %s (the picture drifts %s %% of the frame a second; %.0f %% of the videos are hand-held)", c.Verdict, c.ShiftPctPerS, c.HandheldShare*100)
		if c.Verdict == camshift.Handheld {
			b.WriteString(": the camera never sits still, so tripod footage gets the handheld living camera; that is camera motion, not an effect")
		}
		b.WriteString("\n")
	}
	// The shot lengths, their 90th percentile and the ending are in structureLines.
	if p.Audio.DropS.Median > 0 {
		fmt.Fprintf(&b, "drop: the music drops at %s s in %.0f %% of the videos; the hook ends on it\n", p.Audio.DropS, p.Audio.DropShare*100)
	}
	for _, l := range p.structureLines(true) {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "look: grade %s; footage %s (p90 luma %s)\n", p.Look.Grade, map[bool]string{true: "low-key", false: "normally lit"}[p.Look.LowKey], p.Look.LumaP90)
	if t := p.Text; t != nil && t.TitleShare > 0 {
		fmt.Fprintf(&b, "title: case %s, colour %s, %s, %s, weight %s, condensed %v, lines %d (one line in %.0f %% of the videos)", orDash(t.Case), orDash(t.Colour),
			orDash(t.Position), orDash(t.Alignment), orDash(t.Weight), t.Condensed, t.Lines, t.OneLineShare*100)
		if t.HeightPct > 0 {
			fmt.Fprintf(&b, ", letter height %.1f %% of the frame", t.HeightPct)
		}
		if t.Shadow != "" {
			fmt.Fprintf(&b, ", shadow %s", t.Shadow)
		}
		b.WriteString("\n")
		if pl := patternLine(t.Patterns); pl != "" {
			b.WriteString(pl + "\n")
		}
		if r := p.TitleRecipe(); r != nil {
			b.WriteString(r.Describe() + "\n")
		}
	} else if t != nil {
		b.WriteString("title: none\n")
	}
	fmt.Fprintf(&b, "sound: %s\n", p.Audio.Describe())
	fmt.Fprintf(&b, "sound numbers: intro %s s, lift %s LU, first cut at the lift in %.0f %% of the videos; cuts within 100 ms of an onset %.0f %%, beat-locked %v; sound effects %v",
		p.Audio.IntroS, p.Audio.LiftLU, p.Audio.FirstCutAtLiftShare*100, p.Audio.CutsWithin100ms*100, p.Audio.BeatLocked, p.Audio.Content == ContentMusicEffects)
	return b.String()
}

func labelsWord(t *TextProfile) string {
	if t == nil {
		return "unknown"
	}
	if t.LabelShare == 0 {
		return "none (no exercise labels, one title only)"
	}
	return fmt.Sprintf("in %.0f %% of the videos", t.LabelShare*100)
}


func aggregateTransitions(refs []Measures) TransitionsProfile {
	var tp TransitionsProfile
	kinds := map[string]int{}
	with := 0
	for _, m := range refs {
		if m.Transitions == nil {
			continue
		}
		t := m.Transitions
		tp.Measured++
		tp.Cuts += t.Cuts
		with += t.Blend + t.Flash + t.Zoom
		if t.Blend+t.Flash+t.Zoom > 0 {
			tp.Videos++
		}
		kinds["blend"] += t.Blend
		kinds["flash"] += t.Flash
		kinds["zoom"] += t.Zoom
	}
	if tp.Measured == 0 {
		return tp
	}
	switch {
	case tp.Cuts == 0:
		tp.Used = TransitionsNone
	case with == 0:
		tp.Used = TransitionsCutOnly
	default:
		tp.Used = TransitionsSome
		tp.Share = round2(float64(with) / float64(tp.Cuts))
		for _, k := range []string{"blend", "flash", "zoom"} {
			if kinds[k] > 0 {
				tp.Kinds = append(tp.Kinds, k)
			}
		}
		slices.SortStableFunc(tp.Kinds, func(a, b string) int { return kinds[b] - kinds[a] })
	}
	return tp
}

func aggregateMovement(refs []Measures) MovementProfile {
	var mp MovementProfile
	var on, mid, inside int
	for _, m := range refs {
		if m.Movement == nil {
			continue
		}
		mp.CutsMeasured += m.Movement.CutsMeasured
		on, mid, inside = on+m.Movement.OnBoundary, mid+m.Movement.MidRep, inside+m.Movement.InsideSet
	}
	if mp.CutsMeasured > 0 {
		c := float64(mp.CutsMeasured)
		mp.OnBoundaryShare, mp.MidRepShare, mp.InsideSetShare = round2(float64(on)/c), round2(float64(mid)/c), round2(float64(inside)/c)
	}
	return mp
}
