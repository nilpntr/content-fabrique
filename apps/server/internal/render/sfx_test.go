package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var regexpOutside = regexp.MustCompile(`[^A-Za-z0-9_.,:;=\[\]'()+\-*/<>@ ]`)

func sfxItem(id, name string, at int64, gain *float64) *timeline.SFX {
	return &timeline.SFX{ID: id, Sfx: name, AtMs: at, Export: timeline.ExportLicensed, GainDB: gain}
}

func sfxVariantItem(id, name, variant string, at int64) *timeline.SFX {
	return &timeline.SFX{ID: id, Sfx: name, Variant: variant, AtMs: at, Export: timeline.ExportLicensed}
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

// sfxCeiling is the limiter's ceiling as a linear value, with the slack of the AAC encoder's rounding.
const sfxCeiling = 0.841395 + 0.012

// TestSoundEffectsAreHeardWhereTheyAre renders silent video with effects at known times and checks the audio: silence before
// and between them, energy in each effect's window, the effects' onsets within a frame of their at_ms, a riser that grows, a
// truncated effect at the end of the timeline, a gain that moves the level by its dB, a peak under the limiter, and the same
// samples on every render (no randomness).
func TestSoundEffectsAreHeardWhereTheyAre(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genSource(t, ffmpeg, "flat", "color=c=gray:s=640x360:r=30:d=5")
	resolver := oneFileResolver(src)

	spec := sfxSpec(30,
		sfxItem("s1", timeline.SFXWhoosh, 500, nil),
		sfxItem("s2", timeline.SFXImpact, 1500, nil),
		sfxItem("s3", timeline.SFXRiser, 2400, nil),
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

	// Silence where nothing plays: before the first effect, and after the whoosh until the impact.
	for _, w := range [][2]int{{0, 480}, {1200, 1480}} {
		if r := rms(s, w[0], w[1]); r > 1e-3 {
			t.Errorf("window %v ms should be silent, RMS %.5f (%.1f dBFS)", w, r, db(r))
		}
	}
	// Energy in each effect's own window.
	for name, w := range map[string][2]int{"whoosh": {550, 1050}, "impact": {1500, 1900}, "riser": {3100, 3700}, "swish": {3860, 3990}} {
		if r := rms(s, w[0], w[1]); r < 0.01 {
			t.Errorf("%s: window %v ms has RMS %.5f (%.1f dBFS), want at least -40 dBFS", name, w, r, db(r))
		}
	}
	// Each effect starts where its at_ms says, within a frame (33.3 ms at 30 fps): the first sample within 40 dB of the
	// effect's own peak, in the 100 ms before to the 100 ms after.
	for name, at := range map[string]int{"whoosh": 500, "impact": 1500, "riser": 2400} {
		lo, hi := (at-34)*48, (at+400)*48
		pk := peak(s[lo:hi])
		first := -1
		for i := lo; i < hi; i++ {
			if math.Abs(s[i]) >= pk*0.01 {
				first = i
				break
			}
		}
		if got := float64(first)/48 - float64(at); first < 0 || got < -33.4 || got > 33.4 {
			t.Errorf("%s: the onset is %.1f ms from at_ms, want within a frame (33.3 ms)", name, got)
		}
	}
	// The riser builds: its last 300 ms are much louder than its first.
	if early, late := rms(s, 2400, 2700), rms(s, 3600, 3900); late < 2.5*early {
		t.Errorf("the riser does not build: %.5f at the start, %.5f at the end", early, late)
	}
	// An impact decays: its first 150 ms are louder than the 150 ms that start 250 ms later.
	if first, rest := rms(s, 1500, 1650), rms(s, 1750, 1900); first < 1.5*rest {
		t.Errorf("the impact does not decay: %.5f then %.5f", first, rest)
	}
	// The limiter: nothing over the ceiling, and the effects are not tiny.
	if p := peak(s); p > sfxCeiling || p < 0.1 {
		t.Errorf("peak %.4f (%.1f dBFS), want between -20 dBFS and %.1f dBFS", p, db(p), sfxLimitDB)
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

// TestLimiterKeepsTimeAndLength pins what the mix relies on (issue #132, item 1): alimiter with latency=1 compensates its own
// look-ahead delay. An impulse that is below the ceiling comes out on the sample it went in on, the stream keeps its length, and one
// above the ceiling is brought down to it. If another ffmpeg changes either, this fails and the sound would drift from the picture.
func TestLimiterKeepsTimeAndLength(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	for _, tc := range []struct {
		name string
		amp  float64
		want float64
	}{{"below the ceiling", 0.5, 0.5 / math.Sqrt2}, {"above the ceiling", 1.4, 0.841395}} {
		graph := fmt.Sprintf("aevalsrc='if(eq(n,48000),%s,0)':s=48000:d=2,%s,%s[aout]", num(tc.amp), stereo48, sfxLimiter)
		// stereo48 upmixes a mono source with -3 dB per channel, which is why 0.5 comes out as 0.354.
		s := decodeGraph(t, ffmpeg, graph)
		if n := len(s.l); n != 96000 {
			t.Errorf("%s: %d samples out, want 96000: the limiter changed the length", tc.name, n)
		}
		at, pk := 0, 0.0
		for i, v := range s.l {
			if math.Abs(v) > pk {
				pk, at = math.Abs(v), i
			}
		}
		if at != 48000 {
			t.Errorf("%s: the impulse comes out at sample %d, want 48000: the limiter delays the sound", tc.name, at)
		}
		if math.Abs(pk-tc.want) > 0.02 {
			t.Errorf("%s: the impulse comes out at %.3f, want %.3f", tc.name, pk, tc.want)
		}
	}
}

// An effect on top of a normalised programme does not move the programme: the part of the render before the first effect is the
// same with and without them, the mix stays under the limiter, and its true peak stays under -1 dBTP (measured with ebur128).
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
	// Before the first effect the two are the same programme.
	var worst float64
	for i := 0; i < 1900*48 && i < len(ws) && i < len(ns); i++ {
		worst = math.Max(worst, math.Abs(ws[i]-ns[i]))
	}
	if worst > 0.01 {
		t.Errorf("the programme before the first effect differs by %.4f", worst)
	}
	if p := peak(ws); p > sfxCeiling {
		t.Errorf("the mix peaks at %.4f (%.2f dBFS), over the %.1f dBFS limiter", p, db(p), sfxLimitDB)
	}
	if _, tp := loudness(t, ffmpeg, wOut); tp > -1.0 {
		t.Errorf("true peak of the mix %.2f dBTP, want at most -1 (the limiter is at %.1f dBFS sample peak to leave room for inter-sample peaks and the AAC encoder)", tp, sfxLimitDB)
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
			t.Errorf("%s: the effect is not in the graph: %.300s", prof.Name, plan.FilterComplex)
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
	for _, name := range []string{"whoosh;amovie=/etc/passwd", "boom", "", "WHOOSH", "whoosh,volume=99", "whoosh ", "[fx0]", "sub_drop;x"} {
		spec := sfxSpec(30, &timeline.SFX{ID: "s1", Sfx: name, AtMs: 100, Export: timeline.ExportLicensed})
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("effect %q: err = %v", name, err)
		}
	}
	// Variants: another category's, hostile text, case, and the empty and auto spellings that are fine.
	for _, tc := range [][2]string{
		{"whoosh", "punch"}, {"impact", "air"}, {"whoosh", "air;amovie=/etc/passwd"}, {"whoosh", "air,volume=99"}, {"click", "AUTO"},
		{"riser", "../../etc/passwd"}, {"sting", "hit "}, {"pop", "[fx0]"}, {"glitch", "stutter'"},
	} {
		spec := sfxSpec(30, sfxVariantItem("s1", tc[0], tc[1], 100))
		if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s variant %q: err = %v", tc[0], tc[1], err)
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
	// An id with hostile text reaches the graph only as a hash: the jitter is numbers.
	hostileID := "s1;amovie=/etc/passwd,volume=99"
	g := mustCompile(t, sfxSpec(30, &timeline.SFX{ID: hostileID, Sfx: timeline.SFXWhoosh, Variant: timeline.SFXVariantAuto, AtMs: 100, Export: timeline.ExportLicensed}), Preview).FilterComplex
	if strings.Contains(g, "passwd") || !graphAlphabet.MatchString(g) {
		t.Errorf("the item id reached the graph: %q", regexpOutside.FindString(g))
	}
	// The graph holds only the allowed alphabet, with every category and every variant, then the round-robin and the old spelling
	// (two timelines: one graph of all of them together is over the limit of one process argument).
	var variants, rest []timeline.AudioItem
	at := int64(0)
	for _, cv := range sfxVariants() {
		variants = append(variants, sfxVariantItem(fmt.Sprintf("s%d", len(variants)+1), cv[0], cv[1], at))
		at += 90
	}
	for _, c := range timeline.SFXCategories() {
		rest = append(rest, sfxVariantItem(fmt.Sprintf("a%d", len(rest)+1), c, timeline.SFXVariantAuto, at), sfxItem(fmt.Sprintf("a%d", len(rest)+2), c, at, nil))
		at += 20
	}
	for _, items := range [][]timeline.AudioItem{variants, rest} {
		for _, prof := range Profiles() {
			g := mustCompile(t, sfxSpec(30, items...), prof).FilterComplex
			if !graphAlphabet.MatchString(g) {
				t.Errorf("%s: a character outside the alphabet: %q", prof.Name, regexpOutside.FindString(g))
			}
			if len(g) > MaxFilterGraphBytes {
				t.Errorf("%s: the graph is %d bytes", prof.Name, len(g))
			}
		}
	}
}

// One argument of a process holds 128 KiB on Linux: a graph past MaxFilterGraphBytes is refused at compile time, with a message,
// and not by the sandbox with "argument list too long".
func TestCompileRefusesAGraphTooLongForAnArgument(t *testing.T) {
	var items []timeline.AudioItem
	for i := range 60 {
		items = append(items, sfxVariantItem(fmt.Sprintf("s%d", i+1), timeline.SFXShimmer, "sparkle", int64(i)*50))
	}
	_, err := Compile(sfxSpec(30, items...), Final, plainResolver())
	if !errors.Is(err, ErrInvalidSpec) || !strings.Contains(err.Error(), "filter graph") {
		t.Errorf("60 shimmers: err = %v", err)
	}
	// Thirty mixed effects, a lot for a 4 s clip, compile.
	items = items[:0]
	for i, cv := range sfxVariants()[:30] {
		items = append(items, sfxVariantItem(fmt.Sprintf("s%d", i+1), cv[0], cv[1], int64(i)*100))
	}
	if plan, err := Compile(sfxSpec(30, items...), Final, plainResolver()); err != nil || len(plan.FilterComplex) > MaxFilterGraphBytes {
		t.Errorf("30 effects: %v", err)
	}
}

// Every category and variant, and the round-robin and the old spellings, render together in one timeline: the graph is valid
// for ffmpeg, long as it is, and every effect is where it was put.
func TestEveryCategoryAndVariantRendersInOneTimeline(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genSource(t, ffmpeg, "flat", "color=c=gray:s=640x360:r=30:d=45")
	var items []timeline.AudioItem
	var ats []int64
	at := int64(500)
	for _, cv := range sfxVariants() {
		items = append(items, sfxVariantItem(fmt.Sprintf("s%d", len(items)+1), cv[0], cv[1], at))
		ats = append(ats, at)
		at += 1000
	}
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, at+1500, 0)}, &timeline.AudioTrack{ID: "sound", Items: items})
	out, _ := renderSpec(t, spec, Preview, oneFileResolver(src))
	s := monoSamples(t, ffmpeg, out)
	for i, at := range ats {
		if r := rms(s, int(at)-5, int(at)+300); r < 3e-4 {
			sfx := sfxVariants()[i]
			t.Errorf("%s/%s: nothing is heard at %d ms (RMS %.6f)", sfx[0], sfx[1], at, r)
		}
	}
	if p := peak(s); p > sfxCeiling {
		t.Errorf("peak %.4f over the limiter", p)
	}
}

// genToneSource makes a 6 s clip with a 1 kHz tone as its sound: a stand-in for a clip with speech, whose level the ducking test
// measures at the tone's frequency.
func genToneSource(t *testing.T, ffmpeg string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tone.mp4")
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-y", //nolint:gosec // test helper
		"-f", "lavfi", "-i", "color=c=gray:s=640x360:r=30:d=6", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate the tone source: %v\n%s", err, b)
	}
	return out
}

// toneDB is the level of the 1 kHz tone in x between a and b ms, in dB.
func toneDB(x []float64, aMs, bMs int) float64 {
	e := bandEnergies(x[aMs*48:bMs*48], [][2]float64{{940, 1060}})
	return 10 * math.Log10(math.Max(e[0], 1e-30))
}

// TestSoundEffectsDuckSpeech: where an effect overlaps speech from a clip's own sound, the programme is ducked a few dB under
// it (3 to 6 dB at the hit's peak), recovers after it, and is not touched before it; an effect that does not overlap speech, a
// detail effect (a click) and a timeline without speech duck nothing. The same effect and the same sound are rendered with and
// without the transcript, so the difference is the ducking alone.
func TestSoundEffectsDuckSpeech(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genToneSource(t, ffmpeg)
	resolver := func(words bool) AssetResolver {
		return ResolverFunc(func(string, Variant) (ResolvedAsset, error) {
			r := ResolvedAsset{Location: src, HasAudio: true}
			if words {
				r.Transcript = []Word{{Text: "one", StartMs: 800, EndMs: 1400}, {Text: "two", StartMs: 1500, EndMs: 2300}}
			}
			return r, nil
		})
	}
	render := func(words bool, items ...timeline.AudioItem) []float64 {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 6000, 0, withSourceAudio(0))}, &timeline.AudioTrack{ID: "sound", Items: items})
		out, plan := renderSpec(t, spec, Final, resolver(words))
		if words && len(items) > 0 {
			t.Logf("ducking in the graph: %v", strings.Contains(plan.FilterComplex, "sidechaincompress"))
		}
		return monoSamples(t, ffmpeg, out)
	}
	hit := sfxItem("s1", timeline.SFXImpact, 1000, nil)
	ducked, free := render(true, hit), render(false, hit)
	// Before the hit the programme is the same, around the hit it is lower, and well after it it has recovered.
	if d := toneDB(free, 300, 700) - toneDB(ducked, 300, 700); math.Abs(d) > 0.3 {
		t.Errorf("before the hit the programme differs by %.2f dB", d)
	}
	around := toneDB(free, 1010, 1110) - toneDB(ducked, 1010, 1110)
	if around < 2.5 || around > 7 {
		t.Errorf("the programme is ducked by %.2f dB around the hit, want 2.5 to 7", around)
	}
	if d := toneDB(free, 3500, 5500) - toneDB(ducked, 3500, 5500); math.Abs(d) > 0.3 {
		t.Errorf("long after the hit the programme is still %.2f dB down", d)
	}
	// No overlap with speech (the words end at 2300 ms): nothing is ducked, and the graph has no sidechain.
	late := sfxItem("s1", timeline.SFXImpact, 4000, nil)
	if d := toneDB(render(false, late), 4010, 4110) - toneDB(render(true, late), 4010, 4110); math.Abs(d) > 0.3 {
		t.Errorf("an effect that does not overlap speech ducked the programme by %.2f dB", d)
	}
	// A detail effect (a click) is too small to cover a word: no ducking.
	click := sfxItem("s1", timeline.SFXClick, 1000, nil)
	if d := toneDB(render(false, click), 1010, 1110) - toneDB(render(true, click), 1010, 1110); math.Abs(d) > 0.3 {
		t.Errorf("a click ducked the programme by %.2f dB", d)
	}
	// The compiled graph: a sidechain only where an effect overlaps speech.
	for _, tc := range []struct {
		name  string
		words bool
		item  timeline.AudioItem
		want  bool
	}{
		{"overlap", true, hit, true}, {"no transcript", false, hit, false}, {"no overlap", true, late, false}, {"a click", true, click, false},
	} {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 6000, 0, withSourceAudio(0))}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{tc.item}})
		plan, err := Compile(spec, Final, resolver(tc.words))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(plan.FilterComplex, "sidechaincompress"); got != tc.want {
			t.Errorf("%s: sidechain in the graph %v, want %v", tc.name, got, tc.want)
		}
		if !graphAlphabet.MatchString(plan.FilterComplex) {
			t.Errorf("%s: a character outside the alphabet: %q", tc.name, regexpOutside.FindString(plan.FilterComplex))
		}
	}
}
