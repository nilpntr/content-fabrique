package media

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

// Input is a host file the tool may read. Container is where the tool sees it; use InputPath.
type Input struct {
	Host      string
	Container string
}

// InputPath is the container path of the n-th input of an Invocation.
func InputPath(n int) string { return sandbox.InputDir + "/" + strconv.Itoa(n) }

// OutputPath is the container path of a file in the invocation's output directory.
func OutputPath(name string) string { return sandbox.WorkDir + "/" + name }

// Invocation is one run of a media tool. Args holds the program and its arguments, written against container paths.
type Invocation struct {
	Args   []string
	Inputs []Input
	// OutputDir is a host directory mounted writable at sandbox.WorkDir; empty means the tool gets no writable mount.
	OutputDir string
	// Timeout overrides the runner's wall-clock limit for this one run; zero keeps the runner's. A long clip needs more
	// time than a probe does, so the caller scales it.
	Timeout time.Duration
}

// Runner executes media tools. In production it is a SandboxRunner.
type Runner interface {
	Run(ctx context.Context, inv Invocation) (sandbox.Result, error)
}

// SandboxRunner runs invocations as sandbox jobs of the media-tools image.
type SandboxRunner struct {
	Provider sandbox.Provider
	Image    string
	Limits   sandbox.Limits
	// MaxOutputBytes caps captured stdout and stderr; zero means the sandbox default.
	MaxOutputBytes int
	Labels         map[string]string
}

// WithLabels returns a copy of the runner whose jobs carry the labels (workspace, asset and job kind, for audit and billing).
func (r *SandboxRunner) WithLabels(labels map[string]string) Runner {
	c := *r
	c.Labels = labels
	return &c
}

// Run implements Runner.
func (r *SandboxRunner) Run(ctx context.Context, inv Invocation) (sandbox.Result, error) {
	if r.Provider == nil {
		return sandbox.Result{}, errors.New("media: SandboxRunner has no provider")
	}
	mounts := make([]sandbox.Mount, 0, len(inv.Inputs)+1)
	for _, in := range inv.Inputs {
		mounts = append(mounts, sandbox.Mount{Host: in.Host, Container: in.Container})
	}
	if inv.OutputDir != "" {
		mounts = append(mounts, sandbox.Mount{Host: inv.OutputDir, Container: sandbox.WorkDir, Writable: true})
	}
	limits := r.Limits
	if inv.Timeout > 0 {
		limits.Timeout = inv.Timeout
	}
	return r.Provider.Run(ctx, sandbox.Job{
		Image:          r.Image,
		Args:           inv.Args,
		Mounts:         mounts,
		Limits:         limits,
		Labels:         r.Labels,
		MaxOutputBytes: r.MaxOutputBytes,
	})
}

// ToolError is a tool that ran and failed.
type ToolError struct {
	Tool     string
	ExitCode int
	Stderr   string // the start of the tool's stderr
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("media: %s exited with code %d: %s", e.Tool, e.ExitCode, e.Stderr)
}

// StderrSnippet is the start of a tool's stderr (at most 1 KiB), the part worth putting in an error message.
func StderrSnippet(b []byte) string {
	const limit = 1024
	if len(b) > limit {
		b = b[:limit]
	}
	return string(b)
}
