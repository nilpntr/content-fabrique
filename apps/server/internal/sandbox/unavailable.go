package sandbox

import (
	"errors"
	"fmt"
	"regexp"
)

// UnavailableError is a sandbox that could not run a job at all, or ran it without access to its own files: Docker is not
// running, the image or the gVisor runtime is missing, the resource limits cannot be applied, or the tool could not read its
// inputs or write its output directory (rootless Docker mapping the container user to another uid). It says nothing about the
// media: callers wait and retry, and never tell a creator that a file is broken because of it. It wraps ErrSandbox.
type UnavailableError struct {
	// Reason is the cause in plain words, safe to print to an operator (no paths of the host, no secrets).
	Reason string
	// Err is the underlying failure, for logs.
	Err error
}

func (e *UnavailableError) Error() string {
	if e.Err == nil {
		return "the sandbox is unavailable: " + e.Reason
	}
	return "the sandbox is unavailable: " + e.Reason + " (" + e.Err.Error() + ")"
}

// Unwrap returns ErrSandbox and the underlying failure, so errors.Is(err, ErrSandbox) holds.
func (e *UnavailableError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrSandbox}
	}
	return []error{ErrSandbox, e.Err}
}

// Unavailable reports whether err means the sandbox, not the job's input, is at fault, and returns the plain reason: an
// UnavailableError's own, or a general one for any other ErrSandbox or ErrInputUnreadable. A timeout, a cancelled context and
// a tool's non-zero exit code are not "unavailable".
func Unavailable(err error) (reason string, ok bool) {
	var ue *UnavailableError
	switch {
	case err == nil:
		return "", false
	case errors.As(err, &ue):
		return ue.Reason, true
	case errors.Is(err, ErrInputUnreadable):
		return "the sandbox's user cannot read the job's files or write its output directory", true
	case errors.Is(err, ErrSandbox):
		return "the sandbox could not run the job", true
	}
	return "", false
}

// UnavailableMessage is the line to print for an unavailable sandbox: "the sandbox is unavailable: <reason>".
func UnavailableMessage(reason string) string { return "the sandbox is unavailable: " + reason }

// Plain reasons for docker run's own failures (exit codes 125 to 127), matched on its stderr, in order. The first match wins.
var dockerReasons = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)cannot connect to the docker daemon|is the docker daemon running|connect: no such file or directory|connection refused`),
		"Docker is not running, or this user cannot reach it"},
	{regexp.MustCompile(`(?i)unknown or invalid runtime name|runtime.*not (found|registered)|no such runtime`),
		"the gVisor runtime (runsc) is not registered with Docker; the hardened provider needs it (docs/06, Running on a VPS)"},
	{regexp.MustCompile(`(?i)no such image|pull access denied|image .* not found|unable to find image|repository does not exist`),
		"the media-tools image is not built on this machine (task media:image)"},
	{regexp.MustCompile(`(?i)cgroup|memory limit|cpu (shares|quota|cfs)|pids limit|controller`),
		"Docker cannot apply the CPU, memory or process limits (its cgroup controllers are not delegated to this user)"},
	{regexp.MustCompile(`(?i)seccomp|apparmor|operation not permitted`),
		"Docker's security settings (seccomp, AppArmor or user namespaces) refused to start the container"},
	{regexp.MustCompile(`(?i)executable file not found|no such file or directory`),
		"the media-tools image does not have the tool the job needs"},
}

// dockerFailureReason is the plain reason for a docker run that exited 125, 126 or 127.
func dockerFailureReason(stderr []byte) string {
	s := string(stderr)
	for _, r := range dockerReasons {
		if r.re.MatchString(s) {
			return r.reason
		}
	}
	return "docker could not start the container"
}

// mountDenied matches a tool's complaint that it could not use one of the job's own mounts: the input files under /in, or the
// output directory /work, refused with a permission error. Under rootless Docker a container user other than root is mapped to a
// subordinate uid that does not own the host's files, and every ffmpeg run then fails this way (exit 243) on any file.
var mountDenied = regexp.MustCompile(`(?i)(^|[\s'"(\[])(/work|/in/[^\s'":]*)\b[^\n]*(permission denied|read-only file system|operation not permitted)`)

// mountFailure reports whether a tool's stderr says it could not read its inputs or write its output directory.
func mountFailure(stderr []byte) bool {
	return mountDenied.Match(stderr)
}

// mountFailureReason is the plain reason for mountFailure.
const mountFailureReason = "the tool in the sandbox cannot read its input or write its output directory (with rootless Docker, the container user maps to another uid; docs/06, Running on a VPS)"

// MountFailure returns an UnavailableError when the program of a job (tool, for the message) exited non-zero saying it could
// not read its inputs or write its output directory, and nil otherwise. The docker providers check it themselves; a caller that
// runs a job on another provider (media.SandboxRunner) checks it too, so that no provider's permission problem is ever read as a
// bad file.
func MountFailure(tool string, res Result) error {
	if res.ExitCode == 0 || !mountFailure(res.Stderr) {
		return nil
	}
	return &UnavailableError{Reason: mountFailureReason, Err: fmt.Errorf("%s exited %d: %s", tool, res.ExitCode, snippet(res.Stderr))}
}
