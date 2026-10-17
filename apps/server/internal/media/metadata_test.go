package media

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func load(t *testing.T, name string) Metadata {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Normalise(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func TestIPhonePortraitWithAPAC(t *testing.T) {
	m := load(t, "iphone_portrait_apac")
	if m.Width != 2160 || m.Height != 3840 || m.CodedWidth != 3840 || m.CodedHeight != 2160 {
		t.Errorf("display %dx%d coded %dx%d", m.Width, m.Height, m.CodedWidth, m.CodedHeight)
	}
	if m.Rotation != -90 || m.ClockwiseDegrees() != 90 {
		t.Errorf("rotation %d cw %d", m.Rotation, m.ClockwiseDegrees())
	}
	if m.Container != "mov" || m.FormatName != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container %q (%q)", m.Container, m.FormatName)
	}
	if m.DurationMs != 87135 || m.SizeBytes != 486020117 {
		t.Errorf("duration %d size %d", m.DurationMs, m.SizeBytes)
	}
	if m.FPS.Num != 522800 || m.FPS.Den != 17427 || m.FPS.Value < 29.99 || m.FPS.Value > 30.01 {
		t.Errorf("fps %+v", m.FPS)
	}
	if m.HDR || m.ColorTransfer != "bt709" || m.PixFmt != "yuv420p" || m.Codecs.Video != "h264" {
		t.Errorf("color/codec %+v", m)
	}
	if !m.HasAudio || len(m.AudioStreams) != 2 {
		t.Fatalf("audio %+v", m.AudioStreams)
	}
	aac, apac := m.AudioStreams[0], m.AudioStreams[1]
	if aac.Index != 1 || aac.Codec != "aac" || !aac.Decodable || aac.Channels != 2 {
		t.Errorf("aac %+v", aac)
	}
	if apac.Index != 2 || apac.Codec != "unknown" || apac.CodecTag != "apac" || apac.Decodable || apac.Channels != 4 {
		t.Errorf("apac %+v", apac)
	}
	if m.SelectedAudio == nil || *m.SelectedAudio != 1 {
		t.Errorf("selected %v", m.SelectedAudio)
	}
	if got := strings.Join(m.Codecs.Audio, ","); got != "aac,unknown" {
		t.Errorf("audio codecs %q", got)
	}
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"iphone_portrait_apac", "iphone_hdr_hlg_portrait", "hdr10_pq_landscape"} {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(load(t, name), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("metadata differs from %s (run go test -update):\n%s", path, got)
			}
		})
	}
}

func TestHDR(t *testing.T) {
	hlg := load(t, "iphone_hdr_hlg_portrait")
	if !hlg.HDR || hlg.HDRFormat != "hlg" || hlg.ColorTransfer != "arib-std-b67" || hlg.ColorPrimaries != "bt2020" || hlg.PixFmt != "yuv420p10le" {
		t.Errorf("hlg %+v", hlg)
	}
	if hlg.Width != 2160 || hlg.Height != 3840 {
		t.Errorf("hlg display %dx%d", hlg.Width, hlg.Height)
	}
	pq := load(t, "hdr10_pq_landscape")
	if !pq.HDR || pq.HDRFormat != "pq" || pq.ColorTransfer != "smpte2084" || pq.Rotation != 0 || pq.Width != 3840 || pq.Height != 2160 {
		t.Errorf("pq %+v", pq)
	}
	if pq.Container != "matroska" {
		t.Errorf("container %q", pq.Container)
	}
	if sdr := load(t, "rotated_180"); sdr.HDR || sdr.HDRFormat != "" {
		t.Errorf("sdr flagged as HDR: %+v", sdr)
	}
}

func TestRotation(t *testing.T) {
	r180 := load(t, "rotated_180")
	if r180.Rotation != 180 || r180.ClockwiseDegrees() != 180 || r180.Width != 1920 || r180.Height != 1080 {
		t.Errorf("180: %+v", r180)
	}
	legacy := load(t, "legacy_rotate_tag") // tags.rotate=90 counts clockwise
	if legacy.Rotation != -90 || legacy.ClockwiseDegrees() != 90 || legacy.Width != 720 || legacy.Height != 1280 {
		t.Errorf("legacy: %+v", legacy)
	}
	if legacy.Container != "mp4" {
		t.Errorf("container %q", legacy.Container)
	}
	for in, want := range map[int]int{0: 0, 90: 90, -90: -90, 270: -90, -270: 90, 180: 180, -180: 180, 360: 0, 450: 90} {
		if got := normaliseAngle(in); got != want {
			t.Errorf("normaliseAngle(%d) = %d, want %d", in, got, want)
		}
	}
	if (Metadata{Rotation: 90}).ClockwiseDegrees() != 270 || (Metadata{}).ClockwiseDegrees() != 0 {
		t.Error("ClockwiseDegrees")
	}
}

func TestAnamorphicDisplayWidth(t *testing.T) {
	m := load(t, "anamorphic_pal_dv")
	if m.Width != 768 || m.Height != 576 || m.CodedWidth != 720 {
		t.Errorf("display %dx%d coded %d", m.Width, m.Height, m.CodedWidth)
	}
	if m.ColorTransfer != "" || m.ColorPrimaries != "" || m.ColorSpace != "" {
		t.Errorf("unknown colour values must be dropped: %+v", m)
	}
	if m.Container != "mpeg" {
		t.Errorf("container %q", m.Container)
	}
}

func TestAudioSelection(t *testing.T) {
	only := load(t, "apac_only")
	if !only.HasAudio || only.SelectedAudio != nil || only.AudioStreams[0].Decodable {
		t.Errorf("apac-only must have audio but nothing selectable: %+v", only)
	}
	if _, ok := only.SelectedAudioStream(); ok {
		t.Error("SelectedAudioStream must report none")
	}

	// Two decodable tracks: the default-flagged one wins even when it comes later.
	two := load(t, "cover_art_and_two_tracks")
	if two.SelectedAudio == nil || *two.SelectedAudio != 2 {
		t.Fatalf("selected %v", two.SelectedAudio)
	}
	if a, ok := two.SelectedAudioStream(); !ok || a.Codec != "opus" {
		t.Errorf("selected stream %+v", a)
	}
	// Cover art is not the video stream.
	if two.HasVideo {
		t.Errorf("attached picture counted as video: %+v", two)
	}

	mp3 := load(t, "audio_only_mp3")
	if mp3.HasVideo || !mp3.HasAudio || mp3.DurationMs != 12500 || mp3.Container != "mp3" || mp3.Width != 0 {
		t.Errorf("mp3 %+v", mp3)
	}
	if mp3.AudioStreams[0].CodecTag != "" {
		t.Errorf("placeholder codec tag kept: %q", mp3.AudioStreams[0].CodecTag)
	}
	if mp3.SelectedAudio == nil || *mp3.SelectedAudio != 0 {
		t.Errorf("selected %v", mp3.SelectedAudio)
	}
}

func TestMissingValues(t *testing.T) {
	m := load(t, "vfr_unknown_avg")
	if m.FPS.Num != 60 || m.FPS.Den != 1 || m.FPS.Value != 60 {
		t.Errorf("fps must fall back to r_frame_rate: %+v", m.FPS)
	}
	if m.DurationMs != 10500 || m.SizeBytes != 0 || m.BitRate != 0 {
		t.Errorf("N/A handling: %+v", m)
	}
}

func TestNoTagsLeak(t *testing.T) {
	raw, err := json.Marshal(load(t, "iphone_portrait_apac"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ISO6709", "52.0000", "iPhone TEST", "creation_time", "2000-01-01", "Apple", "quicktime"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("metadata leaks %q: %s", secret, raw)
		}
	}
}

func TestNormaliseRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{"not json": "ffprobe: error", "empty": "", "empty object": "{}", "no streams": `{"streams":[],"format":{}}`} {
		if _, err := Normalise([]byte(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A file ffprobe reads but that has no video or audio is valid metadata with both flags false.
	m, err := Normalise([]byte(`{"streams":[{"index":0,"codec_type":"data"}],"format":{"format_name":"mpegts","duration":"1.0"}}`))
	if err != nil || m.HasVideo || m.HasAudio || m.Container != "mpegts" {
		t.Errorf("data-only file: %+v, %v", m, err)
	}
}

func TestRationalFloat(t *testing.T) {
	if (Rational{Num: 30000, Den: 1001}).Float() < 29.96 || (Rational{}).Float() != 0 || (Rational{Num: 1, Den: 0}).Float() != 0 {
		t.Error("Rational.Float")
	}
}
