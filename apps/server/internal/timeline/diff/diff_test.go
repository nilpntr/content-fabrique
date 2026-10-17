package diff

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func diffDocs(t testing.TB, a, b string) Patch {
	t.Helper()
	p, err := Diff([]byte(a), []byte(b))
	if err != nil {
		t.Fatalf("diff %s -> %s: %v", a, b, err)
	}
	return p
}

func patchJSON(t testing.TB, p Patch) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// roundTrip checks ApplyPatch(a, Diff(a, b)) == b.
func roundTrip(t testing.TB, a, b string) Patch {
	t.Helper()
	p := diffDocs(t, a, b)
	out, err := ApplyPatch([]byte(a), p)
	if err != nil {
		t.Fatalf("apply %s to %s: %v", patchJSON(t, p), a, err)
	}
	if !equalDocs(t, string(out), b) {
		t.Fatalf("%s\n  -> patch %s\n  gives %s\n  want  %s", a, patchJSON(t, p), out, b)
	}
	return p
}

func TestDiffShapes(t *testing.T) {
	cases := []struct {
		name, a, b, want string
	}{
		{"equal", `{"a":1}`, `{"a":1.0}`, `[]`},
		{"replace a value", `{"a":1}`, `{"a":2}`, `[{"op":"replace","path":"/a","value":2}]`},
		{"add and remove members, in key order", `{"a":1,"b":2}`, `{"b":2,"c":3}`,
			`[{"op":"remove","path":"/a"},{"op":"add","path":"/c","value":3}]`},
		{"nested", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":2}}}`, `[{"op":"replace","path":"/a/b/c","value":2}]`},
		{"escaped key", `{"a/b":1}`, `{"a/b":2}`, `[{"op":"replace","path":"/a~1b","value":2}]`},
		{"type change", `{"a":[1]}`, `{"a":{"x":1}}`, `[{"op":"replace","path":"/a","value":{"x":1}}]`},
		{"to null", `{"a":1}`, `{"a":null}`, `[{"op":"replace","path":"/a","value":null}]`},
		{"root type change", `[1]`, `{"a":1}`, `[{"op":"replace","path":"","value":{"a":1}}]`},
		{"plain array: change, grow", `[1,2]`, `[1,3,4]`, `[{"op":"replace","path":"/1","value":3},{"op":"add","path":"/2","value":4}]`},
		{"plain array: shrink, last first", `[1,2,3,4]`, `[1,2]`, `[{"op":"remove","path":"/3"},{"op":"remove","path":"/2"}]`},
		{"id array: remove the middle", `[{"id":"a"},{"id":"b"},{"id":"c"}]`, `[{"id":"a"},{"id":"c"}]`, `[{"op":"remove","path":"/1"}]`},
		{"id array: add in the middle", `[{"id":"a"},{"id":"c"}]`, `[{"id":"a"},{"id":"b"},{"id":"c"}]`, `[{"op":"add","path":"/1","value":{"id":"b"}}]`},
		{"id array: change one element", `[{"id":"a","n":1},{"id":"b","n":2}]`, `[{"id":"a","n":1},{"id":"b","n":3}]`,
			`[{"op":"replace","path":"/1/n","value":3}]`},
		{"id array: one move for a rotation", `[{"id":"a"},{"id":"b"},{"id":"c"},{"id":"d"}]`, `[{"id":"b"},{"id":"c"},{"id":"d"},{"id":"a"}]`,
			`[{"op":"move","from":"/0","path":"/3"}]`},
		{"id array: one move to the front", `[{"id":"a"},{"id":"b"},{"id":"c"}]`, `[{"id":"c"},{"id":"a"},{"id":"b"}]`,
			`[{"op":"move","from":"/2","path":"/0"}]`},
		{"id array: swap two", `[{"id":"a"},{"id":"b"}]`, `[{"id":"b"},{"id":"a"}]`, `[{"op":"move","from":"/0","path":"/1"}]`},
		{"id array: a moved element that also changed", `[{"id":"a","n":1},{"id":"b"}]`, `[{"id":"b"},{"id":"a","n":2}]`,
			`[{"op":"move","from":"/0","path":"/1"},{"op":"replace","path":"/1/n","value":2}]`},
		{"id array: duplicate ids fall back to indexes", `[{"id":"a","n":1},{"id":"a","n":2}]`, `[{"id":"a","n":1},{"id":"a","n":3}]`,
			`[{"op":"replace","path":"/1/n","value":3}]`},
		{"id array: element without an id falls back to indexes", `[{"id":"a"},{"x":1}]`, `[{"id":"a"},{"x":2}]`, `[{"op":"replace","path":"/1/x","value":2}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := roundTrip(t, tc.a, tc.b)
			if got := patchJSON(t, p); got != tc.want {
				t.Fatalf("patch %s\nwant  %s", got, tc.want)
			}
		})
	}
}

func TestDiffErrors(t *testing.T) {
	if _, err := Diff([]byte(`{`), []byte(`{}`)); err == nil {
		t.Fatal("invalid first document accepted")
	}
	if _, err := Diff([]byte(`{}`), []byte(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

// ---- random documents -------------------------------------------------------------------------------------------

type docGen struct{ r *rand.Rand }

func (g docGen) scalar() any {
	switch g.r.IntN(7) {
	case 0:
		return nil
	case 1:
		return g.r.IntN(2) == 0
	case 2:
		return json.Number(fmt.Sprint(g.r.IntN(5)))
	case 3:
		return json.Number(fmt.Sprintf("%g", float64(g.r.IntN(50))/8))
	case 4:
		return json.Number(fmt.Sprint(-g.r.IntN(5)))
	case 5:
		return ""
	}
	return pickStr(g.r, []string{"a", "b", "x/y", "~", "é", "line\nbreak", "\"quoted\""})
}

func pickStr(r *rand.Rand, s []string) string { return s[r.IntN(len(s))] }

var keys = []string{"a", "b", "c", "id", "x/y", "~t", "", "n"}

func (g docGen) value(depth int) any {
	if depth <= 0 {
		return g.scalar()
	}
	switch g.r.IntN(5) {
	case 0:
		return g.scalar()
	case 1:
		return g.object(depth)
	case 2:
		return g.plainArray(depth)
	default:
		return g.idArray(depth) // most of the interesting cases
	}
}

func (g docGen) object(depth int) map[string]any {
	m := map[string]any{}
	for range g.r.IntN(5) {
		m[pickStr(g.r, keys)] = g.value(depth - 1)
	}
	return m
}

func (g docGen) plainArray(depth int) []any {
	out := make([]any, g.r.IntN(5))
	for i := range out {
		out[i] = g.value(depth - 1)
	}
	return out
}

func (g docGen) idArray(depth int) []any {
	ids := []string{"a", "b", "c", "d", "e", "f", "g"}
	g.r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	out := make([]any, g.r.IntN(len(ids)+1))
	for i := range out {
		m := g.object(depth - 1)
		m["id"] = ids[i]
		out[i] = m
	}
	return out
}

// mutate returns a changed copy of v: values change, elements are added, removed, reordered.
func (g docGen) mutate(v any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, c := range x {
			switch g.r.IntN(6) {
			case 0: // drop
			case 1:
				m[k] = g.value(depth - 1)
			default:
				m[k] = g.mutate(c, depth-1)
			}
		}
		for range g.r.IntN(3) {
			m[pickStr(g.r, keys)] = g.value(depth - 1)
		}
		return m
	case []any:
		out := append([]any(nil), x...)
		for range g.r.IntN(4) {
			switch g.r.IntN(5) {
			case 0:
				if len(out) > 0 {
					i := g.r.IntN(len(out))
					out = append(out[:i], out[i+1:]...)
				}
			case 1:
				g.r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			case 2:
				if len(out) > 0 {
					i := g.r.IntN(len(out))
					out[i] = g.mutate(out[i], depth-1)
				}
			case 3: // a new element, with a fresh id if the array is keyed by id
				e := g.value(depth - 1)
				if m, ok := e.(map[string]any); ok {
					m["id"] = fmt.Sprintf("n%d", g.r.IntN(1000))
				}
				i := g.r.IntN(len(out) + 1)
				out = append(out[:i], append([]any{e}, out[i:]...)...)
			default: // rotate
				if len(out) > 1 {
					out = append(out[1:], out[0])
				}
			}
		}
		return out
	}
	if g.r.IntN(2) == 0 {
		return g.scalar()
	}
	return v
}

func TestDiffRandomDocuments(t *testing.T) {
	r := rand.New(rand.NewPCG(42, 4242))
	g := docGen{r: r}
	const iterations = 4000
	moves, adds, removes, replaces := 0, 0, 0, 0
	for i := range iterations {
		a := g.value(4)
		b := g.mutate(a, 4)
		if i%7 == 0 {
			b = g.value(4) // an unrelated document
		}
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		p := roundTrip(t, string(aj), string(bj))
		for _, o := range p {
			switch o.Op {
			case OpMove:
				moves++
			case OpAdd:
				adds++
			case OpRemove:
				removes++
			case OpReplace:
				replaces++
			default:
				t.Fatalf("unexpected op %s", o.Op)
			}
		}
		// A patch of two equal documents is empty, and an empty patch means equal documents.
		if eq := equalDocs(t, string(aj), string(bj)); eq != (len(p) == 0) {
			t.Fatalf("equal=%v but the patch has %d operations: %s -> %s", eq, len(p), aj, bj)
		}
		// The patch survives its own JSON.
		if back, err := ParsePatch([]byte(patchJSON(t, p))); err != nil || patchJSON(t, back) != patchJSON(t, p) {
			t.Fatalf("patch round trip: %v", err)
		}
	}
	t.Logf("%d documents: %d add, %d remove, %d replace, %d move operations", iterations, adds, removes, replaces, moves)
	if moves < 100 || adds < 100 || removes < 100 || replaces < 100 {
		t.Fatal("the generator did not exercise every operation")
	}
}

// Every pair of valid fixtures: the patch from one to the other applies, and gives the other.
func TestDiffFixturePairs(t *testing.T) {
	files, err := filepath.Glob("../../../../../packages/schemas/timeline/fixtures/valid/*.json")
	if err != nil || len(files) < 30 {
		t.Fatalf("fixtures: %v %d", err, len(files))
	}
	sort.Strings(files)
	specs := make([]timeline.Spec, len(files))
	for i, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // test fixtures
		if err != nil {
			t.Fatal(err)
		}
		s, err := timeline.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		specs[i] = *s
	}
	for i := range specs {
		for j := range specs {
			p, err := Specs(specs[i], specs[j])
			if err != nil {
				t.Fatal(err)
			}
			got, err := ApplySpec(specs[i], p)
			if err != nil {
				t.Fatalf("%s -> %s: %v", filepath.Base(files[i]), filepath.Base(files[j]), err)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(specs[j])
			if string(a) != string(b) {
				t.Fatalf("%s -> %s differs", filepath.Base(files[i]), filepath.Base(files[j]))
			}
			if i == j && len(p) != 0 {
				t.Fatalf("%s: a spec differs from itself: %s", filepath.Base(files[i]), patchJSON(t, p))
			}
		}
	}
}

func TestApplySpecRejectsAPatchThatBreaksTheShape(t *testing.T) {
	data, err := os.ReadFile("../../../../../packages/schemas/timeline/fixtures/valid/v01_minimal_9x16.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := timeline.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// An unknown field makes the result no longer a spec.
	if _, err := ApplySpec(*s, mustPatch(t, `[{"op":"add","path":"/zzz","value":1}]`)); err == nil || !strings.Contains(err.Error(), "not a spec") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ApplySpec(*s, mustPatch(t, `[{"op":"remove","path":"/nope"}]`)); err == nil {
		t.Fatal("a failing patch was accepted")
	}
}
