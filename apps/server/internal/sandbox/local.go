package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Local runs the program directly on the host. It exists so development machines and tests that have ffmpeg installed
// can use the same code path. IT ISOLATES NOTHING: no network block, no filesystem confinement, and the memory, CPU and
// PID limits are not enforced (only the timeout is). Never use it for untrusted files outside development.
//
// Mounts are emulated: any argument equal to a mount's Container path, or below it, is rewritten to the host path, and
// the environment is reduced to HOME and TMPDIR so credentials in the worker's environment do not leak.
type Local struct {
	Policy
}

// NewLocal returns the local provider.
func NewLocal(policy Policy) *Local { return &Local{Policy: policy} }

// Name implements Provider.
func (l *Local) Name() string { return "local" }

// Run implements Provider.
func (l *Local) Run(ctx context.Context, job Job) (Result, error) {
	v, err := l.validate(job, false)
	if err != nil {
		return Result{}, err
	}
	if strings.ContainsRune(v.Args[0], filepath.Separator) {
		return Result{}, fmt.Errorf("sandbox: local provider takes a bare program name, not %q", v.Args[0])
	}
	prog, err := exec.LookPath(v.Args[0])
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: local provider: %w", err)
	}
	tmp, err := os.MkdirTemp("", "cf-local-")
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: local provider: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	dir := tmp
	for _, m := range v.mounts {
		if m.Writable {
			dir = m.Host
		}
	}
	args := make([]string, len(v.Args)-1)
	for i, a := range v.Args[1:] {
		args[i] = rewriteMountPath(a, v.mounts)
	}

	runCtx, cancel := context.WithTimeout(ctx, v.Limits.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, prog, args...) // #nosec G204 -- no shell; program is looked up by bare name
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + tmp, "TMPDIR=" + tmp} // the program is started by absolute path, so no PATH
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := newBoundedBuffer(v.maxOutput), newBoundedBuffer(v.maxOutput)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	runErr := cmd.Run()
	res := Result{Duration: time.Since(start)}
	res.Stdout, res.StdoutTruncated = stdout.snapshot()
	res.Stderr, res.StderrTruncated = stderr.snapshot()

	kill, err := finish(runCtx, ctx.Err(), cmd, runErr, v.Limits.Timeout, true, &res)
	_ = kill // the process group was already killed by cmd.Cancel
	return res, err
}

// rewriteMountPath maps a container path (or a path below one) to its host path.
func rewriteMountPath(arg string, mounts []resolvedMount) string {
	for _, m := range mounts {
		if arg == m.Container {
			return m.Host
		}
		if strings.HasPrefix(arg, m.Container+"/") {
			p := filepath.Join(m.Host, arg[len(m.Container):])
			if !insideAny(p, []string{m.Host}) {
				return arg // a ".." escaping the mount is left alone and fails as a missing path
			}
			return p
		}
	}
	return arg
}
