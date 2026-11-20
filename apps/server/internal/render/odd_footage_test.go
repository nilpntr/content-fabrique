package render

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The final render reads the originals, so it meets creator footage as it is: a variable frame rate with a rotation flag, an
// odd picture size in 4:4:4, a 10-bit HLG clip, a clip with a silent sound track, and a mono 8 kHz song. The export is the
// format's size at a constant frame rate, as long as the timeline, with stereo 48 kHz sound at the web loudness: nothing is
// dropped, stretched or left silent. And it carries no rotation flag: before the clip graph dropped the display matrix, the
// first clip's rotation reached the export after the join, and the export played sideways.
func TestFinalRenderOfOddOriginals(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	dir := t.TempDir()
	gen := func(name string, args ...string) string {
		t.Helper()
		out := filepath.Join(dir, name)
		cmd := exec.CommandContext(context.Background(), ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...), out)...) //nolint:gosec // test helper
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("this ffmpeg cannot make %s: %v\n%s", name, err, b)
		}
		return out
	}
	vfr := gen("vfr.mp4", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=3", "-f", "lavfi", "-i", "sine=frequency=300:duration=3",
		"-vf", "select='mod(n\\,34)'", "-fps_mode", "vfr", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac")
	rotated := gen("rotated.mov", "-display_rotation:v:0", "-90", "-i", vfr, "-c", "copy")
	odd := gen("odd.mkv", "-f", "lavfi", "-i", "testsrc2=size=321x241:rate=25:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv444p")
	hlg := gen("hlg.mp4", "-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=2", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "2",
		"-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc", "-c:v", "libx264", "-preset", "ultrafast",
		"-profile:v", "high10", "-pix_fmt", "yuv420p10le", "-c:a", "aac")
	track := gen("song.wav", "-f", "lavfi", "-i", "sine=frequency=220:sample_rate=8000:duration=8", "-ac", "1")

	files := map[string]string{"a_rot": rotated, "a_odd": odd, "a_hlg": hlg, assetMusic: track}
	resolver := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: files[id], HasAudio: id != "a_odd"}, nil
	})
	spec := specOf(30, []timeline.VideoItem{
		clipAt("c1", "a_rot", 0, 2000, 0, withMode(timeline.AudioModeBed)),
		clipAt("c2", "a_odd", 0, 1500, 2000),
		clipAt("c3", "a_hlg", 0, 1500, 3500, withMode(timeline.AudioModeFull)),
	}, &timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{song("m1", 0, 0)}})
	out, _ := renderSpec(t, spec, Final, resolver)

	m, err := media.Probe(context.Background(), testRunner(t), out)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 1080 || m.Height != 1920 || m.Rotation != 0 || m.FPS.Value != 30 || m.VariableFrameRate() || m.HDR {
		t.Errorf("export picture %+v", m)
	}
	if m.DurationMs < 4900 || m.DurationMs > 5100 {
		t.Errorf("export is %d ms, want 5000", m.DurationMs)
	}
	s, ok := m.SelectedAudioStream()
	if !ok || s.Channels != 2 || s.SampleRate != AudioSampleRate {
		t.Fatalf("export sound %+v", m.AudioStreams)
	}
	if lufs, _ := loudness(t, ffmpeg, out); lufs < -16 || lufs > -12 {
		t.Errorf("integrated loudness %.1f LUFS, want about -14", lufs)
	}
}
