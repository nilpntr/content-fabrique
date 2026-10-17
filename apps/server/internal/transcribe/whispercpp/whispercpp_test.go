package whispercpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// sampleJSON is the shape of whisper-cli's --output-json-full with DTW on (large-v3-turbo, 7 s clip, trimmed): special tokens,
// a word of two tokens ("squeeze"), punctuation as its own token, a sound annotation and a token without DTW time.
const sampleJSON = `{
 "result": {"language": "en"},
 "transcription": [{
  "tokens": [
   {"text": "[_BEG_]", "offsets": {"from": 0, "to": 0}, "p": 0.72, "t_dtw": -1},
   {"text": " Drive", "offsets": {"from": 620, "to": 920}, "p": 0.56, "t_dtw": 98},
   {"text": " your", "offsets": {"from": 920, "to": 1650}, "p": 0.91, "t_dtw": 168},
   {"text": " sque", "offsets": {"from": 1650, "to": 2000}, "p": 0.80, "t_dtw": 230},
   {"text": "eze", "offsets": {"from": 2000, "to": 2440}, "p": 0.90, "t_dtw": 240},
   {"text": ",", "offsets": {"from": 2440, "to": 2440}, "p": 0.99, "t_dtw": 241},
   {"text": " [MUSIC]", "offsets": {"from": 2500, "to": 2900}, "p": 0.50, "t_dtw": 280},
   {"text": " back", "offsets": {"from": 2960, "to": 3210}, "p": 0.99, "t_dtw": 326},
   {"text": "[_TT_367]", "offsets": {"from": 7340, "to": 7340}, "p": 0.45, "t_dtw": -1}
  ]
 }]
}`

func TestParseJSONJoinsTokensAndAnchorsOnDTW(t *testing.T) {
	tr, err := ParseJSON([]byte(sampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "en" {
		t.Errorf("language %q", tr.Language)
	}
	var texts []string
	for _, w := range tr.Words {
		texts = append(texts, w.Text)
	}
	if want := []string{"Drive", "your", "squeeze,", "back"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("words %q, want %q", texts, want)
	}
	// The anchor is the DTW time of the last token, 90 ms before it.
	if got := []int64{tr.Words[0].AnchorMs, tr.Words[1].AnchorMs, tr.Words[2].AnchorMs, tr.Words[3].AnchorMs}; !reflect.DeepEqual(got, []int64{890, 1590, 2320, 3170}) {
		t.Errorf("anchors %v", got)
	}
	if c := tr.Words[2].Confidence; c < 0.89 || c > 0.90 { // (0.80 + 0.90 + 0.99) / 3
		t.Errorf("confidence of a word of three tokens: %v", c)
	}
	// Provisional spans tile the speech between the anchors.
	if tr.Words[1].StartMs != (890+1590)/2 || tr.Words[1].EndMs != (1590+2320)/2 {
		t.Errorf("span of the second word: %+v", tr.Words[1])
	}
	for i := 1; i < len(tr.Words); i++ {
		if tr.Words[i].AnchorMs <= tr.Words[i-1].AnchorMs {
			t.Errorf("anchors do not increase at %d", i)
		}
	}
}

func TestParseJSONWithoutDTWUsesTheTokenSpan(t *testing.T) {
	raw := `{"result":{"language":"nl"},"transcription":[{"tokens":[
	 {"text":" Hallo","offsets":{"from":1000,"to":1400},"p":0.9,"t_dtw":-1},
	 {"text":" wereld","offsets":{"from":1400,"to":2000},"p":0.8,"t_dtw":-1}]}]}`
	tr, err := ParseJSON([]byte(raw))
	if err != nil || len(tr.Words) != 2 || tr.Words[0].AnchorMs != 1200 || tr.Words[1].AnchorMs != 1700 || tr.Language != "nl" {
		t.Fatalf("%+v %v", tr, err)
	}
}

func TestParseJSONEnforcesIncreasingAnchorsAndRejectsGarbage(t *testing.T) {
	raw := `{"transcription":[{"tokens":[
	 {"text":" a","offsets":{"from":0,"to":0},"p":1,"t_dtw":200},
	 {"text":" a","offsets":{"from":0,"to":0},"p":1,"t_dtw":150}]}]}`
	tr, err := ParseJSON([]byte(raw))
	if err != nil || tr.Words[1].AnchorMs <= tr.Words[0].AnchorMs {
		t.Fatalf("%+v %v", tr, err)
	}
	if _, err := ParseJSON([]byte("not json")); err == nil {
		t.Error("garbage accepted")
	}
	if tr, err := ParseJSON([]byte(`{"transcription":[]}`)); err != nil || len(tr.Words) != 0 {
		t.Errorf("empty: %+v %v", tr, err)
	}
}

func TestParseSegmentsReadsCentiseconds(t *testing.T) {
	out := "\nDetected 2 speech segments:\nSpeech segment 0: start = 61.00, end = 112.00\nSpeech segment 1: start = 138.00, end = 265.50\n\n"
	segs, err := ParseSegments(out)
	want := []transcribe.Segment{{StartMs: 610, EndMs: 1120}, {StartMs: 1380, EndMs: 2655}}
	if err != nil || !reflect.DeepEqual(segs, want) {
		t.Fatalf("%v %v", segs, err)
	}
	if segs, err := ParseSegments("\nDetected 0 speech segments:\n\n"); err != nil || len(segs) != 0 {
		t.Errorf("none: %v %v", segs, err)
	}
	if _, err := ParseSegments("error: failed to initialize"); err == nil {
		t.Error("an error message was taken for 'no speech'")
	}
	if _, err := ParseSegments("Speech segment 0: start = 9.00, end = 3.00"); err == nil {
		t.Error("a segment that ends before it starts")
	}
}

// fakeRunner stands in for the sandbox: it records the invocation and writes what the tool would.
type fakeRunner struct {
	inv    media.Invocation
	json   string
	stdout string
	exit   int
	err    error
}

func (f *fakeRunner) Run(_ context.Context, inv media.Invocation) (sandbox.Result, error) {
	f.inv = inv
	if f.err != nil {
		return sandbox.Result{}, f.err
	}
	if inv.OutputDir != "" && f.json != "" {
		if err := os.WriteFile(filepath.Join(inv.OutputDir, "out.json"), []byte(f.json), 0o644); err != nil {
			return sandbox.Result{}, err
		}
	}
	return sandbox.Result{ExitCode: f.exit, Stdout: []byte(f.stdout), Stderr: []byte("tool said something")}, nil
}

func testProvider(t *testing.T, r *fakeRunner, spec transcribe.ModelSpec) *Provider {
	t.Helper()
	dir := t.TempDir()
	body := []byte("model")
	sum := sha256.Sum256(body)
	spec.SHA256, spec.File = hex.EncodeToString(sum[:]), "ggml-test.bin"
	if err := os.WriteFile(filepath.Join(dir, spec.File), body, 0o644); err != nil {
		t.Fatal(err)
	}
	vad := transcribe.ModelSpec{Name: "vad", File: "ggml-vad.bin", SHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(filepath.Join(dir, vad.File), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return &Provider{Runner: r, Cache: &transcribe.ModelCache{Dir: dir}, Spec: spec, VADModel: vad, TempDir: t.TempDir(), Threads: 3}
}

func TestTranscribeRunsWhisperCLIWithWordAlignment(t *testing.T) {
	r := &fakeRunner{json: sampleJSON}
	p := testProvider(t, r, transcribe.ModelSpec{Name: "large-v3-turbo-q5_0", DTW: "large.v3.turbo"})
	tr, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/x/audio.wav", DurationMs: 60_000}, transcribe.Options{})
	if err != nil || len(tr.Words) != 4 {
		t.Fatalf("%+v %v", tr, err)
	}
	args := strings.Join(r.inv.Args, " ")
	for _, want := range []string{"whisper-cli", "-m /in/0", "-f /in/1", "-ojf", "-of /work/out", "-l auto", "-t 3", "-nfa", "-dtw large.v3.turbo", "-np"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if len(r.inv.Inputs) != 2 || r.inv.Inputs[1].Host != "/x/audio.wav" || r.inv.OutputDir == "" {
		t.Errorf("mounts %+v", r.inv)
	}
	if r.inv.Timeout != 5*time.Minute+3*time.Minute {
		t.Errorf("timeout %v: want 5x the audio plus 3 minutes", r.inv.Timeout)
	}
	if _, err := os.Stat(r.inv.OutputDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("the output directory was left behind")
	}
	if p.Name() != "whispercpp" || p.Model() != "ggml-test" {
		t.Errorf("%s %s", p.Name(), p.Model())
	}
	if c, v := p.CostMicros(123456); c != 0 || v != "local" {
		t.Errorf("local transcription costs nothing: %d %s", c, v)
	}
}

func TestTranscribeLanguageHandling(t *testing.T) {
	r := &fakeRunner{json: sampleJSON}
	p := testProvider(t, r, transcribe.ModelSpec{Name: "large", DTW: "large.v3.turbo"})
	if _, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{Language: "NL"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.inv.Args, " "), "-l nl") {
		t.Errorf("%v", r.inv.Args)
	}
	if _, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{Language: "en; rm -rf"}); err == nil {
		t.Error("an invalid language reached the command line")
	}
	en := testProvider(t, r, transcribe.ModelSpec{Name: "tiny.en", DTW: "tiny.en", EnglishOnly: true})
	if _, err := en.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{Language: "nl"}); err == nil {
		t.Error("an English-only model was asked for Dutch")
	}
	if _, err := en.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{}); err != nil || !strings.Contains(strings.Join(r.inv.Args, " "), "-l en") {
		t.Errorf("%v %v", err, r.inv.Args)
	}
}

func TestTranscribeToolFailures(t *testing.T) {
	r := &fakeRunner{exit: 1}
	p := testProvider(t, r, transcribe.ModelSpec{Name: "x"})
	_, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{})
	var te *media.ToolError
	if !errors.As(err, &te) || te.Tool != "whisper-cli" || te.ExitCode != 1 {
		t.Errorf("exit code: %v", err)
	}
	r = &fakeRunner{err: sandbox.ErrTimeout}
	p = testProvider(t, r, transcribe.ModelSpec{Name: "x"})
	if _, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{}); !errors.Is(err, sandbox.ErrTimeout) {
		t.Errorf("timeout: %v", err)
	}
	r = &fakeRunner{} // exits 0 and writes nothing
	p = testProvider(t, r, transcribe.ModelSpec{Name: "x"})
	if _, err := p.Transcribe(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}, transcribe.Options{}); err == nil {
		t.Error("a missing output was accepted")
	}
}

func TestDetectSpeechRunsTheVADTool(t *testing.T) {
	r := &fakeRunner{stdout: "Detected 1 speech segments:\nSpeech segment 0: start = 50.00, end = 300.00\n"}
	p := testProvider(t, r, transcribe.ModelSpec{Name: "x"})
	segs, err := p.DetectSpeech(context.Background(), transcribe.Audio{Path: "/x/audio.wav", DurationMs: 10_000})
	if err != nil || len(segs) != 1 || segs[0].StartMs != 500 || segs[0].EndMs != 3000 {
		t.Fatalf("%v %v", segs, err)
	}
	args := strings.Join(r.inv.Args, " ")
	if !strings.HasPrefix(args, "whisper-vad-speech-segments -vm /in/0 -f /in/1") {
		t.Errorf("args %q", args)
	}
	if r.inv.OutputDir != "" {
		t.Error("the VAD tool needs no writable mount")
	}
	r.exit = 3
	if _, err := p.DetectSpeech(context.Background(), transcribe.Audio{Path: "/a", DurationMs: 1000}); err == nil {
		t.Error("a failing tool was taken for 'no speech'")
	}
}
