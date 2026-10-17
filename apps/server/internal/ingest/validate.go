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
	// DefaultMaxDuration is the longest clip or recording accepted.
	DefaultMaxDuration = 4 * time.Hour
	// MinSide and MaxSide bound the picture's width and height (display size), in pixels. 8192 allows 8K video.
	MinSide = 16
	MaxSide = 8192
	// MaxImageSide bounds a still picture: a panorama may be wide.
	MaxImageSide = 16384
	// MaxFPS is the highest frame rate accepted (slow-motion phone video is 240).
	MaxFPS = 480
)

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
