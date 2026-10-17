package transcribe

import (
	"encoding/binary"
	"fmt"
	"os"
)

// SampleRate is the rate of the audio the providers take: the audio_16k.wav derivative.
const SampleRate = 16000

// MaxAudioBytes bounds the audio the pipeline reads into memory: 30 minutes (the longest accepted clip) of 16 kHz mono 16-bit
// PCM is 57.6 MB.
const MaxAudioBytes = 64 << 20

// ReadWAV reads a 16 kHz mono signed 16-bit PCM WAV file. The file is the output of a sandboxed tool, so the header is checked
// before anything is trusted: RIFF/WAVE, one fmt chunk saying PCM, 1 channel, 16000 Hz, 16 bit, and a data chunk that fits in the
// file. Other chunks (LIST, ffmpeg's metadata) are skipped.
func ReadWAV(path string) ([]int16, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxAudioBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrUnusableAudio, fi.Size())
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a path in the job's own directory
	if err != nil {
		return nil, err
	}
	return ParseWAV(b)
}

// ParseWAV is ReadWAV for bytes.
func ParseWAV(b []byte) ([]int16, error) {
	bad := func(why string) ([]int16, error) { return nil, fmt.Errorf("%w: %s", ErrUnusableAudio, why) }
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return bad("not a RIFF/WAVE file")
	}
	var haveFmt bool
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:off+8]))
		body := off + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return bad("short fmt chunk")
			}
			format, ch := binary.LittleEndian.Uint16(b[body:]), binary.LittleEndian.Uint16(b[body+2:])
			rate, bits := binary.LittleEndian.Uint32(b[body+4:]), binary.LittleEndian.Uint16(b[body+14:])
			if format != 1 || ch != 1 || rate != SampleRate || bits != 16 {
				return bad(fmt.Sprintf("want 16 kHz mono 16-bit PCM, got format %d, %d channels, %d Hz, %d bits", format, ch, rate, bits))
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return bad("data before fmt")
			}
			// ffmpeg writing to a pipe leaves the size at 0xFFFFFFFF; a file it finished has the true one.
			end := body + size
			if size < 0 || end > len(b) || end < body {
				end = len(b)
			}
			n := (end - body) / 2
			out := make([]int16, n)
			for i := range out {
				out[i] = int16(binary.LittleEndian.Uint16(b[body+2*i:])) // #nosec G115 -- reinterpreting the sample's bits
			}
			return out, nil
		}
		off = body + size + size&1
		if size < 0 || off < 0 {
			break
		}
	}
	return bad("no data chunk")
}

// EncodeWAV writes samples as a 16 kHz mono 16-bit PCM WAV file.
func EncodeWAV(pcm []int16) []byte {
	n := len(pcm) * 2
	b := make([]byte, 44+n)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n)) // #nosec G115 -- bounded by MaxAudioBytes in every caller
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], SampleRate)
	binary.LittleEndian.PutUint32(b[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n)) // #nosec G115 -- as above
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s)) // #nosec G115 -- reinterpreting the sample's bits
	}
	return b
}

// DurationMsOf is the length of n samples in milliseconds.
func DurationMsOf(n int) int64 { return int64(n) * 1000 / SampleRate }
