package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var regexpOutside = regexp.MustCompile(`[^A-Za-z0-9_.,:;=\[\]'()+\-*/<> ]`)

func sfxItem(id, name string, at int64, gain *float64) *timeline.SFX {
	return &timeline.SFX{ID: id, Sfx: name, AtMs: at, Export: timeline.ExportLicensed, GainDB: gain}
}

func sfxSpec(fps int, items ...timeline.AudioItem) *timeline.Spec {
	return specOf(fps, []timeline.VideoItem{clipAt("c1", assetA, 0, 4000, 0)}, &timeline.AudioTrack{ID: "sound", Items: items})
}

// monoSamples decodes the audio of a render to 48 kHz mono floats.
func monoSamples(t *testing.T, ffmpeg, video string) []float64 {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", video, //nolint:gosec // test helper
		"-vn", "-af", "pan=mono|c0=0.5*c0+0.5*c1", "-ar", "48000", "-f", "f32le", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("decode audio: %v\n%s", err, stderr.String())
	}
	data := stdout.Bytes()
	out := make([]float64, len(data)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])))
	}
	return out
}

// rms is the RMS of the samples from fromMs to toMs.
func rms(s []float64, fromMs, toMs int) float64 {
	a, b := min(fromMs*48, len(s)), min(toMs*48, len(s))
	if b <= a {
		return 0
	}
	sum := 0.0
	for _, v := range s[a:b] {
		sum += v * v
	}
	return math.Sqrt(sum / float64(b-a))
}

func peak(s []float64) float64 {
	p := 0.0
	for _, v := range s {
		p = math.Max(p, math.Abs(v))
	}
	return p
}

func db(x float64) float64 { return 20 * math.Log10(math.Max(x, 1e-9)) }

// TestSoundEffectsAreHeardWhereTheyAre renders silent video with effects at known times and checks the audio: silence before
// and between them, energy in each effect's window, a riser that grows, a truncated effect at the end of the timeline, a
// gain that moves the level by its dB, a peak under the limiter, and the same samples on every render (no randomness).
func TestSoundEffectsAreHeardWhereTheyAre(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genSource(t, ffmpeg, "flat", "color=c=gray:s=640x360:r=30:d=5")
	resolver := oneFileResolver(src)

	spec := sfxSpec(30,
		sfxItem("s1", timeline.SFXWhoosh, 500, nil),
		sfxItem("s2", timeline.SFXImpact, 1500, nil),
		sfxItem("s3", timeline.SFXRiser, 2000, nil),
		sfxItem("s4", timeline.SFXSwish, 3850, nil), // 300 ms long, cut by the end at 4000
	)
	if res := timeline.Validate(mustMarshal(t, spec), testAssets(t)); !res.OK() {
		t.Fatalf("not valid: %+v", res.Issues)
	}
	out, plan := renderSpec(t, spec, Final, resolver)
	if plan.Loudnorm != nil {
		t.Error("a timeline with only sound effects as sound has nothing to normalise")
	}
	s := monoSamples(t, ffmpeg, out)
	if n := len(s); n < 4000*48-2400 || n > 4000*48+2400 {
		t.Fatalf("the audio is %d samples, want about %d", n, 4000*48)
	}

	// Silence where nothing plays: before the first effect, and between the whoosh and the impact.
	for _, w := range [][2]int{{0, 480}, {1250, 1450}} {
		if r := rms(s, w[0], w[1]); r > 1e-3 {
			t.Errorf("window %v ms should be silent, RMS %.5f (%.1f dBFS)", w, r, db(r))
		}
	}
	// Energy in each effect's own window.
	for name, w := range map[string][2]int{"whoosh": {550, 1050}, "impact": {1500, 1900}, "riser": {2800, 3500}, "swish": {3860, 3990}} {
		if r := rms(s, w[0], w[1]); r < 0.01 {
			t.Errorf("%s: window %v ms has RMS %.5f (%.1f dBFS), want at least -40 dBFS", name, w, r, db(r))
		}
	}
	// The riser builds: its last 300 ms are much louder than its first.
	if early, late := rms(s, 2000, 2300), rms(s, 3200, 3500); late < 4*early {
		t.Errorf("the riser does not build: %.5f at the start, %.5f at the end", early, late)
	}
	// An impact is a low hit: most of its energy in the first 150 ms.
	if first, rest := rms(s, 1500, 1650), rms(s, 1650, 1800); first < 2*rest {
		t.Errorf("the impact does not decay: %.5f then %.5f", first, rest)
	}
	// The limiter: nothing over -1 dBFS, and the effects are not tiny.
	if p := peak(s); p > 0.8913+0.01 || p < 0.1 {
		t.Errorf("peak %.4f (%.1f dBFS), want between -20 dBFS and -1 dBFS", p, db(p))
	}

	// A gain of -12 dB on the impact moves its level by about 12 dB.
	quiet := sfxSpec(30, sfxItem("s2", timeline.SFXImpact, 1500, ptr(-12.0)))
	loud := sfxSpec(30, sfxItem("s2", timeline.SFXImpact, 1500, nil))
	qOut, _ := renderSpec(t, quiet, Final, resolver)
	lOut, _ := renderSpec(t, loud, Final, resolver)
	qs, ls := monoSamples(t, ffmpeg, qOut), monoSamples(t, ffmpeg, lOut)
	if d := db(rms(ls, 1500, 2300)) - db(rms(qs, 1500, 2300)); math.Abs(d-12) > 1.5 {
		t.Errorf("gain -12 dB moved the level by %.2f dB", d)
	}

	// The same samples on every render: no randomness at render time.
	again, _ := renderSpec(t, spec, Final, resolver)
	if a, b := sha256.Sum256(floatsToBytes(s)), sha256.Sum256(floatsToBytes(monoSamples(t, ffmpeg, again))); a != b {
		t.Error("two renders of the same spec have different audio")
	}
}

func floatsToBytes(f []float64) []byte {
	b := make([]byte, 0, len(f)*8)
	for _, v := range f {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
	}
	return b
}

// An effect on top of a normalised programme does not move the programme: the part of the render before the first effect is the
// same with and without them, and the whole mix stays under the limiter.
func TestSoundEffectsLeaveTheProgrammeAlone(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	dir, cat := ensureMedia(t)
	resolver := mediaResolver(dir, cat)
	music := &timeline.LicensedTrack{
		AudioCommon: timeline.AudioCommon{ID: "m1", AtMs: 0, SourceOffsetMs: 0, Export: timeline.ExportLicensed, DurationMs: ptr(int64(4000))},
		AssetID:     assetMusic, LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f",
	}
	with := sfxSpec(30, music, sfxItem("s1", timeline.SFXImpact, 2000, nil), sfxItem("s2", timeline.SFXRiser, 2500, ptr(6.0)))
	without := sfxSpec(30, music)
	wOut, wPlan := renderSpec(t, with, Final, resolver)
	nOut, nPlan := renderSpec(t, without, Final, resolver)
	if wPlan.Loudnorm == nil || nPlan.Loudnorm == nil || wPlan.Loudnorm.Filter != nPlan.Loudnorm.Filter {
		t.Fatal("the programme must be normalised the same way with and without the effects")
	}
	ws, ns := monoSamples(t, ffmpeg, wOut), monoSamples(t, ffmpeg, nOut)
	// Before the first effect (with the limiter's look-ahead as margin) the two are the same programme.
	var worst float64
	for i := 0; i < 1900*48 && i < len(ws) && i < len(ns); i++ {
		worst = math.Max(worst, math.Abs(ws[i]-ns[i]))
	}
	if worst > 0.01 {
		t.Errorf("the programme before the first effect differs by %.4f", worst)
	}
	if p := peak(ws); p > 0.8913+0.01 {
		t.Errorf("the mix peaks at %.4f (%.2f dBFS), over the -1 dBFS limiter", p, db(p))
	}
	if after, before := rms(ws, 2000, 2400), rms(ns, 2000, 2400); after < before*1.2 {
		t.Errorf("the impact is not audible over the music: %.4f with, %.4f without", after, before)
	}
}

// Sound effects are ours: they are in every profile and the export, while a trend sound is still only a cue (D-03).
func TestSoundEffectsAndCueOnlyRules(t *testing.T) {
	trend := &timeline.TrendSound{
		AudioCommon: timeline.AudioCommon{ID: "t1", AtMs: 0, Export: timeline.ExportCueOnly},
		TrendID:     trendID,
	}
	spec := sfxSpec(30, trend, sfxItem("s1", timeline.SFXWhoosh, 500, nil))
	for _, prof := range Profiles() {
		plan := mustCompile(t, spec, prof)
		if !strings.Contains(plan.FilterComplex, "anoisesrc") || !strings.Contains(plan.FilterComplex, "[fx0]") {
			t.Errorf("%s: the effect is not in the graph: %s", prof.Name, plan.FilterComplex)
		}
		if len(plan.SoundCues) != 1 || plan.SoundCues[0].ItemID != "t1" {
			t.Errorf("%s: sound cues %+v", prof.Name, plan.SoundCues)
		}
		for _, in := range plan.Inputs {
			if in.Role == RoleAudio {
				t.Errorf("%s: an effect or a cue became an input: %+v", prof.Name, in)
			}
		}
		if strings.Contains(plan.FilterComplex, trendID) {
			t.Errorf("%s: the cue's trend id is in the graph", prof.Name)
		}
	}
}

func TestCompileRefusesHostileSoundEffects(t *testing.T) {
	for _, name := range []string{"whoosh;amovie=/etc/passwd", "boom", "", "WHOOSH", "whoosh,volume=99"} {
		spec := sfxSpec(30, &timeline.SFX{ID: "s1", Sfx: name, AtMs: 100, Export: timeline.ExportLicensed})
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("effect %q: err = %v", name, err)
		}
	}
	for _, g := range []float64{-31, 7, math.NaN(), math.Inf(1)} {
		spec := sfxSpec(30, sfxItem("s1", timeline.SFXImpact, 100, &g))
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("gain %v: err = %v", g, err)
		}
	}
	// An effect marked cue_only is not ours to refuse silently: the export rule must fit the kind.
	spec := sfxSpec(30, &timeline.SFX{ID: "s1", Sfx: timeline.SFXImpact, AtMs: 100, Export: timeline.ExportCueOnly})
	if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("cue_only effect: err = %v", err)
	}
	// An effect that starts at or after the end plays nothing and is left out.
	plan := mustCompile(t, sfxSpec(30, sfxItem("s1", timeline.SFXImpact, 4000, nil)), Final)
	if strings.Contains(plan.FilterComplex, "anoisesrc") || strings.Contains(plan.FilterComplex, "aevalsrc") {
		t.Error("an effect past the end is in the graph")
	}
	// The graph holds only the allowed alphabet, with every effect.
	all := sfxSpec(30,
		sfxItem("s1", timeline.SFXWhoosh, 100, nil), sfxItem("s2", timeline.SFXImpact, 600, ptr(-3.5)),
		sfxItem("s3", timeline.SFXRiser, 1200, nil), sfxItem("s4", timeline.SFXSwish, 3900, nil))
	g := mustCompile(t, all, Preview).FilterComplex
	if !graphAlphabet.MatchString(g) {
		t.Errorf("a character outside the alphabet: %q", regexpOutside.FindString(g))
	}
}
