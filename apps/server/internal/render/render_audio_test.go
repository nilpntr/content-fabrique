package render

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var timing = flag.Bool("timing", false, "render a 30 s timeline with both profiles and log how long it takes")

// ffmpegLog runs ffmpeg and returns what it printed on stderr.
func ffmpegLog(t *testing.T, ffmpeg string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffmpeg, args...) //nolint:gosec // test helper
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr.String())
	}
	return stderr.String()
}

var maxVolume = regexp.MustCompile(`max_volume: (-?[0-9.]+|-inf) dB`)

// peakDB is the loudest sample of the output's audio in dBFS (-91 or lower is digital silence).
func peakDB(t *testing.T, ffmpeg, file string) float64 {
	t.Helper()
	log := ffmpegLog(t, ffmpeg, "-hide_banner", "-nostats", "-i", file, "-vn", "-af", "volumedetect", "-f", "null", "-")
	m := maxVolume.FindStringSubmatch(log)
	if m == nil {
		t.Fatalf("no volumedetect result:\n%s", log)
	}
	if m[1] == "-inf" {
		return math.Inf(-1)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// loudness measures the integrated loudness and true peak of the output's audio the way loudnorm's first pass does.
func loudness(t *testing.T, ffmpeg, file string) (lufs, truePeak float64) {
	t.Helper()
	log := ffmpegLog(t, ffmpeg, "-hide_banner", "-nostats", "-i", file, "-vn", "-af", "loudnorm=I=-14:TP=-1:LRA=11:print_format=json", "-f", "null", "-")
	start, end := bytes.LastIndex([]byte(log), []byte("{")), bytes.LastIndex([]byte(log), []byte("}"))
	if start < 0 || end < start {
		t.Fatalf("no loudnorm statistics:\n%s", log)
	}
	var st loudnormStats
	if err := json.Unmarshal([]byte(log[start:end+1]), &st); err != nil {
		t.Fatal(err)
	}
	lufs, _ = strconv.ParseFloat(st.InputI, 64)
	truePeak, _ = strconv.ParseFloat(st.InputTP, 64)
	return lufs, truePeak
}

func renderFixture(t *testing.T, name string, prof Profile) (Plan, string) {
	t.Helper()
	dir, cat := ensureMedia(t)
	spec := loadFixture(t, name, cat)
	plan, err := Compile(spec, prof, mediaResolver(dir, cat))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: out}); err != nil {
		t.Fatal(err)
	}
	return plan, out
}

// The audio of a render, heard: a licensed track is in the mix and the final mix is at the loudness target; a timeline
// with nothing to play is silent; and the cue_only trend sound (D-03) is nowhere in the file, in either profile.
func TestRenderedAudio(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)

	t.Run("licensed track is mixed and normalised", func(t *testing.T) {
		_, out := renderFixture(t, "v26_audio_licensed_track", Final)
		lufs, tp := loudness(t, ffmpeg, out)
		if math.Abs(lufs-TargetLUFS) > 1.5 {
			t.Errorf("integrated loudness %.2f LUFS, want %d +-1.5", lufs, TargetLUFS)
		}
		if tp > TargetTP+0.5 {
			t.Errorf("true peak %.2f dBTP, want at most %d", tp, TargetTP)
		}
	})

	t.Run("preview mixes the track in a single pass", func(t *testing.T) {
		_, out := renderFixture(t, "v26_audio_licensed_track", Preview)
		if p := peakDB(t, ffmpeg, out); p < -20 {
			t.Errorf("peak %.1f dBFS: the licensed track is not audible", p)
		}
	})

	t.Run("no audio at all is silence", func(t *testing.T) {
		_, out := renderFixture(t, "v01_minimal_9x16", Final)
		if p := peakDB(t, ffmpeg, out); p > -80 {
			t.Errorf("peak %.1f dBFS, want silence", p)
		}
	})

	t.Run("cue_only trend sound is not in the final file", func(t *testing.T) {
		plan, out := renderFixture(t, "v25_audio_trend_sound_cue_only", Final)
		if p := peakDB(t, ffmpeg, out); p > -80 {
			t.Errorf("peak %.1f dBFS: something is playing, but the only sound is cue_only", p)
		}
		if len(plan.SoundCues) != 1 {
			t.Errorf("cues %+v", plan.SoundCues)
		}
	})

	t.Run("preview of the cue_only fixture holds the beat guide and nothing else", func(t *testing.T) {
		_, out := renderFixture(t, "v25_audio_trend_sound_cue_only", Preview)
		// Three markers: the clicks are -12 dBFS sines, mixed in after loudnorm.
		if p := peakDB(t, ffmpeg, out); p < -16 || p > -9 {
			t.Errorf("peak %.1f dBFS, want the beat guide at about -12", p)
		}
	})
}

// timedSpec is a 30 s edit: ten 3.2 s clips with a mix of cuts, crossfades and whips, speeds, looks, reframe modes, source
// audio on some clips, a licensed track and markers.
func timedSpec(cat timeline.MemoryCatalog) *timeline.Spec {
	videos := []string{}
	for _, id := range sortedAssetIDs(cat) {
		if cat[id].Kind == timeline.AssetVideo {
			videos = append(videos, id)
		}
	}
	looksList := []string{"none", "punchy", "warm", "cool", "bw", "matte"}
	modes := []string{timeline.ReframeCover, timeline.ReframeContain, timeline.ReframeBlurFill}
	var items []timeline.VideoItem
	var prevEnd int64
	for i := range 10 {
		it := clipAt(fmt.Sprintf("c%d", i+1), videos[i%len(videos)], int64(i)*2500, int64(i)*2500+3200, 0, withSourceAudio(-3))
		it.Look = &timeline.Look{Preset: looksList[i%len(looksList)]}
		it.Reframe = &timeline.Reframe{Mode: modes[i%len(modes)]}
		var trans int64
		switch {
		case i == 0:
		case i%3 == 1:
			withTransition(timeline.TransitionCrossfade, 300)(&it)
			trans = 300
		case i%3 == 2:
			withTransition(timeline.TransitionWhip, 200)(&it)
			trans = 200
		}
		it.AtMs = prevEnd - trans
		items = append(items, it)
		prevEnd = it.AtMs + timeline.OutputDuration(it)
	}
	at := prevEnd
	spec := specOf(30, items, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
		&timeline.LicensedTrack{AudioCommon: timeline.AudioCommon{ID: "s1", Export: timeline.ExportLicensed, FadeOutMs: ptr(int64(2000))}, AssetID: "ast_01j0h9rew727hk2pt0v5b31h44", LicenseID: "lic_01jg1bfdc146rw2b0vngmx6p6f"},
	}})
	for ms := int64(0); ms < at; ms += 500 {
		kind := timeline.MarkerBeat
		if ms%2000 == 0 {
			kind = timeline.MarkerDownbeat
		}
		spec.Markers = append(spec.Markers, timeline.Marker{AtMs: ms, Kind: kind})
	}
	return spec
}

// TestRenderTiming reports how long a 30 s edit takes to render (design note section 7: a preview in at most 10 s and a
// final in at most 60 s on the target host class). A timing assertion in CI would be flaky, so this only logs:
// go test ./internal/render -run TestRenderTiming -timing -v
func TestRenderTiming(t *testing.T) {
	if !*timing {
		t.Skip("run with -timing to measure")
	}
	ffmpegTools(t)
	dir, cat := ensureMedia(t)
	spec := timedSpec(cat)
	for _, prof := range Profiles() {
		plan, err := Compile(spec, prof, mediaResolver(dir, cat))
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "out.mp4")
		start := time.Now()
		if err := Execute(context.Background(), plan, testRunner(t), ExecuteOptions{Output: out}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.1f s of video (%d frames, %dx%d) rendered in %.1f s", prof.Name, float64(plan.DurationMs)/1000, plan.TotalFrames, plan.OutputSize.Width, plan.OutputSize.Height, time.Since(start).Seconds())
	}
}
