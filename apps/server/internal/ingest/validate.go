package ingest

import (
	"errors"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Limits on what the ingest accepts. They protect the worker (a decode of an absurd resolution, or of days of footage, is
// not something to start) and are far above what a phone or a camera makes.
const (
	// DefaultMaxDuration is the longest clip or recording accepted. It is tied to the timeouts below: the transcode of a clip
	// this long, at the slowest speed the timeouts allow for, is what JobTimeout has to cover (see WorstCase). Raise it only
	// together with JobTimeout.
	DefaultMaxDuration = 30 * time.Minute
	// MinSide and MaxSide bound the picture's width and height (display size), in pixels. 8192 allows 8K video.
	MinSide = 16
	MaxSide = 8192
	// MaxImageSide bounds a still picture: a panorama may be wide.
	MaxImageSide = 16384
	// MaxFPS is the highest frame rate accepted (slow-motion phone video is 240).
	MaxFPS = 480
)

// The time the ingest allows itself. A sandboxed tool is killed at its timeout; the job as a whole is cut off at JobTimeout
// (River's per-job timeout, jobs.AssetIngestWorker.Timeout), and River only hands a running job to another worker after
// JobTimeout plus a margin (jobs.JobRescueAfter), so a run never overlaps its own retry.
const (
	// ProbeTimeout is the limit of the quick tools: ffprobe and the decode check.
	ProbeTimeout = 5 * time.Minute
	// JobTimeout is how long one attempt of an ingest may run, downloading and uploading included. WorstCase of the longest
	// accepted clip has to fit in it, which New checks.
	JobTimeout = 4 * time.Hour
	// transferAllowance is the time reserved for moving the original down and the derivatives up.
	transferAllowance = 30 * time.Minute
)

// HeavyTimeout is the limit of the transcode of a clip of the given length: three times real time plus two minutes, at most
// two hours. A 4K HEVC clip tone mapped to SDR on two vCPUs runs at about a third of real time or better, so this is a
// generous margin, not a target.
func HeavyTimeout(d time.Duration) time.Duration {
	return min(max(3*d+2*time.Minute, 2*time.Minute), 2*time.Hour)
}

// LightTimeout is the limit of the tools that work on the 720 px proxy or on the audio: poster, sprite sheet, hover preview
// and audio extraction. A third of the clip's length, but at least five minutes and at most an hour.
func LightTimeout(d time.Duration) time.Duration {
	return min(max(d/3, ProbeTimeout), time.Hour)
}

// WorstCase is the longest an ingest of a clip of the given length can take if every tool runs to its timeout: the transfers,
// the probe and decode check, the transcode and the probe of the proxy, then poster, sprite, hover preview and audio.
func WorstCase(d time.Duration) time.Duration {
	return 2*transferAllowance + 3*ProbeTimeout + HeavyTimeout(d) + 4*LightTimeout(d)
}

// errKilled marks a tool that was killed by a signal (the out-of-memory killer's 137, a 143 from a stopping container): the
// machine's doing, not the file's. It is retried, never reported as a bad file.
var errKilled = errors.New("the tool was killed")

// killedExit reports whether an exit code is the shell's 128 plus a signal number (SIGHUP to SIGTERM). ffmpeg's own error
// exits are 1, or a negative AVERROR code seen modulo 256, which in practice is above this range (an invalid-data error is
// 183, an end-of-file one 187); the one risk is a crash of ffmpeg on a malicious file (SIGSEGV is 139), which is then retried
// and, on the last attempt, fails the asset as processing_failed.
func killedExit(code int) bool { return code >= 129 && code <= 143 }

// fileError is a failure caused by the file, not by us or the infrastructure: retrying cannot help, the asset is marked
// failed with Reason (a store.Failure code, safe to show a user) and the job ends.
type fileError struct {
	reason string
	err    error
}

func (e *fileError) Error() string { return "ingest: " + e.reason + ": " + e.err.Error() }
func (e *fileError) Unwrap() error { return e.err }

func fileErr(reason string, err error) *fileError { return &fileError{reason: reason, err: err} }

// Validate checks the probe of an uploaded file against what the ingest can process, for the asset kind. It returns the
// failure reason (a store.Failure code) and why, or an empty reason when the file is acceptable.
func Validate(kind string, m media.Metadata, maxDuration time.Duration) (reason string, err error) {
	dur := time.Duration(m.DurationMs) * time.Millisecond
	switch kind {
	case store.AssetKindVideo:
		switch {
		case !m.HasVideo:
			return store.FailureNoVideo, errors.New("the file has no video stream")
		case m.Codecs.Video == "" || m.Codecs.Video == "unknown":
			return store.FailureUndecodableVideo, errors.New("the video codec is not recognised")
		case m.DurationMs <= 0:
			return store.FailureNoDuration, errors.New("the file has no usable duration")
		case dur > maxDuration:
			return store.FailureTooLong, errors.New("the video is longer than the limit")
		case !sideOK(m.Width, MaxSide) || !sideOK(m.Height, MaxSide):
			return store.FailureResolution, errors.New("the picture size is outside the supported range")
		case m.FPS.Value > MaxFPS:
			return store.FailureResolution, errors.New("the frame rate is outside the supported range")
		}
	case store.AssetKindAudio:
		switch {
		case m.SelectedAudio == nil:
			return store.FailureNoAudio, errors.New("the file has no decodable audio stream")
		case m.DurationMs <= 0:
			return store.FailureNoDuration, errors.New("the file has no usable duration")
		case dur > maxDuration:
			return store.FailureTooLong, errors.New("the recording is longer than the limit")
		}
	case store.AssetKindImage:
		switch {
		case !m.HasVideo:
			return store.FailureUnsupportedFormat, errors.New("the file holds no picture")
		case !sideOK(m.Width, MaxImageSide) || !sideOK(m.Height, MaxImageSide):
			return store.FailureResolution, errors.New("the picture size is outside the supported range")
		}
	default:
		return store.FailureKindMismatch, errors.New("unknown asset kind")
	}
	return "", nil
}

func sideOK(n, limit int) bool { return n >= MinSide && n <= limit }

// classifyToolError maps a failed ffprobe or ffmpeg run on the original to a user-safe reason.
func classifyToolError(te *media.ToolError, formatReason string) string {
	s := strings.ToLower(te.Stderr)
	switch {
	case strings.Contains(s, "not on whitelist"):
		return store.FailureUnsupportedFormat
	case strings.Contains(s, "invalid data found"), strings.Contains(s, "moov atom not found"), strings.Contains(s, "end of file"):
		return store.FailureCorruptMedia
	}
	return formatReason
}
