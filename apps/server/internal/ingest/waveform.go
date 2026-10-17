package ingest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// Waveform is waveform.json: the loudest sample in each of Peaks' equal slices of the audio, 0 to 1 (1 is full scale), from
// audio_16k.wav, which is the selected audio stream mixed down to mono.
type Waveform struct {
	Version        int       `json:"version"`
	SampleRate     int       `json:"sample_rate"`
	DurationMs     int64     `json:"duration_ms"`
	SamplesPerPeak int       `json:"samples_per_peak"`
	Peaks          []float64 `json:"peaks"`
}

// maxWAVSamples bounds how much audio is read: 48 hours at 16 kHz.
const maxWAVSamples = 48 * 3600 * media.AudioSampleRate

// errNoAudio is returned for a WAV file without a single sample.
var errNoAudio = errors.New("ingest: the audio track has no samples")

// ComputeWaveform reads a mono 16 kHz 16-bit PCM WAV file (what media.Audio16kArgs makes) and returns up to points peaks.
// The file came out of a sandbox, so its header is checked and never trusted for sizes: the number of samples is what is
// really in the file.
func ComputeWaveform(path string, points int) (Waveform, error) {
	if points < 1 {
		return Waveform{}, errors.New("ingest: waveform needs at least one point")
	}
	f, err := os.Open(path) // #nosec G304 -- a file this package moved out of the job's output directory
	if err != nil {
		return Waveform{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return Waveform{}, err
	}
	br := bufio.NewReaderSize(f, 1<<16)
	dataOffset, declared, err := readWAVHeader(br)
	if err != nil {
		return Waveform{}, err
	}
	avail := fi.Size() - dataOffset
	if declared > avail || declared == 0 {
		declared = avail // a stream-style header (size unknown) or a truncated file
	}
	samples := declared / 2
	if samples == 0 {
		return Waveform{}, errNoAudio
	}
	if samples > maxWAVSamples {
		return Waveform{}, fmt.Errorf("ingest: the audio track is longer than %d samples", maxWAVSamples)
	}

	// Sample j belongs to peak j*n/samples, so the peaks are n equal slices of the whole track.
	n := int(min(int64(points), samples))
	peaks := make([]float64, n)
	var (
		buf  [4096]byte
		read int64
	)
	for read < samples {
		want := int(min(int64(len(buf)/2), samples-read)) * 2
		got, err := io.ReadFull(br, buf[:want])
		for i := 0; i+1 < got; i += 2 {
			v := int(binary.LittleEndian.Uint16(buf[i:])) // two's complement: 0..32767 are positive, 32768..65535 are -32768..-1
			if v >= 32768 {
				v = 65536 - v
			}
			slot := int(read * int64(n) / samples)
			peaks[slot] = max(peaks[slot], float64(v)/32768)
			read++
		}
		if err != nil {
			break // a short read ends the data; what was read counts
		}
	}
	if read == 0 {
		return Waveform{}, errNoAudio
	}
	for i := range peaks {
		peaks[i] = round3(peaks[i])
	}
	return Waveform{
		Version: 1, SampleRate: media.AudioSampleRate, DurationMs: read * 1000 / media.AudioSampleRate,
		SamplesPerPeak: int((samples + int64(n) - 1) / int64(n)), Peaks: peaks,
	}, nil
}

func round3(v float64) float64 { return math.Round(math.Min(v, 1)*1000) / 1000 }

// readWAVHeader reads RIFF/WAVE chunks up to the start of the data chunk, checks that the format is mono 16 kHz 16-bit PCM
// and returns the offset of the data and the data size the header declares.
func readWAVHeader(r *bufio.Reader) (dataOffset, dataSize int64, err error) {
	var head [12]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, 0, fmt.Errorf("ingest: audio_16k.wav: %w", err)
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, 0, errors.New("ingest: audio_16k.wav is not a WAV file")
	}
	offset := int64(12)
	sawFmt := false
	for chunks := 0; chunks < 32; chunks++ {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return 0, 0, fmt.Errorf("ingest: audio_16k.wav has no data chunk: %w", err)
		}
		offset += 8
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[0:4]) {
		case "fmt ":
			if size < 16 || size > 1024 {
				return 0, 0, errors.New("ingest: audio_16k.wav has an unusual fmt chunk")
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, 0, err
			}
			offset += size
			format, channels := binary.LittleEndian.Uint16(b[0:]), binary.LittleEndian.Uint16(b[2:])
			rate, bits := binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint16(b[14:])
			if format != 1 || channels != 1 || rate != media.AudioSampleRate || bits != 16 {
				return 0, 0, fmt.Errorf("ingest: audio_16k.wav is format %d, %d channels, %d Hz, %d bits; want mono 16 kHz 16-bit PCM", format, channels, rate, bits)
			}
			sawFmt = true
		case "data":
			if !sawFmt {
				return 0, 0, errors.New("ingest: audio_16k.wav has its data before its format")
			}
			return offset, size, nil
		default:
			if _, err := io.CopyN(io.Discard, r, size+size%2); err != nil {
				return 0, 0, fmt.Errorf("ingest: audio_16k.wav: %w", err)
			}
			offset += size + size%2
		}
	}
	return 0, 0, errors.New("ingest: audio_16k.wav has too many chunks before its data")
}
