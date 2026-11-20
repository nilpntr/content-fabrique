package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner runs jobs: one at a time, in the fixed directories of the image contract.
type Runner struct {
	// InDir and WorkDir are where /in and /work are (the image's own /in and /work; a temporary directory in tests).
	InDir, WorkDir string
	// TmpDir holds each job's private HOME and TMPDIR.
	TmpDir string
	// CPUs and MemoryMB are what this runner has (its container's limits). A job that asks for more is refused; zero means
	// unknown, and nothing is checked.
	CPUs     float64
	MemoryMB int
	// Client fetches the inputs and uploads the outputs (see newClient: egress allowlist, no redirects).
	Client *http.Client
	// Self is the cf-job binary, re-executed in front of every step to set its resource limits (execMain).
	Self string
}

// pidHeadroom is added to Limits.PIDs for RLIMIT_NPROC, which counts every thread of the user, cf-job's own included.
const pidHeadroom = 64

// jobError is a failure of the runner's part of a job, with its kind (KindSandbox or KindFiles).
type jobError struct {
	kind string
	err  error
}

func (e *jobError) Error() string { return e.err.Error() }

func sandboxErr(format string, a ...any) error {
	return &jobError{kind: KindSandbox, err: fmt.Errorf(format, a...)}
}

func filesErr(format string, a ...any) error {
	return &jobError{kind: KindFiles, err: fmt.Errorf(format, a...)}
}

// job is one job and its status.
type job struct {
	id     string
	m      Manifest
	hash   [32]byte
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	st       Status
	finished time.Time
}

func newJob(parent context.Context, id string, m Manifest) *job {
	ctx, cancel := context.WithCancel(parent)
	return &job{id: id, m: m, hash: m.hash(), ctx: ctx, cancel: cancel, done: make(chan struct{}),
		st: Status{ID: id, State: StateRunning, ExitCode: -1, Steps: []StepResult{}}}
}

// status is a copy of the job's status.
func (j *job) status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.st
	st.Steps = append([]StepResult{}, j.st.Steps...)
	st.Outputs = append([]OutputInfo(nil), j.st.Outputs...)
	return st
}

func (j *job) terminal() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// checkCapacity refuses a job that asks for more CPU or memory than the runner has: those two are enforced by the container
// the runner is in (compose cpus and mem_limit, the Cloudflare instance type), not per step.
func (r *Runner) checkCapacity(m Manifest) error {
	if r.CPUs > 0 && m.Limits.CPU > r.CPUs {
		return fmt.Errorf("limits.cpu %v is more than this runner's %v", m.Limits.CPU, r.CPUs)
	}
	if r.MemoryMB > 0 && m.Limits.MemoryMB > r.MemoryMB {
		return fmt.Errorf("limits.memory_mb %d is more than this runner's %d", m.Limits.MemoryMB, r.MemoryMB)
	}
	return nil
}

// execute runs the job to its end and closes j.done. The job's timeout covers the downloads, the steps and the uploads.
// j.done is closed only after the job's processes are gone and its files removed, so the next job starts clean.
func (r *Runner) execute(j *job) {
	start := time.Now()
	state, err := r.run(j, start)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.st.State = state
	j.st.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		j.st.Error = err.Error()
		var je *jobError
		if state == StateError && errors.As(err, &je) {
			j.st.ErrorKind = je.kind
		}
	}
	j.st.TimedOut = state == StateTimedOut
	j.finished = time.Now()
	close(j.done)
}

// run does the job's work and returns its terminal state. The job's files are removed before it returns.
func (r *Runner) run(j *job, start time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(j.ctx, j.m.timeout())
	defer cancel()
	// stopped says why ctx ended: DELETE, or the timeout.
	stopped := func() (string, error) {
		if j.ctx.Err() != nil {
			return StateCancelled, errors.New("cancelled")
		}
		return StateTimedOut, fmt.Errorf("the job timed out after %v", j.m.timeout())
	}

	tmp, err := r.prepare()
	defer r.cleanup(tmp)
	if err != nil {
		return StateError, err
	}
	if err := r.download(ctx, j.m); err != nil {
		if ctx.Err() != nil {
			return stopped()
		}
		return StateError, err
	}
	for i, s := range j.m.Steps {
		res, err := r.runStep(ctx, s, j.m, tmp)
		j.mu.Lock()
		j.st.Steps = append(j.st.Steps, res)
		j.st.ExitCode = res.ExitCode
		j.st.DurationMs = time.Since(start).Milliseconds()
		j.mu.Unlock()
		switch {
		case res.TimedOut:
			return StateTimedOut, fmt.Errorf("step %d timed out", i)
		case err != nil && ctx.Err() != nil:
			return stopped()
		case err != nil:
			return StateError, fmt.Errorf("step %d: %w", i, err)
		case res.ExitCode != 0:
			return StateFailed, nil
		}
	}
	outs, err := r.upload(ctx, j.m)
	j.mu.Lock()
	j.st.Outputs = outs
	j.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return stopped()
		}
		return StateError, err
	}
	return StateSucceeded, nil
}

// prepare empties /in and /work (a previous job's files are never seen) and makes the job's private temporary directory.
func (r *Runner) prepare() (string, error) {
	for _, d := range []string{r.InDir, r.WorkDir} {
		if err := emptyDir(d); err != nil {
			return "", sandboxErr("empty %s: %v", d, err)
		}
	}
	tmp, err := os.MkdirTemp(r.TmpDir, "cf-job-")
	if err != nil {
		return "", sandboxErr("temporary directory: %v", err)
	}
	return tmp, nil
}

// cleanup removes what the job left: its inputs, its work files and its temporary directory.
func (r *Runner) cleanup(tmp string) {
	for _, d := range []string{r.InDir, r.WorkDir} {
		if err := emptyDir(d); err != nil {
			fmt.Fprintf(os.Stderr, "cf-job: empty %s: %v\n", d, err)
		}
	}
	if tmp != "" {
		if err := removeAll(tmp); err != nil {
			fmt.Fprintf(os.Stderr, "cf-job: remove %s: %v\n", tmp, err)
		}
	}
}

// emptyDir removes everything inside dir, which stays (it may be a mount point).
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := removeAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeAll is os.RemoveAll that, when that fails, first makes the directories writable again: a step may have taken the
// write bit off one. The walk is confined to p's parent with os.Root and never follows a symlink, so a link the job left (or
// swapped in) cannot point the chmod outside.
func removeAll(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return err
	}
	makeWritable(root, filepath.Base(p))
	_ = root.Close()
	return os.RemoveAll(p)
}

func makeWritable(root *os.Root, name string) {
	fi, err := root.Lstat(name)
	if err != nil || !fi.IsDir() {
		return
	}
	_ = root.Chmod(name, 0o700)
	entries, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		return
	}
	for _, e := range entries {
		makeWritable(root, name+"/"+e.Name())
	}
}

// download fetches every input to its path, read-only, within MaxInputBytes in total.
func (r *Runner) download(ctx context.Context, m Manifest) error {
	root, err := os.OpenRoot(r.InDir)
	if err != nil {
		return sandboxErr("open %s: %v", r.InDir, err)
	}
	defer func() { _ = root.Close() }()
	remaining := m.maxInput()
	for _, in := range m.Inputs {
		n, err := r.fetch(ctx, root, in, remaining)
		if err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

func (r *Runner) fetch(ctx context.Context, root *os.Root, in Input, remaining int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return 0, sandboxErr("input %s: %v", in.Path, err)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return 0, sandboxErr("input %s: %v", in.Path, plainErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, sandboxErr("input %s: GET answered %s", in.Path, resp.Status)
	}
	if resp.ContentLength > remaining {
		return 0, filesErr("input %s: %d bytes is over what is left of max_input_bytes (%d)", in.Path, resp.ContentLength, remaining)
	}
	name := rel(in.Path, InputDir)
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return 0, sandboxErr("input %s: %v", in.Path, err)
		}
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, sandboxErr("input %s: %v", in.Path, err)
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, remaining+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return n, sandboxErr("input %s: %v", in.Path, plainErr(err))
	case n > remaining:
		return n, filesErr("input %s: the inputs are over max_input_bytes", in.Path)
	case resp.ContentLength >= 0 && n != resp.ContentLength:
		return n, sandboxErr("input %s: got %d of %d bytes", in.Path, n, resp.ContentLength)
	}
	if err := root.Chmod(name, 0o444); err != nil {
		return n, sandboxErr("input %s: %v", in.Path, err)
	}
	return n, nil
}

// runStep runs one step through `cf-job __exec`, which sets the limits and then becomes the program. A non-zero exit is a
// result, not an error; the error is for a step that could not be started or was stopped by ctx (res.TimedOut tells the
// step's own timeout apart).
func (r *Runner) runStep(ctx context.Context, s Step, m Manifest, tmp string) (StepResult, error) {
	res := StepResult{ExitCode: -1}
	stepCtx := ctx
	if s.TimeoutMs > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(s.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	prog := s.Args[0]
	if strings.ContainsRune(prog, '/') && !filepath.IsAbs(prog) {
		return res, sandboxErr("program %q must be a bare name or an absolute path", prog)
	}
	prog, err := exec.LookPath(prog)
	if err != nil {
		return res, sandboxErr("program %q: %v", s.Args[0], err)
	}
	argv := append([]string{execCommand, strconv.Itoa(m.Limits.PIDs + pidHeadroom), strconv.FormatInt(fileCap(m), 10), prog}, s.Args...)
	cmd := exec.CommandContext(stepCtx, r.Self, argv...) // #nosec G204 -- no shell; the program is looked up by name, the runner is this binary
	cmd.Dir = r.WorkDir
	// Exactly these: no token or other variable of the runner reaches a tool.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "TMPDIR=" + tmp}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := newBoundedBuffer(m.maxOutput()), newBoundedBuffer(m.maxOutput())
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	runErr := cmd.Run()
	res.DurationMs = time.Since(start).Milliseconds()
	res.Stdout, res.StdoutTruncated = stdout.snapshot()
	res.Stderr, res.StderrTruncated = stderr.snapshot()

	ps := cmd.ProcessState
	switch {
	case ps != nil && ps.Exited():
		res.ExitCode = ps.ExitCode()
		if res.ExitCode == execFailed && bytes.HasPrefix(res.Stderr, []byte(execMarker)) {
			res.ExitCode = -1
			return res, sandboxErr("%s", strings.TrimSpace(string(res.Stderr)))
		}
		return res, nil
	case stepCtx.Err() != nil && ps != nil:
		// Killed because a context ended: the step's own timeout, the job's timeout or a DELETE.
		res.TimedOut = ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded)
		return res, stepCtx.Err()
	case ps != nil:
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.ExitCode = 128 + int(ws.Signal())
		}
		return res, nil
	default:
		return res, sandboxErr("start %q: %v", s.Args[0], runErr)
	}
}

// fileCap is RLIMIT_FSIZE for the steps: the largest output cap, so a tool cannot write a file the upload would refuse.
func fileCap(m Manifest) int64 {
	c := int64(DefaultMaxFileBytes)
	if len(m.Outputs) > 0 {
		c = 0
		for _, o := range m.Outputs {
			c = max(c, o.maxBytes())
		}
	}
	return c
}

// upload checks every output first (present, a regular file and not a symlink, within its cap) and only then uploads them,
// so a job with a bad output uploads nothing.
func (r *Runner) upload(ctx context.Context, m Manifest) ([]OutputInfo, error) {
	if len(m.Outputs) == 0 {
		return nil, nil
	}
	root, err := os.OpenRoot(r.WorkDir)
	if err != nil {
		return nil, sandboxErr("open %s: %v", r.WorkDir, err)
	}
	defer func() { _ = root.Close() }()
	files := make([]*os.File, len(m.Outputs))
	sizes := make([]int64, len(m.Outputs))
	defer func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}()
	for i, o := range m.Outputs {
		f, err := root.OpenFile(rel(o.Path, WorkDir), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, filesErr("output %s is missing", o.Path)
		case err != nil:
			return nil, filesErr("output %s: not a regular file inside %s (%v)", o.Path, WorkDir, plainPathErr(err))
		}
		files[i] = f
		fi, err := f.Stat()
		if err != nil {
			return nil, sandboxErr("output %s: %v", o.Path, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, filesErr("output %s is not a regular file", o.Path)
		}
		if fi.Size() > o.maxBytes() {
			return nil, filesErr("output %s: %d bytes is over its cap of %d", o.Path, fi.Size(), o.maxBytes())
		}
		sizes[i] = fi.Size()
	}
	var done []OutputInfo
	for i, o := range m.Outputs {
		if err := r.put(ctx, o, files[i], sizes[i]); err != nil {
			return done, err
		}
		done = append(done, OutputInfo{Path: o.Path, Bytes: sizes[i]})
	}
	return done, nil
}

func (r *Runner) put(ctx context.Context, o Output, f *os.File, size int64) error {
	var body io.Reader = http.NoBody
	if size > 0 {
		body = io.LimitReader(f, size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, o.URL, body)
	if err != nil {
		return sandboxErr("output %s: %v", o.Path, err)
	}
	req.ContentLength = size
	if o.ContentType != "" {
		req.Header.Set("Content-Type", o.ContentType)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return sandboxErr("output %s: %v", o.Path, plainErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return sandboxErr("output %s: PUT answered %s", o.Path, resp.Status)
	}
	return nil
}

// plainErr drops the URL from an HTTP client error: a presigned URL carries its signature and must not reach logs or the status.
func plainErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// plainPathErr drops the path from a filesystem error (the message names the output already).
func plainPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// boundedBuffer keeps the first max bytes written to it and discards the rest, so a chatty tool never blocks on a full pipe
// and never grows the runner's memory without bound.
type boundedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	max       int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{max: limit} }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := min(b.max-len(b.buf), len(p))
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	if room < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte{}, b.buf...), b.truncated
}
