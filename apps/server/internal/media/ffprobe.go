package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrUnparsable is returned (wrapped) by Probe when ffprobe ran and exited cleanly but its output is not something Normalise
// can use: the file is the problem, not the sandbox.
var ErrUnparsable = errors.New("media: ffprobe output could not be understood")

// ProbeArgs is the ffprobe command line that prints everything Normalise needs as JSON. The input is a container path.
// `-protocol_whitelist file` stops playlist-style inputs (HLS, concat) from opening anything but local files, and
// `-format_whitelist` limits the demuxers to UntrustedFormats.
func ProbeArgs(input string) []string {
	return []string{
		"ffprobe",
		"-v", "error",
		"-protocol_whitelist", "file",
		"-format_whitelist", UntrustedFormats,
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", input,
	}
}

// Probe runs ffprobe on a host file through the runner and returns its normalised metadata.
func Probe(ctx context.Context, r Runner, path string) (Metadata, error) {
	return probeWith(ctx, r, path, ProbeArgs)
}

// probeWith is Probe with the ffprobe command line chosen by the caller (the format whitelist differs per asset kind).
func probeWith(ctx context.Context, r Runner, path string, args func(input string) []string) (Metadata, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("media: probe %q: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Metadata{}, fmt.Errorf("media: probe: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Metadata{}, fmt.Errorf("media: probe %q: not a regular file", path)
	}
	res, err := r.Run(ctx, Invocation{
		Args:   args(InputPath(0)),
		Inputs: []Input{{Host: abs, Container: InputPath(0)}},
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("media: probe %q: %w", filepath.Base(path), err)
	}
	if res.ExitCode != 0 {
		return Metadata{}, &ToolError{Tool: "ffprobe", ExitCode: res.ExitCode, Stderr: StderrSnippet(res.Stderr)}
	}
	if res.StdoutTruncated {
		return Metadata{}, errors.New("media: ffprobe output exceeded the capture limit")
	}
	m, err := Normalise(res.Stdout)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %w", ErrUnparsable, err)
	}
	if m.SizeBytes == 0 {
		m.SizeBytes = fi.Size()
	}
	return m, nil
}
