package styleprofile

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

func testWorkspace() ids.WorkspaceID { return ids.WorkspaceID{UUID: uuid.New()} }

// ---- the picture, from synthetic prints ----------------------------------------------------------------------------------------

func sceneText(pairs ...float64) []byte {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		fmt.Fprintf(&b, "frame:%d   pts:%d      pts_time:%.6f\nlavfi.scene_score=%.6f\n", i/2, i/2, pairs[i], pairs[i+1])
	}
	return []byte(b.String())
}

func TestDetectCuts(t *testing.T) {
	// A cut spreads over two frames (the stronger wins), a weak score is no cut, one at the very start and one at the very end are
	// flickers, and two real cuts a second apart are both kept.
	pts, err := parseScene(sceneText(0.1, 0.5, 0.5, 0.01, 1.0, 0.45, 1.033, 0.30, 1.5, 0.05, 2.0, 0.09, 2.5, 0.12, 3.5, 0.9, 3.9, 0.8, 4.0, 0.0))
	if err != nil {
		t.Fatal(err)
	}
	got := DetectCuts(pts, 4.0)
	want := []float64{1.0, 2.5, 3.5}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cuts %v, want %v", got, want)
	}
	shots := ShotLengths(got, 4.0)
	if fmt.Sprint(shots) != "[1 1.5 1 0.5]" {
		t.Errorf("shots %v", shots)
	}
	if got := ShotLengths(nil, 9.5); fmt.Sprint(got) != "[9.5]" {
		t.Errorf("no cuts: %v", got)
	}
	if _, err := parseScene([]byte("frame:1 pts:1 pts_time:abc\n")); err == nil {
		t.Error("a bad time was accepted")
	}
	if _, err := parseScene([]byte("frame:1 pts:1 pts_time:0.1\nlavfi.scene_score=NaN\n")); err == nil {
		t.Error("NaN was accepted")
	}
}

func TestParseStatsAndLight(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "frame:%d pts:%d pts_time:%.2f\nlavfi.signalstats.YAVG=%d\nlavfi.signalstats.YHIGH=%d\nlavfi.signalstats.SATAVG=20\nlavfi.signalstats.SATHIGH=40\n", i, i, float64(i)/4, 40+i, 90+i)
	}
	fr, err := parseStats([]byte(b.String()))
	if err != nil || len(fr) != 8 {
		t.Fatalf("%v %d", err, len(fr))
	}
	mean, p90, sat, satP90 := lightOf(fr)
	if mean <= 0 || mean >= 0.5 || p90 <= mean || sat <= 0 || satP90 < sat {
		t.Errorf("light: %.3f %.3f %.3f %.3f", mean, p90, sat, satP90)
	}
	if _, err := parseStats([]byte("frame:0\nlavfi.signalstats.YAVG=bad\n")); err == nil {
		t.Error("a bad statistic was accepted")
	}
}

// A static picture has no motion, a picture that grows 2 % a step is a punch-in, and one bright frame is a flash.
func TestMotionStats(t *testing.T) {
	const sz, px = media.StyleThumbSize, media.StyleThumbPixels
	pattern := func(scale float64) []byte {
		f := make([]byte, px)
		for y := 0; y < sz; y++ {
			for x := 0; x < sz; x++ {
				dx, dy := (float64(x)-23.5)/scale, (float64(y)-23.5)/scale
				f[y*sz+x] = byte(128 + 100*math.Sin(dx/1.3)*math.Cos(dy/1.7))
			}
		}
		return f
	}
	static := make([][]byte, 30)
	for i := range static {
		static[i] = pattern(1)
	}
	motion, zooms, push, flashes, _ := motionStats(static)
	if motion != 0 || zooms != 0 || push != 0 || flashes != 0 {
		t.Errorf("static: %v %v %v %v", motion, zooms, push, flashes)
	}
	growing := make([][]byte, 30)
	for i := range growing {
		growing[i] = pattern(1 + 0.02*float64(i))
	}
	motion, zooms, _, _, _ = motionStats(growing)
	if motion <= 0 || zooms < 1 {
		t.Errorf("a quick zoom: motion %v, zooms %v", motion, zooms)
	}
	flash := make([][]byte, 30)
	for i := range flash {
		flash[i] = pattern(1)
	}
	for j := range flash[15] {
		flash[15][j] = 250
	}
	if _, _, _, flashes, _ = motionStats(flash); flashes != 1 {
		t.Errorf("flashes %d", flashes)
	}
	if m, z, p, f, v := motionStats(nil); m != 0 || z != 0 || p != 0 || f != 0 || v != 1 {
		t.Error("no frames")
	}
}

// ---- the sound, from synthetic curves ------------------------------------------------------------------------------------------

func curve(f func(t float64) float64, end float64) []audiocheck.LoudnessPoint {
	var c []audiocheck.LoudnessPoint
	for t := 0.0; t <= end; t += 0.1 {
		c = append(c, audiocheck.LoudnessPoint{TimeS: math.Round(t*10) / 10, LUFS: f(t)})
	}
	return c
}

func TestFindLiftAndArc(t *testing.T) {
	// Quiet for 2.3 s, then a jump of 9 LU, then steady: the lift is at about 2.3 s.
	quietThenLoud := curve(func(t float64) float64 {
		if t < 2.3 {
			return -32
		}
		return -23
	}, 12)
	at, lu := findLift(quietThenLoud)
	if math.Abs(at-2.3) > 0.6 || lu < 6 {
		t.Errorf("lift at %.1f of %.1f LU", at, lu)
	}
	// A slow build has no lift of LiftMinLU in a second and a half: no lift.
	slow := curve(func(t float64) float64 { return -30 + t*0.4 }, 20)
	if at, lu := findLift(slow); lu != 0 || at != 0 {
		t.Errorf("a slow build has a lift: %.1f %.1f", at, lu)
	}
	if _, arc := arcOf(slow); arc != ArcBuilds {
		t.Errorf("arc %s", arc)
	}
	if _, arc := arcOf(curve(func(float64) float64 { return -20 }, 10)); arc != ArcFlat {
		t.Errorf("flat arc %s", arc)
	}
	if _, arc := arcOf(curve(func(t float64) float64 { return -14 - t }, 10)); arc != ArcFalls {
		t.Errorf("falling arc %s", arc)
	}
	if at, lu := findLift(nil); at != 0 || lu != 0 {
		t.Error("nil curve")
	}
}

// A pulse train of the flux every 500 ms is 120 BPM (or its half or double, which every estimate is ambiguous by).
func TestTempoFromFlux(t *testing.T) {
	flux := make([]float64, 1200)
	for i := 20; i < len(flux); i += 50 {
		flux[i] = 1
	}
	bpm, conf, phase := tempoFromFlux(flux)
	ratio := bpm / 120
	if conf < 0.1 || (math.Abs(ratio-1) > 0.05 && math.Abs(ratio-0.5) > 0.03 && math.Abs(ratio-2) > 0.1) {
		t.Errorf("tempo %.1f, confidence %.2f", bpm, conf)
	}
	if phase < 0 || phase > 60000/bpm+1 {
		t.Errorf("phase %.0f ms", phase)
	}
	// Noise has no tempo to speak of.
	noise := make([]float64, 1200)
	for i := range noise {
		noise[i] = math.Abs(math.Sin(float64(i) * 12.9898 * 78.233))
	}
	if _, c, _ := tempoFromFlux(noise); c > 0.5 {
		t.Errorf("noise has confidence %.2f", c)
	}
}


// A report with a quiet 2.3 s intro and a lift, onsets near the cuts and a build to the end reads as the creator's sound.
func TestAnalyseAudio(t *testing.T) {
	c := curve(func(t float64) float64 {
		if t < 2.3 {
			return -33
		}
		return -24 + t*0.5
	}, 14)
	rep := audiocheck.Report{HasAudio: true, DurationS: 14}
	rep.Loudness.IntegratedLUFS, rep.Loudness.RangeLU, rep.Loudness.EndLU = -17, 8, 1
	rep.Loudness.Momentary = c
	for _, ms := range []int64{50, 2300, 2900, 5000, 5400, 9000} {
		rep.Onsets.List = append(rep.Onsets.List, audiocheck.Onset{TimeMs: ms, RiseDB: 12})
	}
	flux := make([]float64, 1400)
	for i := 20; i < len(flux); i += 80 {
		flux[i] = 1
	}
	a := AnalyseAudio(rep, spectrum{Flux: flux, Flatness: 0.05}, []float64{2.4, 5.2, 9.1})
	if !a.HasAudio || math.Abs(a.IntroS-2.3) > 0.7 || a.LiftLU < 6 || a.Arc != ArcBuilds || !a.MusicPresent || a.Content != ContentMusicOnly {
		t.Errorf("audio: %+v", a)
	}
	if a.IntegratedLUFS != -17 || len(a.CutOnsetMs) != 3 || a.BeatLocked && a.CutsWithin100ms < 0.8 {
		t.Errorf("audio: %+v", a)
	}
	// The same sound with a high flatness is music with ambience or effects.
	if b := AnalyseAudio(rep, spectrum{Flux: flux, Flatness: 0.5}, nil); b.Content != ContentMusicEffects {
		t.Errorf("flat sound: %s", b.Content)
	}
	// No audio track at all.
	if n := AnalyseAudio(audiocheck.Report{}, spectrum{}, nil); n.HasAudio || n.Content != ContentUnknown || n.Speech != "unknown" {
		t.Errorf("no audio: %+v", n)
	}
}

// ---- the real thing: ffmpeg through the local sandbox ------------------------------------------------------------------------------

// A 12 s video of four colour blocks (hard cuts at 2, 5 and 9 s) over a quiet tone that lifts at 2.3 s: the cuts, the shot lengths, the
// lift and the loudness come out as built. It skips without ffmpeg.
func TestMeasureARealVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(root, "ref.mp4")
	gen := exec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=270x480:rate=30:duration=2",
		"-f", "lavfi", "-i", "color=c=0x2060c0:s=270x480:r=30:d=3",
		"-f", "lavfi", "-i", "testsrc=size=270x480:rate=30:duration=4",
		"-f", "lavfi", "-i", "color=c=0xc03020:s=270x480:r=30:d=3",
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=44100:duration=12",
		"-filter_complex", "[0:v][1:v][2:v][3:v]concat=n=4:v=1:a=0,format=yuv420p[v];[4:a]volume='if(lt(t,2.3),0.03,0.4)':eval=frame[a]",
		"-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-c:a", "aac", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
	runner := &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root, tmp}}), Limits: sandbox.DefaultLimits()}
	an := &Analyzer{Runner: runner}
	res, err := an.AnalyzeFile(context.Background(), testWorkspace(), clip)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Measures
	if len(m.CutsS) != 3 || math.Abs(m.CutsS[0]-2) > 0.15 || math.Abs(m.CutsS[1]-5) > 0.15 || math.Abs(m.CutsS[2]-9) > 0.15 {
		t.Errorf("cuts %v", m.CutsS)
	}
	if len(m.ShotsS) != 4 || math.Abs(m.ShotsS[3]-3) > 0.2 || math.Abs(m.DurationS-12) > 0.2 {
		t.Errorf("shots %v of %.2f s", m.ShotsS, m.DurationS)
	}
	a := m.Audio
	if !a.HasAudio || math.Abs(a.IntroS-2.3) > 0.8 || a.LiftLU < 6 {
		t.Errorf("audio: intro %.1f, lift %.1f LU", a.IntroS, a.LiftLU)
	}
	if a.IntegratedLUFS > -10 || a.IntegratedLUFS < -40 {
		t.Errorf("loudness %.1f", a.IntegratedLUFS)
	}
	if res.Vision != nil || m.Text != nil {
		t.Error("a vision call was made without a reader")
	}
	// A file that is not a video is refused.
	junk := filepath.Join(root, "junk.mp4")
	if err := os.WriteFile(junk, []byte("this is not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := an.AnalyzeFile(context.Background(), testWorkspace(), junk); err == nil {
		t.Error("junk was analysed")
	}
}

func TestParseSceneRefusesATruncatedLine(t *testing.T) {
	if _, err := parseScene([]byte("frame:12 pts:400 pts_time:")); err == nil {
		t.Error("a line cut off after pts_time: was accepted")
	}
}

func TestBeatEvidence(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	if !p.Firm() || !p.CutsOffBeat() || p.Audio.CutsMeasured < MinCutsForBeat {
		t.Fatalf("firm %v, off beat %v, cuts measured %d", p.Firm(), p.CutsOffBeat(), p.Audio.CutsMeasured)
	}
	// A creator of montages whose cuts we could not measure is not called loose; one of long takes is (a grid means nothing to them).
	m := referenceMeasures(t)
	for i := range m {
		m[i].Audio.CutOnsetMs, m[i].Audio.CutBeatMs = nil, nil
	}
	q := Aggregate(m)
	if q.Audio.CutsMeasured != 0 || !q.CutsOffBeat() || !strings.Contains(q.Describe(), "too few cuts to say") {
		t.Errorf("long takes, no measured cuts: %d %v\n%s", q.Audio.CutsMeasured, q.CutsOffBeat(), q.Describe())
	}
	for i := range m {
		m[i].CutsS = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		m[i].ShotsS = []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
		m[i].DurationS = 13
	}
	if q = Aggregate(m); q.Pacing.Style != PaceMontage || q.CutsOffBeat() {
		t.Errorf("a montage with unmeasured cuts is called loose: %s %v", q.Pacing.Style, q.CutsOffBeat())
	}
	var nilP *Profile
	if nilP.Firm() || nilP.CutsOffBeat() {
		t.Error("a nil profile")
	}
	if !Aggregate(m[:3]).Firm() || Aggregate(m[:2]).Firm() {
		t.Error("the profile is firm from three references")
	}
}
