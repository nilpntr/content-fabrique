package audiocheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// PCMRate is the sample rate of the mono dump the onset detection and the sheet read: 16 kHz holds everything the detector needs
// (the energy of the edit's sounds up to 8 kHz) at a fifth of the size of the stereo original.
const PCMRate = 16000

const (
	// silenceDB and silenceMinS define a silent gap: under -50 dBFS for at least 100 ms (earshot's silent holes are 0.04 s and up).
	silenceDB   = -50.0
	silenceMinS = 0.1
	// maxPCMBytes caps the dump the sandbox writes: MaxDurationS at 16 kHz mono float.
	maxPCMBytes = (MaxDurationS + 5) * PCMRate * 4
	// maxMetaBytes caps the loudness metadata file.
	maxMetaBytes = 16 << 20
	runTimeout   = 3 * time.Minute
)

// measured is everything ffmpeg said about the file.
type measured struct {
	durationS float64
	fps       float64
	hasAudio  bool

	integrated, lra, truePeak float64
	momentary                 []LoudnessPoint
	silences                  []Gap
	channels                  []chanStats
	pcm                       []float32
}

type chanStats struct {
	peakDB, rmsDB, dc float64
	peakCount         int
}

// graph is the one filter graph the check runs: the audio is split four ways: the loudness (ebur128 with its metadata written to
// a file in the work directory, no per-frame log; the dump is downmixed by aformat, which keeps the level of a mono file and gives
// 0.5 L + 0.5 R for a stereo one; the name is relative because the sandbox's working directory is the work
// directory and an argument is only rewritten for the local provider when it is a whole path), the silences (silencedetect logs them), the per-channel statistics (astats logs
// a summary at the end) and the mono 16 kHz dump that the Go analysis reads. Nothing here comes from the file or the caller.
var graph = "[0:a]asplit=4[a1][a2][a3][a4];" +
	"[a1]ebur128=peak=true:framelog=quiet:metadata=1,ametadata=mode=print:file=loudness.txt,anullsink;" +
	"[a2]silencedetect=noise=" + strconv.Itoa(int(silenceDB)) + "dB:d=" + strconv.FormatFloat(silenceMinS, 'f', -1, 64) + ",anullsink;" +
	"[a3]astats=metadata=0:reset=0,anullsink;" +
	"[a4]aresample=" + strconv.Itoa(PCMRate) + ",aformat=channel_layouts=mono[pcm]"

// measure runs ffmpeg in the sandbox over mp4 and reads what it wrote.
func measure(ctx context.Context, runner media.Runner, mp4, dir string) (measured, error) {
	args := []string{
		"ffmpeg", "-hide_banner", "-nostdin", "-nostats", "-protocol_whitelist", "file", "-format_whitelist", media.UntrustedFormats,
		"-t", strconv.Itoa(MaxDurationS + 1), "-i", media.InputPath(0), "-vn", "-filter_complex", graph, "-map", "[pcm]", "-f", "f32le", media.OutputPath("mono.f32"),
	}
	res, err := runner.Run(ctx, media.Invocation{
		Args: args, Inputs: []media.Input{{Host: mp4, Container: media.InputPath(0)}}, OutputDir: dir, Timeout: runTimeout,
	})
	if err != nil {
		return measured{}, fmt.Errorf("audiocheck: running ffmpeg: %w", err)
	}
	log := string(res.Stderr)
	m := measured{integrated: -70, truePeak: math.Inf(-1)}
	m.durationS, m.fps = parseInput(log)
	if res.ExitCode != 0 {
		// A video without an audio track is a finding, not a failure of the check; a file ffmpeg could not open at all is an error.
		if strings.Contains(log, "Input #0") && (strings.Contains(log, "matches no streams") || strings.Contains(log, "does not contain any stream") || !strings.Contains(log, "Audio:")) {
			return m, nil
		}
		return measured{}, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	m.hasAudio = true
	if m.durationS > MaxDurationS {
		return measured{}, fmt.Errorf("%w: %.0f s (the limit is %d s)", ErrTooLong, m.durationS, MaxDurationS)
	}
	if !parseSummary(log, &m) {
		return measured{}, errors.New("audiocheck: ffmpeg's log has no loudness summary (the log was cut or ffmpeg's format changed)")
	}
	m.silences = parseSilences(log, m.durationS)
	m.channels = parseAstats(log)
	if len(m.channels) == 0 {
		return measured{}, errors.New("audiocheck: ffmpeg's log has no channel statistics (the log was cut or ffmpeg's format changed)")
	}
	pcm, err := readPCM(filepath.Join(dir, "mono.f32"))
	if err != nil {
		return measured{}, err
	}
	m.pcm = pcm
	if m.durationS <= 0 {
		m.durationS = float64(len(pcm)) / PCMRate
	}
	// ffmpeg read at most MaxDurationS+1 s: a dump that long is a file whose header understated its length.
	if len(pcm) > (MaxDurationS+0.5)*PCMRate {
		return measured{}, fmt.Errorf("%w: more than %d s of sound (the limit is %d s)", ErrTooLong, MaxDurationS, MaxDurationS)
	}
	meta, err := readRegular(filepath.Join(dir, "loudness.txt"), maxMetaBytes)
	if err != nil {
		return measured{}, err
	}
	m.momentary = parseMomentary(meta)
	return m, nil
}

// readRegular reads a file the sandbox wrote: a regular file (not a link) under a size cap.
func readRegular(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("audiocheck: ffmpeg wrote no %s: %w", filepath.Base(path), err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, fmt.Errorf("audiocheck: %s is not a regular file of reasonable size", filepath.Base(path))
	}
	return os.ReadFile(path) //nolint:gosec // a file in our work directory, checked above
}

func readPCM(path string) ([]float32, error) {
	data, err := readRegular(path, maxPCMBytes)
	if err != nil {
		return nil, err
	}
	n := len(data) / 4
	out := make([]float32, n)
	for i := range out {
		v := math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		if v != v || math.IsInf(float64(v), 0) {
			v = 0
		}
		out[i] = v
	}
	return out, nil
}

var (
	// The patterns are anchored to the start of a line: the metadata tags of the input (untrusted) are printed in the same log, and a
	// tag value is only ever printed after "name : " on its lines, so a line that starts with ffmpeg's own prefix is ffmpeg's.
	reDuration = regexp.MustCompile(`(?m)^  Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	reVideo    = regexp.MustCompile(`(?m)^  Stream #0:\d+.*Video:.*, (\d+(?:\.\d+)?) fps`)
	reSummary  = regexp.MustCompile(`(?m)^\[Parsed_ebur128_\d+ @ 0x[0-9a-f]+\] Summary:`)
	reLUFS     = regexp.MustCompile(`I:\s+(-?[0-9.]+|-inf) LUFS`)
	reLRA      = regexp.MustCompile(`LRA:\s+(-?[0-9.]+) LU\b`)
	rePeak     = regexp.MustCompile(`Peak:\s+(-?[0-9.]+|-inf) dBFS`)
	reSilStart = regexp.MustCompile(`^\[silencedetect @ 0x[0-9a-f]+\] silence_start: (-?[0-9.e+-]+)`)
	reSilEnd   = regexp.MustCompile(`^\[silencedetect @ 0x[0-9a-f]+\] silence_end: (-?[0-9.e+-]+) \| silence_duration: (-?[0-9.e+-]+)`)
	reStat     = regexp.MustCompile(`^\[Parsed_astats_\d+ @ 0x[0-9a-f]+\] (.+)$`)
	reFrame    = regexp.MustCompile(`^frame:\d+\s+pts:\d+\s+pts_time:(\S+)`)
)

// parseInput reads the duration and the video frame rate from the input description at the top of ffmpeg's log.
func parseInput(log string) (durationS, fps float64) {
	if m := reDuration.FindStringSubmatch(log); m != nil {
		h, _ := strconv.ParseFloat(m[1], 64)
		mi, _ := strconv.ParseFloat(m[2], 64)
		sec, _ := strconv.ParseFloat(m[3], 64)
		durationS = h*3600 + mi*60 + sec
	}
	if m := reVideo.FindStringSubmatch(log); m != nil {
		fps, _ = strconv.ParseFloat(m[1], 64)
	}
	return durationS, fps
}

func num(s string) float64 {
	if s == "-inf" {
		return math.Inf(-1)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v != v {
		return math.Inf(-1)
	}
	return v
}

// parseSummary reads the ebur128 summary: integrated loudness, loudness range and the true peak. It reports whether there was one.
func parseSummary(log string, m *measured) bool {
	all := reSummary.FindAllStringIndex(log, -1)
	if len(all) == 0 {
		return false
	}
	s := log[all[len(all)-1][0]:]
	if v := reLUFS.FindStringSubmatch(s); v != nil {
		m.integrated = num(v[1])
	}
	if v := reLRA.FindStringSubmatch(s); v != nil {
		m.lra = num(v[1])
	}
	if v := rePeak.FindStringSubmatch(s); v != nil {
		m.truePeak = num(v[1])
	}
	return true
}

// parseSilences reads silencedetect's lines into gaps. A silence that runs to the end of the file is closed at the duration.
func parseSilences(log string, durationS float64) []Gap {
	var gaps []Gap
	start := math.NaN()
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimRight(line, "\r")
		if v := reSilStart.FindStringSubmatch(line); v != nil {
			start = math.Max(0, num(v[1]))
		} else if v := reSilEnd.FindStringSubmatch(line); v != nil {
			end, dur := num(v[1]), num(v[2])
			if math.IsNaN(start) {
				start = math.Max(0, end-dur)
			}
			gaps = append(gaps, Gap{StartS: start, EndS: end, DurationS: end - start})
			start = math.NaN()
		}
	}
	if !math.IsNaN(start) && durationS > start {
		gaps = append(gaps, Gap{StartS: start, EndS: durationS, DurationS: durationS - start})
	}
	return gaps
}

// parseAstats reads the per-channel statistics from astats' final log: one block per "Channel: n", then "Overall" (a line with no
// colon), which is skipped.
func parseAstats(log string) []chanStats {
	var out []chanStats
	var cur *chanStats
	for _, line := range strings.Split(log, "\n") {
		m := reStat.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if strings.TrimSpace(m[1]) == "Overall" {
			cur = nil
			continue
		}
		key, val, ok := strings.Cut(m[1], ": ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if key == "Channel" {
			out = append(out, chanStats{peakDB: math.Inf(-1), rmsDB: math.Inf(-1)})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "DC offset":
			cur.dc = num(val)
		case "Peak level dB":
			cur.peakDB = num(val)
		case "RMS level dB":
			cur.rmsDB = num(val)
		case "Peak count":
			n, _ := strconv.Atoi(val)
			cur.peakCount = n
		}
	}
	return out
}

// parseMomentary reads the ebur128 metadata (ametadata print): a "frame: ... pts_time:t" line, then its keys.
func parseMomentary(meta []byte) []LoudnessPoint {
	var pts []LoudnessPoint
	sc := bufio.NewScanner(bytes.NewReader(meta))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	t := math.NaN()
	for sc.Scan() {
		line := sc.Text()
		if m := reFrame.FindStringSubmatch(line); m != nil {
			t = num(m[1])
			continue
		}
		if v, ok := strings.CutPrefix(line, "lavfi.r128.M="); ok && !math.IsNaN(t) {
			l := num(v)
			if math.IsInf(l, -1) || l < -120 {
				l = -120
			}
			pts = append(pts, LoudnessPoint{TimeS: t, LUFS: l})
		}
		if len(pts) > 100000 {
			break
		}
	}
	return pts
}
