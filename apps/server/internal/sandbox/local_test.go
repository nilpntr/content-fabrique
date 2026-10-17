package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRewritesMountsAndRunsInOutputDir(t *testing.T) {
	job, pol, root := goodJob(t)
	if err := os.WriteFile(filepath.Join(root, "clip.mov"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Args = []string{"cat", "/in/0"}
	res, err := NewLocal(pol).Run(context.Background(), job)
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hello" {
		t.Fatalf("cat: %+v, %v", res, err)
	}

	// The writable mount is the working directory, and /work/... maps into it.
	job.Args = []string{"cp", "/in/0", "/work/copy"}
	if res, err = NewLocal(pol).Run(context.Background(), job); err != nil || res.ExitCode != 0 {
		t.Fatalf("cp: %+v, %v", res, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "out", "copy")); err != nil || string(b) != "hello" {
		t.Fatalf("output file: %q, %v", b, err)
	}

	// ".." out of the mount is not rewritten into a host path.
	job.Args = []string{"cat", "/work/../clip.mov"}
	if res, err = NewLocal(pol).Run(context.Background(), job); err != nil || res.ExitCode == 0 {
		t.Fatalf("escape must fail: %+v, %v", res, err)
	}
}

func TestLocalDoesNotLeakEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	res, err := NewLocal(Policy{}).Run(context.Background(), Job{Args: []string{"env"}, Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Stdout), "sk-should-not-leak") || strings.Contains(string(res.Stdout), "ANTHROPIC") {
		t.Fatalf("environment leaked: %s", res.Stdout)
	}
}

func TestLocalTimeoutKillsProcessGroup(t *testing.T) {
	limits := DefaultLimits()
	limits.Timeout = 300 * time.Millisecond
	start := time.Now()
	res, err := NewLocal(Policy{}).Run(context.Background(), Job{Args: []string{"sleep", "30"}, Limits: limits})
	if !errors.Is(err, ErrTimeout) || !res.TimedOut {
		t.Fatalf("want a timeout, got %+v, %v", res, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the process was not killed promptly")
	}
}

func TestLocalExitCodeAndBareName(t *testing.T) {
	res, err := NewLocal(Policy{}).Run(context.Background(), Job{Args: []string{"sh", "-c", "exit 3"}, Limits: DefaultLimits()})
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("exit: %+v, %v", res, err)
	}
	if _, err := NewLocal(Policy{}).Run(context.Background(), Job{Args: []string{"/bin/echo", "x"}, Limits: DefaultLimits()}); err == nil {
		t.Fatal("a program path must be refused")
	}
}

func TestBoundedBuffer(t *testing.T) {
	b := newBoundedBuffer(5)
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte("defgh")); n != 5 || err != nil {
		t.Fatal(n, err)
	}
	got, trunc := b.snapshot()
	if string(got) != "abcde" || !trunc {
		t.Fatalf("%q %v", got, trunc)
	}
}

// A command that exited by itself is a result even if the deadline passed while Wait was returning; only a command that was
// killed while the context was done is a timeout.
func TestFinishPrefersTheExitOverTheContext(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	run := func(script string) (*exec.Cmd, error) {
		cmd := exec.Command("sh", "-c", script) // #nosec G204 -- test script
		return cmd, cmd.Run()
	}

	cmd, err := run("exit 3")
	var res Result
	kill, ferr := finish(dead, nil, cmd, err, time.Second, false, &res)
	if ferr != nil || kill || res.ExitCode != 3 || res.TimedOut {
		t.Fatalf("clean exit with a done context: kill=%v err=%v %+v", kill, ferr, res)
	}

	cmd, err = run("exit 0")
	res = Result{}
	if kill, ferr = finish(dead, nil, cmd, err, time.Second, false, &res); ferr != nil || kill || res.ExitCode != 0 {
		t.Fatalf("clean exit 0: kill=%v err=%v %+v", kill, ferr, res)
	}

	// Killed by a signal while the context is done: a timeout (or the parent's cancellation).
	cmd, err = run("kill -9 $$")
	res = Result{}
	kill, ferr = finish(dead, nil, cmd, err, time.Second, false, &res)
	if !errors.Is(ferr, ErrTimeout) || !kill || !res.TimedOut {
		t.Fatalf("killed under a done context: kill=%v err=%v %+v", kill, ferr, res)
	}
	res = Result{}
	if _, ferr = finish(dead, dead.Err(), cmd, err, time.Second, false, &res); !errors.Is(ferr, context.Canceled) || res.TimedOut {
		t.Fatalf("parent cancelled: %v %+v", ferr, res)
	}

	// Killed by a signal with a live context: a sandbox failure for docker, 128+signal for the local tool.
	live := context.Background()
	cmd, err = run("kill -9 $$")
	res = Result{}
	if _, ferr = finish(live, nil, cmd, err, time.Second, false, &res); !errors.Is(ferr, ErrSandbox) {
		t.Fatalf("signalled docker CLI: %v", ferr)
	}
	res = Result{}
	if _, ferr = finish(live, nil, cmd, err, time.Second, true, &res); ferr != nil || res.ExitCode != 137 {
		t.Fatalf("signalled local tool: %v %+v", ferr, res)
	}
}

func TestLocalSegfaultIsAResult(t *testing.T) {
	res, err := NewLocal(Policy{}).Run(context.Background(), Job{Args: []string{"sh", "-c", "kill -SEGV $$"}, Limits: DefaultLimits()})
	if err != nil || res.ExitCode != 139 {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestCheckLocalAllowed(t *testing.T) {
	for _, env := range []string{"", "local", "ci", "bench", "LOCAL"} {
		if err := CheckLocalAllowed(env); err != nil {
			t.Errorf("%q: %v", env, err)
		}
	}
	for _, env := range []string{"staging", "production", "Production", " staging "} {
		if err := CheckLocalAllowed(env); err == nil {
			t.Errorf("%q must be refused", env)
		}
	}
}
