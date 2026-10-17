package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// fixtureProvider answers every job with a recorded ffprobe JSON and remembers the job it was given.
type fixtureProvider struct {
	name string
	json []byte
	job  sandbox.Job
}

func (f *fixtureProvider) Name() string { return f.name }
func (f *fixtureProvider) Run(_ context.Context, j sandbox.Job) (sandbox.Result, error) {
	f.job = j
	return sandbox.Result{Stdout: f.json}, nil
}

func fixtureDeps(t *testing.T, fixture string) (mediaDeps, *fixtureProvider, *string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "media", "testdata", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	fp := &fixtureProvider{name: "docker", json: raw}
	var gotName string
	return mediaDeps{newProvider: func(name, _, _ string) (sandbox.Provider, error) {
		gotName = name
		return fp, nil
	}}, fp, &gotName
}

func runMedia(t *testing.T, deps mediaDeps, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdMedia(args, &out, &errb, deps)
	return code, out.String(), errb.String()
}

func tempClip(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.mov")
	if err := os.WriteFile(p, make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMediaProbeTable(t *testing.T) {
	deps, fp, name := fixtureDeps(t, "iphone_portrait_apac")
	clip := tempClip(t)
	code, out, errs := runMedia(t, deps, "probe", clip)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if *name != "docker" {
		t.Errorf("default provider %q", *name)
	}
	want := `file       clip.mov
container  mov (mov,mp4,m4a,3gp,3g2,mj2)
duration   87.135 s
size       463.5 MiB
video      h264 yuv420p
display    2160x3840 (stored 3840x2160, rotation -90 deg, turn 90 deg clockwise)
fps        522800/17427 (29.999)
colour     bt709 / bt709 / bt709 (SDR)
audio      #1 aac [mp4a], 2 ch, 48000 Hz: selected
           #2 unknown [apac], 4 ch, 48000 Hz: undecodable, skipped
`
	if out != want {
		t.Fatalf("table differs.\n got:\n%s\nwant:\n%s", out, want)
	}
	if fp.job.Network != sandbox.NetworkNone || fp.job.Image != "media-tools:dev" || len(fp.job.Mounts) != 1 || fp.job.Mounts[0].Writable {
		t.Errorf("job %+v", fp.job)
	}
	if strings.Contains(out, "iPhone") || strings.Contains(out, "ISO6709") {
		t.Error("tags leaked into the output")
	}
}

func TestMediaProbeJSONAndFlagOrder(t *testing.T) {
	deps, _, name := fixtureDeps(t, "iphone_hdr_hlg_portrait")
	clip := tempClip(t)
	// Flags after the file name work too.
	code, out, errs := runMedia(t, deps, "probe", clip, "--json", "--provider", "hardened")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if *name != "hardened" {
		t.Errorf("provider %q", *name)
	}
	var m media.Metadata
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !m.HDR || m.HDRFormat != "hlg" || m.Width != 2160 || m.Height != 3840 || m.SelectedAudio == nil || *m.SelectedAudio != 1 {
		t.Errorf("%+v", m)
	}
	if code, out, _ = runMedia(t, deps, "probe", "--json", clip); code != 0 || !strings.HasPrefix(out, "{") {
		t.Errorf("flags before the file: %d %s", code, out)
	}
}

func TestMediaProbeHDRTable(t *testing.T) {
	deps, _, _ := fixtureDeps(t, "hdr10_pq_landscape")
	code, out, errs := runMedia(t, deps, "probe", tempClip(t))
	if code != 0 || !strings.Contains(out, "(HDR PQ)") || !strings.Contains(out, "display    3840x2160 (stored 3840x2160, rotation none)") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
}

func TestMediaProbeNoDecodableAudio(t *testing.T) {
	deps, _, _ := fixtureDeps(t, "apac_only")
	_, out, _ := runMedia(t, deps, "probe", tempClip(t))
	if !strings.Contains(out, "no decodable audio stream") {
		t.Fatalf("%s", out)
	}
}

func TestMediaUsageErrors(t *testing.T) {
	deps, _, _ := fixtureDeps(t, "rotated_180")
	clip := tempClip(t)
	for name, args := range map[string][]string{
		"no args":        {},
		"unknown":        {"nope"},
		"no file":        {"probe"},
		"two files":      {"probe", clip, clip},
		"bad provider":   {"probe", clip, "--provider", "podman"},
		"bad bench runs": {"bench", "--runs", "0"},
		"bench extra":    {"bench", "extra"},
	} {
		d := deps
		if name == "bad provider" || name == "bad bench runs" {
			d = defaultMediaDeps()
		}
		if code, _, _ := runMedia(t, d, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if code, out, _ := runMedia(t, deps, "help"); code != 0 || !strings.Contains(out, "cfctl media probe") {
		t.Errorf("help: %d", code)
	}
	if code, _, errs := runMedia(t, deps, "probe", filepath.Join(t.TempDir(), "missing.mov")); code != 1 || !strings.Contains(errs, "missing.mov") {
		t.Errorf("missing file: %d %s", code, errs)
	}
}

func TestDefaultProviders(t *testing.T) {
	root := t.TempDir()
	for name, want := range map[string]string{"local": "local", "docker": "docker", "hardened": "hardened"} {
		p, err := newMediaProvider(name, "docker", root)
		if err != nil || p.Name() != want {
			t.Errorf("%s: %v, %v", name, p, err)
		}
	}
	if _, err := newMediaProvider("cloudflare", "docker", root); err == nil {
		t.Error("cloudflare is not available here")
	}
}

func TestBenchTable(t *testing.T) {
	var b bytes.Buffer
	ms := func(n ...int) []time.Duration {
		var out []time.Duration
		for _, v := range n {
			out = append(out, time.Duration(v)*time.Millisecond)
		}
		return out
	}
	step := func(start, tr, cp []int) map[string][]time.Duration {
		return map[string][]time.Duration{stepStartup: ms(start...), stepTranscode: ms(tr...), stepCopy: ms(cp...)}
	}
	writeBenchTable(&b, []benchResult{
		{provider: "docker", steps: step([]int{300, 310, 290}, []int{10000, 10200, 9800}, []int{1000, 1000, 1000})},
		{provider: "hardened", steps: step([]int{700, 710, 690}, []int{11000, 11220, 10780}, []int{3000, 3100, 2900})},
	}, benchInfo{image: "media-tools:dev", cpus: 2, memory: 2048, seconds: 30, runs: 3})
	out := b.String()
	for _, want := range []string{
		"| start-up (ffprobe -version) | docker | 0.30 / 0.31 / 0.29 | 0.30 | 1.00x (+0.0%) |",
		"| transcode 1080p H.264 CRF 23 veryfast | hardened | 11.00 / 11.22 / 10.78 | 11.00 | 1.10x (+10.0%) |",
		"| copy 0.9 GB (read + write, no encode) | hardened | 3.00 / 3.10 / 2.90 | 3.00 | 3.00x (+200.0%) |",
		"vs docker",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if median(ms(1, 2, 3, 4)) != 2500*time.Microsecond {
		t.Error("even median")
	}
}

// With ffmpeg on the PATH, the local provider is exercised end to end on small generated files, including HDR tags.
func TestMediaProbeLocalGenerated(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	hdr := filepath.Join(dir, "hdr.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=128x72:rate=25:duration=1",
		"-f", "lavfi", "-i", "sine=duration=1",
		"-vf", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", "-c:a", "aac", hdr) // #nosec G204 -- fixed arguments in a test
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate a sample: %v: %s", err, out)
	}
	code, out, errs := runMedia(t, defaultMediaDeps(), "probe", hdr, "--provider", "local", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var m media.Metadata
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if !m.HDR || m.HDRFormat != "pq" || m.Width != 128 || m.Height != 72 || m.Container != "mp4" || m.SelectedAudio == nil || m.DurationMs < 900 {
		t.Errorf("%+v", m)
	}
}
