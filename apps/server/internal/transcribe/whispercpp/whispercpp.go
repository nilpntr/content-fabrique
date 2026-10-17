// Package whispercpp is the local transcription provider (D-27): whisper.cpp's whisper-cli and its voice-activity tool, run in the
// media-tools sandbox on CPU. Local, ci and bench use it; the model is large-v3-turbo quantised (a tiny model in CI), pinned by
// checksum and downloaded on first use (transcribe.ModelCache).
//
// Word timing: whisper.cpp's default token timestamps are off by 200 ms or more, so the provider asks for DTW alignment (-dtw, the
// model's alignment heads over the cross-attention), which puts every token at a stable point near the end of the word, and hands
// the words to transcribe.Refine as anchors; Refine moves the boundaries onto the audio.
package whispercpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// Name is the provider's configuration name.
const Name = "whispercpp"

// dtwLagMs is how far before a token's DTW time the middle of the word lies, measured on read speech with large-v3-turbo: the
// attention peaks near the end of the word.
const dtwLagMs = 90

// maxJSONBytes bounds the JSON the tool writes (the sandbox's output is untrusted).
const maxJSONBytes = 64 << 20

// Provider transcribes with whisper-cli.
type Provider struct {
	// Runner runs the tools in the sandbox.
	Runner media.Runner
	// Cache supplies the model files.
	Cache *transcribe.ModelCache
	// Spec is the speech model.
	Spec transcribe.ModelSpec
	// VADModel is the voice-activity model. Zero value: ModelSileroVAD.
	VADModel transcribe.ModelSpec
	// TempDir holds the tool's output directories. It must be inside the sandbox's allowed roots.
	TempDir string
	// Threads is whisper's thread count (the sandbox's CPU limit); zero means 4.
	Threads int
	// WallSecondsPerAudioSecond and ComputeCost price a run as compute (price table: transcription.whisper_cpp_local and compute): the
	// wall time is estimated as this many seconds per second of audio on Threads vCPUs, and ComputeCost turns vCPU milliseconds into
	// micro-dollars and names the price table version. Both unset keeps the old behaviour: free, version "local".
	WallSecondsPerAudioSecond float64
	ComputeCost               func(vcpuMs int64) (micros int64, version string)
}

var (
	_ transcribe.Provider = (*Provider)(nil)
	_ transcribe.Detector = (*Provider)(nil)
)

// Name implements transcribe.Provider.
func (p *Provider) Name() string { return Name }

// Model implements transcribe.Provider. The name is the model's file stem, as the ledger records it.
func (p *Provider) Model() string { return strings.TrimSuffix(p.Spec.File, ".bin") }

// CostMicros implements transcribe.Provider. Local transcription costs compute: the estimated vCPU time at the price table's compute
// price. Without a compute price it is free (version "local").
func (p *Provider) CostMicros(audioMs int64) (int64, string) {
	if p.ComputeCost == nil || p.WallSecondsPerAudioSecond <= 0 {
		return 0, "local"
	}
	_, vcpuMs := p.Quantity(audioMs)
	return p.ComputeCost(vcpuMs)
}

// Quantity says how the ledger row of audioMs of audio is measured: the estimated vCPU milliseconds when the run is priced as
// compute, audio milliseconds otherwise.
func (p *Provider) Quantity(audioMs int64) (unit string, quantity int64) {
	if p.ComputeCost == nil || p.WallSecondsPerAudioSecond <= 0 {
		return "audio_ms", audioMs
	}
	threads := int64(p.Threads)
	if threads <= 0 {
		threads = 4
	}
	return "vcpu_ms", int64(float64(audioMs)*p.WallSecondsPerAudioSecond+0.5) * threads
}

// MaxDuration is the longest a transcription of audioMs of audio can take (the limit whisper-cli runs under), for the lifetime of the
// reservation in the database.
func (p *Provider) MaxDuration(audioMs int64) time.Duration {
	return Timeout(time.Duration(audioMs)*time.Millisecond) + VADTimeout(time.Duration(audioMs)*time.Millisecond)
}

func (p *Provider) threads() string {
	if p.Threads > 0 {
		return strconv.Itoa(p.Threads)
	}
	return "4"
}

// Timeout is how long whisper-cli may run on audio of the given length: five times real time plus three minutes, at most three hours.
func Timeout(d time.Duration) time.Duration { return min(5*d+3*time.Minute, 3*time.Hour) }

// VADTimeout is the limit of the voice-activity run.
func VADTimeout(d time.Duration) time.Duration { return min(d/2+time.Minute, 30*time.Minute) }

// Transcribe implements transcribe.Provider.
func (p *Provider) Transcribe(ctx context.Context, audio transcribe.Audio, opts transcribe.Options) (transcribe.Transcript, error) {
	model, err := p.Cache.Ensure(ctx, p.Spec)
	if err != nil {
		return transcribe.Transcript{}, err
	}
	lang := strings.ToLower(strings.TrimSpace(opts.Language))
	switch {
	case lang == "":
		lang = "auto"
	case p.Spec.EnglishOnly && lang != "en":
		return transcribe.Transcript{}, fmt.Errorf("whispercpp: model %s is English only, cannot transcribe %q", p.Spec.Name, lang)
	case !langRE.MatchString(lang):
		return transcribe.Transcript{}, fmt.Errorf("whispercpp: invalid language %q", lang)
	}
	if p.Spec.EnglishOnly {
		lang = "en"
	}
	out, err := os.MkdirTemp(p.TempDir, "whisper-")
	if err != nil {
		return transcribe.Transcript{}, err
	}
	defer func() { _ = os.RemoveAll(out) }()
	if err := os.Chmod(out, 0o777); err != nil { // #nosec G302 -- the container user may be another uid; the directory is ours alone
		return transcribe.Transcript{}, err
	}

	args := []string{"whisper-cli", "-m", media.InputPath(0), "-f", media.InputPath(1), "-ojf", "-of", media.OutputPath("out"),
		"-l", lang, "-np", "-t", p.threads(), "-nfa"}
	if p.Spec.DTW != "" {
		args = append(args, "-dtw", p.Spec.DTW)
	}
	res, err := p.Runner.Run(ctx, media.Invocation{
		Args:      args,
		Inputs:    []media.Input{{Host: model, Container: media.InputPath(0)}, {Host: audio.Path, Container: media.InputPath(1)}},
		OutputDir: out,
		Timeout:   Timeout(time.Duration(audio.DurationMs) * time.Millisecond),
	})
	if err != nil {
		return transcribe.Transcript{}, fmt.Errorf("whisper-cli: %w", err)
	}
	if res.ExitCode != 0 {
		return transcribe.Transcript{}, &media.ToolError{Tool: "whisper-cli", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	path := filepath.Join(out, "out.json")
	fi, err := os.Lstat(path)
	if err != nil {
		return transcribe.Transcript{}, fmt.Errorf("whisper-cli wrote no output: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxJSONBytes {
		return transcribe.Transcript{}, errors.New("whisper-cli wrote an output that is not a regular file of a sane size")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a fixed name inside our own output directory
	if err != nil {
		return transcribe.Transcript{}, err
	}
	tr, err := ParseJSON(raw)
	if err != nil {
		return transcribe.Transcript{Calls: []transcribe.Call{{AudioMs: audio.DurationMs}}}, err
	}
	tr.Calls = []transcribe.Call{{AudioMs: audio.DurationMs}}
	return tr, nil
}

var langRE = regexp.MustCompile(`^[a-z]{2,3}$`)

// fullJSON is the part of whisper-cli's --output-json-full that is read.
type fullJSON struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Tokens []token `json:"tokens"`
	} `json:"transcription"`
}

type token struct {
	Text    string `json:"text"`
	Offsets struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"offsets"`
	P    float64 `json:"p"`
	TDTW int64   `json:"t_dtw"` // centiseconds; -1 when the model has no alignment heads
}

var (
	// annotations are what whisper writes for sounds: [MUSIC], (applause), ♪.
	annotation = regexp.MustCompile(`^(\[[^\]]*\]|\([^)]*\)|[♪♫\s]+)$`)
	special    = regexp.MustCompile(`^\[_[A-Z]+_?[0-9]*\]$`)
)

// ParseJSON turns whisper-cli's --output-json-full into words. Tokens are joined into words (a token that starts with a space
// starts a word; punctuation and word pieces stick to the word before), sound annotations are dropped, and each word gets an
// anchor from its last token's DTW time (or, without DTW, the middle of its token span) with a provisional start and end halfway
// to its neighbours, which transcribe.Refine replaces.
func ParseJSON(raw []byte) (transcribe.Transcript, error) {
	var j fullJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return transcribe.Transcript{}, fmt.Errorf("whisper-cli output: %w", err)
	}
	type acc struct {
		text         string
		first, last  token
		pSum         float64
		n            int
		hasDTW       bool
		fromMs, toMs int64
	}
	var words []*acc
	for _, seg := range j.Transcription {
		for _, t := range seg.Tokens {
			if special.MatchString(t.Text) {
				continue
			}
			if t.Text == "" {
				continue
			}
			if strings.HasPrefix(t.Text, " ") || len(words) == 0 {
				words = append(words, &acc{text: strings.TrimLeft(t.Text, " "), first: t, last: t, pSum: t.P, n: 1, hasDTW: t.TDTW >= 0, fromMs: t.Offsets.From, toMs: t.Offsets.To})
				continue
			}
			w := words[len(words)-1]
			w.text += t.Text
			w.last = t
			w.pSum += t.P
			w.n++
			w.hasDTW = w.hasDTW && t.TDTW >= 0
			w.toMs = t.Offsets.To
		}
	}
	tr := transcribe.Transcript{Language: j.Result.Language}
	for _, w := range words {
		text := strings.TrimSpace(w.text)
		if text == "" || annotation.MatchString(text) {
			continue
		}
		var anchor int64
		if w.hasDTW && w.last.TDTW >= 0 {
			anchor = w.last.TDTW*10 - dtwLagMs
		} else {
			anchor = (w.fromMs + w.toMs) / 2
		}
		anchor = max(anchor, 1)
		tr.Words = append(tr.Words, transcribe.Word{
			Text: text, StartMs: anchor, EndMs: anchor, Confidence: math.Round(w.pSum/float64(w.n)*1000) / 1000, AnchorMs: anchor,
		})
	}
	// Anchors must increase; a model that repeats itself can step back.
	for i := 1; i < len(tr.Words); i++ {
		if tr.Words[i].AnchorMs <= tr.Words[i-1].AnchorMs {
			tr.Words[i].AnchorMs = tr.Words[i-1].AnchorMs + 10
		}
	}
	// Provisional spans: halfway to the neighbours, so an unrefined transcript still tiles the speech.
	for i := range tr.Words {
		a := tr.Words[i].AnchorMs
		lo, hi := max(a-250, 0), a+250
		if i > 0 {
			lo = (tr.Words[i-1].AnchorMs + a) / 2
		}
		if i+1 < len(tr.Words) {
			hi = (a + tr.Words[i+1].AnchorMs) / 2
		}
		tr.Words[i].StartMs, tr.Words[i].EndMs = lo, hi
	}
	return tr, nil
}

var segmentRE = regexp.MustCompile(`Speech segment \d+: start = ([0-9.]+), end = ([0-9.]+)`)

// DetectSpeech implements transcribe.Detector with whisper.cpp's Silero voice-activity model: a few milliseconds of CPU per second
// of audio.
func (p *Provider) DetectSpeech(ctx context.Context, audio transcribe.Audio) ([]transcribe.Segment, error) {
	spec := p.VADModel
	if spec.File == "" {
		spec = transcribe.ModelSileroVAD
	}
	model, err := p.Cache.Ensure(ctx, spec)
	if err != nil {
		return nil, err
	}
	res, err := p.Runner.Run(ctx, media.Invocation{
		Args:    []string{"whisper-vad-speech-segments", "-vm", media.InputPath(0), "-f", media.InputPath(1), "-t", p.threads(), "-np"},
		Inputs:  []media.Input{{Host: model, Container: media.InputPath(0)}, {Host: audio.Path, Container: media.InputPath(1)}},
		Timeout: VADTimeout(time.Duration(audio.DurationMs) * time.Millisecond),
	})
	if err != nil {
		return nil, fmt.Errorf("whisper-vad-speech-segments: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, &media.ToolError{Tool: "whisper-vad-speech-segments", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	return ParseSegments(string(res.Stdout))
}

// ParseSegments reads the "Speech segment N: start = S, end = E" lines of the tool (centiseconds: the tool's own usage text says so, and its "%.2f" formatting hides it).
func ParseSegments(out string) ([]transcribe.Segment, error) {
	segs := []transcribe.Segment{}
	for _, m := range segmentRE.FindAllStringSubmatch(out, -1) {
		a, err1 := strconv.ParseFloat(m[1], 64)
		b, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil || b < a || a < 0 {
			return nil, fmt.Errorf("whisper-vad-speech-segments: cannot read %q", m[0])
		}
		segs = append(segs, transcribe.Segment{StartMs: int64(math.Round(a * 10)), EndMs: int64(math.Round(b * 10))})
	}
	if len(segs) == 0 && !strings.Contains(out, "Detected 0 speech segments") {
		return nil, fmt.Errorf("whisper-vad-speech-segments: no segment list in its output: %q", media.StderrSnippet([]byte(out)))
	}
	return segs, nil
}
