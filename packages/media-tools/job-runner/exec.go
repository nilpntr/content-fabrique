package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// execCommand is the hidden subcommand the runner starts every step through: `cf-job __exec <nproc> <fsize> <program>
// <argv...>` sets the step's resource limits on itself and then replaces itself with the program (execve), so the limits
// apply to the tool and everything it starts, and the runner itself is never limited.
const execCommand = "__exec"

// execFailed is __exec's exit code when it could not set a limit or start the program; its stderr then starts with execMarker,
// which tells it apart from a tool that exits 125 by itself.
const (
	execFailed = 125
	execMarker = "cf-job exec: "
)

// The limits every step gets besides Limits.PIDs (RLIMIT_NPROC) and the output cap (RLIMIT_FSIZE): the same open-file and
// core-dump limits as the docker provider (--ulimit nofile=1024:1024, core=0).
const (
	stepNoFile = 1024
	stepCore   = 0
)

func execMain(args []string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, execMarker+format+"\n", a...)
		return execFailed
	}
	if len(args) < 4 {
		return fail("usage: %s <nproc> <fsize> <program> <argv0> [args...]", execCommand)
	}
	nproc, err1 := strconv.ParseUint(args[0], 10, 64)
	fsize, err2 := strconv.ParseUint(args[1], 10, 64)
	if err1 != nil || err2 != nil {
		return fail("bad limits %q %q", args[0], args[1])
	}
	if err := setLimits(nproc, fsize); err != nil {
		return fail("set limits: %v", err)
	}
	prog, argv := args[2], args[3:]
	err := syscall.Exec(prog, argv, os.Environ()) // #nosec G204 G702 -- the runner resolved the program from the job it was given; no shell
	return fail("start %s: %v", argv[0], err)
}

// setLimit lowers a limit, soft and hard, to v: never above the current hard limit, which an unprivileged process cannot raise.
func setLimit(resource int, v uint64) error {
	var cur syscall.Rlimit
	if err := syscall.Getrlimit(resource, &cur); err != nil {
		return err
	}
	v = min(v, cur.Max)
	return syscall.Setrlimit(resource, &syscall.Rlimit{Cur: v, Max: v})
}
