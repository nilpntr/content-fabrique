package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// A frame normally takes a few milliseconds (seek and capture). Rarely, Chrome's screenshot of a frame never comes back (seen once in
// CI: the first frame of a render hung past 20 s). A frame that takes longer than the frame timeout is tried again: first on the
// same page (seek and capture again; the seek is absolute, so the frame is the same), then, if that hangs as well, on a fresh
// Chrome (launched, loaded and mounted again) once per run, resuming from that frame. The frames stay deterministic: a frame is a
// function of its time and the job alone (the virtual clock and the seeded random of the runtime), whichever page draws it. Only
// after that does the render fail, saying so.

// page is a loaded scene that draws frames: a browser, a session, the scene mounted.
type page interface {
	frame(ctx context.Context, timeout time.Duration, ms float64, clip PixelRect, scale float64) ([]byte, error)
	// problems is what went wrong inside the page so far (nil for nothing).
	problems() error
	// logTail is the end of Chrome's own log, for an error message.
	logTail() string
	close()
}

// frameRecovery counts what the frames needed: retries on the same page and restarts of Chrome.
type frameRecovery struct {
	Retries  int `json:"retries"`
	Restarts int `json:"restarts"`
}

// frames draws the frames of a run through a page, retrying a frame that times out (see above).
type frames struct {
	open     func(ctx context.Context) (page, error)
	cur      page
	timeout  time.Duration
	restarts int // restarts left
	hang     func(n int64) bool
	rec      frameRecovery
	note     func(format string, args ...any)
}

// capture draws frame n (at ms).
func (f *frames) capture(ctx context.Context, n int64, ms float64, clip PixelRect, scale float64) ([]byte, error) {
	try := func() ([]byte, error) {
		if f.hang != nil && f.hang(n) {
			// A test's hang: the frame takes the whole timeout, as a wedged screenshot does.
			hctx, cancel := context.WithTimeout(ctx, f.timeout)
			defer cancel()
			<-hctx.Done()
			return nil, fmt.Errorf("cdp: Page.captureScreenshot: %w", hctx.Err())
		}
		return f.cur.frame(ctx, f.timeout, ms, clip, scale)
	}
	timedOut := func(err error) bool { return errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil }

	png, err := try()
	if !timedOut(err) {
		return png, err
	}
	first := err
	f.rec.Retries++
	f.note("frame %d (%.3f s) took over %v; drawing it again", n, ms/1000, f.timeout)
	png, err = try()
	if !timedOut(err) {
		return png, err
	}
	if f.restarts <= 0 {
		return nil, fmt.Errorf("no frame in %v, twice, and chrome was restarted once already in this render: %w", f.timeout, first)
	}
	// Chrome is wedged. What went wrong in the page before the hang still fails the render; a restart does not hide it.
	if perr := f.cur.problems(); perr != nil {
		return nil, perr
	}
	f.restarts--
	f.rec.Restarts++
	f.note("frame %d (%.3f s) hung twice; restarting chrome and resuming from it", n, ms/1000)
	f.cur.close()
	fresh, oerr := f.open(ctx)
	if oerr != nil {
		return nil, fmt.Errorf("no frame in %v, twice, and chrome did not start again: %w", f.timeout, oerr)
	}
	f.cur = fresh
	png, err = try()
	if timedOut(err) {
		return nil, fmt.Errorf("no frame in %v, twice, nor once more after restarting chrome: %w", f.timeout, err)
	}
	return png, err
}

// parseHang reads the test-only --test-hang flag, "N:K": the first K attempts to draw frame N take the whole frame timeout, as a
// wedged screenshot does. Empty is no hang. It exists for the sandbox tests of the retry; the worker never passes it.
func parseHang(s string) (func(n int64) bool, error) {
	if s == "" {
		return nil, nil
	}
	a, b, ok := strings.Cut(s, ":")
	frame, err1 := strconv.ParseInt(a, 10, 64)
	times, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || frame < 0 || times < 1 || times > 10 {
		return nil, fmt.Errorf("--test-hang %q: want frame:times", s)
	}
	left := times
	return func(n int64) bool {
		if n != frame || left == 0 {
			return false
		}
		left--
		return true
	}, nil
}

// stderrNote writes a note of the run to stderr (the worker shows it with a failure).
func stderrNote(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "cf-motion: "+format+"\n", args...)
}
