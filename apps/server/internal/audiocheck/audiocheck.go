// Package audiocheck is the editor's ears (P3-11): a listening report on a rendered video, built only from ffmpeg measurements and
// plain Go, with no model. It measures loudness (integrated, true peak, range, and the momentary curve), silence, where the sound
// starts and stops, when the first sound and the body level arrive, whether the sound lands on the cuts of the edit and on the
// planned sound effects, how dense the effects are, and the usual defects (clipping, DC offset, a lopsided stereo image). The
// result is a Report: a plain-language summary first, a pass/fail checklist against targets (each with its source and whether it
// is an estimate), the structured numbers, and a PNG sheet that draws the sound on the contact sheet's time axis so that a vision
// model can look at it.
//
// Why no model: the sound-design notes of the earshot project (https://github.com/Leonxlnx/earshot, MIT licence, docs/LIMITS.md)
// say themselves that the measurements are the trustworthy part and the model verdicts are hints, and a model brings a 1.4 GB
// download, 20 to 40 s of load time per call and a licence review (D-16) for a verdict that cannot be told from taste. The
// targets below are earshot's measured profile of seven launch films and the delivery practice of the web, with each source named
// in checks.go; where a number is an estimate it says so. See docs/design/audio-v1.md.
//
// How the editor's check_edit tool calls it (the wiring is not part of P3-11):
//
//	report, err := audiocheck.Check(ctx, runner, mp4Path, plan.TimeMap,
//		audiocheck.WithFPS(plan.FPS), audiocheck.WithSFX(audiocheck.PlannedSFXFromSpec(spec)))
//	// report.Summary is for the model's context, report.Checklist for the tool result, report.Sheet (a PNG) as an image.
package audiocheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Status of one check.
const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
	// StatusInfo is a number that has no target here (or one that does not apply to this edit): it is reported, never failed.
	StatusInfo = "info"
)

// Report is the whole listening report. Everything but Sheet is JSON.
type Report struct {
	// Summary is the plain-language reading of the report: the verdict, what to fix and the key numbers. It comes first.
	Summary string `json:"summary"`
	// Profile is what the edit's sound is held to: ProfileProgramme (speech or music, the web's loudness) or ProfileSoundReady (a bed
	// and effects, to which the creator adds a trend sound).
	Profile   string  `json:"profile"`
	DurationS float64 `json:"duration_s"`
	FPS       float64 `json:"fps"`
	HasAudio  bool    `json:"has_audio"`

	// Checklist is the pass/fail list against the targets. Every entry names where its target comes from.
	Checklist []CheckItem `json:"checklist"`

	Loudness Loudness `json:"loudness"`
	Silence  Silence  `json:"silence"`
	Onsets   Onsets   `json:"onsets"`
	Hook     Hook     `json:"hook"`
	Sync     Sync     `json:"sync"`
	Density  Density  `json:"density"`
	Problems Problems `json:"problems"`
	// Bed is the measurement of the ambience bed, in the sound-ready profile.
	Bed *BedStats `json:"bed,omitempty"`

	// Sheet is the PNG: a waveform and a loudness strip with the cuts, the effects, the onsets and the silences, on the contact
	// sheet's time axis. It is not part of the JSON.
	Sheet []byte `json:"-"`
}

// CheckItem is one line of the checklist.
type CheckItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Value is what was measured and Target what it is held to, both as short text with units.
	Value  string `json:"value"`
	Target string `json:"target"`
	// Source says where the target comes from: "earshot: measured on 7 launch films" or "practice: ...".
	Source string `json:"source"`
	// Estimate is true when the target is our estimate and not a measured or standardised number.
	Estimate bool `json:"estimate,omitempty"`
	// Fix is what to do when it does not pass.
	Fix string `json:"fix,omitempty"`
}

// Loudness is the ebur128 measurement.
type Loudness struct {
	IntegratedLUFS float64 `json:"integrated_lufs"`
	TruePeakDBTP   float64 `json:"true_peak_dbtp"`
	RangeLU        float64 `json:"range_lu"`
	// Momentary is the momentary loudness (400 ms window) every 100 ms.
	Momentary []LoudnessPoint `json:"momentary"`
	// EndLU is the level of the last 3 s against the integrated loudness, in LU.
	EndLU float64 `json:"end_lu"`
}

// LoudnessPoint is one point of the momentary curve.
type LoudnessPoint struct {
	TimeS float64 `json:"time_s"`
	LUFS  float64 `json:"lufs"`
}

// Silence lists the silent gaps.
type Silence struct {
	// ThresholdDB is the level below which it is silent.
	ThresholdDB float64 `json:"threshold_db"`
	MinGapS     float64 `json:"min_gap_s"`
	Gaps        []Gap   `json:"gaps"`
	// LongestS is the longest gap, EndS the silence at the very end.
	LongestS float64 `json:"longest_s"`
	EndS     float64 `json:"end_s"`
}

// Gap is one silent stretch.
type Gap struct {
	StartS    float64 `json:"start_s"`
	EndS      float64 `json:"end_s"`
	DurationS float64 `json:"duration_s"`
}

// Onsets are the moments a new sound starts, found from the flux of the energy.
type Onsets struct {
	// PerSecond is the number of onsets per second; SalientPerSecond counts only the big ones (a rise of 12 dB or more).
	PerSecond         float64 `json:"per_second"`
	SalientPerSecond  float64 `json:"salient_per_second"`
	Count             int     `json:"count"`
	SalientCount      int     `json:"salient_count"`
	List              []Onset `json:"list"`
	LongestWithoutS   float64 `json:"longest_without_s"`
	LongestWithoutAtS float64 `json:"longest_without_at_s"`
}

// Onset is one onset: when, and how big the rise was.
type Onset struct {
	TimeMs int64   `json:"time_ms"`
	RiseDB float64 `json:"rise_db"`
}

// Hook is how the sound opens.
type Hook struct {
	// FirstSoundS is when the sound first rises above -50 dBFS.
	FirstSoundS float64 `json:"first_sound_s"`
	// TimeToBodyS is when the momentary loudness first gets within BodyWithinLU of the integrated loudness. The momentary window is
	// 400 ms long, so even a sound that starts at once reads about 0.3 s.
	TimeToBodyS  float64 `json:"time_to_body_s"`
	BodyWithinLU float64 `json:"body_within_lu"`
}

// Sync says how the sound sits on the picture.
type Sync struct {
	FPS  float64   `json:"fps"`
	Cuts []CutSync `json:"cuts"`
	// The shares are of the cuts that have one: with an onset within a frame, and within 100 ms.
	CutsWithinFrameShare float64 `json:"cuts_within_frame_share"`
	CutsWithin100msShare float64 `json:"cuts_within_100ms_share"`
	// ChanceWithin100msShare is what the same cuts would score if they were moved off the picture by fixed offsets: with dense
	// sound almost every point has an onset near it, so a share near the chance level says nothing about sync.
	ChanceWithin100msShare float64 `json:"chance_within_100ms_share"`
	// SFX is the planned effects and where each was heard.
	SFX                 []SFXSync `json:"sfx"`
	SFXWithinFrameShare float64   `json:"sfx_within_frame_share"`
	SFXWithin100msShare float64   `json:"sfx_within_100ms_share"`
	SFXPlanned          int       `json:"sfx_planned"`
	SFXFound            int       `json:"sfx_found"`
}

// CutSync is one cut and the nearest onset.
type CutSync struct {
	AtMs int64 `json:"at_ms"`
	// OnsetMs is the nearest onset (-1: none), DeltaMs onset minus cut.
	OnsetMs int64 `json:"onset_ms"`
	DeltaMs int64 `json:"delta_ms"`
}

// BedStats is the ambience bed as measured in the momentary loudness (sound-ready profile).
type BedStats struct {
	// LevelLUFS is the median momentary loudness over the sound, P10LUFS its 10th percentile (the dips), Coverage the share of the time
	// the momentary loudness is above BedPresentLUFS.
	LevelLUFS float64 `json:"level_lufs"`
	P10LUFS   float64 `json:"p10_lufs"`
	Coverage  float64 `json:"coverage"`
}

// SFXSync is one planned effect and the onset measured near its at_ms.
type SFXSync struct {
	ID      string `json:"id"`
	Sfx     string `json:"sfx"`
	Variant string `json:"variant,omitempty"`
	AtMs    int64  `json:"at_ms"`
	// OnsetMs is the measured onset (-1: none within 250 ms), DeltaMs onset minus at_ms.
	OnsetMs     int64 `json:"onset_ms"`
	DeltaMs     int64 `json:"delta_ms"`
	WithinFrame bool  `json:"within_frame"`
	// Accent is true for the loud designed hits; PeakLUFS is the loudest momentary loudness over the stretch the effect covers (from 200 ms in to 400 ms after its end), and
	// MarginLU how far that is over the bed (sound-ready profile).
	Accent   bool    `json:"accent"`
	PeakLUFS float64 `json:"peak_lufs"`
	MarginLU float64 `json:"margin_lu"`
}

// Density is how busy the sound is.
type Density struct {
	SFXCount       int     `json:"sfx_count"`
	SFXPer10s      float64 `json:"sfx_per_10s"`
	OnsetsPerS     float64 `json:"onsets_per_s"`
	LongestQuietS  float64 `json:"longest_quiet_s"`
	LongestQuietAt float64 `json:"longest_quiet_at_s"`
}

// Problems are defects of the file itself.
type Problems struct {
	// PeakDBFS is the highest sample of the decoded file; Clipping is true when it reaches full scale (or goes over it, which
	// the decoder of a lossy file can do).
	PeakDBFS     float64   `json:"peak_dbfs"`
	PeakCount    int       `json:"peak_count"`
	Clipping     bool      `json:"clipping"`
	DCOffset     float64   `json:"dc_offset"`
	DC           bool      `json:"dc"`
	ChannelRMSDB []float64 `json:"channel_rms_db"`
	// ImbalanceDB is the difference between the loudest and the quietest channel.
	ImbalanceDB float64 `json:"imbalance_db"`
	Imbalance   bool    `json:"imbalance"`
}

// PlannedSFX is a sound effect of the timeline: where it was put, to check where it was heard.
type PlannedSFX struct {
	ID      string `json:"id"`
	Sfx     string `json:"sfx"`
	Variant string `json:"variant,omitempty"`
	AtMs    int64  `json:"at_ms"`
}

// PlannedSFXFromSpec lists the sound effects of a timeline spec, for WithSFX.
func PlannedSFXFromSpec(spec *timeline.Spec) []PlannedSFX {
	var out []PlannedSFX
	if spec == nil {
		return nil
	}
	for _, t := range spec.Tracks {
		at, ok := t.(*timeline.AudioTrack)
		if !ok {
			continue
		}
		for _, it := range at.Items {
			if s, ok := it.(*timeline.SFX); ok {
				out = append(out, PlannedSFX{ID: s.ID, Sfx: s.Sfx, Variant: s.Variant, AtMs: s.AtMs})
			}
		}
	}
	return out
}

// The profiles (P3-12). A programme edit has speech or music and is held to the web's loudness; a sound-ready edit has no programme
// sound (no speech, no music: the clips' sound is a low bed, and the creator adds a trend sound in TikTok) and is held to what that
// export needs instead.
const (
	ProfileProgramme  = "programme"
	ProfileSoundReady = "sound_ready"
)

// ProfileFromSpec chooses the profile of an edit: sound-ready when no sound of the programme is exported (no clip in full mode, no
// licensed track, no creator audio), programme otherwise. A trend sound is cue-only and never counts.
func ProfileFromSpec(spec *timeline.Spec) string {
	if spec == nil {
		return ProfileProgramme
	}
	for _, t := range spec.Tracks {
		switch tr := t.(type) {
		case *timeline.VideoTrack:
			for _, it := range tr.Items {
				// A clip with a speed ramp has its speech muted by the renderer (a bed plays on): it is not a programme.
				if timeline.ClipAudioMode(it.Audio) == timeline.AudioModeFull && !it.HasRamp() {
					return ProfileProgramme
				}
			}
		case *timeline.AudioTrack:
			for _, it := range tr.Items {
				switch it.(type) {
				case *timeline.LicensedTrack, *timeline.CreatorAudio:
					return ProfileProgramme
				}
			}
		}
	}
	return ProfileSoundReady
}

// Option changes how Check works.
type Option func(*config)

type config struct {
	fps     float64
	sfx     []PlannedSFX
	workDir string
	profile string
}

// WithFPS sets the frame rate that "within a frame" is measured against (the plan's FPS). Without it the rate is read from the file.
func WithFPS(fps int) Option { return func(c *config) { c.fps = float64(fps) } }

// WithProfile sets the profile (ProfileProgramme, the default, or ProfileSoundReady).
func WithProfile(p string) Option { return func(c *config) { c.profile = p } }

// WithSpec gives everything the check takes from the timeline: the planned effects, the frame rate and the profile (ProfileFromSpec).
func WithSpec(spec *timeline.Spec) Option {
	return func(c *config) {
		if spec == nil {
			return
		}
		c.sfx = PlannedSFXFromSpec(spec)
		c.fps = float64(spec.Output.FPS)
		c.profile = ProfileFromSpec(spec)
	}
}

// WithSFX gives the planned sound effects, to measure where each is heard and how dense they are.
func WithSFX(items []PlannedSFX) Option { return func(c *config) { c.sfx = items } }

// WithWorkDir is the host directory the sandbox writes its output to (it must be inside the sandbox policy's allowed roots, empty
// and used by this call alone). Without it Check makes a temporary directory under os.TempDir and removes it.
func WithWorkDir(dir string) Option { return func(c *config) { c.workDir = dir } }

// ErrTooLong is returned for a file longer than MaxDurationS: the report is for the short edits the product makes.
var ErrTooLong = errors.New("audiocheck: the file is too long to check")

// MaxDurationS is the longest file Check takes.
const MaxDurationS = 15 * 60

// Check listens to a rendered video: it runs ffmpeg once in the sandbox (loudness, silence, levels and a mono 16 kHz dump of the
// sound), analyses the dump in Go, and returns the report. timeMap is the render's time map (render.Plan.TimeMap): its main-track
// entries give the cuts; entries without a source (text) are ignored. mp4Path is an untrusted file: it is only ever an input of the
// sandbox job.
func Check(ctx context.Context, runner media.Runner, mp4Path string, timeMap []render.TimeMapEntry, opts ...Option) (Report, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	if runner == nil {
		return Report{}, errors.New("audiocheck: no runner")
	}
	dir := cfg.workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "audiocheck-")
		if err != nil {
			return Report{}, fmt.Errorf("audiocheck: %w", err)
		}
		defer func() { _ = os.RemoveAll(d) }()
		dir = d
	}
	abs, err := filepath.Abs(mp4Path)
	if err != nil {
		return Report{}, fmt.Errorf("audiocheck: %w", err)
	}
	m, err := measure(ctx, runner, abs, dir)
	if err != nil {
		return Report{}, err
	}
	return analyse(m, timeMap, cfg), nil
}
