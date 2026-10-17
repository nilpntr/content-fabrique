package media

import (
	"fmt"
	"strconv"
)

// UntrustedFormats are the container formats we accept from uploads: the demuxers ffmpeg may use on a user's file. Every
// other demuxer (playlists such as HLS and concat that open further files, the lavfi source, raw and exotic formats) is
// refused with "Format not on whitelist". Names are ffmpeg demuxer names; "mov" etc. are the aliases of one demuxer, so
// they are listed individually. To accept another format, add its demuxer name here and say why.
const UntrustedFormats = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi,mpegts,mpeg,flv,asf,mp3,aac,wav,flac,ogg"

// inputOptions are the options placed before -i for a file we did not make: local files only, known containers only.
func inputOptions() []string {
	return []string{"-protocol_whitelist", "file", "-format_whitelist", UntrustedFormats}
}

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

// RawClipArgs generates a synthetic 1080p 30 fps clip as uncompressed video in AVI (about 93 MB per second of video). It
// exists to give the I/O benchmark a big file; nothing in the product uses raw video.
func RawClipArgs(output string, seconds int) ([]string, error) {
	if seconds <= 0 || seconds > 60 {
		return nil, fmt.Errorf("media: raw clip length %d s must be in 1..60", seconds)
	}
	a := common()
	a = append(a,
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30:duration="+strconv.Itoa(seconds),
		"-c:v", "rawvideo", "-pix_fmt", "yuv420p",
		output,
	)
	return a, nil
}

// CopyArgs remuxes every stream without re-encoding: the work is reading and writing the file. Input and output are
// container paths.
func CopyArgs(input, output string) []string {
	a := append(common(), inputOptions()...)
	return append(a, "-i", input, "-map", "0", "-c", "copy", output)
}

// TranscodeOptions parameterise TranscodeH264Args.
type TranscodeOptions struct {
	Preset string // x264 preset, default "veryfast"
	CRF    *int   // 0..51 (0 is lossless); nil means the default, 23. Use CRF(n).
}

// CRF returns a pointer for TranscodeOptions.CRF, so that 0 (lossless) can be told apart from "not set".
func CRF(n int) *int { return &n }

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
	crf := 23
	if o.CRF != nil {
		crf = *o.CRF
	}
	if !x264Presets[o.Preset] {
		return nil, fmt.Errorf("media: unknown x264 preset %q", o.Preset)
	}
	if crf < 0 || crf > 51 {
		return nil, fmt.Errorf("media: CRF %d must be in 0..51", crf)
	}
	a := append(common(), inputOptions()...)
	a = append(a,
		"-i", input,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c:v", "libx264", "-preset", o.Preset, "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p",
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
