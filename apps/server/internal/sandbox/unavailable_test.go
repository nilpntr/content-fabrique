package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerFailureReason(t *testing.T) {
	cases := map[string]string{
		"docker: Cannot connect to the Docker daemon at unix:///run/user/1000/docker.sock. Is the docker daemon running?":                                     "Docker is not running",
		`docker: Error response from daemon: unknown or invalid runtime name: runsc.`:                                                                         "gVisor runtime (runsc) is not registered",
		`Unable to find image 'media-tools:dev' locally` + "\ndocker: Error response from daemon: No such image":                                              "media-tools image is not built",
		`docker: Error response from daemon: OCI runtime create failed: unable to apply cgroup configuration: mkdir /sys/fs/cgroup/memory: permission denied`: "cgroup controllers",
		`docker: Error response from daemon: failed to create shim task: seccomp: config provided but seccomp not supported`:                                  "security settings",
		`something nobody expected`: "docker could not start the container",
	}
	for stderr, want := range cases {
		if got := dockerFailureReason([]byte(stderr)); !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want it to mention %q", stderr, got, want)
		}
	}
}

func TestMountFailure(t *testing.T) {
	yes := []string{
		"[out#0/wav @ 0x5581] Error opening output /work/audio_16k.wav: Permission denied\nError opening output file /work/audio_16k.wav.",
		"/in/0: Permission denied",
		"Could not open '/work/proxy_720.mp4': Read-only file system",
	}
	no := []string{
		"/in/0: Invalid data found when processing input",
		"moov atom not found",
		"Permission denied", // no mount path: not ours to judge
		"/tmp/x: Permission denied",
		"",
	}
	for _, s := range yes {
		if !mountFailure([]byte(s)) {
			t.Errorf("want a mount failure: %q", s)
		}
	}
	for _, s := range no {
		if mountFailure([]byte(s)) {
			t.Errorf("not a mount failure: %q", s)
		}
	}
}

func TestRootlessInfo(t *testing.T) {
	cases := map[string]bool{
		`["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`: true,
		`["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]`: false,
		`null`:               false,
		`not json`:           false,
		`["rootless"]`:       false,
		`["name=rootlessX"]`: false,
	}
	for in, want := range cases {
		if got := rootlessInfo([]byte(in)); got != want {
			t.Errorf("rootlessInfo(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestUnavailable(t *testing.T) {
	ue := &UnavailableError{Reason: "Docker is not running", Err: errors.New("exit 125")}
	wrapped := fmt.Errorf("ffmpeg: %w", ue)
	if r, ok := Unavailable(wrapped); !ok || r != "Docker is not running" {
		t.Fatalf("Unavailable(wrapped) = %q, %v", r, ok)
	}
	if !errors.Is(wrapped, ErrSandbox) {
		t.Fatal("an UnavailableError must be an ErrSandbox")
	}
	if !strings.HasPrefix(ue.Error(), "the sandbox is unavailable: Docker is not running") {
		t.Fatalf("message: %q", ue.Error())
	}
	if _, ok := Unavailable(fmt.Errorf("%w: x", ErrSandbox)); !ok {
		t.Fatal("any ErrSandbox is unavailable")
	}
	if _, ok := Unavailable(fmt.Errorf("%w: x", ErrInputUnreadable)); !ok {
		t.Fatal("ErrInputUnreadable is unavailable")
	}
	for _, err := range []error{nil, ErrTimeout, context.Canceled, errors.New("ffmpeg exited 1")} {
		if _, ok := Unavailable(err); ok {
			t.Errorf("%v is not unavailable", err)
		}
	}
}

// fakeDockerScript writes a docker CLI stand-in that runs body for `run` and answers `info` with info.
func fakeDockerScript(t *testing.T, info, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nif [ \"$1\" = info ]; then echo '" + info + "'; exit 0; fi\nif [ \"$1\" = rm ] || [ \"$1\" = inspect ]; then exit 1; fi\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test script must be executable
		t.Fatal(err)
	}
	return bin
}

// Under rootless Docker the container runs as 0:0 (the daemon's own host user); elsewhere as the worker's uid.
func TestDockerRootlessUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the worker's own uid is root here")
	}
	echoUser := `prev=; for a in "$@"; do if [ "$prev" = --user ]; then echo "$a"; fi; prev="$a"; done; exit 0`
	own := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	for _, tc := range []struct {
		info string
		want string
	}{
		{`["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`, "0:0"},
		{`["name=seccomp,profile=builtin","name=cgroupns"]`, own},
	} {
		root := tmpRoot(t)
		out := filepath.Join(root, "out")
		if err := os.Mkdir(out, 0o700); err != nil {
			t.Fatal(err)
		}
		d := &Docker{Policy: Policy{AllowedRoots: []string{root}}, Binary: fakeDockerScript(t, tc.info, echoUser)}
		for range 2 { // the second job reuses the cached answer
			res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffmpeg"}, Limits: DefaultLimits(),
				Mounts: []Mount{{Host: out, Container: WorkDir, Writable: true}}})
			if err != nil || strings.TrimSpace(string(res.Stdout)) != tc.want {
				t.Fatalf("info %s: user %q, %v; want %s", tc.info, res.Stdout, err, tc.want)
			}
		}
	}
}

// A tool that cannot write /work, and docker's own failures, are UnavailableErrors with a plain reason, never a tool exit code
// that a caller could read as a bad file.
func TestDockerUnavailableOutcomes(t *testing.T) {
	no := false
	for _, tc := range []struct {
		body string
		want string
	}{
		{`echo "Error opening output /work/audio_16k.wav: Permission denied" >&2; exit 243`, "cannot read its input or write its output"},
		{`echo "docker: Error response from daemon: unknown or invalid runtime name: runsc" >&2; exit 125`, "runsc"},
		{`echo "docker: Cannot connect to the Docker daemon at unix:///var/run/docker.sock" >&2; exit 125`, "Docker is not running"},
	} {
		d := &Docker{Binary: fakeDockerScript(t, "[]", tc.body), Rootless: &no, User: "1:1"}
		_, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffmpeg"}, Limits: DefaultLimits()})
		reason, ok := Unavailable(err)
		if !ok || !strings.Contains(reason, tc.want) || !errors.Is(err, ErrSandbox) {
			t.Errorf("%s: got %q, %v (%v); want a reason with %q", tc.body, reason, ok, err, tc.want)
		}
	}
	// A tool that rejects the file is still a result.
	d := &Docker{Binary: fakeDockerScript(t, "[]", `echo "/in/0: Invalid data found when processing input" >&2; exit 183`), Rootless: &no, User: "1:1"}
	res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffprobe"}, Limits: DefaultLimits()})
	if err != nil || res.ExitCode != 183 {
		t.Fatalf("a bad file: %+v, %v", res, err)
	}
}
