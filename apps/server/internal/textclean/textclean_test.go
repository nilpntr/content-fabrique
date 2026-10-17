package textclean

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		max      int
		want     string
		ok       bool
	}{
		{"plain", "Barbell row", 60, "Barbell row", true},
		{"spaces collapse", "  Barbell \t\n  row  ", 60, "Barbell row", true},
		{"markup removed", "Row <script>alert(1)</script> `x` {{y}}", 60, "Row scriptalert(1)/script x y", true},
		{"control and bidi removed", "gym\u202efdp.mp4\u0000\u200b", 60, "gymfdp.mp4", true},
		{"invalid utf8", "bad\xff", 60, "", false},
		{"empty after cleaning", " <>`{} ", 60, "", false},
		{"cut at a word", "chest supported dumbbell row on an incline bench", 30, "chest supported dumbbell row", true},
		{"cut hard when no good space", strings.Repeat("a", 100), 10, strings.Repeat("a", 10), true},
		{"trailing punctuation trimmed", "Row -", 60, "Row", true},
		{"nfc", "e\u0301", 60, "\u00e9", true},
		{"emoji kept", "Leg day 🔥", 60, "Leg day 🔥", true},
	} {
		got, ok := Clean(tc.in, tc.max)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: Clean(%q, %d) = %q, %v; want %q, %v", tc.name, tc.in, tc.max, got, ok, tc.want, tc.ok)
		}
	}
}
