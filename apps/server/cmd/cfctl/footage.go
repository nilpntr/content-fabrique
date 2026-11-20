package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/ingest"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
)

const footageUsage = `Usage:
  cfctl footage check <file> [<file>...] [--as clip|music] [--provider local|docker|hardened] [--json] [flags]

check says, for each file, whether it can be used in an edit: usable, usable with a warning, or not usable, with the
reasons in plain words. It reads the file the way the upload does (the whole picture is decoded and the sound is read),
inside the sandbox provider, and never copies or uploads it. Files that are exactly the same are pointed out.

  --as        what the files are for: clip (footage) or music (a song, or the sound of a video such as a screen
              recording). Default: clip for a file with a picture, music for one without.
  --provider  local (ffmpeg on this machine, development only: it isolates nothing), docker (default) or hardened
  --json      print the results as JSON
  --image     the media-tools image (default media-tools:dev)
  --docker    the docker CLI (default docker)

The exit code is 0 when every file is usable (with or without a warning), 1 when one is not or could not be checked, and
3 when the sandbox is unavailable (Docker is not running, the image or the gVisor runtime is missing, or the tools cannot
write their output directory, as under rootless Docker without its setup; see docs/06): then no file was judged, because
nothing is known about them.
`

// footageDeps are the things cmdFootage needs from outside, replaced in tests.
type footageDeps struct {
	newProvider func(name, dockerBin string, roots ...string) (sandbox.Provider, error)
	lookupEnv   func(string) (string, bool)
}

func defaultFootageDeps() footageDeps {
	return footageDeps{newProvider: newSandboxProvider, lookupEnv: os.LookupEnv}
}

func cmdFootage(args []string, stdout, stderr io.Writer, deps footageDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, footageUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, footageUsage)
		return 0
	case "check":
		return cmdFootageCheck(args[1:], stdout, stderr, deps)
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl footage: unknown command %q\n\n%s", args[0], footageUsage)
		return 2
	}
}

// footageResult is one file's line in the JSON output.
type footageResult struct {
	File string `json:"file"`
	ingest.Assessment
	Error string `json:"error,omitempty"`
	// SandboxUnavailable says the check could not run because of the sandbox, not the file; Error then says why.
	SandboxUnavailable bool `json:"sandboxUnavailable,omitempty"`
}

// exitSandboxUnavailable is the exit code when the sandbox could not run the check.
const exitSandboxUnavailable = 3

func cmdFootageCheck(args []string, stdout, stderr io.Writer, deps footageDeps) int {
	fs := flag.NewFlagSet("footage check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, footageUsage) }
	as := fs.String("as", "", "clip or music")
	provider := fs.String("provider", "docker", "local, docker or hardened")
	asJSON := fs.Bool("json", false, "print JSON")
	image := fs.String("image", "media-tools:dev", "media-tools image")
	dockerBin := fs.String("docker", "docker", "docker CLI")
	files, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "cfctl footage check: "+format+"\n", a...)
		return code
	}
	if len(files) == 0 {
		return fail(2, "name at least one file")
	}
	use := ingest.Use(*as)
	if use != "" && use != ingest.UseClip && use != ingest.UseMusic {
		return fail(2, "--as is clip or music, not %q", *as)
	}
	if *provider == "local" {
		env, _ := deps.lookupEnv("CF_ENV")
		if err := sandbox.CheckLocalAllowed(env); err != nil {
			return fail(2, "%v", err)
		}
	}

	// The sandbox may mount the files the arguments point to, after symlinks, and their directories are the allowed roots
	// (as for media probe), plus a fresh work directory for what the check writes.
	// A file that cannot be found is reported with the others, which are still checked.
	targets := make([]string, len(files))
	resolveErr := make([]error, len(files))
	sizes := map[int64]int{} // how many of the files have each size: only those that share one are hashed
	size := make([]int64, len(files))
	var roots []string
	for i, f := range files {
		abs, err := filepath.Abs(f)
		if err == nil {
			targets[i], err = filepath.EvalSymlinks(abs)
		}
		var fi os.FileInfo
		if err == nil {
			fi, err = os.Stat(targets[i])
		}
		if err != nil {
			resolveErr[i] = err
			continue
		}
		size[i] = fi.Size()
		sizes[size[i]]++
		roots = append(roots, filepath.Dir(targets[i]))
	}
	tmp, err := os.MkdirTemp("", "cf-footage-")
	if err != nil {
		return fail(1, "%v", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	work, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return fail(1, "%v", err)
	}
	p, err := deps.newProvider(*provider, *dockerBin, append(roots, work)...)
	if err != nil {
		return fail(2, "%v", err)
	}
	if p.Name() == "local" {
		_, _ = fmt.Fprintln(stderr, "cfctl: the local provider runs ffmpeg on this machine without isolation: development only")
	}
	runner := &media.SandboxRunner{Provider: p, Image: *image, Limits: sandbox.DefaultLimits(), Labels: map[string]string{"kind": "footage_check"}}

	ctx, stop := signalContext()
	defer stop()
	code := 0
	results := make([]footageResult, len(files))
	seen := map[[sha256.Size]byte]string{}
	unavailable := ""
	for i, target := range targets {
		results[i].File = files[i]
		if resolveErr[i] != nil {
			results[i].Error = resolveErr[i].Error()
			code = max(code, 1)
			continue
		}
		if unavailable != "" {
			// The same sandbox would fail the same way on every other file.
			results[i].Error, results[i].SandboxUnavailable = unavailable, true
			continue
		}
		a, err := ingest.Check(ctx, runner, target, ingest.CheckOptions{Use: use, WorkDir: work})
		if reason, ok := sandbox.Unavailable(err); ok && ctx.Err() == nil {
			unavailable = sandbox.UnavailableMessage(reason)
			results[i].Error, results[i].SandboxUnavailable = unavailable, true
			code = exitSandboxUnavailable
			_, _ = fmt.Fprintf(stderr, "cfctl footage check: %v\n", err) // the detail, for whoever fixes the setup
			continue
		}
		if err != nil {
			results[i].Error = err.Error()
			code = max(code, 1)
			continue
		}
		if sizes[size[i]] > 1 {
			if sum, err := fileSum(target); err == nil {
				if first, dup := seen[sum]; dup {
					a.Add(ingest.DuplicateFinding(first))
				} else {
					seen[sum] = files[i] // as typed, so two clip.mp4 in different folders are told apart
				}
			}
		}
		if a.Verdict == ingest.Unusable {
			code = max(code, 1)
		}
		results[i].Assessment = a
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return fail(1, "%v", err)
		}
		return code
	}
	for i, r := range results {
		if i > 0 {
			_, _ = fmt.Fprintln(stdout)
		}
		writeAssessment(stdout, filepath.Base(r.File), r)
	}
	return code
}

// writeAssessment prints one file's verdict and reasons, warnings and problems before notes.
func writeAssessment(w io.Writer, name string, r footageResult) {
	if r.SandboxUnavailable {
		_, _ = fmt.Fprintf(w, "%s: could not be checked\n  - %s. This says nothing about the file: fix the setup and check it again.\n", name, strings.ToUpper(r.Error[:1])+r.Error[1:])
		return
	}
	if r.Error != "" {
		_, _ = fmt.Fprintf(w, "%s: could not be checked\n  - It could not be read (%s). Check that the file is there and try again.\n", name, r.Error)
		return
	}
	as := ""
	switch r.Use {
	case ingest.UseClip:
		as = " (as a clip)"
	case ingest.UseMusic:
		as = " (as music)"
	}
	_, _ = fmt.Fprintf(w, "%s%s: %s\n", name, as, r.Verdict.Label())
	for _, level := range []ingest.Verdict{ingest.Unusable, ingest.UsableWithWarning, ingest.Usable} {
		for _, f := range r.Findings {
			if f.Level != level {
				continue
			}
			prefix := "  - "
			if level == ingest.Usable {
				prefix = "  - Note: "
			}
			_, _ = fmt.Fprintf(w, "%s%s\n", prefix, f.Message)
		}
	}
}

func fileSum(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path) // #nosec G304 -- the operator named this file
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
