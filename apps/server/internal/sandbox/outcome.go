package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// finish records how a started command ended in res and returns the error to report, if any. kill says the caller must
// stop what the command started (the container, for docker) because the command was cut short by the deadline or ctx.
//
// A command that exited by itself is a result, whatever the context says by the time Wait returns: a clean exit just
// before the deadline is not a timeout. Only a command that did not exit (it was killed) while the context was done is a
// timeout or a cancellation. signalsAreResults turns a command killed by a signal into the shell convention 128+signal
// (the local provider, where the command is the tool); for docker a signalled CLI is a sandbox failure.
func finish(runCtx context.Context, parentErr error, cmd *exec.Cmd, runErr error, timeout time.Duration, signalsAreResults bool, res *Result) (kill bool, err error) {
	ps := cmd.ProcessState
	switch {
	case ps != nil && ps.Exited():
		res.ExitCode = ps.ExitCode()
		return false, nil
	case runCtx.Err() != nil && ps != nil:
		res.ExitCode = -1
		if parentErr != nil {
			return true, parentErr
		}
		res.TimedOut = true
		return true, fmt.Errorf("%w after %v", ErrTimeout, timeout)
	case ps != nil && signalsAreResults:
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.ExitCode = 128 + int(ws.Signal())
			return false, nil
		}
		res.ExitCode = -1
		return false, nil
	case runErr != nil:
		res.ExitCode = -1
		return false, fmt.Errorf("%w: %w", ErrSandbox, runErr)
	default:
		res.ExitCode = -1
		return false, fmt.Errorf("%w: command ended without an exit status", ErrSandbox)
	}
}
