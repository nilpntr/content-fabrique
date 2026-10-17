package whispercpp

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// Needs Docker, a built media-tools image and a speech synthesiser (espeak-ng, or macOS say) with ffmpeg, so it only runs when asked
// (CI does, with the tiny model):
//
//	go test ./internal/transcribe/whispercpp -run Integration -docker-image media-tools:dev [-model tiny.en] [-models-dir DIR]
//
// -models-dir keeps the downloaded models between runs; without it the test downloads into a temporary directory.
var (
	dockerImage = flag.String("docker-image", "", "media-tools image for the whisper.cpp integration tests")
	modelFlag   = flag.String("model", "tiny.en", "speech model of the integration tests")
	modelsDir   = flag.String("models-dir", "", "model cache directory of the integration tests")
)

const sentence = "The quick brown fox jumps over the lazy dog"

// speak writes a 16 kHz mono WAV of the sentence with whatever synthesiser the machine has.
func speak(t *testing.T, dir string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	raw := filepath.Join(dir, "speech.raw")
	switch {
	case have("espeak-ng"):
		raw += ".wav"
		run(t, "espeak-ng", "-v", "en-us", "-s", "130", "-w", raw, sentence)
	case have("say"):
		raw += ".aiff"
		run(t, "say", "-v", "Samantha", "-o", raw, sentence)
	default:
		t.Skip("neither espeak-ng nor say is installed")
	}
	out := filepath.Join(dir, "speech.wav")
	// Half a second of silence either side, as in a clip.
	run(t, ffmpeg, "-nostdin", "-v", "error", "-y", "-i", raw, "-af", "adelay=500:all=1,apad=pad_dur=0.5", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", out)
	return out
}

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func run(t *testing.T, bin string, args ...string) {
	t.Helper()
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil { // #nosec G204 -- fixed tools and test-made paths
		t.Fatalf("%s: %v\n%s", bin, err, out)
	}
}

func integrationProvider(t *testing.T, root string) *Provider {
	t.Helper()
	if *dockerImage == "" {
		t.Skip("set -docker-image to run the whisper.cpp integration tests")
	}
	spec, ok := transcribe.SpeechModel(*modelFlag)
	if !ok {
		t.Fatalf("unknown model %q", *modelFlag)
	}
	models := *modelsDir
	if models == "" {
		models = filepath.Join(root, "models")
	}
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := filepath.EvalSymlinks(models)
	if err != nil {
		t.Fatal(err)
	}
	d := sandbox.NewDocker(sandbox.Policy{AllowedRoots: []string{root, models}})
	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0o750); err != nil {
		t.Fatal(err)
	}
	return &Provider{
		Runner: &media.SandboxRunner{Provider: d, Image: *dockerImage, Limits: sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 5 * time.Minute, PIDs: 256}},
		Cache:  &transcribe.ModelCache{Dir: models}, Spec: spec, VADModel: transcribe.ModelSileroVAD, TempDir: tmp, Threads: 2,
	}
}

// The real tools in the real image: the voice-activity check finds speech in a synthesised sentence and none in silence, and the
// transcription recognises most of the words in order, with times that increase and stay inside the clip. A loose check on
// purpose: the tiny model and a robotic voice make a few mistakes; the figure for the timing is measured with the large model
// on clips with known timings (scripts/transcribe-eval).
func TestIntegrationSpeechAndSilence(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := integrationProvider(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	clip := filepath.Join(root, "in")
	if err := os.Mkdir(clip, 0o755); err != nil {
		t.Fatal(err)
	}
	wav := speak(t, clip)
	pcm, err := transcribe.ReadWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	audio := transcribe.Audio{Path: wav, DurationMs: transcribe.DurationMsOf(len(pcm))}

	segs, err := p.DetectSpeech(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	if ms := transcribe.SpeechMs(segs); ms < transcribe.MinSpeechMs || segs[0].StartMs < 300 || segs[len(segs)-1].EndMs > audio.DurationMs+200 {
		t.Fatalf("speech segments %+v of a %d ms clip", segs, audio.DurationMs)
	}

	tr, err := p.Transcribe(ctx, audio, transcribe.Options{Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "en" {
		t.Errorf("language %q", tr.Language)
	}
	words := transcribe.WithinSpeech(transcribe.Refine(tr.Words, pcm), segs)
	want := strings.Fields(strings.ToLower(sentence))
	got := map[string]bool{}
	for _, w := range words {
		got[strings.ToLower(strings.Trim(w.Text, " .,!?"))] = true
	}
	var hit int
	for _, w := range want {
		if got[w] {
			hit++
		}
	}
	if hit < 5 {
		t.Errorf("recognised %d of %d words (%v): %+v", hit, len(want), want, words)
	}
	var prevEnd int64
	for i, w := range words {
		if w.StartMs < prevEnd || w.EndMs <= w.StartMs || w.EndMs > audio.DurationMs+100 {
			t.Errorf("word %d %+v: times must increase and stay inside the %d ms clip (previous end %d)", i, w, audio.DurationMs, prevEnd)
		}
		prevEnd = w.EndMs
	}

	// Silence has no speech: the pipeline would never call the transcriber.
	silent := filepath.Join(clip, "silence.wav")
	run(t, "ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "4", "-c:a", "pcm_s16le", silent)
	segs, err = p.DetectSpeech(ctx, transcribe.Audio{Path: silent, DurationMs: 4000})
	if err != nil || transcribe.SpeechMs(segs) >= transcribe.MinSpeechMs {
		t.Fatalf("silence: %+v %v", segs, err)
	}
}
