package main

import (
	"fmt"
	"syscall"
)

// rlimitNPROC is RLIMIT_NPROC on Linux (amd64 and arm64), which the syscall package does not name.
const rlimitNPROC = 6

// protectSelf makes the runner's memory and /proc entries (environ, mem) unreadable to the steps, which run as the same user:
// PR_SET_DUMPABLE 0 (a ptrace-mode check guards /proc/<pid>/environ). execve resets it for the tools themselves.
func protectSelf() error {
	const prSetDumpable = 4
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

// setLimits applies a step's limits to the current process before it becomes the tool. RLIMIT_NPROC counts the threads of
// the whole user (host-wide without a user namespace), so it bounds the step's processes and threads (Limits.PIDs plus
// headroom for the runner's own) only when the runner has a user of its own: compose runs it as one.
func setLimits(nproc, fsize uint64) error {
	for _, l := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"nproc", rlimitNPROC, nproc},
		{"fsize", syscall.RLIMIT_FSIZE, fsize},
		{"nofile", syscall.RLIMIT_NOFILE, stepNoFile},
		{"core", syscall.RLIMIT_CORE, stepCore},
	} {
		if err := setLimit(l.resource, l.value); err != nil {
			return fmt.Errorf("%s: %w", l.name, err)
		}
	}
	return nil
}
