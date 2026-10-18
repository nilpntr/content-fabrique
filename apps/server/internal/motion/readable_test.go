package motion

import (
	"math"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The cubic-bezier solver at points worked by hand: ease-out-expo (0.16, 1, 0.3, 1) is 95 % of the way at 42.6 % of its time, and the
// exit ease-in (0.7, 0, 0.84, 0) 10 % of the way at 67 %.
func TestBezierTimeFor(t *testing.T) {
	for _, c := range []struct {
		b    bezier
		p    float64
		want float64
	}{{easeOutExpo, 0.95, 0.426}, {easeExit, 0.10, 0.670}, {bezier{0, 0, 1, 1}, 0.5, 0.5}} {
		if got := c.b.timeFor(c.p); math.Abs(got-c.want) > 0.005 {
			t.Errorf("%+v.timeFor(%g) = %.3f, want %.3f", c.b, c.p, got, c.want)
		}
	}
}

// The readable window lies inside the item and around the old "fully in to exit" span: it starts no later than FullyInMs (a settle
// is readable) and ends no earlier than ExitMs (an exit is not instant). The three graphics overlay_readable failed in the live runs
// read for 1.2 s and more (TestIntegrationReadableWindow holds the window to the rendered frames).
func TestReadableWindow(t *testing.T) {
	for _, e := range []string{timeline.EnergyCalm, timeline.EnergyLively, timeline.EnergyHype} {
		for _, it := range sampleItems() {
			it.Params.Energy = e
			tm := Schedule(it, 1)
			if tm.LegibleMs > tm.FullyInMs || tm.LeavingMs < tm.ExitMs || tm.LeavingMs > it.DurationMs || tm.LegibleMs < 0 {
				t.Errorf("%s (%s): readable %d-%d, fully in %d, exit %d, duration %d", it.Template, e, tm.LegibleMs, tm.LeavingMs, tm.FullyInMs, tm.ExitMs, it.DurationMs)
			}
		}
	}
	for _, it := range liveReadableItems() {
		tm := Schedule(it, 1)
		if tm.ReadableMs() < 1200 {
			t.Errorf("%s %s reads for %d ms (from %d to %d)", it.ID, it.Template, tm.ReadableMs(), tm.LegibleMs, tm.LeavingMs)
		}
	}
	// Too short is still too short: a three-word title in 1.2 s cannot be read for 1.2 s.
	short := liveReadableItems()[0]
	short.DurationMs = 1200
	if tm := Schedule(short, 1); tm.ReadableMs() >= 1200 {
		t.Errorf("a 1.2 s title reads for %d ms", tm.ReadableMs())
	}
}
