package diff

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func mustSpec(t *testing.T) timeline.Spec {
	t.Helper()
	spec, err := timeline.Parse([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	return *spec
}

// A patch that applies cleanly is always summarized, whatever it moves and wherever it moves it.
func TestSummarizeMoves(t *testing.T) {
	cases := []struct {
		name, patch string
		want        []string
	}{
		{"within an items array", `[{"op":"move","from":"/tracks/0/items/0","path":"/tracks/0/items/2"}]`, []string{"moved c1 to position 3"}},
		{"to the end of the items array", `[{"op":"move","from":"/tracks/0/items/0","path":"/tracks/0/items/-"}]`, []string{"moved c1 to the end"}},
		{"to another track", `[{"op":"move","from":"/tracks/0/items/0","path":"/tracks/1/items/0"}]`, []string{"moved clip c1 to the text track text"}},
		{"to the end of another track", `[{"op":"move","from":"/tracks/1/items/0","path":"/tracks/2/items/-"}]`, []string{"moved text t1 to the audio track sound"}},
		{"out of the items to a field", `[{"op":"move","from":"/tracks/0/items/0","path":"/output/spare"}]`, []string{"moved clip c1 to /output/spare"}},
		{"a field into items", `[{"op":"move","from":"/output/fps","path":"/tracks/1/items/0/fps"}]`, nil},
		{"a whole track", `[{"op":"move","from":"/tracks/2","path":"/tracks/0"}]`, []string{"reordered tracks"}},
		{"a track into a marker slot", `[{"op":"move","from":"/tracks/2","path":"/markers/0"}]`, nil},
		{"a marker", `[{"op":"move","from":"/markers/1","path":"/markers/0"}]`, []string{}},
		{"a marker out of the markers", `[{"op":"move","from":"/markers/1","path":"/output/marker"}]`, nil},
		{"an item field to another item", `[{"op":"move","from":"/tracks/0/items/0/source","path":"/tracks/0/items/1/spare"}]`, nil},
		{"a move onto itself", `[{"op":"move","from":"/tracks/0/items/1","path":"/tracks/0/items/1"}]`, []string{"moved c2 to position 2"}},
		{"a copy", `[{"op":"copy","from":"/tracks/0/items/0","path":"/tracks/0/items/-"}]`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := summarize(t, tc.patch)
			if len(lines) == 0 && tc.name != "a marker" {
				t.Fatalf("no line for %s", tc.patch)
			}
			// Exact lines where the case says so; otherwise only that there is a sentence.
			if tc.want != nil && !slices.Equal(lines, tc.want) {
				t.Fatalf("got %q want %q", lines, tc.want)
			}
		})
	}
}

func TestSummarizeNeverFailsOnAPatchThatApplies(t *testing.T) {
	base, err := decode([]byte(summaryBase))
	if err != nil {
		t.Fatal(err)
	}
	baseJSON, _ := json.Marshal(base)
	paths := []string{
		"/tracks/0", "/tracks/0/items", "/tracks/0/items/0", "/tracks/0/items/1/source", "/tracks/0/items/1/source/in_ms",
		"/tracks/1/items/0", "/tracks/1/items/0/anchor", "/tracks/2/items/0", "/tracks/2", "/tracks/3", "/markers", "/markers/0",
		"/markers/0/at_ms", "/output", "/output/fps", "/meta", "/schema", "/tracks/0/items/-", "/tracks/-", "/markers/-", "/tracks/1/items/-",
	}
	applied := 0
	for _, op := range []string{OpMove, OpCopy} {
		for _, from := range paths {
			for _, to := range paths {
				p := Patch{{Op: op, From: from, Path: to}}
				if _, err := ApplyPatch(baseJSON, p); err != nil {
					continue
				}
				applied++
				spec := mustSpec(t)
				if _, err := Summarize(spec, p); err != nil {
					t.Errorf("%s %s -> %s applies but Summarize fails: %v", op, from, to, err)
				}
			}
		}
	}
	for _, op := range []string{OpRemove, OpAdd, OpReplace} {
		for _, to := range paths {
			p := Patch{{Op: op, Path: to, Value: json.RawMessage(`{"id":"zz","type":"text","items":[]}`)}}
			if op == OpRemove {
				p[0].Value = nil
			}
			if _, err := ApplyPatch(baseJSON, p); err != nil {
				continue
			}
			applied++
			if _, err := Summarize(mustSpec(t), p); err != nil {
				t.Errorf("%s %s applies but Summarize fails: %v", op, to, err)
			}
		}
	}
	if applied < 100 {
		t.Fatalf("only %d patches applied; the test checks too little", applied)
	}
}

func TestEqualNumbersHugeExponents(t *testing.T) {
	start := time.Now()
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1e999999999", "1e999999998", false},
		{"1e999999999", "1e999999999", true},
		{"1E400", "1e400", true},
		{"1e401", "1E401", false}, // beyond the limit: equal only when written the same
		{"1e-401", "1e-401", true},
		{"1e-999999999", "0", false},
		{"1e1000000000000000000000", "1", false},
		{"0.5e1", "5", true},
		{"10e-1", "1", true},
		{"1e400", "1e-400", false},
		{strings.Repeat("9", 70), strings.Repeat("9", 70), true},
		{strings.Repeat("9", 70), strings.Repeat("9", 69) + "8", false},
	} {
		got, err := Equal([]byte(tc.a), []byte(tc.b))
		if err != nil || got != tc.want {
			t.Errorf("Equal(%s, %s) = %v, %v; want %v", tc.a, tc.b, got, err, tc.want)
		}
	}
	// A diff of such documents is fast and still round-trips.
	a, b := `{"x":[1e999999999,1e999999998]}`, `{"x":[1e999999998,1e999999999]}`
	roundTrip(t, a, b)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}
