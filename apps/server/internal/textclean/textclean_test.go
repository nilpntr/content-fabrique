package textclean

import (
	"strings"
	"testing"
)

type tc struct {
	name, in string
	max      int
	want     string
	ok       bool
}

func run(t *testing.T, fn func(string, int) (string, bool), cases []tc) {
	t.Helper()
	for _, c := range cases {
		got, ok := fn(c.in, c.max)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: (%q, %d) = %q, %v; want %q, %v", c.name, c.in, c.max, got, ok, c.want, c.ok)
		}
	}
}

func TestSharedRules(t *testing.T) {
	shared := []tc{
		{"plain", "Barbell row", 60, "Barbell row", true},
		{"spaces collapse", "  Barbell \t\n  row  ", 60, "Barbell row", true},
		{"control and bidi removed", "gym\u202efdp.mp4\u0000\u200b", 60, "gymfdp.mp4", true},
		{"invalid utf8", "bad\xff", 60, "", false},
		{"empty after cleaning", " \u200b\u0000 ", 60, "", false},
		{"cut at a word", "chest supported dumbbell row on an incline bench", 30, "chest supported dumbbell row", true},
		{"cut hard when no good space", strings.Repeat("a", 100), 10, strings.Repeat("a", 10), true},
		{"nfc", "e\u0301", 60, "\u00e9", true},
		{"emoji kept", "Leg day 🔥", 60, "Leg day 🔥", true},
		{"private use dropped", "a\ue000b", 60, "ab", true},
	}
	run(t, User, shared)
	run(t, Model, shared)
}

func TestUserKeepsTypedPunctuation(t *testing.T) {
	run(t, User, []tc{
		{"typed punctuation", "Gino's $100 rows [take 2] | ^heavy~ {a} <b> `c` \\d", 100, "Gino's $100 rows [take 2] | ^heavy~ {a} <b> `c` \\d", true},
		{"trailing punctuation kept", "Row -", 60, "Row -", true},
		{"only punctuation is text", "<>", 60, "<>", true},
	})
}

func TestModelDropsMarkup(t *testing.T) {
	run(t, Model, []tc{
		{"markup removed", "Row <script>alert(1)</script> `x` {{y}} $HOME [a](b) a|b ^~", 100, "Row scriptalert(1)/script x y HOME a(b) ab", true},
		{"only markup", " <>`{} ", 60, "", false},
		{"trailing punctuation trimmed", "Row -", 60, "Row", true},
	})
}
