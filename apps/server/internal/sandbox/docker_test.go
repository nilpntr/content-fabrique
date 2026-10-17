package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerBuildArgsGolden(t *testing.T) {
	job, pol, root := goodJob(t)
	job.Labels = map[string]string{"workspace_id": "wks_1", "kind": "probe"}
	d := &Docker{Policy: pol, User: "1000:1000"}
	v, err := pol.validate(job, true)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(d.buildArgs(v, "cf-sbx-test", "1000:1000"), "\n")
	got = strings.ReplaceAll(got, root, "<root>")
	want := `run
--rm
--name
cf-sbx-test
--pull
never
--network
none
--read-only
--cap-drop
ALL
--security-opt
no-new-privileges
--ipc
none
--user
1000:1000
--pids-limit
512
--memory
2048m
--memory-swap
2048m
--cpus
2
--ulimit
nofile=1024:1024
--ulimit
core=0
--tmpfs
/tmp:rw,noexec,nosuid,nodev,mode=1777,size=256m
--env
HOME=/tmp
--env
TMPDIR=/tmp
--label
cf.kind=probe
--label
cf.workspace_id=wks_1
--mount
type=bind,"src=<root>/clip.mov",dst=/in/0,readonly
--mount
type=bind,"src=<root>/out",dst=/work
media-tools:dev
ffprobe
-i
/in/0`
	if got != want {
		t.Fatalf("docker args differ.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHardenedAddsRuntimeOnly(t *testing.T) {
	job, pol, _ := goodJob(t)
	v, err := pol.validate(job, true)
	if err != nil {
		t.Fatal(err)
	}
	dock := NewDocker(pol)
	hard := NewHardened(pol)
	if hard.Name() != "hardened" || dock.Name() != "docker" {
		t.Fatalf("names: %s, %s", hard.Name(), dock.Name())
	}
	da := dock.buildArgs(v, "n", "1:1")
	ha := hard.buildArgs(v, "n", "1:1")
	if len(ha) != len(da)+2 {
		t.Fatalf("hardened should add exactly --runtime runsc:\n%v\n%v", da, ha)
	}
	joined := strings.Join(ha, " ")
	if !strings.Contains(joined, "--runtime runsc") {
		t.Fatalf("no runtime in %v", ha)
	}
	// Every isolation flag is present for both providers.
	for _, flag := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--pull never", "--pids-limit", "--memory ", "--cpus "} {
		for _, args := range [][]string{da, ha} {
			if !strings.Contains(strings.Join(args, " "), flag) {
				t.Errorf("missing %q in %v", flag, args)
			}
		}
	}
}

// Argument injection: nothing the caller controls can become a docker option.
func TestDockerArgsInjection(t *testing.T) {
	root := t.TempDir()
	weird := filepath.Join(root, `a,"b",readonly=false,dst=/etc:c.mov`)
	if err := os.WriteFile(weird, nil, 0o600); err != nil {
		t.Skipf("filesystem refuses the name: %v", err)
	}
	pol := Policy{AllowedRoots: []string{root}}
	job := Job{
		Image:  "media-tools:dev",
		Args:   []string{"ffprobe", "--privileged", "-v", "--mount=type=bind,src=/,dst=/host"},
		Mounts: []Mount{{Host: weird, Container: "/in/0"}},
		Limits: DefaultLimits(),
	}
	v, err := pol.validate(job, true)
	if err != nil {
		t.Fatal(err)
	}
	args := NewDocker(pol).buildArgs(v, "n", "1:1")
	// The program arguments come after the image, where docker no longer parses options.
	img := -1
	for i, a := range args {
		if a == "media-tools:dev" {
			img = i
		}
	}
	if img < 0 || strings.Join(args[img+1:], " ") != "ffprobe --privileged -v --mount=type=bind,src=/,dst=/host" {
		t.Fatalf("program arguments not after the image: %v", args)
	}
	// The odd path is one CSV-quoted field with its quotes doubled.
	var spec string
	for i, a := range args {
		if a == "--mount" {
			spec = args[i+1]
		}
	}
	want := `type=bind,"src=` + strings.ReplaceAll(weird, `"`, `""`) + `",dst=/in/0,readonly`
	if spec != want {
		t.Fatalf("mount spec\n got %s\nwant %s", spec, want)
	}
	if n := strings.Count(strings.Join(args[:img], " "), "--mount"); n != 1 {
		t.Fatalf("expected one --mount before the image, got %d", n)
	}
}

func TestDockerBadImageNeverReachesDocker(t *testing.T) {
	d := &Docker{Binary: "/nonexistent/docker"}
	_, err := d.Run(context.Background(), Job{Image: "--privileged", Args: []string{"x"}, Limits: DefaultLimits()})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("want an image validation error, got %v", err)
	}
}

// fakeDocker writes a shell script standing in for the docker CLI and returns its path and the marker file the
// script appends `rm` calls to.
func fakeDocker(t *testing.T) (bin, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
if [ "$1" = rm ]; then echo "$@" >> '` + marker + `'; exit 0; fi
case "$*" in
  *sleepy*) exec sleep 30 ;;
  *exit7*) echo oops >&2; exit 7 ;;
  *exit125*) echo 'docker: Error response from daemon: pull access denied' >&2; exit 125 ;;
  *exit127*) echo 'exec: not found' >&2; exit 127 ;;
  *noisy*) head -c 100000 /dev/zero | tr '\0' x; head -c 100000 /dev/zero | tr '\0' y >&2; exit 0 ;;
esac
echo "$@"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test script must be executable
		t.Fatal(err)
	}
	return bin, marker
}

func TestDockerRunOutcomes(t *testing.T) {
	bin, marker := fakeDocker(t)
	d := &Docker{Binary: bin, User: "1:1"}
	limits := DefaultLimits()
	ctx := context.Background()

	res, err := d.Run(ctx, Job{Image: "media-tools:dev", Args: []string{"ffprobe", "-version"}, Limits: limits})
	if err != nil || res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "media-tools:dev ffprobe -version") {
		t.Fatalf("plain run: %+v, %v", res, err)
	}

	res, err = d.Run(ctx, Job{Image: "media-tools:dev", Args: []string{"exit7"}, Limits: limits})
	if err != nil || res.ExitCode != 7 || strings.TrimSpace(string(res.Stderr)) != "oops" {
		t.Fatalf("exit code: %+v, %v", res, err)
	}

	res, err = d.Run(ctx, Job{Image: "media-tools:dev", Args: []string{"noisy"}, Limits: limits, MaxOutputBytes: 1000})
	if err != nil || len(res.Stdout) != 1000 || len(res.Stderr) != 1000 || !res.StdoutTruncated || !res.StderrTruncated {
		t.Fatalf("truncation: out=%d err=%d %+v, %v", len(res.Stdout), len(res.Stderr), res, err)
	}

	limits.Timeout = 300 * time.Millisecond
	res, err = d.Run(ctx, Job{Image: "media-tools:dev", Args: []string{"sleepy"}, Limits: limits})
	if !errors.Is(err, ErrTimeout) || !res.TimedOut {
		t.Fatalf("timeout: %+v, %v", res, err)
	}
	calls, rerr := os.ReadFile(marker)
	if rerr != nil || !strings.HasPrefix(string(calls), "rm --force cf-sbx-") {
		t.Fatalf("the container must be removed by name after a timeout, got %q (%v)", calls, rerr)
	}
}

// Docker's own failures (125, 126, 127: daemon error, missing image or runtime, program not runnable) are sandbox errors, never a
// tool exit code, so a caller cannot mistake a missing image for "ffmpeg rejected the file".
func TestDockerInfrastructureFailuresAreErrors(t *testing.T) {
	bin, _ := fakeDocker(t)
	d := &Docker{Binary: bin, User: "1:1"}
	for _, prog := range []string{"exit125", "exit127"} {
		res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{prog}, Limits: DefaultLimits()})
		if !errors.Is(err, ErrSandbox) {
			t.Fatalf("%s: want ErrSandbox, got %v (exit %d)", prog, err, res.ExitCode)
		}
		if errors.Is(err, ErrTimeout) {
			t.Errorf("%s: not a timeout", prog)
		}
		if !strings.Contains(err.Error(), "docker run exited") {
			t.Errorf("%s: %v", prog, err)
		}
	}
	// ...while a tool's own exit code is a result.
	if res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"exit7"}, Limits: DefaultLimits()}); err != nil || res.ExitCode != 7 {
		t.Fatalf("tool exit code: %+v, %v", res, err)
	}
	// A docker CLI that cannot be started at all is a sandbox failure too.
	missing := &Docker{Binary: "/nonexistent/docker", User: "1:1"}
	if _, err := missing.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"x"}, Limits: DefaultLimits()}); !errors.Is(err, ErrSandbox) {
		t.Fatalf("missing docker binary: %v", err)
	}
}

func TestUserFor(t *testing.T) {
	tests := []struct {
		uid, gid int
		override string
		want     string
		chown    bool
	}{
		{0, 0, "", "65534:65534", true},
		{0, 1000, "", "65534:65534", true},
		{1000, 1000, "", "1000:1000", false},
		{1000, 0, "", "1000:0", false}, // gid 0 alone is not root: keep the uid
		{501, 20, "", "501:20", false},
		{0, 0, "7:8", "7:8", false},
	}
	for _, tc := range tests {
		got, chown := userFor(tc.uid, tc.gid, tc.override)
		if got != tc.want || chown != tc.chown {
			t.Errorf("userFor(%d,%d,%q) = %q,%v want %q,%v", tc.uid, tc.gid, tc.override, got, chown, tc.want, tc.chown)
		}
	}
}

// The container user must be able to read inputs. A file it cannot read is refused up front, with a reason, instead of
// starting a container that fails with "Permission denied".
func TestDockerRefusesInputsTheContainerUserCannotRead(t *testing.T) {
	bin, _ := fakeDocker(t)
	root := tmpRoot(t)
	private := filepath.Join(root, "private.mov")
	public := filepath.Join(root, "public.mov")
	for p, mode := range map[string]os.FileMode{private: 0o600, public: 0o644} {
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	other := fmt.Sprintf("%d:%d", os.Getuid()+1000, os.Getgid()+1000)
	run := func(user, file string) error {
		d := &Docker{Policy: Policy{AllowedRoots: []string{root}}, Binary: bin, User: user}
		_, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffprobe", "/in/0"}, Limits: DefaultLimits(),
			Mounts: []Mount{{Host: file, Container: "/in/0"}}})
		return err
	}
	if err := run(other, private); !errors.Is(err, ErrInputUnreadable) {
		t.Fatalf("a 0600 file of another user must be refused, got %v", err)
	}
	if err := run(other, public); err != nil {
		t.Fatalf("a world-readable file must pass: %v", err)
	}
	// The worker's own uid reads its own 0600 file: the default for a non-root worker.
	if os.Getuid() != 0 {
		if err := run(fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), private); err != nil {
			t.Fatalf("own 0600 file: %v", err)
		}
	}
	// The output directory must be writable by the container user.
	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o755); err != nil { // #nosec G301 -- test
		t.Fatal(err)
	}
	d := &Docker{Policy: Policy{AllowedRoots: []string{root}}, Binary: bin, User: other}
	_, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffmpeg"}, Limits: DefaultLimits(),
		Mounts: []Mount{{Host: out, Container: WorkDir, Writable: true}}})
	if !errors.Is(err, ErrInputUnreadable) {
		t.Fatalf("a read-only output dir must be refused, got %v", err)
	}
}

func TestDockerContextCancel(t *testing.T) {
	bin, marker := fakeDocker(t)
	d := &Docker{Binary: bin, User: "1:1"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	_, err := d.Run(ctx, Job{Image: "media-tools:dev", Args: []string{"sleepy"}, Limits: DefaultLimits()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if _, rerr := os.Stat(marker); rerr != nil {
		t.Fatal("the container was not removed after cancellation")
	}
}

func TestDockerMissingBinary(t *testing.T) {
	d := &Docker{Binary: "/nonexistent/docker", User: "1:1"}
	_, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"x"}, Limits: DefaultLimits()})
	if err == nil {
		t.Fatal("expected an error")
	}
}
