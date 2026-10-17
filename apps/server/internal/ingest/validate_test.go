package ingest

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func videoMeta() media.Metadata {
	sel := 1
	return media.Metadata{
		HasVideo: true, DurationMs: 10000, Width: 1080, Height: 1920, FPS: media.FPS{Value: 30},
		Codecs: media.Codecs{Video: "h264"}, HasAudio: true, SelectedAudio: &sel,
	}
}

func TestValidate(t *testing.T) {
	ok := videoMeta()
	for _, c := range []struct {
		name   string
		kind   string
		mutate func(*media.Metadata)
		want   string
	}{
		{"a normal video", store.AssetKindVideo, func(*media.Metadata) {}, ""},
		{"an 8K video", store.AssetKindVideo, func(m *media.Metadata) { m.Width, m.Height = 7680, 4320 }, ""},
		{"a video with no audio", store.AssetKindVideo, func(m *media.Metadata) { m.SelectedAudio, m.HasAudio = nil, false }, ""},
		{"no video stream", store.AssetKindVideo, func(m *media.Metadata) { m.HasVideo = false }, store.FailureNoVideo},
		{"unknown codec", store.AssetKindVideo, func(m *media.Metadata) { m.Codecs.Video = "unknown" }, store.FailureUndecodableVideo},
		{"no codec", store.AssetKindVideo, func(m *media.Metadata) { m.Codecs.Video = "" }, store.FailureUndecodableVideo},
		{"no duration", store.AssetKindVideo, func(m *media.Metadata) { m.DurationMs = 0 }, store.FailureNoDuration},
		{"too long", store.AssetKindVideo, func(m *media.Metadata) { m.DurationMs = int64(5 * time.Hour / time.Millisecond) }, store.FailureTooLong},
		{"too small", store.AssetKindVideo, func(m *media.Metadata) { m.Width = 8 }, store.FailureResolution},
		{"too big", store.AssetKindVideo, func(m *media.Metadata) { m.Height = 20000 }, store.FailureResolution},
		{"absurd frame rate", store.AssetKindVideo, func(m *media.Metadata) { m.FPS.Value = 5000 }, store.FailureResolution},
		{"audio", store.AssetKindAudio, func(m *media.Metadata) { m.HasVideo = false }, ""},
		{"audio without a decodable stream", store.AssetKindAudio, func(m *media.Metadata) { m.SelectedAudio = nil }, store.FailureNoAudio},
		{"audio with no duration", store.AssetKindAudio, func(m *media.Metadata) { m.DurationMs = 0 }, store.FailureNoDuration},
		{"an image", store.AssetKindImage, func(m *media.Metadata) { m.DurationMs = 0 }, ""},
		{"an image that holds no picture", store.AssetKindImage, func(m *media.Metadata) { m.HasVideo = false }, store.FailureUnsupportedFormat},
		{"an unknown kind", "document", func(*media.Metadata) {}, store.FailureKindMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := ok
			c.mutate(&m)
			reason, err := Validate(c.kind, m, DefaultMaxDuration)
			if reason != c.want || (reason != "") != (err != nil) {
				t.Fatalf("reason %q, err %v; want %q", reason, err, c.want)
			}
		})
	}
}

// wav builds a mono 16 kHz 16-bit PCM WAV from samples, with the header ffmpeg writes.
func wav(samples []int16, mutate func(*[44]byte)) []byte {
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(samples)*2))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], 16000)
	binary.LittleEndian.PutUint32(h[28:], 32000)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(samples)*2))
	if mutate != nil {
		mutate(&h)
	}
	var b bytes.Buffer
	b.Write(h[:])
	_ = binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestComputeWaveform(t *testing.T) {
	// Two seconds: a second of half scale, then a second of silence with one full-scale click.
	s := make([]int16, 32000)
	for i := 0; i < 16000; i++ {
		s[i] = 16384
		if i%2 == 1 {
			s[i] = -16384
		}
	}
	s[20000] = -32768
	wf, err := ComputeWaveform(writeTemp(t, wav(s, nil)), 100)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Version != 1 || wf.SampleRate != 16000 || wf.DurationMs != 2000 || len(wf.Peaks) != 100 || wf.SamplesPerPeak != 320 {
		t.Fatalf("%+v", wf)
	}
	for i, p := range wf.Peaks {
		var want float64
		switch {
		case i < 50:
			want = 0.5
		case i == 62: // sample 20000 / 320 = 62.5
			want = 1
		}
		if math.Abs(p-want) > 0.001 {
			t.Fatalf("peak %d = %v, want %v", i, p, want)
		}
	}

	// Fewer samples than points: one peak per sample.
	wf, err = ComputeWaveform(writeTemp(t, wav([]int16{0, 1000, -2000}, nil)), 1000)
	if err != nil || len(wf.Peaks) != 3 || wf.Peaks[2] != round3(2000.0/32768) {
		t.Fatalf("%+v %v", wf, err)
	}

	// A header that declares more data than the file holds (or a stream-style 0) is not believed.
	long := wav(s, func(h *[44]byte) { binary.LittleEndian.PutUint32(h[40:], 0x7fffffff) })
	if wf, err = ComputeWaveform(writeTemp(t, long), 50); err != nil || wf.DurationMs != 2000 || len(wf.Peaks) != 50 {
		t.Fatalf("%+v %v", wf, err)
	}
	stream := wav(s, func(h *[44]byte) { binary.LittleEndian.PutUint32(h[40:], 0) })
	if wf, err = ComputeWaveform(writeTemp(t, stream), 50); err != nil || wf.DurationMs != 2000 {
		t.Fatalf("%+v %v", wf, err)
	}
}

func TestComputeWaveformRefusesWhatIsNotAnAudio16kFile(t *testing.T) {
	good := wav([]int16{1, 2, 3}, nil)
	for name, data := range map[string][]byte{
		"empty":        nil,
		"not riff":     []byte("this is not a wav file at all, not even close, no"),
		"stereo":       wav([]int16{1, 2}, func(h *[44]byte) { binary.LittleEndian.PutUint16(h[22:], 2) }),
		"44.1 kHz":     wav([]int16{1, 2}, func(h *[44]byte) { binary.LittleEndian.PutUint32(h[24:], 44100) }),
		"8 bit":        wav([]int16{1, 2}, func(h *[44]byte) { binary.LittleEndian.PutUint16(h[34:], 8) }),
		"float":        wav([]int16{1, 2}, func(h *[44]byte) { binary.LittleEndian.PutUint16(h[20:], 3) }),
		"no samples":   wav(nil, nil),
		"header only":  good[:30],
		"data first":   append(append([]byte("RIFF\x00\x00\x00\x00WAVEdata\x04\x00\x00\x00abcd"), good[12:36]...), 0),
		"huge fmt":     wav([]int16{1}, func(h *[44]byte) { binary.LittleEndian.PutUint32(h[16:], 1<<30) }),
		"endless junk": append([]byte("RIFF\x00\x00\x00\x00WAVE"), bytes.Repeat([]byte("junk\x00\x00\x00\x00"), 100)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ComputeWaveform(writeTemp(t, data), 100); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := ComputeWaveform(filepath.Join(t.TempDir(), "missing.wav"), 100); err == nil {
		t.Fatal("a missing file")
	}
	if _, err := ComputeWaveform(writeTemp(t, good), 0); err == nil {
		t.Fatal("zero points")
	}
}
