package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// The wire contract of a remote sandbox job (docs/design/render-runtime.md, section 3). The Go worker's `remote` provider
// (apps/server/internal/sandbox/remote.go) writes the manifest and reads the status; testdata/manifest.json and
// testdata/status.json are kept equal on both sides by the server's TestRemoteWireMatchesRunnerContract.

// Container paths of the image contract: inputs are downloaded under InputDir, the steps write under WorkDir.
const (
	InputDir = "/in"
	WorkDir  = "/work"
)

// Manifest is the body of PUT /jobs/{id}: what to download, what to run and what to upload.
type Manifest struct {
	Inputs  []Input  `json:"inputs"`
	Steps   []Step   `json:"steps"`
	Outputs []Output `json:"outputs"`
	Limits  Limits   `json:"limits"`
	// MaxOutputBytes caps each step's stdout and stderr; zero means DefaultMaxOutputBytes.
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
	// MaxInputBytes caps the sum of the downloaded inputs; zero means DefaultMaxInputBytes.
	MaxInputBytes int64 `json:"max_input_bytes,omitempty"`
	// Labels are metadata for logs and billing (workspace, job kind); never secrets.
	Labels map[string]string `json:"labels,omitempty"`
}

// Input is a file the steps read: a presigned GET URL downloaded to Path before the first step. Path is under /in, or under
// /work for a file a tool reads by a name relative to its working directory (the subtitles script of a render).
type Input struct {
	URL  string `json:"url"`
	Path string `json:"path"`
}

// Step is one program run, without a shell, with /work as its working directory. Steps run in order and share /work.
type Step struct {
	Args []string `json:"args"`
	// TimeoutMs bounds this step; zero means what is left of the job's timeout.
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// Output is a file the steps wrote (under /work), uploaded to a presigned PUT URL once every step exited 0.
type Output struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	// ContentType is sent as the Content-Type header (the URL may be signed with it).
	ContentType string `json:"content_type,omitempty"`
	// MaxBytes caps the file; zero means DefaultMaxFileBytes.
	MaxBytes int64 `json:"max_bytes,omitempty"`
}

// Limits bound the job. Every field is required. Timeout covers the whole job: downloads, steps and uploads.
type Limits struct {
	CPU       float64 `json:"cpu"`
	MemoryMB  int     `json:"memory_mb"`
	TimeoutMs int64   `json:"timeout_ms"`
	PIDs      int     `json:"pids"`
}

// States of a job. Every state but StateRunning is terminal.
const (
	StateRunning   = "running"
	StateSucceeded = "succeeded" // every step exited 0 and every output was uploaded
	StateFailed    = "failed"    // a step exited non-zero; later steps did not run and nothing was uploaded
	StateTimedOut  = "timed_out" // the job's or a step's timeout expired and the step was killed
	StateError     = "error"     // the runner failed: see ErrorKind
	StateCancelled = "cancelled" // DELETE /jobs/{id} stopped it
)

// Error kinds of StateError.
const (
	// KindSandbox: the runner could not do its part (a download or upload failed, a program could not be started). Retry.
	KindSandbox = "sandbox"
	// KindFiles: the job's files broke a rule (inputs over MaxInputBytes, an output missing, not a regular file or over its cap).
	KindFiles = "files"
)

// Status is the body of every answer about a job.
type Status struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"`
	// ExitCode, DurationMs and TimedOut summarise the job: the exit code of the last step that ran (-1 when none finished), the
	// wall time so far, and whether a timeout killed it.
	ExitCode   int          `json:"exit_code"`
	DurationMs int64        `json:"duration_ms"`
	TimedOut   bool         `json:"timed_out"`
	Steps      []StepResult `json:"steps"`
	Outputs    []OutputInfo `json:"outputs,omitempty"`
}

// StepResult is the outcome of one step. Stdout and Stderr are base64 in JSON.
type StepResult struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	DurationMs      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out"`
}

// OutputInfo is an uploaded output.
type OutputInfo struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Defaults and hard caps. The defaults match the worker's render limits (renderjobs.MaxInputBytes, MaxOutputBytes) and the
// sandbox's output cap (sandbox.DefaultMaxOutputBytes).
const (
	DefaultMaxOutputBytes = 4 << 20
	DefaultMaxInputBytes  = 8 << 30
	DefaultMaxFileBytes   = 4 << 30

	maxManifestBytes = 8 << 20
	maxInputs        = 256
	maxSteps         = 16
	maxOutputs       = 64
	maxCPU           = 64
	maxMemoryMB      = 256 * 1024
	maxPIDs          = 65536
	maxTimeout       = 24 * time.Hour
	maxStreamBytes   = 64 << 20
	maxInputCap      = 64 << 30
)

var (
	idRE    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	labelRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,128}$`)
)

// validID reports whether id is a job id: the render id plus the attempt, or anything else of the same shape.
func validID(id string) bool { return idRE.MatchString(id) }

// parseManifest decodes and validates a manifest. Unknown fields are refused, so a newer worker cannot be half understood.
func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if dec.More() {
		return m, errors.New("manifest: trailing data")
	}
	return m, m.validate()
}

// hash is the manifest's identity for idempotent PUTs: the same id with the same manifest is the same job.
func (m Manifest) hash() [32]byte {
	b, _ := json.Marshal(m) // a decoded manifest always marshals
	return sha256.Sum256(b)
}

func (m Manifest) timeout() time.Duration {
	return time.Duration(m.Limits.TimeoutMs) * time.Millisecond
}

func (m Manifest) maxOutput() int {
	if m.MaxOutputBytes == 0 {
		return DefaultMaxOutputBytes
	}
	return m.MaxOutputBytes
}

func (m Manifest) maxInput() int64 {
	if m.MaxInputBytes == 0 {
		return DefaultMaxInputBytes
	}
	return m.MaxInputBytes
}

func (o Output) maxBytes() int64 {
	if o.MaxBytes == 0 {
		return DefaultMaxFileBytes
	}
	return o.MaxBytes
}

func (m Manifest) validate() error {
	l := m.Limits
	switch {
	case l.CPU <= 0 || l.CPU > maxCPU:
		return fmt.Errorf("manifest: limits.cpu %v must be in (0, %d]", l.CPU, maxCPU)
	case l.MemoryMB <= 0 || l.MemoryMB > maxMemoryMB:
		return fmt.Errorf("manifest: limits.memory_mb %d must be in (0, %d]", l.MemoryMB, maxMemoryMB)
	case l.PIDs <= 0 || l.PIDs > maxPIDs:
		return fmt.Errorf("manifest: limits.pids %d must be in (0, %d]", l.PIDs, maxPIDs)
	case l.TimeoutMs <= 0 || m.timeout() > maxTimeout:
		return fmt.Errorf("manifest: limits.timeout_ms %d must be in (0, %d]", l.TimeoutMs, maxTimeout.Milliseconds())
	case m.MaxOutputBytes < 0 || m.MaxOutputBytes > maxStreamBytes:
		return fmt.Errorf("manifest: max_output_bytes %d must be in [0, %d]", m.MaxOutputBytes, maxStreamBytes)
	case m.MaxInputBytes < 0 || m.MaxInputBytes > maxInputCap:
		return fmt.Errorf("manifest: max_input_bytes %d must be in [0, %d]", m.MaxInputBytes, int64(maxInputCap))
	case len(m.Steps) == 0 || len(m.Steps) > maxSteps:
		return fmt.Errorf("manifest: %d steps; want 1 to %d", len(m.Steps), maxSteps)
	case len(m.Inputs) > maxInputs:
		return fmt.Errorf("manifest: %d inputs; at most %d", len(m.Inputs), maxInputs)
	case len(m.Outputs) > maxOutputs:
		return fmt.Errorf("manifest: %d outputs; at most %d", len(m.Outputs), maxOutputs)
	}
	for i, s := range m.Steps {
		if err := validArgs(s.Args); err != nil {
			return fmt.Errorf("manifest: step %d: %w", i, err)
		}
		if s.TimeoutMs < 0 || s.TimeoutMs > l.TimeoutMs {
			return fmt.Errorf("manifest: step %d: timeout_ms %d must be in [0, limits.timeout_ms]", i, s.TimeoutMs)
		}
	}
	seen := map[string]bool{}
	for i, in := range m.Inputs {
		if err := validFilePath(in.Path, inputDir(in.Path)); err != nil {
			return fmt.Errorf("manifest: input %d: %w", i, err)
		}
		if err := validURL(in.URL); err != nil {
			return fmt.Errorf("manifest: input %d: %w", i, err)
		}
		if seen[in.Path] {
			return fmt.Errorf("manifest: input %d: path %q is used twice", i, in.Path)
		}
		seen[in.Path] = true
	}
	// An input that is a directory of another (/in/a and /in/a/b) cannot both be files.
	for p := range seen {
		for dir := path.Dir(p); dir != InputDir && dir != WorkDir && dir != "/"; dir = path.Dir(dir) {
			if seen[dir] {
				return fmt.Errorf("manifest: input %q is inside input %q", p, dir)
			}
		}
	}
	seen = map[string]bool{}
	for i, out := range m.Outputs {
		if err := validFilePath(out.Path, WorkDir); err != nil {
			return fmt.Errorf("manifest: output %d: %w", i, err)
		}
		if err := validURL(out.URL); err != nil {
			return fmt.Errorf("manifest: output %d: %w", i, err)
		}
		if out.MaxBytes < 0 {
			return fmt.Errorf("manifest: output %d: max_bytes must not be negative", i)
		}
		if strings.ContainsAny(out.ContentType, "\r\n\x00") {
			return fmt.Errorf("manifest: output %d: content_type has control characters", i)
		}
		if seen[out.Path] {
			return fmt.Errorf("manifest: output %d: path %q is used twice", i, out.Path)
		}
		seen[out.Path] = true
	}
	for k, v := range m.Labels {
		if !labelRE.MatchString(k) || !labelRE.MatchString(v) {
			return fmt.Errorf("manifest: label %q=%q uses characters outside [a-zA-Z0-9._/-]", k, v)
		}
	}
	return nil
}

func validArgs(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return errors.New("args needs a program")
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("program %q must not start with '-'", args[0])
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return errors.New("an argument contains a NUL byte")
		}
	}
	return nil
}

// validFilePath checks that p is a clean absolute path strictly below dir.
func validFilePath(p, dir string) error {
	if !strings.HasPrefix(p, dir+"/") || path.Clean(p) != p {
		return fmt.Errorf("path %q must be a clean path under %s/", p, dir)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return fmt.Errorf("path %q has control characters", p)
		}
	}
	return nil
}

func validURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("url must be an absolute http or https URL without user info")
	}
	return nil
}

// inputDir is the directory an input path is downloaded under: WorkDir for a path under /work/, InputDir for any other (which
// validFilePath then checks).
func inputDir(p string) string {
	if strings.HasPrefix(p, WorkDir+"/") {
		return WorkDir
	}
	return InputDir
}

// rel is p relative to dir (p is valid under dir).
func rel(p, dir string) string { return strings.TrimPrefix(p, dir+"/") }
