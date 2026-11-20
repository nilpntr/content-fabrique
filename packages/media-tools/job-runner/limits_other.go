//go:build !linux

package main

import (
	"fmt"
	"syscall"
)

// setLimits on a development machine: the file-size, open-file and core limits only. The image runs on Linux, where
// limits_linux.go also bounds the processes.
func setLimits(_, fsize uint64) error {
	if err := setLimit(syscall.RLIMIT_FSIZE, fsize); err != nil {
		return fmt.Errorf("fsize: %w", err)
	}
	if err := setLimit(syscall.RLIMIT_NOFILE, stepNoFile); err != nil {
		return fmt.Errorf("nofile: %w", err)
	}
	if err := setLimit(syscall.RLIMIT_CORE, stepCore); err != nil {
		return fmt.Errorf("core: %w", err)
	}
	return nil
}
