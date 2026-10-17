// Package sandbox is the one interface through which every process that touches untrusted media runs (architecture
// section 8, D-08). A Provider runs one Job in one fresh sandbox and returns its Result; there is no long-lived sandbox
// to reuse across tenants.
//
// Providers in this package:
//   - local:    runs the program directly on the host. For development and tests only; it isolates nothing.
//   - docker:   docker run with no network, a read-only root, no capabilities and resource limits.
//   - hardened: the docker provider under the gVisor runtime (runsc). Used for all deterministic media jobs.
//
// The isolation rules are properties of the Job type, not options: the network is always off, inputs are read-only,
// there is at most one writable directory, and a job without limits is refused.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Container paths of the image contract. Inputs are mounted read-only under InputDir and the only writable mount is
// WorkDir (plus the provider's tmpfs on /tmp).
const (
	InputDir = "/in"
	WorkDir  = "/work"
)

// Provider runs jobs in a sandbox.
type Provider interface {
	// Name is the provider's name as used in configuration: "local", "docker" or "hardened".
	Name() string
	// Run executes the job and waits for it. A non-zero exit code is not an error: it is reported in Result. An error
	// means the job was refused (validation), the sandbox could not be run, the timeout expired (ErrTimeout) or ctx
	// was cancelled. A Result is returned alongside ErrTimeout with whatever output was captured.
	Run(ctx context.Context, job Job) (Result, error)
}

// ErrTimeout is returned (wrapped) when a job exceeded Limits.Timeout and was killed.
var ErrTimeout = errors.New("sandbox: job timed out")

// Network is the job's network policy. Only NetworkNone exists: egress is deny-all, and bytes reach a sandbox through
// mounts (or, later, presigned URLs handled by the caller), never through an open network.
type Network string

// NetworkNone is the zero value and the only accepted policy.
const NetworkNone Network = ""

// Job is one program run in a fresh sandbox.
type Job struct {
	// Image is the container image (docker and hardened; the local provider ignores it).
	Image string
	// Args is the program and its arguments, run without a shell. Args[0] is looked up in the image's PATH.
	Args []string
	// Mounts are the host paths the job can see. A mount is read-only unless Writable; at most one may be Writable.
	Mounts []Mount
	// Limits are mandatory.
	Limits Limits
	// Network must be NetworkNone.
	Network Network
	// Labels are attached to the container for billing and audit (workspace id, job kind). Keys and values are
	// restricted to a safe character set. They are metadata only; never put secrets here.
	Labels map[string]string
	// MaxOutputBytes caps each of Stdout and Stderr; the rest is discarded. Zero means DefaultMaxOutputBytes.
	MaxOutputBytes int
}

// Mount makes a host file or directory visible inside the sandbox.
type Mount struct {
	// Host is an absolute path under one of the provider's allowed roots.
	Host string
	// Container is where it appears: a path under InputDir for a read-only mount, exactly WorkDir for the writable one.
	Container string
	// Writable marks the job's output directory. It must be a directory.
	Writable bool
}

// Limits bound a job's resources. Every field is required.
type Limits struct {
	CPU      float64       // vCPUs, for example 2 or 0.5
	MemoryMB int           // hard memory limit, swap included
	Timeout  time.Duration // wall-clock limit for the whole job
	PIDs     int           // maximum number of processes and threads
}

// DefaultLimits are sensible limits for a media job on a worker.
func DefaultLimits() Limits {
	return Limits{CPU: 2, MemoryMB: 2048, Timeout: 10 * time.Minute, PIDs: 512}
}

// Result is the outcome of a job.
type Result struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool // output beyond MaxOutputBytes was discarded
	StderrTruncated bool
	Duration        time.Duration
	TimedOut        bool
}

// DefaultMaxOutputBytes caps Stdout and Stderr each when Job.MaxOutputBytes is zero.
const DefaultMaxOutputBytes = 4 << 20

// Policy is the host-side configuration shared by the providers.
type Policy struct {
	// AllowedRoots are absolute directories; every mount's Host path, after symlinks are resolved, must be inside one
	// of them. Empty means no mount is allowed (fail closed).
	AllowedRoots []string
}

// Hard caps so a bug in a caller cannot request an absurd sandbox.
const (
	maxCPU      = 64
	maxMemoryMB = 256 * 1024
	maxPIDs     = 65536
	maxTimeout  = 24 * time.Hour
)

var (
	imageRE = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._/:@+-]{0,254}$`)
	labelRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,128}$`)
)

// resolvedMount is a validated mount with its host path resolved through symlinks.
type resolvedMount struct {
	Host      string
	Container string
	Writable  bool
}

// validated is a job that passed validation, ready for a provider to execute.
type validated struct {
	Job
	mounts    []resolvedMount
	maxOutput int
}

// validate checks a job against the isolation rules. needImage is true for container providers.
func (p Policy) validate(job Job, needImage bool) (validated, error) {
	v := validated{Job: job, maxOutput: job.MaxOutputBytes}
	if v.maxOutput == 0 {
		v.maxOutput = DefaultMaxOutputBytes
	}
	if v.maxOutput < 0 {
		return v, errors.New("sandbox: MaxOutputBytes must not be negative")
	}
	if job.Network != NetworkNone {
		return v, fmt.Errorf("sandbox: network policy %q is not allowed: the network is always off", job.Network)
	}
	if needImage && !imageRE.MatchString(job.Image) {
		return v, fmt.Errorf("sandbox: invalid image reference %q", job.Image)
	}
	if len(job.Args) == 0 || job.Args[0] == "" {
		return v, errors.New("sandbox: Args needs a program")
	}
	if strings.HasPrefix(job.Args[0], "-") {
		return v, fmt.Errorf("sandbox: program %q must not start with '-'", job.Args[0])
	}
	for _, a := range job.Args {
		if strings.ContainsRune(a, 0) {
			return v, errors.New("sandbox: argument contains a NUL byte")
		}
	}
	if err := validateLimits(job.Limits); err != nil {
		return v, err
	}
	for k, val := range job.Labels {
		if !labelRE.MatchString(k) || !labelRE.MatchString(val) {
			return v, fmt.Errorf("sandbox: label %q=%q uses characters outside [a-zA-Z0-9._/-]", k, val)
		}
	}
	mounts, err := p.resolveMounts(job.Mounts)
	if err != nil {
		return v, err
	}
	v.mounts = mounts
	return v, nil
}

func validateLimits(l Limits) error {
	switch {
	case l.CPU <= 0 || l.CPU > maxCPU:
		return fmt.Errorf("sandbox: Limits.CPU %v must be in (0, %d]", l.CPU, maxCPU)
	case l.MemoryMB <= 0 || l.MemoryMB > maxMemoryMB:
		return fmt.Errorf("sandbox: Limits.MemoryMB %d must be in (0, %d]", l.MemoryMB, maxMemoryMB)
	case l.PIDs <= 0 || l.PIDs > maxPIDs:
		return fmt.Errorf("sandbox: Limits.PIDs %d must be in (0, %d]", l.PIDs, maxPIDs)
	case l.Timeout <= 0 || l.Timeout > maxTimeout:
		return fmt.Errorf("sandbox: Limits.Timeout %v must be in (0, %v]", l.Timeout, maxTimeout)
	}
	return nil
}

// containerPathOK reports whether p is a clean absolute container path made of plain characters.
func containerPathOK(p string) bool {
	if p == "" || !path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ',' || r == ':' || r == '"' || r == '\\' || r == ' ' {
			return false
		}
	}
	return true
}

func (p Policy) resolveMounts(mounts []Mount) ([]resolvedMount, error) {
	roots, err := p.resolvedRoots()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	writable := 0
	out := make([]resolvedMount, 0, len(mounts))
	for _, m := range mounts {
		if !containerPathOK(m.Container) {
			return nil, fmt.Errorf("sandbox: invalid container path %q", m.Container)
		}
		if seen[m.Container] {
			return nil, fmt.Errorf("sandbox: container path %q is mounted twice", m.Container)
		}
		seen[m.Container] = true
		if m.Writable {
			writable++
			if writable > 1 {
				return nil, errors.New("sandbox: at most one writable mount is allowed")
			}
			if m.Container != WorkDir {
				return nil, fmt.Errorf("sandbox: the writable mount must be at %s, not %q", WorkDir, m.Container)
			}
		} else if !strings.HasPrefix(m.Container, InputDir+"/") {
			return nil, fmt.Errorf("sandbox: a read-only mount must be under %s/, not %q", InputDir, m.Container)
		}
		host, err := resolveHost(m, roots)
		if err != nil {
			return nil, err
		}
		out = append(out, resolvedMount{Host: host, Container: m.Container, Writable: m.Writable})
	}
	return out, nil
}

func (p Policy) resolvedRoots() ([]string, error) {
	roots := make([]string, 0, len(p.AllowedRoots))
	for _, r := range p.AllowedRoots {
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("sandbox: allowed root %q is not absolute", r)
		}
		resolved, err := filepath.EvalSymlinks(r)
		if err != nil {
			return nil, fmt.Errorf("sandbox: allowed root %q: %w", r, err)
		}
		roots = append(roots, resolved)
	}
	return roots, nil
}
