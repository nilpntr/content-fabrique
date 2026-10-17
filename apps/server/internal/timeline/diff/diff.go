package diff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Specs returns the JSON Patch that turns from into to.
func Specs(from, to timeline.Spec) (Patch, error) {
	a, err := json.Marshal(from)
	if err != nil {
		return nil, fmt.Errorf("diff: encode the first spec: %w", err)
	}
	b, err := json.Marshal(to)
	if err != nil {
		return nil, fmt.Errorf("diff: encode the second spec: %w", err)
	}
	return Diff(a, b)
}

// ApplySpec applies patch to spec and decodes the result as a spec again, strictly. It is atomic.
func ApplySpec(spec timeline.Spec, patch Patch) (timeline.Spec, error) {
	doc, err := json.Marshal(spec)
	if err != nil {
		return timeline.Spec{}, fmt.Errorf("diff: encode the spec: %w", err)
	}
	out, err := ApplyPatch(doc, patch)
	if err != nil {
		return timeline.Spec{}, err
	}
	parsed, err := timeline.Parse(out)
	if err != nil {
		return timeline.Spec{}, fmt.Errorf("diff: the patched document is not a spec: %w", err)
	}
	return *parsed, nil
}

// Diff returns the RFC 6902 JSON Patch that turns the JSON document from into to: ApplyPatch(from, Diff(from, to))
// is equal to to. The patch is empty (not nil) when the documents are equal.
//
// Objects are diffed key by key. An array whose elements are all objects with a unique string "id" (the tracks, and
// the items of a track) is diffed by id: elements that left are removed, elements that changed place are moved with a
// "move" operation, new elements are added at their place, and each element that stays is diffed in place. Any other
// array is diffed by index. Correctness comes first: a patch may be longer than the smallest possible one, but it is
// always right.
func Diff(from, to []byte) (Patch, error) {
	a, err := decode(from)
	if err != nil {
		return nil, fmt.Errorf("diff: first document: %w", err)
	}
	b, err := decode(to)
	if err != nil {
		return nil, fmt.Errorf("diff: second document: %w", err)
	}
	d := &differ{patch: Patch{}}
	if err := d.value(nil, a, b); err != nil {
		return nil, err
	}
	return d.patch, nil
}

type differ struct {
	patch Patch
}

func (d *differ) emit(op, path, from string, value any) error {
	o := Operation{Op: op, Path: path, From: from}
	if op == OpAdd || op == OpReplace {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("diff: encode a value: %w", err)
		}
		o.Value = raw
	}
	d.patch = append(d.patch, o)
	return nil
}

func (d *differ) value(path []string, a, b any) error {
	if equalValues(a, b) {
		return nil
	}
	switch x := a.(type) {
	case map[string]any:
		if y, ok := b.(map[string]any); ok {
			return d.object(path, x, y)
		}
	case []any:
		if y, ok := b.([]any); ok {
			return d.array(path, x, y)
		}
	}
	return d.emit(OpReplace, Pointer(path...), "", b)
}

func (d *differ) object(path []string, a, b map[string]any) error {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		av, inA := a[k]
		bv, inB := b[k]
		child := append(path[:len(path):len(path)], k)
		switch {
		case inA && !inB:
			if err := d.emit(OpRemove, Pointer(child...), "", nil); err != nil {
				return err
			}
		case !inA && inB:
			if err := d.emit(OpAdd, Pointer(child...), "", bv); err != nil {
				return err
			}
		default:
			if err := d.value(child, av, bv); err != nil {
				return err
			}
		}
	}
	return nil
}

// elementID returns the "id" of an array element, if it is an object with a string id.
func elementID(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := m["id"].(string)
	return id, ok
}

// idKeys returns the ids of an array whose elements all have a unique string id.
func idKeys(arr []any) ([]string, bool) {
	ids := make([]string, len(arr))
	seen := make(map[string]bool, len(arr))
	for i, v := range arr {
		id, ok := elementID(v)
		if !ok || seen[id] {
			return nil, false
		}
		seen[id] = true
		ids[i] = id
	}
	return ids, true
}

func (d *differ) array(path []string, a, b []any) error {
	aIDs, okA := idKeys(a)
	bIDs, okB := idKeys(b)
	if okA && okB {
		return d.arrayByID(path, a, b, aIDs, bIDs)
	}
	return d.arrayByIndex(path, a, b)
}

func at(path []string, i int) []string {
	return append(path[:len(path):len(path)], strconv.Itoa(i))
}

func (d *differ) arrayByIndex(path []string, a, b []any) error {
	n := min(len(a), len(b))
	for i := range n {
		if err := d.value(at(path, i), a[i], b[i]); err != nil {
			return err
		}
	}
	for i := len(a) - 1; i >= n; i-- {
		if err := d.emit(OpRemove, Pointer(at(path, i)...), "", nil); err != nil {
			return err
		}
	}
	for i := n; i < len(b); i++ {
		if err := d.emit(OpAdd, Pointer(at(path, i)...), "", b[i]); err != nil {
			return err
		}
	}
	return nil
}

// arrayByID emits, in order: removals of elements that are gone (last first, so indexes stay valid), moves of the
// elements that must change place, adds of new elements, and the diffs of the elements that stay. After the moves the
// kept elements are in the order of b, so every add can go at its final index, and every kept element sits at its
// final index when it is diffed.
func (d *differ) arrayByID(path []string, a, b []any, aIDs, bIDs []string) error {
	inB := make(map[string]int, len(b)) // id -> index in b
	for j, id := range bIDs {
		inB[id] = j
	}
	aByID := make(map[string]any, len(a))
	for i, id := range aIDs {
		aByID[id] = a[i]
	}

	// 1. Removals, last first.
	cur := append([]string(nil), aIDs...)
	for i := len(cur) - 1; i >= 0; i-- {
		if _, kept := inB[cur[i]]; !kept {
			if err := d.emit(OpRemove, Pointer(at(path, i)...), "", nil); err != nil {
				return err
			}
			cur = append(cur[:i], cur[i+1:]...)
		}
	}

	// 2. Moves. The elements that stay put are a longest increasing subsequence of the kept elements' positions in b;
	// every other kept element moves, in the order of b, to just after its predecessor in b.
	pos := make([]int, len(cur))
	for i, id := range cur {
		pos[i] = inB[id]
	}
	stable := make(map[string]bool)
	for _, i := range longestIncreasing(pos) {
		stable[cur[i]] = true
	}
	kept := make([]string, 0, len(cur)) // the kept ids in the order of b
	for _, id := range bIDs {
		if _, ok := aByID[id]; ok {
			kept = append(kept, id)
		}
	}
	for k, id := range kept {
		if stable[id] {
			continue
		}
		from := indexOf(cur, id)
		rest := append(append([]string(nil), cur[:from]...), cur[from+1:]...)
		dest := 0
		if k > 0 {
			dest = indexOf(rest, kept[k-1]) + 1
		}
		if dest == from {
			continue
		}
		if err := d.emit(OpMove, Pointer(at(path, dest)...), Pointer(at(path, from)...), nil); err != nil {
			return err
		}
		cur = append(rest[:dest], append([]string{id}, rest[dest:]...)...)
	}

	// 3. Adds, at their final index.
	for j, id := range bIDs {
		if _, ok := aByID[id]; ok {
			continue
		}
		if err := d.emit(OpAdd, Pointer(at(path, j)...), "", b[j]); err != nil {
			return err
		}
	}

	// 4. The elements that stay, in place.
	for j, id := range bIDs {
		old, ok := aByID[id]
		if !ok {
			continue
		}
		if err := d.value(at(path, j), old, b[j]); err != nil {
			return err
		}
	}
	return nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// longestIncreasing returns the indexes of a longest strictly increasing subsequence of seq.
func longestIncreasing(seq []int) []int {
	if len(seq) == 0 {
		return nil
	}
	tails := make([]int, 0, len(seq)) // tails[l] = index in seq of the smallest tail of an increasing run of length l+1
	prev := make([]int, len(seq))
	for i, v := range seq {
		l := sort.Search(len(tails), func(k int) bool { return seq[tails[k]] >= v })
		if l > 0 {
			prev[i] = tails[l-1]
		} else {
			prev[i] = -1
		}
		if l == len(tails) {
			tails = append(tails, i)
		} else {
			tails[l] = i
		}
	}
	out := make([]int, len(tails))
	for i, k := len(tails)-1, tails[len(tails)-1]; i >= 0; i, k = i-1, prev[k] {
		out[i] = k
	}
	return out
}
