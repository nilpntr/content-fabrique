package media

import (
	"fmt"
	"strconv"
)

// common is the start of every ffmpeg command: no stdin, quiet, overwrite, local files only.
func common() []string {
	return []string{"ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y"}
}

// TestClipArgs generates a synthetic 1080p 30 fps clip of the given length: the testsrc2 pattern and a 440 Hz sine,
// H.264 and AAC. It reads nothing from disk, so it is the benchmark's only input that is not a probe of a real file.
func TestClipArgs(output string, seconds int) ([]string, error) {
	if seconds <= 0 || seconds > 3600 {
		return nil, fmt.Errorf("media: test clip length %d s must be in 1..3600", seconds)
	}
	d := strconv.Itoa(seconds)
	a := common()
	a = append(a,
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30:duration="+d,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+d,
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		output,
	)
	return a, nil
}

// TranscodeOptions parameterise TranscodeH264Args.
type TranscodeOptions struct {
	Preset string // x264 preset, default "veryfast"
	CRF    int    // 0..51; default 23 when zero
}

var x264Presets = map[string]bool{
	"ultrafast": true, "superfast": true, "veryfast": true, "faster": true, "fast": true,
	"medium": true, "slow": true, "slower": true, "veryslow": true,
}

// TranscodeH264Args re-encodes the first video stream and, if present, the first audio stream to H.264 and AAC in an MP4
// with the index up front. Input and output are container paths.
func TranscodeH264Args(input, output string, o TranscodeOptions) ([]string, error) {
	if o.Preset == "" {
		o.Preset = "veryfast"
	}
	if o.CRF == 0 {
		o.CRF = 23
	}
	if !x264Presets[o.Preset] {
		return nil, fmt.Errorf("media: unknown x264 preset %q", o.Preset)
	}
	if o.CRF < 0 || o.CRF > 51 {
		return nil, fmt.Errorf("media: CRF %d must be in 0..51", o.CRF)
	}
	a := common()
	a = append(a,
		"-protocol_whitelist", "file",
		"-i", input,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c:v", "libx264", "-preset", o.Preset, "-crf", strconv.Itoa(o.CRF), "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		output,
	)
	return a, nil
}

// VersionArgs prints the ffmpeg build configuration; used to measure start-up cost and to inspect the image.
func VersionArgs(tool string) ([]string, error) {
	if tool != "ffmpeg" && tool != "ffprobe" {
		return nil, fmt.Errorf("media: unknown tool %q", tool)
	}
	return []string{tool, "-hide_banner", "-version"}, nil
}
