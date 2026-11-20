package ingest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The footage check: what a creator's file is good for, in plain words. It answers, before (or instead of) an upload, the
// question the ingest answers by succeeding or failing, and adds what the ingest copes with silently but the creator should
// know (an HDR clip is converted, a landscape clip is cropped, a song that is silent). Every outcome is one of three
// verdicts with reasons a non-developer can act on; nothing in a reason names a codec, a tool or a stream.
//
// Assess is the pure part: it judges a probe plus what a decode measured. Check runs the tools (through a media.Runner, so
// in production inside the sandbox) and calls Assess. `cfctl footage check` prints the result.

// Verdict is how usable a file is.
type Verdict string

// The verdicts, from best to worst.
const (
	Usable            Verdict = "usable"
	UsableWithWarning Verdict = "usable_with_warning"
	Unusable          Verdict = "unusable"
)

func (v Verdict) rank() int {
	switch v {
	case UsableWithWarning:
		return 1
	case Unusable:
		return 2
	}
	return 0
}

// Label is the verdict as a creator reads it.
func (v Verdict) Label() string {
	switch v {
	case UsableWithWarning:
		return "usable, with a warning"
	case Unusable:
		return "not usable"
	}
	return "usable"
}

// Use is what the file is meant to be in an edit.
type Use string

// The uses: a clip of footage, or the music of the edit (a song file, or the sound of a video such as a screen recording).
const (
	UseClip  Use = "clip"
	UseMusic Use = "music"
)

// Finding is one thing the check noticed. Level Usable is a note: the pipeline handles it, the creator may like to know.
type Finding struct {
	// Code is a stable name for the case, for tests and tools; never shown as the reason.
	Code    string  `json:"code"`
	Level   Verdict `json:"level"`
	Message string  `json:"message"`
}

// The finding codes.
const (
	CodeEmpty           = "empty_file"
	CodeUnreadable      = "unreadable"
	CodeCorrupt         = "corrupt"
	CodeNoPicture       = "no_picture"
	CodeUndecodable     = "undecodable_picture"
	CodeNoDuration      = "no_duration"
	CodeTooLong         = "too_long"
	CodeLong            = "long"
	CodeTooShort        = "too_short"
	CodeShort           = "very_short"
	CodeSize            = "picture_size"
	CodeCutOff          = "cut_off"
	CodeDamagedFrames   = "damaged_frames"
	CodeVariableRate    = "variable_frame_rate"
	CodeSlowMotion      = "slow_motion"
	CodeHLG             = "hdr_hlg"
	CodePQ              = "hdr_pq"
	CodeRotated         = "rotated"
	CodeLandscape       = "landscape"
	CodeSquare          = "square"
	CodeExtremeShape    = "extreme_shape"
	CodeNoSound         = "no_sound"
	CodeSilent          = "silent"
	CodeSoundUnreadable = "sound_unreadable"
	CodeLowSampleRate   = "low_sample_rate"
	CodeMono            = "mono"
	CodeFromVideo       = "sound_from_video"
	CodeScreenRecording = "screen_recording"
	CodeDuplicate       = "duplicate"
)

// Assessment is the check's answer for one file.
type Assessment struct {
	Use      Use       `json:"use,omitempty"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings"`
	// DurationMs is the probed length (0 when the file could not be read), PlayableMs how much of the picture decoded (0 when
	// no decode ran).
	DurationMs int64 `json:"durationMs,omitempty"`
	PlayableMs int64 `json:"playableMs,omitempty"`
}

// Add records a finding and lowers the verdict to its level when that is worse.
func (a *Assessment) Add(f Finding) {
	a.Findings = append(a.Findings, f)
	if f.Level.rank() > a.Verdict.rank() {
		a.Verdict = f.Level
	}
}

// Has reports whether a finding with the code was recorded.
func (a Assessment) Has(code string) bool {
	return slices.ContainsFunc(a.Findings, func(f Finding) bool { return f.Code == code })
}

// Measured is what a decode of the file found, on top of the probe. The zero value means nothing was measured, and Assess
// then judges the probe alone.
type Measured struct {
	// Decoded is true when the whole picture was decoded; PlayableMs is then how much of it decoded and Damaged whether the
	// decoder reported errors on the way.
	Decoded    bool
	PlayableMs int64
	Damaged    bool
	// SoundChecked is true when the selected sound track was extracted; SoundUnreadable when that failed, else Peak is its
	// loudest sample (0 to 1, full scale).
	SoundChecked    bool
	SoundUnreadable bool
	Peak            float64
	// SoundMs is how long the extracted sound is. A clip plays as long as its longer stream (so does its proxy): sound
	// that outlasts a picture which decoded to its end is not a cut-off clip.
	SoundMs int64
}

// Thresholds of the check.
const (
	// SilentPeak is the loudest sample (about -60 dBFS) at or under which a sound track counts as silent. The waveform's
	// peaks are rounded to 0.001, so the comparison includes it.
	SilentPeak = 0.001
	// TooShortMs is the length under which a clip cannot show anything (about two frames); ShortMs the length under which
	// it can only be a flash.
	TooShortMs = 100
	ShortMs    = 1000
	// LongClip is the length above which the creator is told that preparing the clip takes a while.
	LongClip = 10 * time.Minute
	// LowSampleRate is the sample rate under which music sounds dull (telephone and old voice-memo rates).
	LowSampleRate = 32000
)

// failureFinding is the plain reason for the ingest's failure codes, in the words of the web library's.
func failureFinding(reason string, maxDuration time.Duration) Finding {
	f := Finding{Level: Unusable}
	switch reason {
	case store.FailureUnsupportedFormat:
		f.Code, f.Message = CodeUnreadable, "This file can't be opened. It may not be a video or a sound file, or its format is not supported. Try exporting it again as MP4 or MOV."
	case store.FailureCorruptMedia:
		f.Code, f.Message = CodeCorrupt, "The file is damaged or was cut off. Try exporting or sending it again."
	case store.FailureNoVideo:
		f.Code, f.Message = CodeNoPicture, "There is no video in this file. If it is a song, add it as music instead."
	case store.FailureUndecodableVideo:
		f.Code, f.Message = CodeUndecodable, "The video in this file can't be read. Try exporting it again from your phone or editing app."
	case store.FailureNoDuration:
		f.Code, f.Message = CodeNoDuration, "The length of this file can't be read, which usually means it is damaged. Try exporting it again."
	case store.FailureTooLong:
		f.Code, f.Message = CodeTooLong, fmt.Sprintf("This file is longer than %s, the most it can be. Cut it into shorter parts first.", minutes(maxDuration))
	case store.FailureResolution:
		f.Code, f.Message = CodeSize, "The picture of this clip is too small or too big, or it has too many frames per second, to be used."
	case store.FailureNoAudio:
		f.Code, f.Message = CodeNoSound, "There is no sound in this file, so it can't be used as music."
	default:
		f.Code, f.Message = CodeUnreadable, "This file can't be used. Try exporting it again."
	}
	return f
}

// FailureAssessment is the assessment of a file the ingest refuses with the given reason (a store.Failure code).
func FailureAssessment(use Use, reason string, maxDuration time.Duration) Assessment {
	a := Assessment{Use: use, Verdict: Usable}
	a.Add(failureFinding(reason, maxDuration))
	return a
}

// DuplicateFinding is the warning for a file that is byte for byte the same as another one, named other.
func DuplicateFinding(other string) Finding {
	return Finding{Code: CodeDuplicate, Level: UsableWithWarning,
		Message: fmt.Sprintf("This is the same file as %s. Adding it twice only gives the same shots twice.", other)}
}

// Assess judges a probed file for a use. maxDuration is the ingest's limit (zero means DefaultMaxDuration).
func Assess(use Use, m media.Metadata, x Measured, maxDuration time.Duration) Assessment {
	if maxDuration <= 0 {
		maxDuration = DefaultMaxDuration
	}
	kind := store.AssetKindVideo
	if use == UseMusic {
		kind = store.AssetKindAudio
	}
	a := Assessment{Use: use, Verdict: Usable, DurationMs: m.DurationMs, PlayableMs: x.PlayableMs}
	if reason, _ := Validate(kind, m, maxDuration); reason != "" {
		a.Add(failureFinding(reason, maxDuration))
		return a
	}
	if use == UseMusic {
		assessMusic(&a, m, x)
	} else {
		assessClip(&a, m, x)
	}
	return a
}

func assessClip(a *Assessment, m media.Metadata, x Measured) {
	dur := m.DurationMs
	if x.Decoded {
		x.PlayableMs = max(x.PlayableMs, x.SoundMs)
		a.PlayableMs = x.PlayableMs
		switch {
		case x.PlayableMs < TooShortMs && m.DurationMs >= TooShortMs:
			// almost nothing of a longer clip plays: the ingest fails it as damaged (checkProxy)
			a.Add(failureFinding(store.FailureCorruptMedia, 0))
			return
		case playableMs(m.DurationMs, x.PlayableMs) != m.DurationMs:
			dur = x.PlayableMs
			a.Add(Finding{Code: CodeCutOff, Level: UsableWithWarning, Message: fmt.Sprintf(
				"This clip is cut off: only the first %s of %s play. Only that part will be used; export or send it again to get all of it.",
				secondsText(x.PlayableMs), secondsText(m.DurationMs))})
		case x.Damaged:
			a.Add(Finding{Code: CodeDamagedFrames, Level: UsableWithWarning,
				Message: "A few frames of this clip are damaged. It will mostly look fine, but a short glitch may show."})
		}
	}
	switch {
	case dur < TooShortMs:
		a.Add(Finding{Code: CodeTooShort, Level: Unusable, Message: "This clip is too short to show anything (under a tenth of a second)."})
		return
	case dur < ShortMs:
		a.Add(Finding{Code: CodeShort, Level: UsableWithWarning, Message: "This clip is under a second long, so it can only appear as a quick flash."})
	case time.Duration(dur)*time.Millisecond > LongClip:
		a.Add(Finding{Code: CodeLong, Level: UsableWithWarning, Message: fmt.Sprintf(
			"This is a long clip (%s). It works, but it takes a while to prepare; trimming it to the part you need is faster.", minutes(time.Duration(dur)*time.Millisecond))})
	}

	switch m.HDRFormat {
	case "hlg":
		a.Add(Finding{Code: CodeHLG, Level: Usable, Message: "This clip was filmed in HDR. It is converted for normal screens, so its colours can look a little flatter than on your phone."})
	case "pq":
		a.Add(Finding{Code: CodePQ, Level: UsableWithWarning, Message: "This clip was filmed in a bright HDR format. It is converted for normal screens, but its colours may look paler than on your phone. If you can, export it as a normal (SDR) video."})
	}
	switch {
	case m.SlowMotion():
		a.Add(Finding{Code: CodeSlowMotion, Level: Usable, Message: "This clip was filmed in slow motion."})
	case m.VariableFrameRate():
		a.Add(Finding{Code: CodeVariableRate, Level: Usable, Message: "The number of frames per second changes while this clip plays, which is normal for phone video. It is evened out, so it plays smoothly."})
	}
	if m.ClockwiseDegrees() != 0 {
		a.Add(Finding{Code: CodeRotated, Level: Usable, Message: "This clip was saved sideways or upside down. It is turned the right way up."})
	}
	assessShape(a, m)
	if looksLikeScreenRecording(m) {
		a.Add(Finding{Code: CodeScreenRecording, Level: Usable, Message: "This looks like a screen recording rather than camera footage."})
	}

	switch {
	case m.SelectedAudio == nil:
		a.Add(Finding{Code: CodeNoSound, Level: Usable, Message: "This clip has no sound. That is fine under music, but it can't be used for talking parts or captions."})
	case x.SoundChecked && x.SoundUnreadable:
		a.Add(Finding{Code: CodeSoundUnreadable, Level: UsableWithWarning, Message: "The sound of this clip can't be read, so the clip will be used without its sound."})
	case x.SoundChecked && x.Peak <= SilentPeak:
		a.Add(Finding{Code: CodeSilent, Level: Usable, Message: "The sound of this clip is silent. That is fine under music, but it can't be used for talking parts or captions."})
	}
}

// assessShape warns about a picture that the vertical edit will crop a lot of.
func assessShape(a *Assessment, m media.Metadata) {
	w, h := float64(m.Width), float64(m.Height)
	if w <= 0 || h <= 0 {
		return
	}
	r := w / h
	switch {
	case r >= 4 || r <= 0.25:
		a.Add(Finding{Code: CodeExtremeShape, Level: UsableWithWarning, Message: "This clip is a very thin strip. A vertical video has to cut most of it away or show it very small."})
	case r > 1.05:
		a.Add(Finding{Code: CodeLandscape, Level: UsableWithWarning, Message: "This clip is landscape (wider than tall). In a vertical video its sides are cut off, so keep the action in the middle."})
	case r >= 0.8:
		a.Add(Finding{Code: CodeSquare, Level: Usable, Message: "This clip is about square. In a vertical video a little of its sides is cut off."})
	}
}

func assessMusic(a *Assessment, m media.Metadata, x Measured) {
	switch {
	case x.SoundChecked && x.SoundUnreadable:
		a.Add(Finding{Code: CodeSoundUnreadable, Level: Unusable, Message: "The sound in this file can't be read, so it can't be used as music. Try exporting it again."})
		return
	case x.SoundChecked && x.Peak <= SilentPeak:
		a.Add(Finding{Code: CodeSilent, Level: Unusable, Message: "The sound in this file is silent, so it can't be used as music."})
		return
	}
	switch {
	case m.DurationMs < TooShortMs:
		a.Add(Finding{Code: CodeTooShort, Level: Unusable, Message: "This sound is too short to be music (under a tenth of a second)."})
		return
	case m.DurationMs < 5000:
		a.Add(Finding{Code: CodeShort, Level: UsableWithWarning, Message: "This sound is only a few seconds long, so it will loop or end early under an edit."})
	}
	if m.HasVideo {
		if looksLikeScreenRecording(m) {
			a.Add(Finding{Code: CodeScreenRecording, Level: UsableWithWarning, Message: "This looks like a screen recording. Only its sound is used as music; check that no notification sounds, taps or talking are in it."})
		} else {
			a.Add(Finding{Code: CodeFromVideo, Level: UsableWithWarning, Message: "This is a video. Only its sound is used as music; check that nothing but the song can be heard in it."})
		}
	}
	if s, ok := m.SelectedAudioStream(); ok {
		if s.SampleRate > 0 && s.SampleRate < LowSampleRate {
			a.Add(Finding{Code: CodeLowSampleRate, Level: UsableWithWarning, Message: "The sound quality of this file is low (like a phone call), so the music can sound dull. Use a better copy of the song if you have one."})
		}
		if s.Channels == 1 {
			a.Add(Finding{Code: CodeMono, Level: Usable, Message: "This song is in mono. It works, but it sounds less wide than a stereo copy."})
		}
	}
}

// looksLikeScreenRecording guesses from the picture's shape: a phone screen is about 19.5 by 9, which no camera mode
// records (cameras film 16:9, 4:3 or square). A clip cropped to that shape in an app counts too; the guess only changes a
// note's wording.
func looksLikeScreenRecording(m media.Metadata) bool {
	if !m.HasVideo || m.Width <= 0 || m.Height <= 0 {
		return false
	}
	r := float64(min(m.Width, m.Height)) / float64(max(m.Width, m.Height))
	return r >= 0.43 && r <= 0.475
}

func secondsText(ms int64) string {
	s := float64(ms) / 1000
	if s < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", s), ".0") + " seconds"
	}
	return fmt.Sprintf("%d seconds", int64(math.Round(s)))
}

func minutes(d time.Duration) string {
	n := int(math.Round(d.Minutes()))
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}

// ---- Running the check ---------------------------------------------------------------------------------------------------

// CheckOptions configure Check.
type CheckOptions struct {
	// Use is what the file is for; empty picks UseClip for a file with a picture and UseMusic for one without.
	Use Use
	// WorkDir is a directory the runner may write into (inside the sandbox provider's allowed roots, no symlinks in its
	// path); Check makes and removes its own directories in it.
	WorkDir string
	// MaxDuration is the ingest's limit; zero means DefaultMaxDuration.
	MaxDuration time.Duration
}

// Check probes the file at path, decodes its picture all the way through and reads its sound, all through runner, and
// judges it. The error is only for a check that could not run (the sandbox, the disk, a cancelled context, a tool that
// was killed); a bad file is an Assessment with the verdict Unusable. A sandbox that cannot run the tools, or runs them
// without access to their files, is an error that sandbox.Unavailable recognises, never a verdict on the file.
func Check(ctx context.Context, runner media.Runner, path string, o CheckOptions) (Assessment, error) {
	if !filepath.IsAbs(o.WorkDir) {
		return Assessment{}, errors.New("ingest: the footage check needs an absolute work directory")
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = DefaultMaxDuration
	}
	use := o.Use
	fi, err := os.Stat(path)
	if err != nil {
		return Assessment{}, err
	}
	if fi.Size() == 0 {
		a := Assessment{Use: use, Verdict: Usable}
		a.Add(Finding{Code: CodeEmpty, Level: Unusable, Message: "This file is empty. Try exporting or sending it again."})
		return a, nil
	}

	m, err := media.Probe(ctx, runner, path)
	var te *media.ToolError
	switch {
	case errors.As(err, &te) && killedExit(te.ExitCode):
		return Assessment{}, fmt.Errorf("probe: %w (exit code %d)", errKilled, te.ExitCode)
	case errors.As(err, &te):
		return FailureAssessment(use, classifyToolError(te, store.FailureUnsupportedFormat), o.MaxDuration), nil
	case errors.Is(err, media.ErrUnparsable):
		return FailureAssessment(use, store.FailureCorruptMedia, o.MaxDuration), nil
	case err != nil:
		return Assessment{}, fmt.Errorf("probe: %w", err)
	}
	if use == "" {
		use = UseClip
		if !m.HasVideo {
			use = UseMusic
		}
	}
	// A file the ingest refuses needs no decoding to say so.
	if a := Assess(use, m, Measured{}, o.MaxDuration); a.Verdict == Unusable {
		return a, nil
	}

	dur := time.Duration(m.DurationMs) * time.Millisecond
	var x Measured
	if use == UseClip {
		if err := decodeAll(ctx, runner, o.WorkDir, path, m, dur, &x); err != nil {
			return Assessment{}, err
		}
	}
	if m.SelectedAudio != nil {
		if err := measureSound(ctx, runner, o.WorkDir, path, m, dur, &x); err != nil {
			return Assessment{}, err
		}
	}
	return Assess(use, m, x, o.MaxDuration), nil
}

// decodeAll runs media.FullDecodeArgs. A decoder that gives up counts as nothing playable; one that complains on the way as
// damage.
func decodeAll(ctx context.Context, runner media.Runner, dir, path string, m media.Metadata, dur time.Duration, x *Measured) error {
	args, err := media.FullDecodeArgs(media.InputPath(0), m)
	if err != nil {
		return err
	}
	out, res, err := execTool(ctx, runner, dir, HeavyTimeout(dur), args, path)
	_ = os.RemoveAll(out)
	x.Decoded = true
	switch {
	case toolFailed(err):
		x.PlayableMs, _ = media.DecodedMs(res.Stdout) // whatever decoded before the decoder gave up
		x.Damaged = true
		return nil
	case err != nil:
		return err
	}
	x.PlayableMs, _ = media.DecodedMs(res.Stdout)
	x.Damaged = len(strings.TrimSpace(string(res.Stderr))) > 0
	return nil
}

// measureSound extracts the selected track as the ingest does (16 kHz mono) and reads its loudest sample.
func measureSound(ctx context.Context, runner media.Runner, dir, path string, m media.Metadata, dur time.Duration, x *Measured) error {
	args, err := media.Audio16kArgs(media.InputPath(0), media.OutputPath(fileAudio16k), m)
	if err != nil {
		return err
	}
	out, _, err := execTool(ctx, runner, dir, LightTimeout(dur), args, path)
	defer func() { _ = os.RemoveAll(out) }()
	x.SoundChecked = true
	switch {
	case toolFailed(err):
		x.SoundUnreadable = true
		return nil
	case err != nil:
		return err
	}
	wav := filepath.Join(out, fileAudio16k)
	fi, err := os.Lstat(wav)
	if errors.Is(err, os.ErrNotExist) {
		// ffmpeg exited 0 but wrote nothing: it always creates its output once it starts, so this is the sandbox (an output
		// directory it cannot write), not the sound.
		return &sandbox.UnavailableError{Reason: "the tool in the sandbox exited cleanly but wrote no output (can it write its output directory?)", Err: errNoOutput}
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxVideoFile {
		x.SoundUnreadable = true
		return nil
	}
	wf, err := ComputeWaveform(wav, media.WaveformPoints)
	if err != nil {
		x.SoundUnreadable = true
		return nil
	}
	for _, p := range wf.Peaks {
		x.Peak = max(x.Peak, p)
	}
	x.SoundMs = wf.DurationMs
	return nil
}
