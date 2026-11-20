package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/sandbox/sandboxtest"
)

var updateContract = flag.Bool("update-contract", false, "rewrite the runner's testdata/manifest.json and status.json")

const runnerTestdata = "../../../../packages/media-tools/job-runner/testdata"

func remoteJob() Job {
	return Job{
		ID: "rnd_01HZ-1",
		Steps: []Step{
			{Args: []string{"ffmpeg", "-i", "/in/0", "/work/out.mp4"}},
			{Args: []string{"ffprobe", "/work/out.mp4"}, Timeout: time.Minute},
		},
		Inputs:         []Input{{URL: "https://objects.example/b/src.mp4?X-Amz-Signature=abc", Path: "/in/0"}},
		Outputs:        []Output{{Path: "/work/out.mp4", URL: "https://objects.example/b/out.mp4?X-Amz-Signature=def", ContentType: "video/mp4", MaxBytes: 1 << 30}},
		Limits:         Limits{CPU: 4, MemoryMB: 4096, Timeout: 30 * time.Minute, PIDs: 1024},
		Labels:         map[string]string{"kind": "render"},
		MaxOutputBytes: 1 << 20,
	}
}

// The runner (cf-job) parses the manifest this provider writes and answers the status this provider reads. The runner's tests
// parse these two files with its own types (TestContractTestdata), so a change on one side fails a test on the other.
func TestRemoteWireMatchesRunnerContract(t *testing.T) {
	m, _, err := remoteManifest(remoteJob())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	status, err := json.MarshalIndent(remoteStatus{
		ID: "rnd_01HZ-1", State: remoteSucceeded, ExitCode: 0, DurationMs: 1234,
		Steps: []remoteStepResult{
			{ExitCode: 0, Stdout: []byte{}, Stderr: []byte("frame=1\n"), DurationMs: 1000},
			{ExitCode: 0, Stdout: []byte("{}"), Stderr: []byte{}, StderrTruncated: true, DurationMs: 30},
		},
		Outputs: []remoteOutputInfo{{Path: "/work/out.mp4", Bytes: 4096}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"manifest.json": manifest, "status.json": status} {
		path := filepath.Join(runnerTestdata, name)
		if *updateContract {
			if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil { //nolint:gosec // test data
				t.Fatal(err)
			}
		}
		have, err := os.ReadFile(path) //nolint:gosec // test data
		if err != nil {
			t.Fatalf("%v (run go test ./internal/sandbox -run TestRemoteWireMatchesRunnerContract -update-contract)", err)
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(data)) {
			t.Errorf("the runner's testdata/%s is not what this provider writes: run go test ./internal/sandbox -run TestRemoteWireMatchesRunnerContract -update-contract, then the runner's tests", name)
		}
	}
	var back remoteStatus
	if err := json.Unmarshal(status, &back); err != nil || len(back.Steps) != 2 || string(back.Steps[0].Stderr) != "frame=1\n" {
		t.Fatalf("status round trip: %+v %v", back, err)
	}
}

func TestRemoteValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Job)
		want string
	}{
		{"mounts", func(j *Job) { j.Mounts = []Mount{{Host: "/tmp/x", Container: "/in/0"}} }, "not Mounts"},
		{"no id", func(j *Job) { j.ID = "" }, "ID"},
		{"id with a slash", func(j *Job) { j.ID = "a/b" }, "ID"},
		{"args and steps", func(j *Job) { j.Args = []string{"ffmpeg"} }, "not both"},
		{"no program", func(j *Job) { j.Steps = nil }, "no program"},
		{"flag as program", func(j *Job) { j.Steps[0].Args = []string{"-x"} }, "start with '-'"},
		{"NUL", func(j *Job) { j.Steps[0].Args = []string{"ffmpeg", "a\x00b"} }, "NUL"},
		{"step timeout over the job's", func(j *Job) { j.Steps[1].Timeout = time.Hour }, "Timeout"},
		{"no limits", func(j *Job) { j.Limits = Limits{} }, "Limits.CPU"},
		{"network", func(j *Job) { j.Network = "host" }, "always off"},
		{"input outside /in", func(j *Job) { j.Inputs[0].Path = "/etc/passwd" }, "under /in/"},
		{"input with ..", func(j *Job) { j.Inputs[0].Path = "/in/../etc" }, "under /in/"},
		{"input path twice", func(j *Job) { j.Inputs = append(j.Inputs, j.Inputs[0]) }, "twice"},
		{"input URL", func(j *Job) { j.Inputs[0].URL = "file:///etc/passwd" }, "http or https"},
		{"output outside /work", func(j *Job) { j.Outputs[0].Path = "/in/x" }, "under /work/"},
		{"output URL with user", func(j *Job) { j.Outputs[0].URL = "https://u:p@objects.example/x" }, "user info"},
		{"output cap", func(j *Job) { j.Outputs[0].MaxBytes = -1 }, "MaxBytes"},
		{"content type", func(j *Job) { j.Outputs[0].ContentType = "a\r\nb: c" }, "ContentType"},
		{"label", func(j *Job) { j.Labels = map[string]string{"a b": "c"} }, "label"},
		{"input cap", func(j *Job) { j.MaxInputBytes = -1 }, "MaxInputBytes"},
	}
	r := &Remote{BaseURL: "http://127.0.0.1:1", Token: "t"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := remoteJob()
			c.edit(&j)
			_, err := r.Run(context.Background(), j)
			if err == nil || !strings.Contains(err.Error(), c.want) || errors.Is(err, ErrSandbox) {
				t.Fatalf("got %v, want a refusal containing %q", err, c.want)
			}
			if strings.Contains(err.Error(), "X-Amz-Signature") {
				t.Errorf("the error names a presigned URL: %v", err)
			}
		})
	}
	// A single program in Args is one step.
	j := remoteJob()
	j.Steps, j.Args = nil, []string{"ffprobe", "/in/0"}
	m, _, err := remoteManifest(j)
	if err != nil || len(m.Steps) != 1 || m.Steps[0].Args[0] != "ffprobe" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestOtherProvidersRefuseRemoteFields(t *testing.T) {
	job, pol, _ := goodJob(t)
	for name, edit := range map[string]func(*Job){
		"ID":      func(j *Job) { j.ID = "x" },
		"Inputs":  func(j *Job) { j.Inputs = []Input{{URL: "http://x/y", Path: "/in/9"}} },
		"Outputs": func(j *Job) { j.Outputs = []Output{{URL: "http://x/y", Path: "/work/y"}} },
		"Steps":   func(j *Job) { j.Steps = []Step{{Args: []string{"ffprobe"}}} },
	} {
		j := job
		edit(&j)
		if _, err := pol.validate(j, true); err == nil || !strings.Contains(err.Error(), "remote provider") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNewRemoteAndCheckRemoteAllowed(t *testing.T) {
	if _, err := NewRemote("localhost:8080", "t"); err == nil {
		t.Error("a URL without a scheme was accepted")
	}
	if _, err := NewRemote("http://localhost:8080", ""); err == nil {
		t.Error("no token was accepted")
	}
	r, err := NewRemote("http://localhost:8080/", "t")
	if err != nil || r.BaseURL != "http://localhost:8080" || r.Name() != "remote" {
		t.Fatalf("%+v %v", r, err)
	}
	const gw = "https://jobs.example"
	for _, c := range []struct {
		env, runner, gateway string
		ok                   bool
	}{
		{"local", "http://localhost:8080", "", true},
		{"ci", "http://localhost:8080", gw, true},
		{"staging", "http://localhost:8080", gw, false},
		{"production", "http://10.0.0.5:8080", gw, false},
		{"production", gw + "/", gw, true},
		{"Staging", gw, "", false},
	} {
		if err := CheckRemoteAllowed(c.env, c.runner, c.gateway); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// fakeRunner answers the job contract from a script.
type fakeRunner struct {
	mu       sync.Mutex
	puts     int
	gets     int
	deletes  atomic.Int32
	put      func(n int) (int, any)
	get      func(n int) (int, any)
	gotToken string
	srv      *httptest.Server
}

func newFakeRunner(t *testing.T, f *fakeRunner) *Remote {
	t.Helper()
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		f.gotToken = req.Header.Get("Authorization")
		var code int
		var body any
		switch req.Method {
		case http.MethodPut:
			f.puts++
			code, body = f.put(f.puts)
		case http.MethodGet:
			f.gets++
			code, body = f.get(f.gets)
		case http.MethodDelete:
			f.deletes.Add(1)
			code = http.StatusNoContent
		}
		f.mu.Unlock()
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "1")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(f.srv.Close)
	return &Remote{BaseURL: f.srv.URL, Token: "secret-token", PollInterval: 5 * time.Millisecond}
}

func running() remoteStatus {
	return remoteStatus{ID: "rnd_01HZ-1", State: remoteRunning, ExitCode: -1}
}

func TestRemoteRunMapsTheStatus(t *testing.T) {
	ok := remoteStatus{ID: "rnd_01HZ-1", State: remoteSucceeded, DurationMs: 2500, Steps: []remoteStepResult{
		{ExitCode: 0, Stderr: []byte("one"), DurationMs: 1000}, {ExitCode: 0, Stdout: []byte("two"), DurationMs: 500},
	}, Outputs: []remoteOutputInfo{{Path: "/work/out.mp4", Bytes: 10}}}
	failed := remoteStatus{State: remoteFailed, Steps: []remoteStepResult{{ExitCode: 0}, {ExitCode: 1, Stderr: []byte("bad")}}}
	cases := []struct {
		name   string
		status remoteStatus
		check  func(*testing.T, Result, error)
	}{
		{"succeeded", ok, func(t *testing.T, res Result, err error) {
			if err != nil || res.ExitCode != 0 || string(res.Stdout) != "two" || len(res.Steps) != 2 || string(res.Steps[0].Stderr) != "one" ||
				res.Duration != 2500*time.Millisecond || res.Steps[1].Duration != 500*time.Millisecond {
				t.Fatalf("%+v %v", res, err)
			}
		}},
		{"a failing step is a result", failed, func(t *testing.T, res Result, err error) {
			if err != nil || res.ExitCode != 1 || string(res.Stderr) != "bad" {
				t.Fatalf("%+v %v", res, err)
			}
		}},
		{"timed out", remoteStatus{State: remoteTimedOut, Steps: []remoteStepResult{{ExitCode: -1, TimedOut: true}}}, func(t *testing.T, res Result, err error) {
			if !errors.Is(err, ErrTimeout) || !res.TimedOut {
				t.Fatalf("%+v %v", res, err)
			}
		}},
		{"runner failure", remoteStatus{State: remoteError, ErrorKind: remoteKindSandbox, Error: "input /in/0: GET answered 403"}, func(t *testing.T, _ Result, err error) {
			if !errors.Is(err, ErrSandbox) || !strings.Contains(err.Error(), "403") {
				t.Fatal(err)
			}
		}},
		{"files", remoteStatus{State: remoteError, ErrorKind: remoteKindFiles, Error: "output /work/out.mp4 is missing"}, func(t *testing.T, _ Result, err error) {
			if !errors.Is(err, ErrFiles) || errors.Is(err, ErrSandbox) {
				t.Fatal(err)
			}
		}},
		{"success without the outputs", remoteStatus{State: remoteSucceeded, Steps: ok.Steps}, func(t *testing.T, _ Result, err error) {
			if !errors.Is(err, ErrSandbox) {
				t.Fatal(err)
			}
		}},
		{"oversized streams are cut", remoteStatus{State: remoteFailed, Steps: []remoteStepResult{{ExitCode: 2, Stderr: bytes.Repeat([]byte("e"), 2<<20)}}}, func(t *testing.T, res Result, err error) {
			if err != nil || len(res.Stderr) != 1<<20 || !res.StderrTruncated {
				t.Fatalf("%d %v %v", len(res.Stderr), res.StderrTruncated, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRunner{
				put: func(int) (int, any) { return http.StatusCreated, running() },
				get: func(n int) (int, any) {
					if n < 3 {
						return http.StatusOK, running()
					}
					return http.StatusOK, c.status
				},
			}
			r := newFakeRunner(t, f)
			res, err := r.Run(context.Background(), remoteJob())
			c.check(t, res, err)
			if f.gotToken != "Bearer secret-token" {
				t.Errorf("token %q", f.gotToken)
			}
			if f.deletes.Load() != 1 {
				t.Errorf("%d deletes, want 1: a finished job is forgotten", f.deletes.Load())
			}
		})
	}
}

func TestRemoteStartAnswers(t *testing.T) {
	done := remoteStatus{State: remoteSucceeded, Steps: []remoteStepResult{{}, {}}, Outputs: []remoteOutputInfo{{}}}
	t.Run("busy, then free", func(t *testing.T) {
		f := &fakeRunner{
			put: func(n int) (int, any) {
				if n == 1 {
					return http.StatusServiceUnavailable, map[string]string{"error": "busy"}
				}
				return http.StatusCreated, running()
			},
			get: func(int) (int, any) { return http.StatusOK, done },
		}
		if _, err := newFakeRunner(t, f).Run(context.Background(), remoteJob()); err != nil || f.puts != 2 {
			t.Fatalf("%v after %d PUTs", err, f.puts)
		}
	})
	t.Run("busy for too long", func(t *testing.T) {
		f := &fakeRunner{put: func(int) (int, any) { return http.StatusServiceUnavailable, map[string]string{"error": "busy"} }}
		r := newFakeRunner(t, f)
		r.BusyWait = time.Millisecond
		if _, err := r.Run(context.Background(), remoteJob()); !errors.Is(err, ErrSandbox) {
			t.Fatal(err)
		}
	})
	for _, c := range []struct {
		code    int
		sandbox bool
		want    string
	}{
		{http.StatusBadRequest, false, "refused"},
		{http.StatusConflict, false, "different manifest"},
		{http.StatusUnauthorized, true, "401"},
		{http.StatusInternalServerError, true, "500"},
	} {
		f := &fakeRunner{put: func(int) (int, any) { return c.code, map[string]string{"error": "nope"} }}
		_, err := newFakeRunner(t, f).Run(context.Background(), remoteJob())
		if err == nil || errors.Is(err, ErrSandbox) != c.sandbox || !strings.Contains(err.Error(), c.want) {
			t.Errorf("PUT %d: %v", c.code, err)
		}
		// A PUT that may have started the job is followed by a DELETE; a refusal started nothing.
		if want := map[bool]int32{true: 1, false: 0}[c.sandbox]; f.deletes.Load() != want {
			t.Errorf("PUT %d: %d deletes, want %d", c.code, f.deletes.Load(), want)
		}
	}
	t.Run("the job vanished", func(t *testing.T) {
		f := &fakeRunner{put: func(int) (int, any) { return http.StatusCreated, running() },
			get: func(int) (int, any) { return http.StatusNotFound, map[string]string{"error": "no such job"} }}
		if _, err := newFakeRunner(t, f).Run(context.Background(), remoteJob()); !errors.Is(err, ErrSandbox) {
			t.Fatal(err)
		}
	})
	t.Run("an unknown state", func(t *testing.T) {
		f := &fakeRunner{put: func(int) (int, any) { return http.StatusCreated, remoteStatus{State: "weird"} }}
		if _, err := newFakeRunner(t, f).Run(context.Background(), remoteJob()); !errors.Is(err, ErrSandbox) {
			t.Fatal(err)
		}
	})
}

func TestRemoteCancelAndDeadlineDeleteTheJob(t *testing.T) {
	forever := func() *fakeRunner {
		return &fakeRunner{put: func(int) (int, any) { return http.StatusCreated, running() },
			get: func(int) (int, any) { return http.StatusOK, running() }}
	}
	f := forever()
	r := newFakeRunner(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, remoteJob()); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Fatalf("cancelled: %v", err)
	}
	if f.deletes.Load() != 1 {
		t.Errorf("cancelled: %d deletes", f.deletes.Load())
	}

	f = forever()
	r = newFakeRunner(t, f)
	r.Grace = time.Millisecond
	j := remoteJob()
	j.Limits.Timeout = 100 * time.Millisecond
	j.Steps[1].Timeout = 0
	res, err := r.Run(context.Background(), j)
	if !errors.Is(err, ErrTimeout) || !res.TimedOut {
		t.Fatalf("silent runner: %+v %v", res, err)
	}
	if f.deletes.Load() != 1 {
		t.Errorf("silent runner: %d deletes", f.deletes.Load())
	}
}

// The provider against the real runner: cf-job is built from packages/media-tools/job-runner and served on a local port, with
// a stand-in for the bucket. The steps are plain host programs (cp, false); the Docker integration test in internal/render
// runs the image's cf-job with ffmpeg and MinIO.
func TestRemoteAgainstCFJob(t *testing.T) {
	for _, p := range []string{"cp", "false"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("%s is not on PATH", p)
		}
	}
	var mu sync.Mutex
	objects := map[string][]byte{"src": []byte("footage")}
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimPrefix(req.URL.Path, "/b/")
		switch req.Method {
		case http.MethodGet:
			if b, ok := objects[key]; ok {
				_, _ = w.Write(b)
				return
			}
			http.NotFound(w, req)
		case http.MethodPut:
			objects[key], _ = io.ReadAll(req.Body)
		}
	}))
	defer bucket.Close()

	const token = "cf-job-test-token-0123"
	base := sandboxtest.StartCFJob(t, token)
	r, err := NewRemote(base, token)
	if err != nil {
		t.Fatal(err)
	}
	r.PollInterval = 20 * time.Millisecond
	job := Job{
		ID:      "rnd_test-1",
		Inputs:  []Input{{URL: bucket.URL + "/b/src", Path: "/in/0"}},
		Steps:   []Step{{Args: []string{"cp", "/in/0", "/work/a"}}, {Args: []string{"cp", "/work/a", "/work/b"}}},
		Outputs: []Output{{Path: "/work/b", URL: bucket.URL + "/b/out"}},
		Limits:  Limits{CPU: 1, MemoryMB: 256, Timeout: time.Minute, PIDs: 64},
	}
	res, err := r.Run(context.Background(), job)
	if err != nil || res.ExitCode != 0 || len(res.Steps) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	mu.Lock()
	got := string(objects["out"])
	mu.Unlock()
	if got != "footage" {
		t.Errorf("uploaded %q", got)
	}

	// A failing step is a result; the next job id reuses the runner.
	job.ID, job.Steps, job.Outputs = "rnd_test-2", []Step{{Args: []string{"false"}}}, nil
	if res, err := r.Run(context.Background(), job); err != nil || res.ExitCode != 1 {
		t.Fatalf("failing step: %+v %v", res, err)
	}
	// An output the steps did not write is ErrFiles.
	job.ID, job.Steps, job.Outputs = "rnd_test-3", []Step{{Args: []string{"cp", "/in/0", "/work/x"}}}, []Output{{Path: "/work/y", URL: bucket.URL + "/b/y"}}
	if _, err := r.Run(context.Background(), job); !errors.Is(err, ErrFiles) {
		t.Fatalf("missing output: %v", err)
	}
	// The wrong token is a sandbox failure, never a run.
	bad, _ := NewRemote(base, "wrong-token-wrong-token")
	job.ID = "rnd_test-4"
	if _, err := bad.Run(context.Background(), job); !errors.Is(err, ErrSandbox) {
		t.Fatalf("wrong token: %v", err)
	}
}
