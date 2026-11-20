package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Remote runs a job at a runner over HTTP (docs/design/render-runtime.md, section 3): `cf-job serve` in the media-tools image
// under compose (dev, VPS), or the gateway Worker in front of a Cloudflare Container (staging, production). Both serve
//
//	PUT    /jobs/{id}   the manifest; idempotent (the same id and manifest answer the running job's status)
//	GET    /jobs/{id}   the status
//	DELETE /jobs/{id}   stop and forget the job
//
// with a bearer token. The job's files travel as presigned URLs (Job.Inputs, Job.Outputs) instead of mounts, and a job may
// carry ordered Steps that share one /work. Run sends the manifest, polls the status until the job ends and deletes it; when
// ctx ends or the runner stays silent past Limits.Timeout plus Grace, it deletes the job (the runner kills it) and returns.
//
// Errors follow Provider: a step that exits non-zero is a Result, ErrTimeout is a step or job past its timeout, ErrSandbox is
// the runner failing its part (unreachable, busy past BusyWait, a download or upload failing, a program that could not start),
// ErrFiles is the job's files breaking a rule, and a manifest the runner refuses is a plain error.
type Remote struct {
	// BaseURL is the runner, for example http://localhost:8080 (MEDIA_RUNNER_URL).
	BaseURL string
	// Token is the bearer token (SANDBOX_GATEWAY_TOKEN).
	Token string
	// Client is the HTTP client; nil means one with sensible timeouts.
	Client *http.Client
	// PollInterval is the time between two status reads; default 500 ms.
	PollInterval time.Duration
	// BusyWait is how long a PUT keeps trying while the runner is busy with another job (503); default 2 minutes. After it the
	// job fails with ErrSandbox and the queue retries it.
	BusyWait time.Duration
	// Grace is how long past Limits.Timeout the provider waits for the runner to report the timeout itself; default 2 minutes.
	Grace time.Duration
}

// NewRemote returns the remote provider for the runner at baseURL.
func NewRemote(baseURL, token string) (*Remote, error) {
	if err := checkRunnerURL(baseURL); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("sandbox: the remote provider needs the runner's bearer token (SANDBOX_GATEWAY_TOKEN)")
	}
	return &Remote{BaseURL: strings.TrimRight(baseURL, "/"), Token: token}, nil
}

// CheckRemoteAllowed refuses a remote runner other than the gateway when cfEnv, the value of CF_ENV, names a staging or
// production environment: there the runner is the sandbox gateway Worker (SANDBOX_GATEWAY_URL), never a cf-job serve on a host.
func CheckRemoteAllowed(cfEnv, runnerURL, gatewayURL string) error {
	switch strings.ToLower(strings.TrimSpace(cfEnv)) {
	case "staging", "production":
		norm := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
		if gatewayURL == "" || norm(runnerURL) != norm(gatewayURL) {
			return fmt.Errorf("the remote sandbox runner must be the sandbox gateway (SANDBOX_GATEWAY_URL) when CF_ENV=%s", cfEnv)
		}
	}
	return nil
}

func checkRunnerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("sandbox: the remote runner URL %q must be an absolute http or https URL without user info, query or fragment", raw)
	}
	return nil
}

// Name implements Provider.
func (r *Remote) Name() string { return "remote" }

const (
	defaultPollInterval = 500 * time.Millisecond
	defaultBusyWait     = 2 * time.Minute
	defaultGrace        = 2 * time.Minute
	// maxPollFailures is how many status reads in a row may fail (network) before the job counts as lost.
	maxPollFailures = 10
)

// Run implements Provider.
func (r *Remote) Run(ctx context.Context, job Job) (Result, error) {
	m, maxOut, err := remoteManifest(job)
	if err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: remote manifest: %w", err)
	}
	limit := int64(maxOut)*3*int64(len(m.Steps)) + 1<<20 // two base64 streams per step, and the rest

	start := time.Now()
	st, err := r.start(ctx, job.ID, body, limit)
	if err != nil {
		// The PUT may have reached the runner and started the job although its answer was lost: stop it, or it holds the
		// runner for its whole timeout. A refusal (400, 409) started nothing, and a 409 names another manifest's job.
		if ctx.Err() != nil || errors.Is(err, ErrSandbox) {
			r.forget(job.ID)
		}
		return Result{ExitCode: -1}, err
	}
	// The runner's clock starts when it accepts the job, so this one does too (time spent waiting for a busy runner is not
	// the job's).
	runCtx, cancel := context.WithTimeout(ctx, job.Limits.Timeout+orDefault(r.Grace, defaultGrace))
	defer cancel()
	failures := 0
	tick := time.NewTicker(orDefault(r.PollInterval, defaultPollInterval))
	defer tick.Stop()
	for st.State == remoteRunning {
		select {
		case <-runCtx.Done():
			r.forget(job.ID)
			res := Result{ExitCode: -1, Duration: time.Since(start)}
			err := r.stopped(ctx, job)
			res.TimedOut = errors.Is(err, ErrTimeout)
			return res, err
		case <-tick.C:
		}
		next, code, err := r.call(runCtx, http.MethodGet, job.ID, nil, limit)
		switch {
		case err != nil && runCtx.Err() == nil:
			if failures++; failures >= maxPollFailures {
				r.forget(job.ID)
				return Result{ExitCode: -1}, fmt.Errorf("%w: remote job %s: the runner stopped answering: %w", ErrSandbox, job.ID, err)
			}
		case err != nil:
			// runCtx ended during the read: the select above handles it.
		case code == http.StatusNotFound:
			return Result{ExitCode: -1}, fmt.Errorf("%w: remote job %s is gone from the runner (it restarted?)", ErrSandbox, job.ID)
		case code != http.StatusOK:
			if failures++; failures >= maxPollFailures {
				r.forget(job.ID)
				return Result{ExitCode: -1}, fmt.Errorf("%w: remote job %s: GET answered %d", ErrSandbox, job.ID, code)
			}
		default:
			failures = 0
			st = next
		}
	}
	r.forget(job.ID)
	return st.result(job, maxOut)
}

// stopped is the error for a job whose context ended: the caller's cancellation, or the provider's own deadline (the runner
// never reported the timeout).
func (r *Remote) stopped(ctx context.Context, job Job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w after %v (the runner did not report it)", ErrTimeout, job.Limits.Timeout)
}

// start PUTs the manifest. A busy runner (503) is asked again until BusyWait; a network error is retried a few times, which
// is safe because the PUT is idempotent.
func (r *Remote) start(ctx context.Context, id string, body []byte, limit int64) (remoteStatus, error) {
	busyUntil := time.Now().Add(orDefault(r.BusyWait, defaultBusyWait))
	netFailures := 0
	for {
		st, code, err := r.call(ctx, http.MethodPut, id, body, limit)
		wait := time.Second
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			if netFailures++; netFailures >= 3 {
				return st, fmt.Errorf("%w: remote job %s: %w", ErrSandbox, id, err)
			}
		case code == http.StatusCreated || code == http.StatusOK:
			return st, nil
		case code == http.StatusServiceUnavailable:
			if time.Now().After(busyUntil) {
				return st, fmt.Errorf("%w: remote job %s: the runner is still busy after %v: %s", ErrSandbox, id, orDefault(r.BusyWait, defaultBusyWait), snippet([]byte(st.Error)))
			}
			wait = 2 * time.Second
		case code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge:
			return st, fmt.Errorf("sandbox: the runner refused remote job %s: %s", id, snippet([]byte(st.Error)))
		case code == http.StatusConflict:
			return st, fmt.Errorf("sandbox: remote job %s already exists at the runner with a different manifest", id)
		default:
			return st, fmt.Errorf("%w: remote job %s: PUT answered %d: %s", ErrSandbox, id, code, snippet([]byte(st.Error)))
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// forget deletes the job at the runner (stopping it if it still runs). Failures are ignored: the runner forgets finished jobs
// by itself, and its own deadline stops a running one.
func (r *Remote) forget(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, _, _ = r.call(ctx, http.MethodDelete, id, nil, 1<<20)
}

func (r *Remote) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return defaultRemoteClient
}

var defaultRemoteClient = &http.Client{
	Timeout:       time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// call makes one request and decodes the answer: a status, or an error message. err is a transport error only.
func (r *Remote) call(ctx context.Context, method, id string, body []byte, limit int64) (remoteStatus, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+"/jobs/"+url.PathEscape(id), rd)
	if err != nil {
		return remoteStatus{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return remoteStatus{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return remoteStatus{}, 0, err
	}
	if int64(len(data)) > limit {
		return remoteStatus{}, 0, fmt.Errorf("the runner's answer is over %d bytes", limit)
	}
	var st remoteStatus
	if len(data) > 0 && resp.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(data, &st); err != nil && resp.StatusCode/100 == 2 {
			return remoteStatus{}, 0, fmt.Errorf("decode the runner's answer: %w", err)
		}
	}
	if resp.StatusCode/100 == 2 && method != http.MethodDelete && !validRemoteState(st.State) {
		return remoteStatus{}, 0, fmt.Errorf("the runner answered an unknown state %q", st.State)
	}
	return st, resp.StatusCode, nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// The wire format, shared with cf-job (packages/media-tools/job-runner/manifest.go). TestRemoteWireMatchesRunnerContract
// keeps the runner's testdata equal to what this file writes.

type remoteManifestJSON struct {
	Inputs         []remoteInput     `json:"inputs"`
	Steps          []remoteStep      `json:"steps"`
	Outputs        []remoteOutput    `json:"outputs"`
	Limits         remoteLimits      `json:"limits"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	MaxInputBytes  int64             `json:"max_input_bytes,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type remoteInput struct {
	URL  string `json:"url"`
	Path string `json:"path"`
}

type remoteStep struct {
	Args      []string `json:"args"`
	TimeoutMs int64    `json:"timeout_ms,omitempty"`
}

type remoteOutput struct {
	Path        string `json:"path"`
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
}

type remoteLimits struct {
	CPU       float64 `json:"cpu"`
	MemoryMB  int     `json:"memory_mb"`
	TimeoutMs int64   `json:"timeout_ms"`
	PIDs      int     `json:"pids"`
}

const (
	remoteRunning   = "running"
	remoteSucceeded = "succeeded"
	remoteFailed    = "failed"
	remoteTimedOut  = "timed_out"
	remoteError     = "error"
	remoteCancelled = "cancelled"

	remoteKindSandbox = "sandbox"
	remoteKindFiles   = "files"
)

func validRemoteState(s string) bool {
	switch s {
	case remoteRunning, remoteSucceeded, remoteFailed, remoteTimedOut, remoteError, remoteCancelled:
		return true
	}
	return false
}

type remoteStatus struct {
	ID         string             `json:"id"`
	State      string             `json:"state"`
	Error      string             `json:"error,omitempty"`
	ErrorKind  string             `json:"error_kind,omitempty"`
	ExitCode   int                `json:"exit_code"`
	DurationMs int64              `json:"duration_ms"`
	TimedOut   bool               `json:"timed_out"`
	Steps      []remoteStepResult `json:"steps"`
	Outputs    []remoteOutputInfo `json:"outputs,omitempty"`
}

type remoteStepResult struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	DurationMs      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out"`
}

type remoteOutputInfo struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// result turns a terminal status into the Provider's Result and error.
func (st remoteStatus) result(job Job, maxOut int) (Result, error) {
	res := Result{ExitCode: -1, Duration: time.Duration(st.DurationMs) * time.Millisecond}
	for _, s := range st.Steps {
		sr := Result{ExitCode: s.ExitCode, Stdout: s.Stdout, Stderr: s.Stderr, StdoutTruncated: s.StdoutTruncated,
			StderrTruncated: s.StderrTruncated, Duration: time.Duration(s.DurationMs) * time.Millisecond, TimedOut: s.TimedOut}
		// The runner caps the streams too; this keeps the promise of MaxOutputBytes whatever it sends.
		if len(sr.Stdout) > maxOut {
			sr.Stdout, sr.StdoutTruncated = sr.Stdout[:maxOut], true
		}
		if len(sr.Stderr) > maxOut {
			sr.Stderr, sr.StderrTruncated = sr.Stderr[:maxOut], true
		}
		res.Steps = append(res.Steps, sr)
	}
	if n := len(res.Steps); n > 0 {
		last := res.Steps[n-1]
		res.ExitCode, res.Stdout, res.Stderr = last.ExitCode, last.Stdout, last.Stderr
		res.StdoutTruncated, res.StderrTruncated, res.TimedOut = last.StdoutTruncated, last.StderrTruncated, last.TimedOut
	}
	want := len(job.Steps)
	if want == 0 {
		want = 1
	}
	if len(res.Steps) > want {
		return res, fmt.Errorf("%w: remote job %s reported %d steps for %d", ErrSandbox, job.ID, len(res.Steps), want)
	}
	switch st.State {
	case remoteSucceeded:
		if len(res.Steps) != want || res.ExitCode != 0 || len(st.Outputs) != len(job.Outputs) {
			return res, fmt.Errorf("%w: remote job %s reported success with %d of %d steps and %d of %d outputs", ErrSandbox, job.ID, len(res.Steps), want, len(st.Outputs), len(job.Outputs))
		}
		return res, nil
	case remoteFailed:
		if res.ExitCode == 0 || len(res.Steps) == 0 {
			return res, fmt.Errorf("%w: remote job %s reported a failure without a failing step", ErrSandbox, job.ID)
		}
		return res, nil
	case remoteTimedOut:
		res.TimedOut = true
		return res, fmt.Errorf("%w after %v: %s", ErrTimeout, job.Limits.Timeout, snippet([]byte(st.Error)))
	case remoteError:
		if st.ErrorKind == remoteKindFiles {
			return res, fmt.Errorf("%w: remote job %s: %s", ErrFiles, job.ID, snippet([]byte(st.Error)))
		}
		return res, fmt.Errorf("%w: remote job %s: %s", ErrSandbox, job.ID, snippet([]byte(st.Error)))
	case remoteCancelled:
		return res, fmt.Errorf("%w: remote job %s was cancelled at the runner", ErrSandbox, job.ID)
	}
	return res, fmt.Errorf("%w: remote job %s ended in state %q", ErrSandbox, job.ID, st.State)
}

var remoteIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// remoteManifest validates a job for the remote provider and returns its manifest and the stream cap.
func remoteManifest(job Job) (remoteManifestJSON, int, error) {
	var m remoteManifestJSON
	maxOut := job.MaxOutputBytes
	if maxOut == 0 {
		maxOut = DefaultMaxOutputBytes
	}
	switch {
	case maxOut < 0:
		return m, 0, errors.New("sandbox: MaxOutputBytes must not be negative")
	case job.MaxInputBytes < 0:
		return m, 0, errors.New("sandbox: MaxInputBytes must not be negative")
	case len(job.Mounts) > 0:
		return m, 0, errors.New("sandbox: a remote job takes Inputs and Outputs (presigned URLs), not Mounts")
	case job.Network != NetworkNone:
		return m, 0, fmt.Errorf("sandbox: network policy %q is not allowed: the network is always off", job.Network)
	case !remoteIDRE.MatchString(job.ID):
		return m, 0, fmt.Errorf("sandbox: remote job ID %q must match %s", job.ID, remoteIDRE)
	case len(job.Args) > 0 && len(job.Steps) > 0:
		return m, 0, errors.New("sandbox: a job sets Args or Steps, not both")
	}
	if err := validateLimits(job.Limits); err != nil {
		return m, 0, err
	}
	steps := job.Steps
	if len(steps) == 0 {
		steps = []Step{{Args: job.Args}}
	}
	for i, s := range steps {
		if err := checkArgs(s.Args); err != nil {
			return m, 0, fmt.Errorf("sandbox: step %d: %w", i, err)
		}
		if s.Timeout < 0 || s.Timeout > job.Limits.Timeout {
			return m, 0, fmt.Errorf("sandbox: step %d: Timeout %v must be in [0, Limits.Timeout]", i, s.Timeout)
		}
		m.Steps = append(m.Steps, remoteStep{Args: s.Args, TimeoutMs: s.Timeout.Milliseconds()})
	}
	if err := checkLabels(job.Labels); err != nil {
		return m, 0, err
	}
	m.Inputs = []remoteInput{}
	seen := map[string]bool{}
	for i, in := range job.Inputs {
		if !containerPathOK(in.Path) || (!strings.HasPrefix(in.Path, InputDir+"/") && !strings.HasPrefix(in.Path, WorkDir+"/")) {
			return m, 0, fmt.Errorf("sandbox: input %d: path %q must be a plain path under %s/ or %s/", i, in.Path, InputDir, WorkDir)
		}
		if err := checkObjectURL(in.URL); err != nil {
			return m, 0, fmt.Errorf("sandbox: input %d: %w", i, err)
		}
		if seen[in.Path] {
			return m, 0, fmt.Errorf("sandbox: input path %q is used twice", in.Path)
		}
		seen[in.Path] = true
		m.Inputs = append(m.Inputs, remoteInput(in))
	}
	m.Outputs = []remoteOutput{}
	seen = map[string]bool{}
	for i, o := range job.Outputs {
		if !containerPathOK(o.Path) || !strings.HasPrefix(o.Path, WorkDir+"/") {
			return m, 0, fmt.Errorf("sandbox: output %d: path %q must be a plain path under %s/", i, o.Path, WorkDir)
		}
		if err := checkObjectURL(o.URL); err != nil {
			return m, 0, fmt.Errorf("sandbox: output %d: %w", i, err)
		}
		if o.MaxBytes < 0 {
			return m, 0, fmt.Errorf("sandbox: output %d: MaxBytes must not be negative", i)
		}
		if strings.ContainsAny(o.ContentType, "\r\n\x00") {
			return m, 0, fmt.Errorf("sandbox: output %d: ContentType has control characters", i)
		}
		if seen[o.Path] {
			return m, 0, fmt.Errorf("sandbox: output path %q is used twice", o.Path)
		}
		seen[o.Path] = true
		m.Outputs = append(m.Outputs, remoteOutput(o))
	}
	m.Limits = remoteLimits{CPU: job.Limits.CPU, MemoryMB: job.Limits.MemoryMB, TimeoutMs: job.Limits.Timeout.Milliseconds(), PIDs: job.Limits.PIDs}
	m.MaxOutputBytes = job.MaxOutputBytes
	m.MaxInputBytes = job.MaxInputBytes
	m.Labels = job.Labels
	return m, maxOut, nil
}

// checkObjectURL accepts an absolute http or https URL: a presigned object URL. The URL is never put in an error message,
// because it carries its signature.
func checkObjectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("the URL must be an absolute http or https URL without user info")
	}
	return nil
}
