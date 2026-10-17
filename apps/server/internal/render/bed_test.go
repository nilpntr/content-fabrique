package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The sound-ready export (P3-12): clips in bed mode are a low ambience, evened out across clips and not normalised up to the programme;
// an edit with no programme sound is a bed and the effects, normalised as a whole toward -17 LUFS and lifted by at most 8 dB.

// genLevelSource makes a 4 s clip whose sound is a noise-like 300 Hz+700 Hz mix at the given level (dBFS peak of each part).
func genLevelSource(t *testing.T, ffmpeg, name string, peakDB float64) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name+".mp4")
	a := num(math.Pow(10, peakDB/20) / 2)
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-y", //nolint:gosec // test helper
		"-f", "lavfi", "-i", "color=c=gray:s=640x360:r=30:d=4", "-f", "lavfi", "-i",
		"aevalsrc='"+a+"*(sin(2*PI*300*t)+sin(2*PI*733*t))|"+a+"*(sin(2*PI*300*t)+sin(2*PI*733*t))':s=48000:d=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "192k", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v\n%s", name, err, b)
	}
	return out
}

func withMode(mode string) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.Audio = &timeline.VideoAudio{Mode: mode} }
}

func TestBedModeCompilesToASoundReadyMix(t *testing.T) {
	a := clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeBed))
	b := clipAt("c2", assetB, 0, 2000, 2000, withMode(timeline.AudioModeBed))
	spec := specOf(30, []timeline.VideoItem{a, b}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{sfxItem("s1", timeline.SFXImpact, 500, nil)}})
	for _, prof := range Profiles() {
		plan := mustCompile(t, spec, prof)
		if !plan.SoundReady || plan.Bed == nil || len(plan.Bed.Clips) != 2 {
			t.Fatalf("%s: sound ready %v bed %+v", prof.Name, plan.SoundReady, plan.Bed)
		}
		ln := plan.Loudnorm
		if ln == nil || !ln.TwoPass || ln.MaxGainDB != SoundReadyMaxBoostDB || !strings.Contains(ln.Filter, "I=-17:TP=-2.5") || len(ln.Pass1Args) == 0 {
			t.Errorf("%s: loudnorm %+v", prof.Name, ln)
		}
		g := plan.FilterComplex
		for _, want := range []string{"volume@bed0=0dB", "volume@bed1=0dB", "afade=t=in:st=0:d=0.020", "afade=t=out:st=1.980:d=0.020", "[abed][as0][as1]amix=inputs=3"} {
			if !strings.Contains(g, want) {
				t.Errorf("%s: the graph lacks %q", prof.Name, want)
			}
		}
		// The export is limited under the peak ceiling after the normalisation, so the stem (the same core) is what the export is.
		if !strings.Contains(g, "aformat=sample_fmts=fltp:channel_layouts=stereo,alimiter=") {
			t.Errorf("%s: the sound-ready core has no limiter after the normalisation", prof.Name)
		}
		if strings.Contains(g, "loudnorm=I=-14") {
			t.Errorf("%s: the bed was normalised to the programme's -14", prof.Name)
		}
		if !graphAlphabet.MatchString(g) {
			t.Errorf("%s: a character outside the alphabet: %q", prof.Name, regexpOutside.FindString(g))
		}
		if len(plan.Bed.MeterArgs) == 0 || !strings.Contains(strings.Join(plan.Bed.MeterArgs, " "), "bedmeter_0.txt") {
			t.Errorf("%s: no evening pass: %v", prof.Name, plan.Bed.MeterArgs)
		}
		// The preview has the beat guide only when the spec has markers: with none, no stem.
		if plan.StemLabel != "" {
			t.Errorf("%s: a stem without a guide", prof.Name)
		}
	}
	// Crossfaded clips fade through their transition and not at their edges; a clip with the default mode is silent; full is the programme.
	spec = specOf(30, []timeline.VideoItem{a, clipAt("c2", assetB, 0, 2000, 1500, withMode(timeline.AudioModeBed), withTransition(timeline.TransitionCrossfade, 500))})
	if g := mustCompile(t, spec, Final).FilterComplex; strings.Count(g, "afade=t=in:st=0:d=0.020") != 1 {
		t.Errorf("a crossfaded bed clip also fades at its start:\n%s", PrettyFilter(g))
	}
	spec = specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeFull)), clipAt("c2", assetB, 0, 2000, 2000, withMode(timeline.AudioModeBed))})
	plan := mustCompile(t, spec, Final)
	if plan.SoundReady || plan.Bed == nil || !strings.Contains(plan.Loudnorm.Filter, "I=-14") {
		t.Errorf("speech plus a bed is the programme: sound ready %v loudnorm %+v", plan.SoundReady, plan.Loudnorm)
	}
	if plan.Loudnorm.MaxGainDB != 0 {
		t.Error("the programme's normalisation is not capped")
	}
	roles := []string{}
	for _, bc := range plan.Bed.Clips {
		roles = append(roles, bc.Role)
	}
	if strings.Join(roles, ",") != "ref,bed" {
		t.Errorf("roles %v: the speech clip is the bed's reference", roles)
	}
	// An unknown mode that skipped validation is refused, and a muted clip is silent.
	bad := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, func(it *timeline.VideoItem) { it.Audio = &timeline.VideoAudio{Mode: "loud;x"} })})
	if _, err := Compile(bad, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("a hostile mode: %v", err)
	}
	if plan := mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeMute))}), Final); plan.Loudnorm != nil || plan.Bed != nil {
		t.Error("a muted clip has sound")
	}
}

func TestMeasuredFilterCapsTheBoost(t *testing.T) {
	// A mix measured at -23.45 LUFS with a target of -17 is lifted by 6.45 dB: a cap of 5 dB lowers the target to -18.45 (linear loudnorm
	// applies target - measured and ignores the offset), a cap of 20 leaves it, and a cut is never capped.
	f, err := measuredFilter("loudnorm=I=-17:TP=-1.5:LRA=11", loudnormJSON, 5)
	if err != nil || !strings.HasPrefix(f, "loudnorm=I=-18.45:TP=-1.5") {
		t.Errorf("capped: %q %v", f, err)
	}
	f, err = measuredFilter("loudnorm=I=-17:TP=-1.5:LRA=11", loudnormJSON, 20)
	if err != nil || !strings.HasPrefix(f, "loudnorm=I=-17:") {
		t.Errorf("uncapped: %q %v", f, err)
	}
	f, err = measuredFilter("loudnorm=I=-30:TP=-1.5:LRA=11", loudnormJSON, 5)
	if err != nil || !strings.HasPrefix(f, "loudnorm=I=-30:") {
		t.Errorf("a cut: %q %v", f, err)
	}
	// A nearly silent mix is not normalised at all when it is capped (the single-pass filter would boost its hiss), and keeps the
	// single-pass filter when it is not.
	silent := strings.Replace(loudnormJSON, `"-23.45"`, `"-inf"`, 1)
	if f, err = measuredFilter("loudnorm=I=-17:TP=-1.5:LRA=11", silent, 8); err != nil || f != "anull" {
		t.Errorf("a silent capped mix: %q %v", f, err)
	}
	if f, err = measuredFilter("loudnorm=I=-14:TP=-1:LRA=11", silent, 0); err != nil || f != "" {
		t.Errorf("a silent programme: %q %v", f, err)
	}
}

func TestBedClipsAreEvenedOutAndTheMixIsSoundReady(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	quiet, loud := genLevelSource(t, ffmpeg, "quiet", -38), genLevelSource(t, ffmpeg, "loud", -14)
	byClip := map[string]string{assetA: quiet, assetB: loud}
	resolver := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: byClip[id], HasAudio: true}, nil
	})
	a := clipAt("c1", assetA, 0, 3000, 0, withMode(timeline.AudioModeBed))
	b := clipAt("c2", assetB, 0, 3000, 3000, withMode(timeline.AudioModeBed))
	spec := specOf(30, []timeline.VideoItem{a, b}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{sfxItem("s1", timeline.SFXImpact, 1500, nil), sfxItem("s2", timeline.SFXWhoosh, 4500, nil)}})
	for _, prof := range []Profile{Preview, Final} {
		t.Run(string(prof.Name), func(t *testing.T) {
			out, plan := renderSpec(t, spec, prof, resolver)
			if !plan.SoundReady {
				t.Fatal("not a sound-ready plan")
			}
			s := monoSamples(t, ffmpeg, out)
			// The two clips are 24 dB apart at the source; in the mix their beds are within 2 dB of each other (measured away from the
			// effects: 0.2-1.2 s and 3.2-4.2 s).
			qa, lb := db(rms(s, 200, 1200)), db(rms(s, 3200, 4200))
			if math.Abs(qa-lb) > 2 {
				t.Errorf("the beds are %.1f and %.1f dBFS: not evened out (24 dB apart at the source)", qa, lb)
			}
			lufs, tp := loudness(t, ffmpeg, out)
			if lufs < -19.5 || lufs > -15.5 {
				t.Errorf("integrated loudness %.1f LUFS, want the sound-ready range around -17", lufs)
			}
			if tp > -1.0 {
				t.Errorf("true peak %.2f dBTP", tp)
			}
			// The bed is low: far under the programme's -14 LUFS in the stretches without an effect.
			if bed := db(rms(s, 200, 1200)); bed > -20 {
				t.Errorf("the bed is %.1f dBFS RMS: not an ambience", bed)
			}
			// The effects sit on top of it.
			if hit, bed := db(rms(s, 1500, 1700)), db(rms(s, 200, 1200)); hit < bed+6 {
				t.Errorf("the impact is %.1f dBFS and the bed %.1f: it does not cut through", hit, bed)
			}
		})
	}
}

func TestPreviewStemHasNoBeatGuide(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	src := genLevelSource(t, ffmpeg, "bed", -34)
	resolver := ResolverFunc(func(string, Variant) (ResolvedAsset, error) { return ResolvedAsset{Location: src, HasAudio: true}, nil })
	spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 3000, 0, withMode(timeline.AudioModeBed))})
	spec.Markers = []timeline.Marker{{AtMs: 1000, Kind: timeline.MarkerBeat}, {AtMs: 2000, Kind: timeline.MarkerDownbeat}}
	plan, err := Compile(spec, Preview, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if plan.StemLabel != "astem" {
		t.Fatalf("stem label %q", plan.StemLabel)
	}
	dir := t.TempDir()
	mp4, stem := filepath.Join(dir, "out.mp4"), filepath.Join(dir, "stem.m4a")
	if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: mp4, StemOutput: stem}); err != nil {
		t.Fatal(err)
	}
	// Without a stem asked for, the pad is still read (by a discarding output) and the render succeeds; the printed command has it too.
	if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: filepath.Join(dir, "plain.mp4")}); err != nil {
		t.Fatalf("a render without a stem output: %v", err)
	}
	if a := plan.OutputArgs("x.mp4"); strings.Join(a[len(a)-8:], " ") != "-map [astem] -vn -c:a pcm_s16le -f null -" {
		t.Errorf("the printed command does not read the stem pad: %v", a)
	}
	withGuide, without := monoSamples(t, ffmpeg, mp4), monoSamples(t, ffmpeg, stem)
	// At the beat the preview has a 1 kHz click (30 ms, -12 dB under the guide's level); the stem has only the bed (300 and 733 Hz).
	band := func(x []float64) float64 {
		e := bandEnergies(x[1000*48:1030*48], [][2]float64{{900, 1100}})
		return 10 * math.Log10(math.Max(e[0], 1e-30))
	}
	if with, without := band(withGuide), band(without); with < without+15 {
		t.Errorf("energy at 1 kHz at the beat: %.1f dB in the preview and %.1f dB in the stem: the stem has the guide", with, without)
	}
	if lufsStem, _ := loudness(t, ffmpeg, stem); lufsStem < -30 || lufsStem > -14 {
		t.Errorf("the stem is at %.1f LUFS", lufsStem)
	}
	_ = fmt.Sprint
}

// A ramped clip in bed mode keeps its bed, at the clip's average speed; in full mode (speech) it stays muted.
func TestRampedBedClipKeepsItsBed(t *testing.T) {
	ramp := []timeline.SpeedPoint{{AtMs: 0, Speed: 2}, {AtMs: 1000, Speed: 2}, {AtMs: 1600, Speed: 0.5}}
	r := clipAt("c1", assetA, 1000, 3000, 0, withRamp(ramp...), withMode(timeline.AudioModeBed))
	plan := mustCompile(t, specOf(30, []timeline.VideoItem{r, clipAt("c2", assetB, 0, 2000, timeline.OutputDuration(r), withMode(timeline.AudioModeBed))}), Final)
	if plan.Bed == nil || len(plan.Bed.Clips) != 2 {
		t.Fatalf("bed %+v", plan.Bed)
	}
	g := plan.FilterComplex
	if !strings.Contains(g, "volume@bed0=0dB") || !strings.Contains(g, "atempo=1.078") { // 2000 ms of source in 1855 ms
		t.Errorf("the ramped clip has no bed at its average speed:\n%s", PrettyFilter(g))
	}
	full := clipAt("c1", assetA, 1000, 3000, 0, withRamp(ramp...), withMode(timeline.AudioModeFull))
	plan = mustCompile(t, specOf(30, []timeline.VideoItem{full, clipAt("c2", assetB, 0, 2000, timeline.OutputDuration(full), withMode(timeline.AudioModeBed))}), Final)
	if strings.Contains(plan.FilterComplex, "atempo=1.078") || len(plan.Bed.Clips) != 1 {
		t.Errorf("a speech clip with a ramp has sound: %d bed clips", len(plan.Bed.Clips))
	}
}
