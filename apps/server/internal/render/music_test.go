package render

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The music-led mix (P6-06, D-34): with a song in the export the clips' ambience bed is left out, speech stays with the song ducked under
// it, and the programme is normalised to -14 LUFS with the true peak under -1 dBTP; the song fades out at the end.

func song(id string, at, offset int64) *timeline.CreatorAudio {
	return &timeline.CreatorAudio{AudioCommon: timeline.AudioCommon{ID: id, AtMs: at, SourceOffsetMs: offset, Export: timeline.ExportLicensed}, AssetID: assetMusic}
}

func TestMusicLeavesTheBedOutAndDucksUnderSpeech(t *testing.T) {
	bed := specOf(30, []timeline.VideoItem{
		clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeBed)), clipAt("c2", assetB, 0, 2000, 2000, withMode(timeline.AudioModeBed)),
	}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{song("m1", 0, 1500)}})
	for _, prof := range Profiles() {
		plan := mustCompile(t, bed, prof)
		g := plan.FilterComplex
		if !plan.MusicLed || plan.SoundReady || plan.Bed != nil || strings.Contains(g, "volume@bed") || strings.Contains(g, "[0:a]") {
			t.Errorf("%s: the bed is in a music-led mix (music %v, sound ready %v, bed %+v):\n%s", prof.Name, plan.MusicLed, plan.SoundReady, plan.Bed, PrettyFilter(g))
		}
		if plan.Loudnorm == nil || !strings.Contains(plan.Loudnorm.Filter, "I=-14:TP=-1") {
			t.Errorf("%s: the music is not normalised: %+v", prof.Name, plan.Loudnorm)
		}
		// The song plays to the end: a short fade out, and a few ms of fade in.
		if !strings.Contains(g, "afade=t=in:st=0:d=0.020,afade=t=out:st=3.600:d=0.400") {
			t.Errorf("%s: no fades on the song:\n%s", prof.Name, PrettyFilter(g))
		}
		if strings.Contains(g, "sidechaincompress") {
			t.Errorf("%s: ducked with no speech", prof.Name)
		}
	}
	// Speech over the song: the song is ducked under it.
	speech := specOf(30, []timeline.VideoItem{
		clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeFull)), clipAt("c2", assetB, 0, 2000, 2000, withMode(timeline.AudioModeBed)),
	}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{song("m1", 0, 0)}})
	plan := mustCompile(t, speech, Final)
	g := plan.FilterComplex
	for _, want := range []string{"[as0]asplit=2[aspmix][aspkey]", "[aspkey]sidechaincompress=threshold=0.1:ratio=3:attack=15:release=250[amusicd]", "[abed][aspmix][amusicd]amix=inputs=3"} {
		if !strings.Contains(g, want) {
			t.Errorf("speech: the graph lacks %q:\n%s", want, PrettyFilter(g))
		}
	}
	if strings.Contains(g, "volume@bed") {
		t.Error("speech: the bed clip is in the mix")
	}
	if !graphAlphabet.MatchString(g) {
		t.Errorf("a character outside the alphabet: %q", regexpOutside.FindString(g))
	}
	// Without a song nothing changes: the bed is the sound-ready mix.
	if p := mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withMode(timeline.AudioModeBed))}), Final); p.MusicLed || !p.SoundReady {
		t.Errorf("no song: music %v sound ready %v", p.MusicLed, p.SoundReady)
	}
}

// A render with a song over two clips with their own (loud) sound in bed mode: the export is at -14 LUFS (within 1 LU) with its true
// peak at -1 dBTP or under, and none of the clips' tone is in it.
func TestAMusicLedExportIsAtTheWebLoudness(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	clip := genLevelSource(t, ffmpeg, "gym", -10)
	track := filepath.Join(t.TempDir(), "song.m4a")
	// A kick at 124 BPM and a bass, at a modest level: the normalisation brings it up.
	expr := "0.3*sin(2*PI*(48*mod(t,0.483871)+2.6*(1-exp(-mod(t,0.483871)*30))))*exp(-mod(t,0.483871)*9)+0.08*sin(2*PI*55*t)"
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", //nolint:gosec // test helper
		"aevalsrc='"+expr+"|"+expr+"':s=48000:d=10", "-c:a", "aac", "-b:a", "192k", track)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate the song: %v\n%s", err, b)
	}
	resolver := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		if id == assetMusic {
			return ResolvedAsset{Location: track, HasAudio: true}, nil
		}
		return ResolvedAsset{Location: clip, HasAudio: true}, nil
	})
	spec := specOf(30, []timeline.VideoItem{
		clipAt("c1", assetA, 0, 3000, 0, withMode(timeline.AudioModeBed)), clipAt("c2", assetB, 0, 3000, 3000, withMode(timeline.AudioModeBed)),
	}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{song("m1", 0, 1000)}})
	out, plan := renderSpec(t, spec, Final, resolver)
	if !plan.MusicLed {
		t.Fatal("not music-led")
	}
	lufs, tp := loudness(t, ffmpeg, out)
	if lufs < -15 || lufs > -13 {
		t.Errorf("integrated loudness %.1f LUFS, want -14 within 1 LU", lufs)
	}
	if tp > -0.9 {
		t.Errorf("true peak %.2f dBTP, want -1 or under", tp)
	}
	// The clips' tone (300 and 733 Hz) is not in the export: the song has nothing at 733 Hz.
	s := monoSamples(t, ffmpeg, out)
	e := bandEnergies(s[1000*48:2000*48], [][2]float64{{700, 770}, {40, 70}})
	if e[0] > e[1]/1000 {
		t.Errorf("the clips' sound is in the export: 733 Hz band %.3g against the bass %.3g", e[0], e[1])
	}
}
