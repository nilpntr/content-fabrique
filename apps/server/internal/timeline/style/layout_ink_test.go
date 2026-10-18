package style

import "testing"

func TestInkRiseByWhatTheLineHolds(t *testing.T) {
	for _, c := range []struct {
		line string
		want float64
	}{
		{"BACK DAY 101", inkAscender}, {"Never skip leg day", inkAscender},
		{"(BACK)", inkSymbol}, {"a | b", inkSymbol}, {"$5 / 10", inkSymbol}, {"x @ y", inkSymbol}, {"^~*", inkSymbol},
		{"Ünïcödé", inkAccent}, {"Öl", inkAccent},
		{"Å", inkTall}, {"Ů", inkTall}, {"Ǻ", inkTall}, {"Ế", inkTall}, {"Ä", inkTall},
	} {
		if got := inkRise(c.line); got != c.want {
			t.Errorf("%q: %v, want %v", c.line, got, c.want)
		}
	}
}
