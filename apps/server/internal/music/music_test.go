package music

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

func TestFFT(t *testing.T) {
	// A sine of 8 cycles in 64 samples has its energy in bin 8.
	a := make([]complex128, 64)
	for i := range a {
		a[i] = complex(math.Sin(2*math.Pi*8*float64(i)/64), 0)
	}
	fft(a)
	best, bestV := 0, 0.0
	for k := 0; k < 32; k++ {
		if v := real(a[k])*real(a[k]) + imag(a[k])*imag(a[k]); v > bestV {
			best, bestV = k, v
		}
	}
	if best != 8 {
		t.Errorf("peak in bin %d", best)
	}
}

// A report and a flux made by hand: a pulse every 480 ms (125 BPM), loud from 2.88 s on.
func TestAnalyseFindsTheGridTheDropAndThePhrases(t *testing.T) {
	const dur = 20.0
	flux := make([]float64, int(dur*100))
	for ms := 480; ms < int(dur*1000); ms += 480 {
		w := 1.0
		if ms >= 2880 {
			w = 3
		}
		flux[ms/10] = w
	}
	var curve []audiocheck.LoudnessPoint
	for ts := 0.0; ts < dur; ts += 0.1 {
		l := -24.0
		if ts >= 2.88 {
			l = -14
		}
		curve = append(curve, audiocheck.LoudnessPoint{TimeS: ts, LUFS: l})
	}
	rep := audiocheck.Report{HasAudio: true, DurationS: dur, Loudness: audiocheck.Loudness{IntegratedLUFS: -15, TruePeakDBTP: -1.2, RangeLU: 4, Momentary: curve},
		Onsets: audiocheck.Onsets{List: []audiocheck.Onset{{TimeMs: 2885, RiseDB: 14}, {TimeMs: 5760, RiseDB: 7}, {TimeMs: 6000, RiseDB: 3}}}}
	a := Analyse(rep, Spectrum{Flux: flux})
	if !a.Music || math.Abs(a.TempoBPM-125) > 1.5 {
		t.Fatalf("tempo %v music %v", a.TempoBPM, a.Music)
	}
	if _, d, _ := Nearest(a.BeatsMs, 2400); d > 20 {
		t.Errorf("the beats %v miss the pulse at 2400 ms by %d", a.BeatsMs[:8], d)
	}
	if a.DropMs < 2860 || a.DropMs > 2900 || a.DropLU < 5 {
		t.Errorf("drop at %d (+%v LU)", a.DropMs, a.DropLU)
	}
	if !contains(a.DownbeatsMs, a.DropMs) || !contains(a.PhrasesMs, a.DropMs) {
		t.Errorf("the drop %d is not a downbeat %v or a phrase %v", a.DropMs, a.DownbeatsMs[:4], a.PhrasesMs)
	}
	if len(a.PhrasesMs) > 1 && math.Abs(float64(a.PhrasesMs[1]-a.PhrasesMs[0])-16*a.BeatMs()) > 20 {
		t.Errorf("phrases %v", a.PhrasesMs)
	}
	if len(a.OnsetsMs) != 2 {
		t.Errorf("strong onsets %v", a.OnsetsMs)
	}
	line := a.Line()
	for _, want := range []string{"music: 125", "drop at 2.88 s", "get_music"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %q: %s", want, line)
		}
	}
	// No sound, no beat: no grid, and the line says so.
	if n := Analyse(audiocheck.Report{}, Spectrum{}); n.Music || len(n.BeatsMs) != 0 {
		t.Errorf("silence: %+v", n)
	}
}

func TestNearestAndBetween(t *testing.T) {
	l := []int64{100, 500, 900}
	if at, d, ok := Nearest(l, 640); !ok || at != 500 || d != 140 {
		t.Errorf("%d %d %v", at, d, ok)
	}
	if _, _, ok := Nearest(nil, 1); ok {
		t.Error("empty")
	}
	if b := Between(l, 100, 900); len(b) != 2 {
		t.Errorf("%v", b)
	}
}

func contains(l []int64, v int64) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// The synthetic 124 BPM track with its drop at 3 s, measured through ffmpeg: the analysis finds its tempo, its beats on the kicks and the
// drop on the beat where the full kick comes in.
func TestMeasureTheSyntheticTrack(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("CF_RENDER_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg is not on PATH")
		}
		t.Skip("ffmpeg is not on PATH")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "track.m4a")
	cmd := exec.CommandContext(context.Background(), bin, SyntheticTrackArgs(124, 3, 20, out)...) //nolint:gosec // fixed arguments
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v\n%s", err, b)
	}
	runner := &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{dir, tmp}}), Limits: sandbox.DefaultLimits()}
	a, err := Measure(context.Background(), runner, out, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", a.Line())
	if !a.Music || math.Abs(a.TempoBPM-124) > 1.5 {
		t.Fatalf("tempo %v (confidence %v)", a.TempoBPM, a.Confidence)
	}
	period := 60000 / 124.0
	off := 0
	for _, b := range a.BeatsMs[:20] {
		k := math.Round(float64(b) / period)
		if d := math.Abs(float64(b) - k*period); d > 30 {
			off++
		}
	}
	if off > 2 {
		t.Errorf("%d of the first 20 beats are more than 30 ms off a kick: %v", off, a.BeatsMs[:20])
	}
	want := SyntheticDropMs(124, 3)
	if d := a.DropMs - want; d < -60 || d > 60 {
		t.Errorf("drop at %d ms, want %d (+%v LU)", a.DropMs, want, a.DropLU)
	}
}
