package main

import (
	"fmt"
	"syscall"
)

// rlimitNPROC is RLIMIT_NPROC on Linux (amd64 and arm64), which the syscall package does not name.
const rlimitNPROC = 6

// setLimits applies a step's limits to the current process before it becomes the tool. RLIMIT_NPROC counts the threads of
// the whole user, so it bounds the step's processes and threads (Limits.PIDs plus headroom for the runner's own).
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
