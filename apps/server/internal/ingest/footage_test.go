package ingest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// devWords are words a creator should never have to read in a reason.
var devWords = regexp.MustCompile(`(?i)\b(codec|ffmpeg|ffprobe|stream|container|moov|atom|pixel|fps|hz|khz|metadata|decod\w*|h\.?264|hevc|pq|hlg|bt\.?709|bitrate|sample rate)\b`)

func checkPlainWords(t *testing.T, a Assessment) {
	t.Helper()
	for _, f := range a.Findings {
		if f.Message == "" || f.Code == "" {
			t.Errorf("finding without a code or a message: %+v", f)
		}
		if w := devWords.FindString(f.Message); w != "" {
			t.Errorf("%s: the reason uses %q: %s", f.Code, w, f.Message)
		}
		for _, colour := range []string{"red", "green"} {
			if strings.Contains(strings.ToLower(f.Message), colour) {
				t.Errorf("%s: the reason names a colour: %s", f.Code, f.Message)
			}
		}
	}
}

func phone() media.Metadata {
	aac := 1
	return media.Metadata{
		Container: "mov", DurationMs: 12000, HasVideo: true, HasAudio: true, Width: 1080, Height: 1920, CodedWidth: 1920, CodedHeight: 1080,
		FPS: media.FPS{Rational: media.Rational{Num: 30, Den: 1}, Value: 30}, Codecs: media.Codecs{Video: "h264", Audio: []string{"aac"}},
		AudioStreams:  []media.AudioStream{{Index: 1, Codec: "aac", Channels: 2, SampleRate: 48000, Default: true, Decodable: true}},
		SelectedAudio: &aac,
	}
}

func song() media.Metadata {
	a := 0
	return media.Metadata{
		Container: "mp3", DurationMs: 180000, HasAudio: true, Codecs: media.Codecs{Audio: []string{"mp3"}},
		AudioStreams:  []media.AudioStream{{Index: 0, Codec: "mp3", Channels: 2, SampleRate: 44100, Default: true, Decodable: true}},
		SelectedAudio: &a,
	}
}

// Every case of the footage check, judged from the probe and what a decode measured.
func TestAssessCases(t *testing.T) {
	loud := Measured{Decoded: true, PlayableMs: 12000, SoundChecked: true, Peak: 0.5}
	with := func(f func(*media.Metadata)) media.Metadata { m := phone(); f(&m); return m }
	withSong := func(f func(*media.Metadata)) media.Metadata { m := song(); f(&m); return m }
	for _, c := range []struct {
		name    string
		use     Use
		m       media.Metadata
		x       Measured
		verdict Verdict
		code    string // a finding that must be there; "" for none at all
	}{
		{"an upright phone clip", UseClip, phone(), loud, Usable, ""},
		{"no picture", UseClip, song(), Measured{}, Unusable, CodeNoPicture},
		{"cannot tell the length", UseClip, with(func(m *media.Metadata) { m.DurationMs = 0 }), Measured{}, Unusable, CodeNoDuration},
		{"over the limit", UseClip, with(func(m *media.Metadata) { m.DurationMs = int64(31 * time.Minute / time.Millisecond) }), Measured{}, Unusable, CodeTooLong},
		{"a long clip", UseClip, with(func(m *media.Metadata) { m.DurationMs = int64(12 * time.Minute / time.Millisecond) }), Measured{}, UsableWithWarning, CodeLong},
		{"too short to show", UseClip, with(func(m *media.Metadata) { m.DurationMs = 66 }), Measured{}, Unusable, CodeTooShort},
		{"a flash", UseClip, with(func(m *media.Metadata) { m.DurationMs = 500 }), Measured{}, UsableWithWarning, CodeShort},
		{"a picture too small", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 8, 8 }), Measured{}, Unusable, CodeSize},
		{"nothing decodes", UseClip, phone(), Measured{Decoded: true, Damaged: true}, Unusable, CodeCorrupt},
		{"cut off part way", UseClip, phone(), Measured{Decoded: true, PlayableMs: 4800, Damaged: true}, UsableWithWarning, CodeCutOff},
		{"almost nothing plays", UseClip, phone(), Measured{Decoded: true, PlayableMs: 66, Damaged: true}, Unusable, CodeCorrupt},
		{"a few damaged frames", UseClip, phone(), Measured{Decoded: true, PlayableMs: 12000, Damaged: true}, UsableWithWarning, CodeDamagedFrames},
		{"variable frame rate", UseClip, with(func(m *media.Metadata) { m.FPS.Value = 29.13 }), loud, Usable, CodeVariableRate},
		{"slow motion", UseClip, with(func(m *media.Metadata) { m.FPS.Value = 240 }), loud, Usable, CodeSlowMotion},
		{"HDR HLG", UseClip, with(func(m *media.Metadata) { m.HDR, m.HDRFormat = true, "hlg" }), loud, Usable, CodeHLG},
		{"HDR PQ", UseClip, with(func(m *media.Metadata) { m.HDR, m.HDRFormat = true, "pq" }), loud, UsableWithWarning, CodePQ},
		{"rotated", UseClip, with(func(m *media.Metadata) { m.Rotation = -90 }), loud, Usable, CodeRotated},
		{"landscape", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 1920, 1080 }), loud, UsableWithWarning, CodeLandscape},
		{"square", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 1080, 1080 }), loud, Usable, CodeSquare},
		{"a thin strip", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 1280, 160 }), loud, UsableWithWarning, CodeExtremeShape},
		{"a tall strip", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 160, 1280 }), loud, UsableWithWarning, CodeExtremeShape},
		{"a screen recording as a clip", UseClip, with(func(m *media.Metadata) { m.Width, m.Height = 886, 1920 }), loud, Usable, CodeScreenRecording},
		{"no sound track", UseClip, with(func(m *media.Metadata) { m.SelectedAudio, m.AudioStreams, m.HasAudio = nil, nil, false }), Measured{}, Usable, CodeNoSound},
		{"silent sound", UseClip, phone(), Measured{Decoded: true, PlayableMs: 12000, SoundChecked: true, Peak: 0.0002}, Usable, CodeSilent},
		{"unreadable sound", UseClip, phone(), Measured{Decoded: true, PlayableMs: 12000, SoundChecked: true, SoundUnreadable: true}, UsableWithWarning, CodeSoundUnreadable},

		{"a song", UseMusic, song(), Measured{SoundChecked: true, Peak: 0.9}, Usable, ""},
		{"music without sound", UseMusic, withSong(func(m *media.Metadata) { m.SelectedAudio = nil }), Measured{}, Unusable, CodeNoSound},
		{"silent music", UseMusic, song(), Measured{SoundChecked: true, Peak: 0}, Unusable, CodeSilent},
		{"unreadable music", UseMusic, song(), Measured{SoundChecked: true, SoundUnreadable: true}, Unusable, CodeSoundUnreadable},
		{"phone-call quality", UseMusic, withSong(func(m *media.Metadata) { m.AudioStreams[0].SampleRate = 8000 }), Measured{SoundChecked: true, Peak: 0.9}, UsableWithWarning, CodeLowSampleRate},
		{"mono song", UseMusic, withSong(func(m *media.Metadata) { m.AudioStreams[0].Channels = 1 }), Measured{SoundChecked: true, Peak: 0.9}, Usable, CodeMono},
		{"an odd but fine rate", UseMusic, withSong(func(m *media.Metadata) { m.AudioStreams[0].SampleRate = 96000; m.AudioStreams[0].Channels = 6 }), Measured{SoundChecked: true, Peak: 0.9}, Usable, ""},
		{"a jingle", UseMusic, withSong(func(m *media.Metadata) { m.DurationMs = 2000 }), Measured{SoundChecked: true, Peak: 0.9}, UsableWithWarning, CodeShort},
		{"a screen recording as music", UseMusic, with(func(m *media.Metadata) { m.Width, m.Height = 886, 1920; m.FPS.Value = 41.7 }), loud, UsableWithWarning, CodeScreenRecording},
		{"a camera video as music", UseMusic, phone(), loud, UsableWithWarning, CodeFromVideo},
		{"music too long", UseMusic, withSong(func(m *media.Metadata) { m.DurationMs = int64(40 * time.Minute / time.Millisecond) }), Measured{}, Unusable, CodeTooLong},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := Assess(c.use, c.m, c.x, 0)
			if a.Verdict != c.verdict {
				t.Errorf("verdict %s, want %s: %+v", a.Verdict, c.verdict, a.Findings)
			}
			if c.code == "" && len(a.Findings) != 0 {
				t.Errorf("want no findings, got %+v", a.Findings)
			}
			if c.code != "" && !a.Has(c.code) {
				t.Errorf("no %s finding: %+v", c.code, a.Findings)
			}
			if a.Use != c.use {
				t.Errorf("use %q", a.Use)
			}
			checkPlainWords(t, a)
		})
	}
}

// Every ingest failure code has a plain reason, and an unknown one still gets one.
func TestFailureAssessmentCoversEveryCode(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range []string{store.FailureUnsupportedFormat, store.FailureCorruptMedia, store.FailureNoVideo, store.FailureNoAudio,
		store.FailureUndecodableVideo, store.FailureNoDuration, store.FailureTooLong, store.FailureResolution, "something_new"} {
		a := FailureAssessment(UseClip, reason, 0)
		if a.Verdict != Unusable || len(a.Findings) != 1 {
			t.Fatalf("%s: %+v", reason, a)
		}
		checkPlainWords(t, a)
		if reason != "something_new" {
			if seen[a.Findings[0].Message] {
				t.Errorf("%s shares its reason with another code", reason)
			}
			seen[a.Findings[0].Message] = true
		}
	}
	if m := FailureAssessment(UseClip, store.FailureTooLong, 30*time.Minute).Findings[0].Message; !strings.Contains(m, "30 minutes") {
		t.Errorf("the limit is not named: %s", m)
	}
}

func TestDuplicateFindingWarns(t *testing.T) {
	a := Assess(UseClip, phone(), Measured{}, 0)
	a.Add(DuplicateFinding("gym1.mov"))
	if a.Verdict != UsableWithWarning || !strings.Contains(a.Findings[len(a.Findings)-1].Message, "gym1.mov") {
		t.Fatalf("%+v", a)
	}
	a.Add(Finding{Code: "x", Level: Usable, Message: "a note"})
	if a.Verdict != UsableWithWarning {
		t.Fatal("a note must not raise the verdict back")
	}
}

// A proxy of which almost nothing plays is a damaged file, not a clip of a few milliseconds; a clip that is really that short
// is fine.
func TestCheckProxyRefusesARemnant(t *testing.T) {
	proxy := media.Metadata{HasVideo: true, Codecs: media.Codecs{Video: "h264"}, Width: 360, Height: 640, DurationMs: 66}
	src := media.Metadata{HasVideo: true, Width: 360, Height: 640, DurationMs: 60000}
	if err := checkProxy(proxy, src); err == nil {
		t.Fatal("a 66 ms proxy of a 60 s clip was accepted")
	}
	src.DurationMs = 66
	if err := checkProxy(proxy, src); err != nil {
		t.Fatalf("a 66 ms clip: %v", err)
	}
}

func TestPlayableMs(t *testing.T) {
	for _, c := range []struct{ probed, proxy, want int64 }{
		{12000, 12040, 12000}, // the proxy is a frame longer: the probe stands
		{12000, 11500, 12000}, // under a second short
		{60000, 58500, 60000}, // over a second but under 5 %
		{6000, 2400, 2400},    // cut off
		{6000, 0, 6000},       // no proxy length
	} {
		if got := playableMs(c.probed, c.proxy); got != c.want {
			t.Errorf("playableMs(%d, %d) = %d, want %d", c.probed, c.proxy, got, c.want)
		}
	}
}

// ---- generated fixtures ---------------------------------------------------------------------------------------------------

// footage makes the test files of the robustness cases, each a few kilobytes, with the host's ffmpeg.
type footage struct {
	h *harness
}

func (f footage) gen(name string, args ...string) string {
	f.h.t.Helper()
	out := filepath.Join(f.h.clips, name)
	ffmpeg(f.h.t, append(args, out)...)
	return out
}

func (f footage) write(name string, data []byte) string {
	f.h.t.Helper()
	p := filepath.Join(f.h.clips, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.h.t.Fatal(err)
	}
	return p
}

const (
	src320 = "testsrc2=size=320x240:rate=30:duration="
	tone   = "sine=frequency=440:sample_rate=48000:duration="
)

func x264() []string {
	return []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
}

func (f footage) clip(name, seconds string, extra ...string) string {
	a := []string{"-f", "lavfi", "-i", src320 + seconds, "-f", "lavfi", "-i", tone + seconds}
	a = append(a, x264()...)
	return f.gen(name, append(append(a, "-c:a", "aac"), extra...)...)
}

// variableRate drops every 34th frame of a 30 fps clip, the way a phone skips frames in low light: the average rate is
// 29.1 while the stream says 30.
func (f footage) variableRate(name string) string {
	a := []string{"-f", "lavfi", "-i", src320 + "4", "-f", "lavfi", "-i", tone + "4", "-vf", "select='mod(n\\,34)'", "-fps_mode", "vfr"}
	return f.gen(name, append(append(a, x264()...), "-c:a", "aac")...)
}

// screenRecording is a phone-screen shaped (886x1920 scaled down to 222x480) clip with sound.
func (f footage) screenRecording(name string) string {
	a := []string{"-f", "lavfi", "-i", "testsrc2=size=222x480:rate=30:duration=6", "-f", "lavfi", "-i", tone + "6"}
	return f.gen(name, append(append(a, x264()...), "-c:a", "aac")...)
}

func (f footage) truncated(name string, faststart bool) string {
	var extra []string
	if faststart {
		extra = []string{"-movflags", "+faststart"}
	}
	full := f.clip("full-"+name, "6", extra...)
	b, err := os.ReadFile(full)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return f.write(name, b[:len(b)*4/10])
}

func checkFile(t *testing.T, h *harness, path string, use Use) Assessment {
	t.Helper()
	work := filepath.Join(h.root, "check")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := Check(context.Background(), h.p.cfg.Runner, path, CheckOptions{Use: use, WorkDir: work})
	if err != nil {
		t.Fatalf("check %s: %v", filepath.Base(path), err)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("the check left %d entries in its work directory", len(entries))
	}
	checkPlainWords(t, a)
	return a
}

// The footage check on a real (tiny, generated) file of every case.
func TestCheckGeneratedFootage(t *testing.T) {
	h := newHarness(t)
	f := footage{h}
	junk := bytes.Repeat([]byte("this is not a video. "), 500)
	for _, c := range []struct {
		name    string
		path    func() string
		use     Use
		verdict Verdict
		code    string
	}{
		{"random bytes", func() string { return f.write("junk.mp4", junk) }, "", Unusable, ""},
		{"an empty file", func() string { return f.write("empty.mov", nil) }, "", Unusable, CodeEmpty},
		{"cut off before the index", func() string { return f.truncated("cut.mp4", false) }, "", Unusable, CodeCorrupt},
		{"cut off after the index", func() string { return f.truncated("cutfast.mp4", true) }, "", UsableWithWarning, CodeCutOff},
		{"a variable frame rate", func() string { return f.variableRate("vfr.mp4") }, "", UsableWithWarning, CodeVariableRate}, // landscape too
		{"HDR HLG", func() string {
			return h.makeClip(clip{name: "hlg", w: 1080, h: 1920, seconds: 2, hdr: "hlg", audio: true})
		}, "", Usable, CodeHLG},
		{"SDR", func() string { return h.makeClip(clip{name: "sdr", w: 1080, h: 1920, seconds: 2, audio: true}) }, "", Usable, ""},
		{"HDR PQ", func() string {
			return h.makeClip(clip{name: "pq", w: 1080, h: 1920, seconds: 2, hdr: "pq", audio: true})
		}, "", UsableWithWarning, CodePQ},
		{"a rotation flag", func() string {
			return h.makeClip(clip{name: "rot", w: 1920, h: 1080, seconds: 2, rotate: -90, audio: true})
		}, "", Usable, CodeRotated},
		{"no sound track", func() string { return h.makeClip(clip{name: "mute", w: 1080, h: 1920, seconds: 2}) }, "", Usable, CodeNoSound},
		{"a silent sound track", func() string {
			return f.gen("silent.mp4", append(append([]string{"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=2", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "2"}, x264()...), "-c:a", "aac")...)
		}, "", Usable, CodeSilent},
		{"sound longer than the picture", func() string {
			return f.gen("longsound.mp4", append(append([]string{"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=3", "-f", "lavfi", "-i", tone + "8"}, x264()...), "-c:a", "aac")...)
		}, "", Usable, ""},
		{"too short to show", func() string {
			return f.gen("blink.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=0.04"}, x264()...)...)
		}, "", Unusable, CodeTooShort},
		{"a flash", func() string {
			return f.gen("flash.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=0.5"}, x264()...)...)
		}, "", UsableWithWarning, CodeShort},
		{"an odd size", func() string {
			return f.gen("odd.mkv", "-f", "lavfi", "-i", "testsrc2=size=321x241:rate=30:duration=1", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv444p")
		}, "", UsableWithWarning, CodeLandscape},
		{"a thin strip", func() string {
			return f.gen("strip.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=1280x160:rate=30:duration=1"}, x264()...)...)
		}, "", UsableWithWarning, CodeExtremeShape},
		{"over the length limit", func() string {
			return f.gen("long.mp4", append([]string{"-f", "lavfi", "-i", "color=c=gray:size=16x16:rate=1:duration=1860"}, x264()...)...)
		}, "", Unusable, CodeTooLong},
		{"a song", func() string { return f.gen("song.m4a", "-f", "lavfi", "-i", tone+"6", "-ac", "2", "-c:a", "aac") }, "", Usable, ""},
		{"a mono phone-call recording as music", func() string {
			return f.gen("call.wav", "-f", "lavfi", "-i", "sine=frequency=300:sample_rate=8000:duration=6", "-ac", "1")
		}, "", UsableWithWarning, CodeLowSampleRate},
		{"six channels at 96 kHz as music", func() string {
			return f.gen("surround.flac", "-f", "lavfi", "-i", "sine=frequency=300:sample_rate=96000:duration=6", "-af", "pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0")
		}, "", Usable, ""},
		{"silent music", func() string { return f.gen("quiet.wav", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "6") }, "", Unusable, CodeSilent},
		{"a screen recording as music", func() string { return f.screenRecording("screen.mp4") }, UseMusic, UsableWithWarning, CodeScreenRecording},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := checkFile(t, h, c.path(), c.use)
			if a.Verdict != c.verdict {
				t.Errorf("verdict %s, want %s: %+v", a.Verdict, c.verdict, a.Findings)
			}
			if c.code != "" && !a.Has(c.code) {
				t.Errorf("no %s finding: %+v", c.code, a.Findings)
			}
			if c.code == "" && c.verdict == Usable && len(a.Findings) != 0 {
				t.Errorf("want no findings: %+v", a.Findings)
			}
		})
	}
}

// A clip cut off part way is detected by how much of it decodes.
func TestCheckMeasuresTheCutOffPart(t *testing.T) {
	h := newHarness(t)
	a := checkFile(t, h, footage{h}.truncated("cut2.mp4", true), "")
	if a.DurationMs < 5900 || a.PlayableMs < 1500 || a.PlayableMs > 3500 {
		t.Fatalf("duration %d, playable %d", a.DurationMs, a.PlayableMs)
	}
}

// The ingest survives every case it accepts: the asset is ready with sane derivatives, never a crash, and the recorded length
// is what really plays.
func TestIngestSurvivesOddFootage(t *testing.T) {
	h := newHarness(t)
	f := footage{h}
	for _, c := range []struct {
		name     string
		kind     string
		path     func() string
		audio    bool
		minMs    int64
		maxMs    int64
		proxyW   int
		proxyH   int
		noDerive string // a derivative that must not be there
	}{
		{"a variable frame rate", store.AssetKindVideo, func() string { return f.variableRate("vfr.mp4") }, true, 3900, 4100, 320, 240, ""},
		{"an odd size", store.AssetKindVideo, func() string {
			return f.gen("odd.mkv", "-f", "lavfi", "-i", "testsrc2=size=321x241:rate=30:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv444p")
		}, false, 1900, 2100, 320, 240, "audio_16k"},
		{"a thin strip", store.AssetKindVideo, func() string {
			return f.gen("strip.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=1280x160:rate=30:duration=2"}, x264()...)...)
		}, false, 1900, 2100, 1280, 160, ""},
		{"a tall strip", store.AssetKindVideo, func() string {
			return f.gen("tall.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=160x1280:rate=30:duration=2"}, x264()...)...)
		}, false, 1900, 2100, 160, 1280, ""},
		{"a tenth of a second", store.AssetKindVideo, func() string {
			return f.gen("tiny.mp4", append([]string{"-f", "lavfi", "-i", "testsrc2=size=360x640:rate=30:duration=0.1"}, x264()...)...)
		}, false, 60, 150, 360, 640, ""},
		{"mono 11 kHz sound", store.AssetKindVideo, func() string {
			return f.gen("mono.mp4", append(append([]string{"-f", "lavfi", "-i", src320 + "2", "-f", "lavfi", "-i", "sine=sample_rate=11025:duration=2"}, x264()...), "-c:a", "aac", "-ac", "1")...)
		}, true, 1900, 2200, 320, 240, ""},
		{"a silent sound track", store.AssetKindVideo, func() string {
			return f.gen("silent.mp4", append(append([]string{"-f", "lavfi", "-i", src320 + "2", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "2"}, x264()...), "-c:a", "aac")...)
		}, true, 1900, 2200, 320, 240, ""},
		{"cut off after the index", store.AssetKindVideo, func() string { return f.truncated("cutfast.mp4", true) }, true, 1500, 3500, 320, 240, ""},
		{"a screen recording as music", store.AssetKindAudio, func() string { return f.screenRecording("screen.mp4") }, true, 5900, 6100, 0, 0, "proxy_720"},
		{"six channels at 96 kHz as music", store.AssetKindAudio, func() string {
			return f.gen("surround.flac", "-f", "lavfi", "-i", "sine=frequency=300:sample_rate=96000:duration=3", "-af", "pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0")
		}, true, 2900, 3100, 0, 0, ""},
		{"a phone-call recording as music", store.AssetKindAudio, func() string {
			return f.gen("call.wav", "-f", "lavfi", "-i", "sine=frequency=300:sample_rate=8000:duration=3", "-ac", "1")
		}, true, 2900, 3100, 0, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := h.upload(c.kind, c.path())
			res := h.run(a)
			if res.Status != store.AssetReady {
				t.Fatalf("%+v", res)
			}
			r := h.store.results[fkey(a.WorkspaceID, a.ID)]
			if r.DurationMs < c.minMs || r.DurationMs > c.maxMs {
				t.Errorf("recorded length %d ms, want %d to %d", r.DurationMs, c.minMs, c.maxMs)
			}
			if r.HasAudio != c.audio {
				t.Errorf("has audio %v", r.HasAudio)
			}
			kinds := strings.Join(h.kinds(a), ",")
			if c.noDerive != "" && strings.Contains(kinds, c.noDerive) {
				t.Errorf("derivatives %s include %s", kinds, c.noDerive)
			}
			if c.audio && (!strings.Contains(kinds, "audio_16k") || !strings.Contains(kinds, "waveform")) {
				t.Errorf("derivatives %s lack the audio", kinds)
			}
			if c.kind == store.AssetKindVideo {
				h.checkProxyShape(a, c.proxyW, c.proxyH)
			}
		})
	}
}

// checkProxyShape is checkProxy without the keyframe rule, which a clip shorter than two seconds cannot meet.
func (h *harness) checkProxyShape(a store.Asset, w, hh int) {
	h.t.Helper()
	path, _ := h.derivative(a, store.DerivativeProxy720)
	m := h.probe(path)
	if m.Codecs.Video != "h264" || m.PixFmt != "yuv420p" || m.Width != w || m.Height != hh || m.Rotation != 0 || m.HDR {
		h.t.Fatalf("proxy %+v", m)
	}
	if m.VariableFrameRate() {
		h.t.Fatalf("the proxy is not at a constant rate: %+v", m.FPS)
	}
}

// A clip over the limit fails fast with the reason the library shows, and the check says the same thing.
func TestIngestRefusesAClipOverTheLimit(t *testing.T) {
	h := newHarness(t)
	path := footage{h}.gen("long.mp4", append([]string{"-f", "lavfi", "-i", "color=c=gray:size=16x16:rate=1:duration=1860"}, x264()...)...)
	a := h.upload(store.AssetKindVideo, path)
	if res := h.run(a); res.Status != store.AssetFailed || res.Reason != store.FailureTooLong {
		t.Fatalf("%+v", res)
	}
	if c := checkFile(t, h, path, ""); !c.Has(CodeTooLong) {
		t.Fatalf("%+v", c)
	}
}
