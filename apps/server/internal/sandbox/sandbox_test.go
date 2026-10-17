package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goodJob(t *testing.T) (Job, Policy, string) {
	t.Helper()
	root := t.TempDir()
	in := filepath.Join(root, "clip.mov")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return Job{
		Image:  "media-tools:dev",
		Args:   []string{"ffprobe", "-i", "/in/0"},
		Mounts: []Mount{{Host: filepath.Join(resolved, "clip.mov"), Container: "/in/0"}, {Host: filepath.Join(resolved, "out"), Container: WorkDir, Writable: true}},
		Limits: DefaultLimits(),
	}, Policy{AllowedRoots: []string{root}}, resolved
}

func TestValidateAccepts(t *testing.T) {
	job, pol, _ := goodJob(t)
	v, err := pol.validate(job, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.mounts) != 2 || v.maxOutput != DefaultMaxOutputBytes {
		t.Fatalf("unexpected validated job: %+v", v)
	}
}

func TestValidateRejects(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret")
	if err := os.WriteFile(outsideFile, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(j *Job, root string)
		want   string
	}{
		{"network", func(j *Job, _ string) { j.Network = "bridge" }, "network"},
		{"image empty", func(j *Job, _ string) { j.Image = "" }, "image"},
		{"image dash", func(j *Job, _ string) { j.Image = "--privileged" }, "image"},
		{"image space", func(j *Job, _ string) { j.Image = "a b" }, "image"},
		{"no args", func(j *Job, _ string) { j.Args = nil }, "program"},
		{"flag as program", func(j *Job, _ string) { j.Args = []string{"--privileged"} }, "start with '-'"},
		{"nul arg", func(j *Job, _ string) { j.Args = []string{"ffprobe", "a\x00b"} }, "NUL"},
		{"zero cpu", func(j *Job, _ string) { j.Limits.CPU = 0 }, "CPU"},
		{"zero memory", func(j *Job, _ string) { j.Limits.MemoryMB = 0 }, "MemoryMB"},
		{"zero pids", func(j *Job, _ string) { j.Limits.PIDs = 0 }, "PIDs"},
		{"zero timeout", func(j *Job, _ string) { j.Limits.Timeout = 0 }, "Timeout"},
		{"huge timeout", func(j *Job, _ string) { j.Limits.Timeout = 48 * time.Hour }, "Timeout"},
		{"bad label", func(j *Job, _ string) { j.Labels = map[string]string{"k": "a b"} }, "label"},
		{"relative host", func(j *Job, _ string) { j.Mounts[0].Host = "clip.mov" }, "absolute"},
		{"unclean host", func(j *Job, root string) { j.Mounts[0].Host = root + "/out/../clip.mov" }, "clean"},
		{"outside root", func(j *Job, _ string) { j.Mounts[0].Host = outsideFile }, "outside"},
		{"missing host", func(j *Job, root string) { j.Mounts[0].Host = root + "/nope" }, "no such file"},
		{"symlink escape", func(j *Job, root string) {
			link := filepath.Join(root, "link")
			if err := os.Symlink(outsideFile, link); err != nil {
				t.Fatal(err)
			}
			j.Mounts[0].Host = link
		}, "outside"},
		{"container relative", func(j *Job, _ string) { j.Mounts[0].Container = "in/0" }, "container path"},
		{"container dotdot", func(j *Job, _ string) { j.Mounts[0].Container = "/in/../etc" }, "container path"},
		{"container comma", func(j *Job, _ string) { j.Mounts[0].Container = "/in/a,b" }, "container path"},
		{"container colon", func(j *Job, _ string) { j.Mounts[0].Container = "/in/a:rw" }, "container path"},
		{"ro outside in", func(j *Job, _ string) { j.Mounts[0].Container = "/etc/passwd" }, "read-only mount"},
		{"ro at in itself", func(j *Job, _ string) { j.Mounts[0].Container = "/in" }, "read-only mount"},
		{"writable not work", func(j *Job, _ string) { j.Mounts[1].Container = "/out" }, "writable mount must be at"},
		{"writable twice", func(j *Job, _ string) {
			j.Mounts = append(j.Mounts, Mount{Host: j.Mounts[1].Host, Container: "/work", Writable: true})
		}, "twice"},
		{"writable file", func(j *Job, _ string) { j.Mounts[1].Host = j.Mounts[0].Host }, "directory"},
		{"duplicate container", func(j *Job, _ string) { j.Mounts = append(j.Mounts, Mount{Host: j.Mounts[0].Host, Container: "/in/0"}) }, "twice"},
		{"negative output cap", func(j *Job, _ string) { j.MaxOutputBytes = -1 }, "MaxOutputBytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job, pol, root := goodJob(t)
			job.Mounts = append([]Mount(nil), job.Mounts...)
			tc.mutate(&job, root)
			// "outside root" cases need the outside dir to exist but not be allowed: pol only allows root.
			_, err := pol.validate(job, true)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateNoRootsMeansNoMounts(t *testing.T) {
	job, _, _ := goodJob(t)
	if _, err := (Policy{}).validate(job, true); err == nil {
		t.Fatal("a job with mounts must be refused when no root is allowed")
	}
	job.Mounts = nil
	if _, err := (Policy{}).validate(job, true); err != nil {
		t.Fatalf("a job without mounts needs no root: %v", err)
	}
}

func TestValidateRelativeRoot(t *testing.T) {
	job, _, _ := goodJob(t)
	if _, err := (Policy{AllowedRoots: []string{"relative"}}).validate(job, true); err == nil {
		t.Fatal("a relative allowed root must be refused")
	}
}

func TestRootItselfAndSiblingPrefix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data")
	sibling := filepath.Join(base, "data-other")
	for _, d := range []string{root, sibling} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := filepath.Join(sibling, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pol := Policy{AllowedRoots: []string{root}}
	job := Job{Image: "x", Args: []string{"p"}, Limits: DefaultLimits(), Mounts: []Mount{{Host: f, Container: "/in/f"}}}
	if _, err := pol.validate(job, true); err == nil {
		t.Fatal("/data-other must not count as inside /data")
	}
}
