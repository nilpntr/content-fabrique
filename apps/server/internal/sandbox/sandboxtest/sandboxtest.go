// Package sandboxtest runs the remote provider's runner for tests: cf-job, built from packages/media-tools/job-runner and served
// as a process of the test, with directories of its own and the host's tools (no image is built). The steps' /in and /work
// arguments name its directories (cf-job maps them when it is started with -in and -work).
package sandboxtest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// StartCFJob builds cf-job, serves it on a local port with the bearer token, reaching only the host:port in allowed (empty allows
// any), and returns its URL. It skips the test when Go is not on PATH. The process is killed when the test ends.
func StartCFJob(t testing.TB, token string, allowed ...string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH to build cf-job")
	}
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("sandboxtest: no source path")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cf-job")
	build := exec.Command("go", "build", "-o", bin, ".") // #nosec G204 -- test
	build.Dir = filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "..", "packages", "media-tools", "job-runner")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cf-job: %v\n%s", err, out)
	}
	for _, d := range []string{"in", "work", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil { // #nosec G301 -- test
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "serve", "-addr", "127.0.0.1:0", "-in", filepath.Join(dir, "in"), "-work", filepath.Join(dir, "work"), // #nosec G204 -- test
		"-tmp", filepath.Join(dir, "tmp"), "-cpus", "4", "-memory-mb", "8192", "-allowed-hosts", strings.Join(allowed, ","))
	cmd.Env = append(os.Environ(), "CF_JOB_TOKEN="+token)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if _, rest, ok := strings.Cut(sc.Text(), "listening on "); ok {
				addr <- strings.Fields(rest)[0]
			}
		}
	}()
	select {
	case a := <-addr:
		return "http://" + a
	case <-time.After(time.Minute):
		t.Fatal("cf-job serve did not start")
		return ""
	}
}
