package ops

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func opFixtureFiles(t *testing.T, sub string) []string {
	t.Helper()
	return fixtureFiles(t, filepath.Join("ops", sub))
}

// The embedded schema is the one in packages/schemas.
func TestEmbeddedSchemaIsTheSource(t *testing.T) {
	src := readFile(t, "../../../../../packages/schemas/timeline/ops.v1.schema.json")
	if !bytes.Equal(src, SchemaJSON()) {
		t.Fatal("internal/timeline/ops/schema/ops.v1.schema.json differs from packages/schemas/timeline/ops.v1.schema.json: run `go generate ./internal/timeline/ops`")
	}
}

func TestValidOpFixtures(t *testing.T) {
	seen := map[string]bool{}
	files := opFixtureFiles(t, "valid")
	if len(files) < 30 {
		t.Fatalf("want at least 30 valid op fixtures, have %d", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data := readFile(t, f)
			op, issues := ParseOp(data, 0)
			if len(issues) != 0 {
				t.Fatalf("issues: %+v", issues)
			}
			seen[op.OpName()] = true
			// Encoding gives the same JSON value back.
			again, err := json.Marshal(op)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, data, again) {
				t.Fatalf("round trip differs:\n in: %s\nout: %s", data, again)
			}
			// And it parses again to the same op.
			op2, issues := ParseOp(again, 0)
			if len(issues) != 0 {
				t.Fatalf("re-parse: %+v", issues)
			}
			if !bytes.Equal(mustJSON(t, op), mustJSON(t, op2)) {
				t.Fatal("re-parsed op differs")
			}
		})
	}
	for _, name := range Names {
		if !seen[name] {
			t.Errorf("no valid fixture for %s", name)
		}
	}
}

func TestInvalidOpFixtures(t *testing.T) {
	files := opFixtureFiles(t, "invalid")
	if len(files) < 30 {
		t.Fatalf("want at least 30 invalid op fixtures, have %d", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			op, issues := ParseOp(readFile(t, f), 3)
			if op != nil || len(issues) == 0 {
				t.Fatalf("accepted: %+v", op)
			}
			for _, is := range issues {
				if is.OpIndex != 3 || is.Message == "" || is.Severity != timeline.SeverityError {
					t.Fatalf("issue: %+v", is)
				}
			}
		})
	}
}

// sameJSON compares two JSON documents as values.
func sameJSON(t testing.TB, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(mustJSON(t, x), mustJSON(t, y))
}

func TestParseOpMessages(t *testing.T) {
	cases := []struct {
		name, js string
		code     timeline.Code
		field    string
		contains string
	}{
		{"unknown op", `{"op":"explode"}`, CodeUnknownOp, "/op", "add_clip"},
		{"no op", `{"id":"c1"}`, CodeInvalidOp, "/op", "no \"op\""},
		{"not an object", `[1]`, CodeInvalidOp, "", "JSON object"},
		{"unknown field", `{"op":"remove_item","id":"c1","why":"x"}`, CodeInvalidOp, "", "why"},
		{"key in another case", `{"op":"remove_item","ID":"c1"}`, CodeInvalidOp, "", "ID"},
		{"out of range", `{"op":"set_speed","id":"c1","speed":9}`, CodeInvalidOp, "/speed", "at most 4"},
		{"wrong type", `{"op":"set_speed","id":"c1","speed":"fast"}`, CodeInvalidOp, "/speed", "must be of type number"},
		{"float time", `{"op":"trim_item","id":"c1","out_ms":10.5}`, CodeInvalidOp, "/out_ms", "whole numbers"},
		{"nested", `{"op":"add_clip","asset_id":"` + vid60 + `","source":{"in_ms":-5,"out_ms":10}}`, CodeInvalidOp, "/source/in_ms", "at least 0"},
		{"bad id", `{"op":"remove_item","id":"C 1"}`, CodeInvalidOp, "/id", "does not match"},
		{"one-of hint", `{"op":"move_item","id":"c1"}`, CodeInvalidOp, "", "exactly one of at_ms"},
		{"anchor and position", `{"op":"add_text","at_ms":0,"duration_ms":500,"text":"x","style":"s","anchor":"center","position":{"x":0,"y":0}}`, CodeInvalidOp, "", "exactly one of anchor and position"},
		{"set_sound kind", `{"op":"set_sound","kind":"radio"}`, CodeInvalidOp, "/kind", "trend_sound"},
		{"set_sound missing field", `{"op":"set_sound","kind":"licensed_track","asset_id":"` + musicLicensed + `"}`, CodeInvalidOp, "", "license_id"},
		{"best grid branch", `{"op":"snap_cuts_to_beats","grid":{"bpm":10,"first_beat_ms":0}}`, CodeInvalidOp, "/grid/bpm", "at least 40"},
		{"not json", `{`, CodeInvalidOp, "", "not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, issues := ParseOp([]byte(tc.js), 2)
			if op != nil || len(issues) == 0 {
				t.Fatalf("accepted %s", tc.js)
			}
			var found bool
			for _, is := range issues {
				if is.Code == tc.code && is.Field == tc.field && strings.Contains(is.Message, tc.contains) && is.OpIndex == 2 {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s at %q mentioning %q, got %+v", tc.code, tc.field, tc.contains, issues)
			}
		})
	}
}

func TestParseBatch(t *testing.T) {
	ops, res := ParseBatch([]byte(`[{"op":"remove_item","id":"c1"},{"op":"set_look","preset":"bw"}]`))
	if !res.OK() || len(ops) != 2 || ops[0].OpName() != "remove_item" || ops[1].OpName() != "set_look" {
		t.Fatalf("%+v %+v", ops, res)
	}
	enc, err := MarshalBatch(ops)
	if err != nil || !sameJSON(t, enc, []byte(`[{"op":"remove_item","id":"c1"},{"op":"set_look","preset":"bw"}]`)) {
		t.Fatalf("%s %v", enc, err)
	}

	// Every invalid op is reported, with its index.
	ops, res = ParseBatch([]byte(`[{"op":"remove_item","id":"c1"},{"op":"nope"},{"op":"set_speed","id":"c1","speed":0}]`))
	if ops != nil || res.OK() {
		t.Fatal("accepted a bad batch")
	}
	idx := []int{}
	for _, is := range res.Errors() {
		idx = append(idx, is.OpIndex)
	}
	if !slices.Equal(idx, []int{1, 2}) {
		t.Fatalf("issues: %+v", res.Issues)
	}

	for name, js := range map[string]string{
		"empty":     `[]`,
		"too many":  "[" + strings.Repeat(`{"op":"set_look","preset":"bw"},`, MaxBatchOps) + `{"op":"set_look","preset":"bw"}]`,
		"not array": `{"op":"remove_item","id":"c1"}`,
		"trailing":  `[{"op":"remove_item","id":"c1"}] x`,
		"null":      `null`,
	} {
		if ops, res := ParseBatch([]byte(js)); ops != nil || res.OK() {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, res := ParseBatch([]byte("[" + strings.Repeat(`{"op":"set_look","preset":"bw"},`, MaxBatchOps-1) + `{"op":"set_look","preset":"bw"}]`)); !res.OK() {
		t.Fatalf("a batch of exactly %d ops must parse: %+v", MaxBatchOps, res.Issues)
	}
}

func TestMarshalEveryOp(t *testing.T) {
	// Each op writes its name first, and an empty op still has one.
	for _, name := range Names {
		op := newOp(name)
		data, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), `{"op":"`+name+`"`) {
			t.Errorf("%s: %s", name, data)
		}
		if op.OpName() != name {
			t.Errorf("%s: OpName %s", name, op.OpName())
		}
	}
	if data, _ := MarshalBatch(nil); string(data) != "[]" {
		t.Fatalf("nil batch: %s", data)
	}
}

// FuzzParseOp checks that ParseOp never panics, and that what it accepts encodes and parses again to the same op.
func FuzzParseOp(f *testing.F) {
	for _, sub := range []string{"valid", "invalid"} {
		for _, p := range fixtureFiles(f, filepath.Join("ops", sub)) {
			f.Add(readFile(f, p))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		op, issues := ParseOp(data, 0)
		if op == nil {
			if len(issues) == 0 {
				t.Fatal("rejected without an issue")
			}
			return
		}
		enc, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		again, issues := ParseOp(enc, 0)
		if again == nil {
			t.Fatalf("accepted %s, but not its encoding %s: %+v", data, enc, issues)
		}
		if enc2, _ := json.Marshal(again); !bytes.Equal(enc, enc2) {
			t.Fatalf("round trip differs: %s vs %s", enc, enc2)
		}
	})
}
