package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
	"github.com/nilpntr/content-fabrique/internal/transcribe/whispercpp"
)

const transcribeUsage = `Usage:
  cfctl transcribe file <audio.wav> [flags]
  cfctl transcribe models [flags]

file runs the transcription pipeline's steps on one 16 kHz mono WAV file (the audio_16k.wav derivative; make one with
ffmpeg -i in.mp4 -ar 16000 -ac 1 -c:a pcm_s16le out.wav) the way the worker does: whisper.cpp's voice-activity check, then
whisper-cli with word alignment, both in the media-tools sandbox, then Refine (the words placed on the audio) and the speech
structure. It prints one JSON document: the language, the speech segments, the words as the model placed them (raw_words) and
after refinement (words), the structure and the timings. No database, no object storage. scripts/transcribe-eval/measure.py uses it
to measure word timing against clips with known timings.

models downloads the speech model and the voice-activity model into the model cache and checks their SHA-256, so the first
transcription does not wait for 574 MB.

  --model       large-v3-turbo-q5_0 (default) or tiny.en
  --models-dir  the model cache (default $WHISPER_MODELS_DIR, or <system temp dir>/cf-media/whisper-models)
  --image       the media-tools image (default $MEDIA_TOOLS_IMAGE, or media-tools:dev)
  --provider    docker (default), hardened, or local (whisper-cli on this machine's PATH, no isolation)
  --language    an ISO 639-1 code; default: detect
  --cpus        CPUs for the sandbox and whisper's threads (default 4)
  --docker      the docker CLI (default docker)
`

func cmdTranscribe(args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(stdout, transcribeUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	if sub != "file" && sub != "models" {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: unknown subcommand %q\n\n%s", sub, transcribeUsage)
		return 2
	}
	env := func(k, def string) string {
		if v, ok := lookupEnv(k); ok && v != "" {
			return v
		}
		return def
	}
	fs := flag.NewFlagSet("transcribe "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", env("TRANSCRIBE_MODEL", "large-v3-turbo-q5_0"), "")
	modelsDir := fs.String("models-dir", env("WHISPER_MODELS_DIR", filepath.Join(os.TempDir(), "cf-media", "whisper-models")), "")
	image := fs.String("image", env("MEDIA_TOOLS_IMAGE", "media-tools:dev"), "")
	providerName := fs.String("provider", "docker", "")
	language := fs.String("language", "", "")
	cpus := fs.Int("cpus", 4, "")
	dockerBin := fs.String("docker", "docker", "")
	rest := args[1:]
	var file string
	if sub == "file" {
		if len(rest) == 0 || len(rest[0]) == 0 || rest[0][0] == '-' {
			_, _ = fmt.Fprintf(stderr, "cfctl transcribe file: an audio file is required\n\n%s", transcribeUsage)
			return 2
		}
		file, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	spec, ok := transcribe.SpeechModel(*model)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: unknown model %q (use large-v3-turbo-q5_0 or tiny.en)\n", *model)
		return 2
	}
	if !filepath.IsAbs(*modelsDir) {
		abs, err := filepath.Abs(*modelsDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
			return 1
		}
		*modelsDir = abs
	}
	if err := os.MkdirAll(*modelsDir, 0o755); err != nil { // #nosec G301 G703 -- the operator's own directory; the sandbox's user must be able to read the models
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	models, err := filepath.EvalSymlinks(*modelsDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	cache := &transcribe.ModelCache{Dir: models}

	if sub == "models" {
		for _, m := range []transcribe.ModelSpec{spec, transcribe.ModelSileroVAD} {
			t0 := time.Now()
			p, err := cache.Ensure(ctx, m)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %s: %v\n", m.Name, err)
				return 1
			}
			_, _ = fmt.Fprintf(stdout, "%s: %s (sha256 %s ok, %s)\n", m.Name, p, m.SHA256, time.Since(t0).Round(time.Millisecond))
		}
		return 0
	}

	work, err := os.MkdirTemp("", "cf-transcribe-")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(work) }()
	work, _ = filepath.EvalSymlinks(work)
	staged := filepath.Join(work, "audio.wav")
	if err := copyFile(file, staged); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	pol := sandbox.Policy{AllowedRoots: []string{work, models}}
	var sb sandbox.Provider
	switch *providerName {
	case "docker":
		d := sandbox.NewDocker(pol)
		d.Binary = *dockerBin
		sb = d
	case "hardened":
		h := sandbox.NewHardened(pol)
		h.Binary = *dockerBin
		sb = h
	case "local":
		e, _ := lookupEnv("CF_ENV")
		if err := sandbox.CheckLocalAllowed(e); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
			return 1
		}
		sb = sandbox.NewLocal(pol)
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: unknown provider %q (use docker, hardened or local)\n", *providerName)
		return 2
	}
	out := filepath.Join(work, "tmp")
	if err := os.Mkdir(out, 0o750); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	p := &whispercpp.Provider{
		Runner: &media.SandboxRunner{Provider: sb, Image: *image, Limits: sandbox.Limits{CPU: float64(*cpus), MemoryMB: 4096, Timeout: 10 * time.Minute, PIDs: 512}},
		Cache:  cache, Spec: spec, VADModel: transcribe.ModelSileroVAD, TempDir: out, Threads: *cpus,
	}
	res, err := transcribeFile(ctx, p, staged, *language)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl transcribe: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(res); err != nil {
		return 1
	}
	return 0
}

// transcribeFileResult is the document `cfctl transcribe file` prints.
type transcribeFileResult struct {
	Provider       string               `json:"provider"`
	Model          string               `json:"model"`
	Language       string               `json:"language"`
	AudioMs        int64                `json:"audio_ms"`
	SpeechMs       int64                `json:"speech_ms"`
	Speech         bool                 `json:"speech"`
	Segments       []transcribe.Segment `json:"speech_segments"`
	RawWords       []transcribe.Word    `json:"raw_words"`
	Words          []transcribe.Word    `json:"words"`
	Structure      transcribe.Structure `json:"structure"`
	VADMs          int64                `json:"vad_ms"`
	TranscribeMs   int64                `json:"transcribe_ms"`
	RealtimeFactor float64              `json:"realtime_factor"`
}

// transcribeFile runs the pipeline's steps without the database.
func transcribeFile(ctx context.Context, p *whispercpp.Provider, path, language string) (transcribeFileResult, error) {
	pcm, err := transcribe.ReadWAV(path)
	if err != nil {
		return transcribeFileResult{}, err
	}
	audio := transcribe.Audio{Path: path, DurationMs: transcribe.DurationMsOf(len(pcm))}
	res := transcribeFileResult{Provider: p.Name(), Model: p.Model(), AudioMs: audio.DurationMs, RawWords: []transcribe.Word{}, Words: []transcribe.Word{}}
	t0 := time.Now()
	segs, err := p.DetectSpeech(ctx, audio)
	if err != nil {
		return res, err
	}
	res.VADMs, res.Segments, res.SpeechMs = time.Since(t0).Milliseconds(), segs, transcribe.SpeechMs(segs)
	res.Structure = transcribe.Analyse(nil, "")
	if res.SpeechMs < transcribe.MinSpeechMs {
		return res, nil
	}
	t0 = time.Now()
	tr, err := p.Transcribe(ctx, audio, transcribe.Options{Language: language})
	if err != nil {
		var te *media.ToolError
		if errors.As(err, &te) {
			return res, fmt.Errorf("%w", err)
		}
		return res, err
	}
	res.TranscribeMs = time.Since(t0).Milliseconds()
	res.RealtimeFactor = float64(res.TranscribeMs) / float64(max(audio.DurationMs, 1))
	res.Language, res.Speech = tr.Language, len(tr.Words) > 0
	res.RawWords = tr.Words
	res.Words = transcribe.WithinSpeech(transcribe.Refine(tr.Words, pcm), segs)
	res.Structure = transcribe.Analyse(res.Words, tr.Language)
	return res, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 G703 -- the file the operator named
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- our own temp dir; the container user must read it
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, transcribe.MaxAudioBytes+1)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
