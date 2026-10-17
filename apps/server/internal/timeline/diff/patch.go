// Package diff turns two timeline specs into an RFC 6902 JSON Patch and back (docs/design/timeline-v1.md section 4).
//
// A version of a timeline stores the patch from its parent: Diff makes it, ApplyPatch replays it, and Summarize puts it
// in words for the run feed and the version list. The property the tests hold is ApplyPatch(parent, Diff(parent,
// child)) == child for any two documents.
//
// Only the standard library is used. Documents are handled as generic JSON values with numbers kept as their literal
// text (json.Number), so a number that goes through a patch comes out as it went in.
package diff

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// The six operations of RFC 6902.
const (
	OpAdd     = "add"
	OpRemove  = "remove"
	OpReplace = "replace"
	OpMove    = "move"
	OpCopy    = "copy"
	OpTest    = "test"
)

// Operation is one step of a JSON Patch.
type Operation struct {
	Op string `json:"op"`
	// Path is a JSON Pointer (RFC 6901) into the document; "" is the whole document.
	Path string `json:"path"`
	// From is the source pointer of a move or copy.
	From string `json:"from,omitempty"`
	// Value is the JSON value of an add, replace or test. A JSON null is the four bytes "null", so it can be told
	// from a missing value.
	Value json.RawMessage `json:"value,omitempty"`
}

// Patch is a JSON Patch document: a list of operations applied in order.
type Patch []Operation

// MarshalJSON writes the fields each operation needs and no others: value for add, replace and test (even when it is
// null), from for move and copy.
func (o Operation) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case OpAdd, OpReplace, OpTest:
		if o.Value == nil {
			return nil, fmt.Errorf("diff: %s at %q has no value", o.Op, o.Path)
		}
		return json.Marshal(struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		}{o.Op, o.Path, o.Value})
	case OpMove, OpCopy:
		return json.Marshal(struct {
			Op   string `json:"op"`
			From string `json:"from"`
			Path string `json:"path"`
		}{o.Op, o.From, o.Path})
	case OpRemove:
		return json.Marshal(struct {
			Op   string `json:"op"`
			Path string `json:"path"`
		}{o.Op, o.Path})
	}
	return nil, fmt.Errorf("diff: unknown patch operation %q", o.Op)
}

// ParsePatch decodes a JSON Patch document and checks that every operation has what it needs.
func ParsePatch(data []byte) (Patch, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Patch
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("diff: patch: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("diff: patch: unexpected data after the patch")
	}
	for i, o := range p {
		switch o.Op {
		case OpAdd, OpReplace, OpTest:
			if o.Value == nil {
				return nil, fmt.Errorf("diff: patch operation %d (%s %q) has no value", i, o.Op, o.Path)
			}
		case OpMove, OpCopy, OpRemove:
		default:
			return nil, fmt.Errorf("diff: patch operation %d has unknown op %q", i, o.Op)
		}
	}
	return p, nil
}

// decode reads one JSON value, numbers as json.Number.
func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

// ApplyPatch applies patch to the JSON document doc and returns the new document. It is atomic: on an error nothing
// is returned. doc is not changed. The output has sorted object keys and no insignificant whitespace; use Equal to
// compare documents.
func ApplyPatch(doc []byte, patch Patch) ([]byte, error) {
	root, err := decode(doc)
	if err != nil {
		return nil, fmt.Errorf("diff: document: %w", err)
	}
	for i, op := range patch {
		root, err = applyOp(root, op)
		if err != nil {
			return nil, fmt.Errorf("diff: operation %d (%s %q): %w", i, op.Op, op.Path, err)
		}
	}
	return json.Marshal(root)
}

// Equal reports whether two JSON documents are the same value: object key order, whitespace and the spelling of
// numbers (1, 1.0, 1e0) do not matter.
func Equal(a, b []byte) (bool, error) {
	x, err := decode(a)
	if err != nil {
		return false, fmt.Errorf("diff: first document: %w", err)
	}
	y, err := decode(b)
	if err != nil {
		return false, fmt.Errorf("diff: second document: %w", err)
	}
	return equalValues(x, y), nil
}

// ---- JSON Pointer -----------------------------------------------------------------------------------------------

// Pointer builds a JSON Pointer from tokens, escaping ~ and /.
func Pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(t))
	}
	return b.String()
}

func escapeToken(t string) string {
	t = strings.ReplaceAll(t, "~", "~0")
	return strings.ReplaceAll(t, "/", "~1")
}

func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if p[0] != '/' {
		return nil, fmt.Errorf("pointer %q must be empty or start with /", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, t := range parts {
		if strings.Contains(t, "~") {
			t = strings.ReplaceAll(t, "~1", "/")
			t = strings.ReplaceAll(t, "~0", "~")
			parts[i] = t
		}
	}
	return parts, nil
}

// arrayIndex parses an array index token: digits only, no leading zero.
func arrayIndex(tok string, n int, allowEnd bool) (int, error) {
	if tok == "" || (len(tok) > 1 && tok[0] == '0') {
		return 0, fmt.Errorf("%q is not an array index", tok)
	}
	for _, r := range tok {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not an array index", tok)
		}
	}
	i, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("%q is not an array index", tok)
	}
	limit := n
	if allowEnd {
		limit = n + 1
	}
	if i >= limit {
		return 0, fmt.Errorf("array index %d is out of range (length %d)", i, n)
	}
	return i, nil
}

// ---- applying one operation -------------------------------------------------------------------------------------

func applyOp(root any, op Operation) (any, error) {
	path, err := parsePointer(op.Path)
	if err != nil {
		return nil, err
	}
	switch op.Op {
	case OpAdd:
		v, err := decode(op.Value)
		if err != nil {
			return nil, fmt.Errorf("value: %w", err)
		}
		return addAt(root, path, v)
	case OpReplace:
		v, err := decode(op.Value)
		if err != nil {
			return nil, fmt.Errorf("value: %w", err)
		}
		return replaceAt(root, path, v)
	case OpRemove:
		out, _, err := removeAt(root, path)
		return out, err
	case OpTest:
		want, err := decode(op.Value)
		if err != nil {
			return nil, fmt.Errorf("value: %w", err)
		}
		got, err := getAt(root, path)
		if err != nil {
			return nil, err
		}
		if !equalValues(got, want) {
			return nil, errors.New("test failed: the value differs")
		}
		return root, nil
	case OpMove, OpCopy:
		from, err := parsePointer(op.From)
		if err != nil {
			return nil, err
		}
		if op.Op == OpMove {
			if len(from) < len(path) && equalTokens(from, path[:len(from)]) {
				return nil, errors.New("cannot move a value into one of its own children")
			}
			if equalTokens(from, path) {
				_, err := getAt(root, from)
				return root, err
			}
			root, v, err := removeAt(root, from)
			if err != nil {
				return nil, err
			}
			return addAt(root, path, v)
		}
		v, err := getAt(root, from)
		if err != nil {
			return nil, err
		}
		return addAt(root, path, clone(v))
	}
	return nil, fmt.Errorf("unknown op %q", op.Op)
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func getAt(node any, path []string) (any, error) {
	for _, tok := range path {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[tok]
			if !ok {
				return nil, fmt.Errorf("no member %q", tok)
			}
			node = v
		case []any:
			i, err := arrayIndex(tok, len(n), false)
			if err != nil {
				return nil, err
			}
			node = n[i]
		default:
			return nil, fmt.Errorf("cannot step into a %s with %q", kindOf(node), tok)
		}
	}
	return node, nil
}

func addAt(node any, path []string, val any) (any, error) {
	if len(path) == 0 {
		return val, nil
	}
	tok := path[0]
	switch n := node.(type) {
	case map[string]any:
		if len(path) == 1 {
			n[tok] = val
			return n, nil
		}
		child, ok := n[tok]
		if !ok {
			return nil, fmt.Errorf("no member %q", tok)
		}
		nc, err := addAt(child, path[1:], val)
		if err != nil {
			return nil, err
		}
		n[tok] = nc
		return n, nil
	case []any:
		if len(path) == 1 {
			if tok == "-" {
				return append(n, val), nil
			}
			i, err := arrayIndex(tok, len(n), true)
			if err != nil {
				return nil, err
			}
			n = append(n, nil)
			copy(n[i+1:], n[i:])
			n[i] = val
			return n, nil
		}
		i, err := arrayIndex(tok, len(n), false)
		if err != nil {
			return nil, err
		}
		nc, err := addAt(n[i], path[1:], val)
		if err != nil {
			return nil, err
		}
		n[i] = nc
		return n, nil
	}
	return nil, fmt.Errorf("cannot step into a %s with %q", kindOf(node), tok)
}

func replaceAt(node any, path []string, val any) (any, error) {
	if len(path) == 0 {
		return val, nil
	}
	tok := path[0]
	switch n := node.(type) {
	case map[string]any:
		child, ok := n[tok]
		if !ok {
			return nil, fmt.Errorf("no member %q to replace", tok)
		}
		if len(path) == 1 {
			n[tok] = val
			return n, nil
		}
		nc, err := replaceAt(child, path[1:], val)
		if err != nil {
			return nil, err
		}
		n[tok] = nc
		return n, nil
	case []any:
		i, err := arrayIndex(tok, len(n), false)
		if err != nil {
			return nil, err
		}
		if len(path) == 1 {
			n[i] = val
			return n, nil
		}
		nc, err := replaceAt(n[i], path[1:], val)
		if err != nil {
			return nil, err
		}
		n[i] = nc
		return n, nil
	}
	return nil, fmt.Errorf("cannot step into a %s with %q", kindOf(node), tok)
}

// removeAt removes the value at path and returns the new node and the removed value.
func removeAt(node any, path []string) (out, removed any, err error) {
	if len(path) == 0 {
		return nil, nil, errors.New("cannot remove the whole document")
	}
	tok := path[0]
	switch n := node.(type) {
	case map[string]any:
		child, ok := n[tok]
		if !ok {
			return nil, nil, fmt.Errorf("no member %q to remove", tok)
		}
		if len(path) == 1 {
			delete(n, tok)
			return n, child, nil
		}
		nc, rem, err := removeAt(child, path[1:])
		if err != nil {
			return nil, nil, err
		}
		n[tok] = nc
		return n, rem, nil
	case []any:
		i, err := arrayIndex(tok, len(n), false)
		if err != nil {
			return nil, nil, err
		}
		if len(path) == 1 {
			rem := n[i]
			return append(n[:i], n[i+1:]...), rem, nil
		}
		nc, rem, err := removeAt(n[i], path[1:])
		if err != nil {
			return nil, nil, err
		}
		n[i] = nc
		return n, rem, nil
	}
	return nil, nil, fmt.Errorf("cannot step into a %s with %q", kindOf(node), tok)
}

func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

// clone deep-copies a generic JSON value.
func clone(v any) any {
	switch n := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(n))
		for k, c := range n {
			m[k] = clone(c)
		}
		return m
	case []any:
		s := make([]any, len(n))
		for i, c := range n {
			s[i] = clone(c)
		}
		return s
	}
	return v
}

// equalValues compares two generic JSON values. Numbers compare by value.
func equalValues(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !equalValues(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalValues(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		return ok && equalNumbers(x, y)
	}
	return a == b
}

func equalNumbers(a, b json.Number) bool {
	if a == b {
		return true
	}
	// Numbers whose text is long or whose exponent is huge are only equal when they are written the same way: big.Rat
	// would build a number with that many digits (1e999999) and take that long, and no spec has such numbers.
	if len(a) > 64 || len(b) > 64 || hugeExponent(a) || hugeExponent(b) {
		return false
	}
	x, ok1 := new(big.Rat).SetString(a.String())
	y, ok2 := new(big.Rat).SetString(b.String())
	return ok1 && ok2 && x.Cmp(y) == 0
}

// maxExponent is the largest exponent (either sign) for which numbers are compared by value.
const maxExponent = 400

// hugeExponent reports whether the number is written with an exponent beyond maxExponent (or one that does not fit an
// int64).
func hugeExponent(n json.Number) bool {
	s := string(n)
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return false
	}
	e, err := strconv.ParseInt(strings.TrimPrefix(s[i+1:], "+"), 10, 64)
	return err != nil || e > maxExponent || e < -maxExponent
}
