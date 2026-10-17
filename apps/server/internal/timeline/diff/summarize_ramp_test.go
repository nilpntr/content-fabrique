package diff

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// A speed that goes because a speed ramp replaces it is not "reset to 1x": only the ramp is reported. Removing a speed on its
// own still is.
func TestSummarizeSpeedReplacedByRamp(t *testing.T) {
	base := strings.Replace(summaryBase, `"source": {"in_ms": 1000, "out_ms": 5000}, "at_ms": 3000}`,
		`"source": {"in_ms": 1000, "out_ms": 5000}, "at_ms": 3000, "speed": 2}`, 1)
	spec, err := timeline.Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	run := func(patch string) []string {
		lines, err := Summarize(*spec, mustPatch(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}
	got := run(`[{"op":"remove","path":"/tracks/0/items/1/speed"},{"op":"add","path":"/tracks/0/items/1/speed_ramp","value":[{"at_ms":0,"speed":3},{"at_ms":1500,"speed":0.5}]}]`)
	if want := "set a speed ramp on c2: 3x at 0 ms, 0.5x at 1500 ms"; len(got) != 1 || got[0] != want {
		t.Errorf("a ramp replacing a speed: %q", got)
	}
	got = run(`[{"op":"remove","path":"/tracks/0/items/1/speed"}]`)
	if len(got) != 1 || got[0] != "reset the speed of c2 to 1x" {
		t.Errorf("a speed removed on its own: %q", got)
	}
}
