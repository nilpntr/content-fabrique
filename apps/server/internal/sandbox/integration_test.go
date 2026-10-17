package sandbox

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests need Docker and a built media-tools image, so they only run when asked:
//
//	docker build -t media-tools:dev packages/media-tools
//	go test ./internal/sandbox -run Integration -docker-image media-tools:dev [-docker-runtime runsc]
//
// CI runs them in the media-tools job.
var (
	dockerImage   = flag.String("docker-image", "", "media-tools image for the Docker integration tests")
	dockerRuntime = flag.String("docker-runtime", "", "container runtime for the integration tests, for example runsc")
)

func integrationProvider(t *testing.T, root string) *Docker {
	t.Helper()
	if *dockerImage == "" {
		t.Skip("set -docker-image to run the Docker integration tests")
	}
	d := NewDocker(Policy{AllowedRoots: []string{root}})
	d.Runtime = *dockerRuntime
	return d
}

func tmpRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestIntegrationIsolation(t *testing.T) {
	root := tmpRoot(t)
	d := integrationProvider(t, root)
	in := filepath.Join(root, `weird name, "quoted":colon.txt`)
	if err := os.WriteFile(in, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	job := func(args ...string) Job {
		return Job{
			Image:  *dockerImage,
			Args:   args,
			Mounts: []Mount{{Host: in, Container: "/in/0"}, {Host: out, Container: WorkDir, Writable: true}},
			Limits: Limits{CPU: 1, MemoryMB: 512, Timeout: time.Minute, PIDs: 128},
		}
	}
	ctx := context.Background()
	gen := func(path string) []string {
		return []string{"ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=10:duration=1", path}
	}

	// A file name with a comma, quotes and a colon arrives as one read-only file: ffprobe opens it and rejects its content.
	res, err := d.Run(ctx, job("ffprobe", "-v", "error", "-show_format", "/in/0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || !strings.Contains(string(res.Stderr), "/in/0: Invalid data found") {
		t.Fatalf("odd path did not reach the tool as a file: exit %d err %q", res.ExitCode, res.Stderr)
	}

	// The output mount is writable, and the file belongs to the worker.
	if res, err = d.Run(ctx, job(gen("/work/ok.mp4")...)); err != nil || res.ExitCode != 0 {
		t.Fatalf("write to /work: %+v, %v", res, err)
	}
	fi, err := os.Stat(filepath.Join(out, "ok.mp4"))
	if err != nil || fi.Size() == 0 {
		t.Fatalf("output file: %v", err)
	}

	// Everything else is read-only: the input mount, and the root filesystem.
	for _, path := range []string{"/in/0.mp4", "/in/extra.mp4", "/usr/local/bin/evil.mp4", "/etc/evil.mp4", "/evil.mp4"} {
		res, err = d.Run(ctx, job(gen(path)...))
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == 0 {
			t.Errorf("writing %s must fail", path)
		}
	}
	// /tmp is a tmpfs and works.
	if res, err = d.Run(ctx, job(gen("/tmp/t.mp4")...)); err != nil || res.ExitCode != 0 {
		t.Errorf("/tmp should be writable: %+v, %v", res, err)
	}
	// The host input file is untouched.
	if b, err := os.ReadFile(in); err != nil || string(b) != "not media" {
		t.Errorf("input changed: %q %v", b, err)
	}
}

func TestIntegrationTimeoutRemovesContainer(t *testing.T) {
	root := tmpRoot(t)
	d := integrationProvider(t, root)
	start := time.Now()
	res, err := d.Run(context.Background(), Job{
		Image:  *dockerImage,
		Args:   []string{"ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-f", "null", "-"},
		Limits: Limits{CPU: 1, MemoryMB: 512, Timeout: 3 * time.Second, PIDs: 128},
	})
	if !errors.Is(err, ErrTimeout) || !res.TimedOut {
		t.Fatalf("want a timeout, got %+v, %v", res, err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("took %v to give up", time.Since(start))
	}
	left, err := exec.Command("docker", "ps", "-aq", "--filter", "name=cf-sbx-").Output() // #nosec G204 -- fixed arguments
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(left)) != "" {
		t.Fatalf("containers left behind: %s", left)
	}
}

func TestIntegrationMemoryLimit(t *testing.T) {
	root := tmpRoot(t)
	d := integrationProvider(t, root)
	// A 4K veryslow x264 encode needs hundreds of MiB for its look-ahead and reference frames: under a 64 MiB limit the job must be killed.
	args := []string{"ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=3840x2160:rate=30:duration=2",
		"-threads", "2", "-c:v", "libx264", "-preset", "veryslow", "-x264-params", "rc-lookahead=30:ref=4:threads=2:lookahead-threads=1", "-f", "null", "-"}
	res, err := d.Run(context.Background(), Job{Image: *dockerImage, Args: args, Limits: Limits{CPU: 1, MemoryMB: 64, Timeout: time.Minute, PIDs: 128}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 137 { // SIGKILL from the OOM killer
		t.Fatalf("job should have been OOM-killed: exit %d, stderr %q", res.ExitCode, res.Stderr)
	}
	// The same job within a generous limit works, so the kill above was the limit and not a broken command.
	res, err = d.Run(context.Background(), Job{Image: *dockerImage, Args: args, Limits: Limits{CPU: 2, MemoryMB: 4096, Timeout: 2 * time.Minute, PIDs: 256}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("control run: exit %d, %v, stderr %q", res.ExitCode, err, res.Stderr)
	}
}

// A 0600 input is readable by the container because the container runs as the worker's own uid. When the worker is root the
// container runs as nobody and cannot read a root-owned 0600 file: the provider says so up front, and a staged copy with a
// readable mode works.
func TestIntegrationInputPermissions(t *testing.T) {
	root := tmpRoot(t)
	d := integrationProvider(t, root)
	in := filepath.Join(root, "private.bin")
	if err := os.WriteFile(in, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(in, 0o600); err != nil {
		t.Fatal(err)
	}
	job := Job{Image: *dockerImage, Args: []string{"ffprobe", "-v", "error", "-show_format", "/in/0"}, Limits: DefaultLimits(),
		Mounts: []Mount{{Host: in, Container: "/in/0"}}}
	run := func() (Result, error) { return d.Run(context.Background(), job) }

	if os.Getuid() == 0 {
		if _, err := run(); !errors.Is(err, ErrInputUnreadable) {
			t.Fatalf("a root-owned 0600 input must be refused for the unprivileged container user, got %v", err)
		}
		if err := os.Chmod(in, 0o644); err != nil { // #nosec G302 -- the staged copy is meant to be readable
			t.Fatal(err)
		}
	}
	res, err := run()
	if err != nil {
		t.Fatal(err)
	}
	// ffprobe opened the file (and rejected its content), so there was no permission problem.
	if !strings.Contains(string(res.Stderr), "Invalid data found") || strings.Contains(string(res.Stderr), "Permission denied") {
		t.Fatalf("exit %d, stderr %q", res.ExitCode, res.Stderr)
	}
}

// A missing image is a sandbox failure, not "the tool failed".
func TestIntegrationMissingImageIsASandboxError(t *testing.T) {
	root := tmpRoot(t)
	d := integrationProvider(t, root)
	_, err := d.Run(context.Background(), Job{Image: "media-tools:does-not-exist", Args: []string{"ffprobe"}, Limits: DefaultLimits()})
	if !errors.Is(err, ErrSandbox) {
		t.Fatalf("want ErrSandbox, got %v", err)
	}
}
