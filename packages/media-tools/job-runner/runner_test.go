package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary is also the tools of the tests' steps (`<test binary> __tool <what> ...`) and the runner's own __exec.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case execCommand:
			os.Exit(execMain(os.Args[2:]))
		case "__tool":
			os.Exit(tool(os.Args[2:]))
		}
	}
	os.Exit(m.Run())
}

// tool is what a step runs in these tests.
func tool(args []string) int {
	switch args[0] {
	case "copy": // copy src dst
		b, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(args[2], bytes.ToUpper(b), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("copied", len(b))
		return 0
	case "say": // say text: stdout and stderr
		fmt.Println(args[1])
		fmt.Fprintln(os.Stderr, "err:"+args[1])
		return 0
	case "exit":
		n, _ := strconv.Atoi(args[1])
		fmt.Fprintln(os.Stderr, "exiting", n)
		return n
	case "sleep":
		d, _ := time.ParseDuration(args[1])
		time.Sleep(d)
		return 0
	case "env":
		for _, e := range os.Environ() {
			fmt.Println(e)
		}
		wd, _ := os.Getwd()
		fmt.Println("PWD=" + wd)
		return 0
	case "symlink": // symlink target link
		if err := os.Symlink(args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "write": // write path n
		n, _ := strconv.Atoi(args[2])
		if err := os.WriteFile(args[1], bytes.Repeat([]byte("x"), n), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "spam": // spam n: n bytes on stdout
		n, _ := strconv.Atoi(args[1])
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("y"), n))
		return 0
	}
	return 99
}

// store is a stand-in for the bucket: GET and PUT of /o/<key>.
type store struct {
	mu   sync.Mutex
	objs map[string][]byte
	ct   map[string]string
	srv  *httptest.Server
}

func newStore(t *testing.T) *store {
	s := &store{objs: map[string][]byte{}, ct: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/o/")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			b, ok := s.objs[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			s.objs[key] = b
			s.ct[key] = r.Header.Get("Content-Type")
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *store) url(key string) string { return s.srv.URL + "/o/" + key }

func (s *store) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objs[key]
	return b, ok
}

func (s *store) put(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = b
}

type fixture struct {
	r    *Runner
	srv  *httptest.Server
	s    *server
	st   *store
	self string
}

const testToken = "0123456789abcdef0123"

func newFixture(t *testing.T, allowed ...string) *fixture {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, d := range []string{"in", "work", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{InDir: filepath.Join(dir, "in"), WorkDir: filepath.Join(dir, "work"), TmpDir: filepath.Join(dir, "tmp"),
		CPUs: 4, MemoryMB: 4096, Client: newClient(allowed, nil), Self: self}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := newServer(ctx, r, testToken, allowed)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return &fixture{r: r, srv: srv, s: s, st: newStore(t), self: self}
}

func (f *fixture) step(args ...string) Step {
	return Step{Args: append([]string{f.self, "__tool"}, args...)}
}

func (f *fixture) do(t *testing.T, method, id string, body any, token string) (int, Status, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+"/jobs/"+id, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var st Status
	_ = json.Unmarshal(raw, &st)
	return resp.StatusCode, st, string(raw)
}

func (f *fixture) wait(t *testing.T, id string) Status {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		code, st, raw := f.do(t, http.MethodGet, id, nil, testToken)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", id, code, raw)
		}
		if st.State != StateRunning {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return Status{}
}

func limits() Limits { return Limits{CPU: 1, MemoryMB: 512, TimeoutMs: 20000, PIDs: 256} }

func TestJobDownloadsRunsStepsAndUploads(t *testing.T) {
	f := newFixture(t)
	f.st.put("src", []byte("hello"))
	in := filepath.Join(f.r.InDir, "a", "0")
	work := f.r.WorkDir
	m := Manifest{
		Inputs: []Input{{URL: f.st.url("src"), Path: "/in/a/0"}},
		Steps: []Step{
			f.step("copy", in, filepath.Join(work, "mid.txt")),
			f.step("copy", filepath.Join(work, "mid.txt"), filepath.Join(work, "out.txt")),
			f.step("say", "done"),
		},
		Outputs: []Output{{Path: "/work/out.txt", URL: f.st.url("out"), ContentType: "text/plain"}, {Path: "/work/mid.txt", URL: f.st.url("mid")}},
		Limits:  limits(),
	}
	code, st, raw := f.do(t, http.MethodPut, "r1-1", m, testToken)
	if code != http.StatusCreated || st.State != StateRunning || st.ID != "r1-1" {
		t.Fatalf("PUT: %d %s", code, raw)
	}
	st = f.wait(t, "r1-1")
	if st.State != StateSucceeded || st.ExitCode != 0 || len(st.Steps) != 3 || st.TimedOut {
		t.Fatalf("status %+v", st)
	}
	if string(st.Steps[0].Stdout) != "copied 5\n" || string(st.Steps[2].Stdout) != "done\n" || string(st.Steps[2].Stderr) != "err:done\n" {
		t.Errorf("stdout/stderr %q %q %q", st.Steps[0].Stdout, st.Steps[2].Stdout, st.Steps[2].Stderr)
	}
	if b, _ := f.st.get("out"); string(b) != "HELLO" {
		t.Errorf("uploaded %q", b)
	}
	if f.st.ct["out"] != "text/plain" {
		t.Errorf("content type %q", f.st.ct["out"])
	}
	if len(st.Outputs) != 2 || st.Outputs[0].Bytes != 5 {
		t.Errorf("outputs %+v", st.Outputs)
	}
	// The job's files are gone.
	for _, d := range []string{f.r.InDir, f.r.WorkDir, f.r.TmpDir} {
		if e, _ := os.ReadDir(d); len(e) != 0 {
			t.Errorf("%s still holds %d entries", d, len(e))
		}
	}
}

func TestPutIsIdempotentAndOneJobRunsAtATime(t *testing.T) {
	f := newFixture(t)
	m := Manifest{Steps: []Step{f.step("sleep", "10s")}, Limits: limits()}
	if code, _, raw := f.do(t, http.MethodPut, "a-1", m, testToken); code != http.StatusCreated {
		t.Fatalf("first PUT %d %s", code, raw)
	}
	if code, st, raw := f.do(t, http.MethodPut, "a-1", m, testToken); code != http.StatusOK || st.State != StateRunning {
		t.Fatalf("same PUT again %d %s", code, raw)
	}
	other := m
	other.Steps = []Step{f.step("sleep", "1s")}
	if code, _, raw := f.do(t, http.MethodPut, "a-1", other, testToken); code != http.StatusConflict {
		t.Fatalf("PUT with another manifest %d %s", code, raw)
	}
	if code, _, raw := f.do(t, http.MethodPut, "b-1", other, testToken); code != http.StatusServiceUnavailable {
		t.Fatalf("PUT while busy %d %s", code, raw)
	}
	start := time.Now()
	if code, _, raw := f.do(t, http.MethodDelete, "a-1", nil, testToken); code != http.StatusNoContent {
		t.Fatalf("DELETE %d %s", code, raw)
	}
	if time.Since(start) > 8*time.Second {
		t.Error("DELETE waited for the step instead of killing it")
	}
	if code, _, _ := f.do(t, http.MethodGet, "a-1", nil, testToken); code != http.StatusNotFound {
		t.Errorf("GET after DELETE %d", code)
	}
	if code, _, _ := f.do(t, http.MethodDelete, "a-1", nil, testToken); code != http.StatusNotFound {
		t.Errorf("second DELETE %d", code)
	}
	// Free again.
	if code, _, raw := f.do(t, http.MethodPut, "b-1", other, testToken); code != http.StatusCreated {
		t.Fatalf("PUT after DELETE %d %s", code, raw)
	}
	if st := f.wait(t, "b-1"); st.State != StateSucceeded {
		t.Fatalf("%+v", st)
	}
}

func TestAuthAndRefusals(t *testing.T) {
	f := newFixture(t, "127.0.0.1:1")
	m := Manifest{Steps: []Step{f.step("say", "x")}, Limits: limits()}
	if code, _, _ := f.do(t, http.MethodPut, "a-1", m, "wrong-token-wrong-token"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if code, _, _ := f.do(t, http.MethodGet, "a-1", nil, ""); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _, _ := f.do(t, http.MethodGet, "..bad", nil, testToken); code != http.StatusBadRequest {
		t.Errorf("bad id: %d", code)
	}
	bad := []Manifest{
		{Steps: []Step{f.step("say", "x")}},                       // no limits
		{Limits: limits()},                                        // no steps
		{Steps: []Step{{Args: []string{"-x"}}}, Limits: limits()}, // flag as program
		{Steps: []Step{f.step("x")}, Limits: Limits{CPU: 8, MemoryMB: 1, TimeoutMs: 1, PIDs: 1}}, // more CPU than the runner
		{Steps: []Step{f.step("x")}, Inputs: []Input{{URL: f.st.url("a"), Path: "/etc/passwd"}}, Limits: limits()},
		{Steps: []Step{f.step("x")}, Inputs: []Input{{URL: f.st.url("a"), Path: "/in/../etc"}}, Limits: limits()},
		{Steps: []Step{f.step("x")}, Inputs: []Input{{URL: f.st.url("a"), Path: "/in/a"}, {URL: f.st.url("a"), Path: "/in/a/b"}}, Limits: limits()},
		{Steps: []Step{f.step("x")}, Outputs: []Output{{URL: f.st.url("a"), Path: "/in/x"}}, Limits: limits()},
		{Steps: []Step{f.step("x")}, Outputs: []Output{{URL: "file:///etc/passwd", Path: "/work/x"}}, Limits: limits()},
		// Allowed hosts: the store is not 127.0.0.1:1.
		{Steps: []Step{f.step("x")}, Outputs: []Output{{URL: f.st.url("a"), Path: "/work/x"}}, Limits: limits()},
	}
	for i, m := range bad {
		if code, _, raw := f.do(t, http.MethodPut, "bad-"+strconv.Itoa(i), m, testToken); code != http.StatusBadRequest {
			t.Errorf("manifest %d: %d %s", i, code, raw)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, f.srv.URL+"/jobs/u-1", strings.NewReader(`{"steps":[{"args":["x"]}],"limits":{"cpu":1,"memory_mb":1,"timeout_ms":1,"pids":1},"surprise":1}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: %d", resp.StatusCode)
	}
	var health bytes.Buffer
	if code := healthMain(context.Background(), []string{f.srv.URL + "/healthz"}, &health); code != 0 {
		t.Errorf("healthz: %d %s", code, health.String())
	}
}

func TestFailuresAndTheirKinds(t *testing.T) {
	work := func(f *fixture, n string) string { return filepath.Join(f.r.WorkDir, n) }
	cases := []struct {
		name      string
		manifest  func(f *fixture) Manifest
		state     string
		kind      string
		exit      int
		uploaded  bool
		stepCount int
	}{
		{"a failing step stops the job and nothing is uploaded", func(f *fixture) Manifest {
			return Manifest{Steps: []Step{f.step("write", work(f, "o"), "3"), f.step("exit", "3"), f.step("say", "never")},
				Outputs: []Output{{Path: "/work/o", URL: f.st.url("o")}}, Limits: limits()}
		}, StateFailed, "", 3, false, 2},
		{"a step past its timeout is killed", func(f *fixture) Manifest {
			s := f.step("sleep", "20s")
			s.TimeoutMs = 300
			return Manifest{Steps: []Step{s}, Limits: limits()}
		}, StateTimedOut, "", -1, false, 1},
		{"the job's timeout covers every step", func(f *fixture) Manifest {
			l := limits()
			l.TimeoutMs = 400
			return Manifest{Steps: []Step{f.step("sleep", "20s")}, Limits: l}
		}, StateTimedOut, "", -1, false, 1},
		{"a missing output", func(f *fixture) Manifest {
			return Manifest{Steps: []Step{f.step("say", "x")}, Outputs: []Output{{Path: "/work/none", URL: f.st.url("o")}}, Limits: limits()}
		}, StateError, KindFiles, 0, false, 1},
		{"an output that is a symlink out of /work", func(f *fixture) Manifest {
			return Manifest{Steps: []Step{f.step("symlink", "/etc/hosts", work(f, "o"))}, Outputs: []Output{{Path: "/work/o", URL: f.st.url("o")}}, Limits: limits()}
		}, StateError, KindFiles, 0, false, 1},
		{"an output over its cap", func(f *fixture) Manifest {
			return Manifest{Steps: []Step{f.step("write", work(f, "o"), "100")}, Outputs: []Output{{Path: "/work/o", URL: f.st.url("o"), MaxBytes: 99}, {Path: "/work/p", URL: f.st.url("p"), MaxBytes: 1000}}, Limits: limits()}
		}, StateError, KindFiles, 0, false, 1},
		{"inputs over max_input_bytes", func(f *fixture) Manifest {
			f.st.put("big", bytes.Repeat([]byte("z"), 100))
			return Manifest{Inputs: []Input{{URL: f.st.url("big"), Path: "/in/0"}}, Steps: []Step{f.step("say", "x")}, MaxInputBytes: 50, Limits: limits()}
		}, StateError, KindFiles, -1, false, 0},
		{"an input that is not there", func(f *fixture) Manifest {
			return Manifest{Inputs: []Input{{URL: f.st.url("nothing"), Path: "/in/0"}}, Steps: []Step{f.step("say", "x")}, Limits: limits()}
		}, StateError, KindSandbox, -1, false, 0},
		{"a program that does not exist", func(*fixture) Manifest {
			return Manifest{Steps: []Step{{Args: []string{"cf-job-no-such-tool"}}}, Limits: limits()}
		}, StateError, KindSandbox, -1, false, 1},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := "f-" + strconv.Itoa(i)
			if code, _, raw := f.do(t, http.MethodPut, id, c.manifest(f), testToken); code != http.StatusCreated {
				t.Fatalf("PUT %d %s", code, raw)
			}
			st := f.wait(t, id)
			if st.State != c.state || st.ErrorKind != c.kind || st.ExitCode != c.exit || len(st.Steps) != c.stepCount {
				t.Fatalf("status %+v", st)
			}
			if st.TimedOut != (c.state == StateTimedOut) {
				t.Errorf("timed_out %v", st.TimedOut)
			}
			if _, ok := f.st.get("o"); ok != c.uploaded {
				t.Errorf("uploaded %v", ok)
			}
			if strings.Contains(st.Error, f.st.srv.URL) {
				t.Errorf("the error names the URL: %s", st.Error)
			}
		})
	}
}

func TestStepEnvironmentAndOutputCap(t *testing.T) {
	t.Setenv("CF_JOB_TOKEN", "must-not-leak")
	f := newFixture(t)
	m := Manifest{Steps: []Step{f.step("env"), f.step("spam", "50000")}, MaxOutputBytes: 20000, Limits: limits()}
	if code, _, raw := f.do(t, http.MethodPut, "e-1", m, testToken); code != http.StatusCreated {
		t.Fatalf("PUT %d %s", code, raw)
	}
	st := f.wait(t, "e-1")
	if st.State != StateSucceeded {
		t.Fatalf("%+v", st)
	}
	env := string(st.Steps[0].Stdout)
	if strings.Contains(env, "must-not-leak") || !strings.Contains(env, "HOME="+filepath.Join(f.r.TmpDir, "cf-job-")) {
		t.Errorf("step environment:\n%s", env)
	}
	wd, _ := filepath.EvalSymlinks(f.r.WorkDir)
	if !strings.Contains(env, "PWD="+f.r.WorkDir) && !strings.Contains(env, "PWD="+wd) {
		t.Errorf("the step did not run in /work:\n%s", env)
	}
	if s := st.Steps[1]; len(s.Stdout) != 20000 || !s.StdoutTruncated {
		t.Errorf("stdout %d bytes, truncated %v", len(s.Stdout), s.StdoutTruncated)
	}
}

func TestRunCommand(t *testing.T) {
	f := newFixture(t)
	f.st.put("src", []byte("abc"))
	m := Manifest{
		Inputs:  []Input{{URL: f.st.url("src"), Path: "/in/0"}},
		Steps:   []Step{f.step("copy", filepath.Join(f.r.InDir, "0"), filepath.Join(f.r.WorkDir, "o"))},
		Outputs: []Output{{Path: "/work/o", URL: f.st.url("o")}},
		Limits:  limits(),
	}
	b, _ := json.Marshal(m)
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"run", "-in", f.r.InDir, "-work", f.r.WorkDir, "-tmp", f.r.TmpDir, "-cpus", "2", "-memory-mb", "1024", "-id", "x-1", "-"}, bytes.NewReader(b), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	var st Status
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || st.State != StateSucceeded || st.ID != "x-1" {
		t.Fatalf("%v %s", err, out.String())
	}
	if b, _ := f.st.get("o"); string(b) != "ABC" {
		t.Errorf("uploaded %q", b)
	}
}

func TestDialOverrideAndAllowlist(t *testing.T) {
	st := newStore(t)
	st.put("k", []byte("v"))
	addr := strings.TrimPrefix(st.srv.URL, "http://")
	c := newClient([]string{"objects.test:9000"}, map[string]string{"objects.test:9000": addr})
	resp, err := c.Get("http://objects.test:9000/o/k")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "v" {
		t.Errorf("through the dial override: %q", b)
	}
	resp, err = c.Get(st.srv.URL + "/o/k")
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("a host outside the allowlist: %v", err)
	}
}
