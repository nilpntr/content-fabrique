package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakePage draws "frame <ms>" and hangs (until its context ends, like a screenshot that never comes back) on the attempts the test
// says. hangs is shared by every page of a run, so a hang can outlive a restart.
type fakePage struct {
	id       int
	hangs    map[float64]int // attempts left that hang, by frame time
	problem  error
	closed   bool
	attempts *int
}

func (p *fakePage) frame(ctx context.Context, timeout time.Duration, ms float64, _ PixelRect, _ float64) ([]byte, error) {
	*p.attempts++
	if p.hangs[ms] > 0 {
		p.hangs[ms]--
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		<-ctx.Done()
		return nil, fmt.Errorf("cdp: Page.captureScreenshot: %w", ctx.Err())
	}
	return []byte(fmt.Sprintf("frame %g", ms)), nil
}
func (p *fakePage) problems() error { return p.problem }
func (p *fakePage) logTail() string { return "" }
func (p *fakePage) close()          { p.closed = true }

// run draws frames 0..4 (at n*10 ms) through frames with the given hangs, and returns what it drew, the pages it opened and the error.
func runFake(t *testing.T, hangs map[float64]int, openErr error) ([]string, []*fakePage, frameRecovery, error) {
	t.Helper()
	attempts := 0
	var pages []*fakePage
	open := func(context.Context) (page, error) {
		if openErr != nil && len(pages) > 0 { // the restart fails, not the first launch
			return nil, openErr
		}
		p := &fakePage{id: len(pages), hangs: hangs, attempts: &attempts}
		pages = append(pages, p)
		return p, nil
	}
	first, _ := open(context.Background())
	var notes []string
	f := &frames{open: open, cur: first, timeout: 20 * time.Millisecond, restarts: 1,
		note: func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }}
	var drawn []string
	for n := int64(0); n < 5; n++ {
		png, err := f.capture(context.Background(), n, float64(n*10), PixelRect{}, 1)
		if err != nil {
			return drawn, pages, f.rec, err
		}
		drawn = append(drawn, string(png))
	}
	return drawn, pages, f.rec, nil
}

func want5(t *testing.T, drawn []string) {
	t.Helper()
	if strings.Join(drawn, ",") != "frame 0,frame 10,frame 20,frame 30,frame 40" {
		t.Fatalf("drawn %q", drawn)
	}
}

func TestAFrameThatHangsOnceIsDrawnAgainOnTheSamePage(t *testing.T) {
	drawn, pages, rec, err := runFake(t, map[float64]int{20: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want5(t, drawn)
	if rec.Retries != 1 || rec.Restarts != 0 || len(pages) != 1 {
		t.Errorf("recovery %+v, %d pages", rec, len(pages))
	}
}

func TestAWedgedChromeIsRestartedOnceAndTheRenderResumes(t *testing.T) {
	drawn, pages, rec, err := runFake(t, map[float64]int{20: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want5(t, drawn)
	if rec.Retries != 1 || rec.Restarts != 1 || len(pages) != 2 || !pages[0].closed || pages[1].closed {
		t.Errorf("recovery %+v, %d pages, first closed %v", rec, len(pages), pages[0].closed)
	}
}

func TestTheRenderFailsWhenTheRestartDoesNotHelp(t *testing.T) {
	_, pages, _, err := runFake(t, map[float64]int{20: 3}, nil)
	if err == nil || !strings.Contains(err.Error(), "nor once more after restarting chrome") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if len(pages) != 2 {
		t.Errorf("%d pages", len(pages))
	}
	// One restart a render: a second wedge later fails.
	_, _, _, err = runFake(t, map[float64]int{10: 2, 30: 2}, nil)
	if err == nil || !strings.Contains(err.Error(), "restarted once already") {
		t.Fatalf("a second wedge: %v", err)
	}
}

func TestARestartNeitherHidesAPageProblemNorRunsWithoutChrome(t *testing.T) {
	attempts := 0
	bad := &fakePage{hangs: map[float64]int{0: 2}, problem: errors.New("the page failed: uncaught exception"), attempts: &attempts}
	f := &frames{open: func(context.Context) (page, error) { t.Fatal("restarted over a page problem"); return nil, nil },
		cur: bad, timeout: 10 * time.Millisecond, restarts: 1, note: func(string, ...any) {}}
	if _, err := f.capture(context.Background(), 0, 0, PixelRect{}, 1); err == nil || !strings.Contains(err.Error(), "uncaught exception") {
		t.Fatalf("err %v", err)
	}
	_, _, _, err := runFake(t, map[float64]int{0: 2}, errors.New("chrome: no such file"))
	if err == nil || !strings.Contains(err.Error(), "did not start again") {
		t.Fatalf("err %v", err)
	}
}

// A cancelled render is not a hang: it is not retried.
func TestACancelledRenderIsNotRetried(t *testing.T) {
	attempts := 0
	p := &fakePage{hangs: map[float64]int{0: 5}, attempts: &attempts}
	f := &frames{open: func(context.Context) (page, error) { t.Fatal("restarted"); return nil, nil }, cur: p, timeout: time.Second, restarts: 1,
		note: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := f.capture(ctx, 0, 0, PixelRect{}, 1); err == nil || attempts != 1 {
		t.Fatalf("err %v after %d attempts", err, attempts)
	}
}

func TestParseHang(t *testing.T) {
	h, err := parseHang("12:2")
	if err != nil {
		t.Fatal(err)
	}
	got := []bool{h(11), h(12), h(12), h(12)}
	if fmt.Sprint(got) != "[false true true false]" {
		t.Errorf("hang %v", got)
	}
	for _, bad := range []string{"x", "1", "1:0", "-1:1", "1:99"} {
		if _, err := parseHang(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if h, err := parseHang(""); h != nil || err != nil {
		t.Error("empty")
	}
}
