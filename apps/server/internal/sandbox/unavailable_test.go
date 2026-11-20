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
		"/in/0x: Permission denied",              // not a mount the job was given
		"/work/../etc: Permission denied",        // nor a plain name in /work
		"/work/out.mp4: Operation not permitted", // what gVisor answers a refused call: not a mount problem
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
	// Only causes no input can bring about: a remote runner that restarted, or one unreadable input, keep their usual handling.
	for _, err := range []error{nil, ErrTimeout, context.Canceled, errors.New("ffmpeg exited 1"),
		fmt.Errorf("%w: remote job x is gone from the runner", ErrSandbox), fmt.Errorf("%w: /in/0", ErrInputUnreadable)} {
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

// A file can make ffmpeg print "Permission denied" for a mount path. Unless the self-check job (which opens the inputs raw and
// writes a short silence) fails too, that is the tool's result, not an unavailable sandbox.
func TestDockerMountFailureNeedsTheSelfCheck(t *testing.T) {
	no := false
	root := tmpRoot(t)
	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(root, "clip.mov")
	if err := os.WriteFile(clip, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "selfchecks")
	denied := `echo "Error opening output /work/audio_16k.wav: Permission denied" >&2; exit 243`
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		// The self-check passes: the sandbox works, the input caused the message.
		{"crafted input", `case "$*" in *anullsrc*) echo x >> '` + marker + `'; exit 0 ;; esac; ` + denied, false},
		// The self-check fails the same way: the sandbox cannot use its mounts.
		{"broken sandbox", `case "$*" in *anullsrc*) echo x >> '` + marker + `' ;; esac; ` + denied, true},
	} {
		_ = os.Remove(marker)
		d := &Docker{Policy: Policy{AllowedRoots: []string{root}}, Binary: fakeDockerScript(t, "[]", tc.body), Rootless: &no}
		res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffmpeg"}, Limits: DefaultLimits(),
			Mounts: []Mount{{Host: clip, Container: "/in/0"}, {Host: out, Container: WorkDir, Writable: true}}})
		if _, ok := Unavailable(err); ok != tc.wantErr {
			t.Errorf("%s: unavailable %v (%v), want %v", tc.name, ok, err, tc.wantErr)
		}
		if res.ExitCode != 243 {
			t.Errorf("%s: exit %d", tc.name, res.ExitCode)
		}
		if b, _ := os.ReadFile(marker); string(b) != "x\n" {
			t.Errorf("%s: the self-check ran %q", tc.name, b)
		}
	}
}

// The rootless answer is asked again after rootlessTTL: a daemon swapped for a rootful one must not keep getting 0:0 containers.
func TestDockerRootlessAnswerExpires(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the worker's own uid is root here")
	}
	dir := t.TempDir()
	infoFile := filepath.Join(dir, "info")
	write := func(s string) {
		if err := os.WriteFile(infoFile, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nif [ \"$1\" = info ]; then cat '" + infoFile + "'; exit 0; fi\n" +
		`prev=; for a in "$@"; do if [ "$prev" = --user ]; then echo "$a"; fi; prev="$a"; done; exit 0` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test script must be executable
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	d := &Docker{Binary: bin, now: func() time.Time { return clock }}
	user := func() string {
		res, err := d.Run(context.Background(), Job{Image: "media-tools:dev", Args: []string{"ffmpeg"}, Limits: DefaultLimits()})
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	own := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	write(`["name=seccomp,profile=builtin","name=rootless"]`)
	if got := user(); got != "0:0" {
		t.Fatalf("rootless: %q", got)
	}
	write(`["name=seccomp,profile=builtin"]`) // the daemon is now rootful
	clock = clock.Add(rootlessTTL / 2)
	if got := user(); got != "0:0" {
		t.Fatalf("within the TTL the answer is cached: %q", got)
	}
	clock = clock.Add(rootlessTTL)
	if got := user(); got != own {
		t.Fatalf("after the TTL a rootful daemon must get the worker's uid, got %q", got)
	}
	write("not json") // a daemon that cannot answer: the safe default
	clock = clock.Add(2 * rootlessTTL)
	if got := user(); got != own {
		t.Fatalf("unknown: %q", got)
	}
}
