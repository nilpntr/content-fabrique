package timeline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// fixturesDir is packages/schemas/timeline/fixtures, relative to this package (apps/server/internal/timeline).
const fixturesDir = "../../../../packages/schemas/timeline/fixtures"

func fixtureFiles(t *testing.T, sub string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixturesDir, sub, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s/%s: %v", fixturesDir, sub, err)
	}
	sort.Strings(files)
	return files
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixtures
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureCatalog(t *testing.T) MemoryCatalog {
	t.Helper()
	cat, err := LoadMemoryCatalog(filepath.Join(fixturesDir, "assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) == 0 {
		t.Fatal("assets.json holds no assets")
	}
	return cat
}

type expectation struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Keyword string `json:"keyword"`
}

func TestValidFixtures(t *testing.T) {
	cat := fixtureCatalog(t)
	files := fixtureFiles(t, "valid")
	if len(files) < 30 {
		t.Fatalf("want at least 30 valid fixtures, have %d", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			res := Validate(readFile(t, f), cat)
			if len(res.Issues) != 0 {
				t.Fatalf("want no issues, got %+v", res.Issues)
			}
			if !res.OK() {
				t.Fatal("OK() is false")
			}
		})
	}
}

func TestInvalidFixtures(t *testing.T) {
	cat := fixtureCatalog(t)
	var expect map[string]expectation
	if err := json.Unmarshal(readFile(t, filepath.Join(fixturesDir, "expect.json")), &expect); err != nil {
		t.Fatal(err)
	}
	files := fixtureFiles(t, "invalid")
	if len(files) < 30 {
		t.Fatalf("want at least 30 invalid fixtures, have %d", len(files))
	}
	if len(files) != len(expect) {
		t.Fatalf("%d invalid fixtures but %d entries in expect.json", len(files), len(expect))
	}
	codes := map[string]bool{}
	for _, f := range files {
		name := filepath.Base(f)
		want, ok := expect[name]
		if !ok {
			t.Fatalf("%s has no entry in expect.json", name)
		}
		codes[want.Code] = true
		t.Run(name, func(t *testing.T) {
			res := Validate(readFile(t, f), cat)
			if len(res.Issues) == 0 {
				t.Fatalf("want a %s issue, got none", want.Code)
			}
			// Exactly one code, the expected one.
			if got := res.Codes(); len(got) != 1 || string(got[0]) != want.Code {
				t.Fatalf("want exactly code %q, got %+v", want.Code, res.Issues)
			}
			// For schema issues the expected instance path must be reported. Several issues are allowed there only
			// if the path is among them; the fixtures are written so that it is one.
			if want.Path != "" {
				var paths []string
				for _, i := range res.Issues {
					paths = append(paths, i.Path)
				}
				if !slices.Contains(paths, want.Path) {
					t.Fatalf("want an issue at %q, got %+v", want.Path, res.Issues)
				}
			}
			for _, i := range res.Issues {
				if i.Severity != SeverityError || i.Message == "" {
					t.Fatalf("bad issue %+v", i)
				}
			}
			if res.OK() {
				t.Fatal("OK() is true")
			}
		})
	}
	// Every code of the closed list is exercised by a fixture.
	for _, code := range []Code{
		CodeSchema, CodeDuplicateID, CodeGap, CodeOverlap, CodeSourceOutOfRange, CodeShotTooShort,
		CodeDurationOutOfRange, CodeOutsideTimeline, CodeUnsafeText, CodeUnknownAsset, CodeTransitionTooLong,
		CodeUnlicensedAudio, CodeFadeTooLong, CodeUnknownStyle, CodeUnsupportedCharacter, CodeTextOverlapsCaptions,
	} {
		if !codes[string(code)] {
			t.Errorf("no invalid fixture expects %q", code)
		}
	}
}

// TestFixtureSchemaIssuesAreSingle checks the stronger property that makes the messages useful to an agent: one
// problem is one issue, not one per branch of a oneOf. The one exception is i36, whose second video track really has
// two problems (a second video track, and an id other than "main").
func TestFixtureSchemaIssuesAreSingle(t *testing.T) {
	cat := fixtureCatalog(t)
	var expect map[string]expectation
	if err := json.Unmarshal(readFile(t, filepath.Join(fixturesDir, "expect.json")), &expect); err != nil {
		t.Fatal(err)
	}
	for name, want := range expect {
		if want.Code != string(CodeSchema) {
			continue
		}
		res := Validate(readFile(t, filepath.Join(fixturesDir, "invalid", name)), cat)
		if strings.HasPrefix(name, "i36_") {
			if len(res.Issues) != 2 {
				t.Errorf("%s: want two issues, got %+v", name, res.Issues)
			}
			continue
		}
		if len(res.Issues) != 1 || res.Issues[0].Path != want.Path {
			t.Errorf("%s: want exactly one issue at %q, got %+v", name, want.Path, res.Issues)
		}
	}
}

// TestRoundTrip decodes every valid fixture into the Go types with unknown fields disallowed, encodes it again and
// compares the two documents semantically (as decoded JSON), so the types and the schema cannot drift apart.
func TestRoundTrip(t *testing.T) {
	for _, f := range fixtureFiles(t, "valid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw := readFile(t, f)
			spec, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			enc, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(enc, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip changed the spec\nwant %s\ngot  %s", compact(t, raw), enc)
			}
			// And the second generation is byte-identical to the first (stable encoding).
			spec2, err := Parse(enc)
			if err != nil {
				t.Fatal(err)
			}
			enc2, _ := json.Marshal(spec2)
			if !bytes.Equal(enc, enc2) {
				t.Fatal("encoding is not stable")
			}
		})
	}
}

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestParseIsStrict: the Go types reject what the schema rejects, so a spec cannot sneak a field through them.
func TestParseIsStrict(t *testing.T) {
	cases := map[string]string{
		"unknown top-level field": `{"schema":"x","output":{},"tracks":[],"markers":[],"extra":1}`,
		"unknown track type":      `{"tracks":[{"id":"main","type":"hologram","items":[]}]}`,
		"unknown audio kind":      `{"tracks":[{"id":"a","type":"audio","items":[{"kind":"jingle"}]}]}`,
		"unknown field in clip":   `{"tracks":[{"id":"main","type":"video","items":[{"id":"c1","bogus":1}]}]}`,
		"unknown field in audio":  `{"tracks":[{"id":"a","type":"audio","items":[{"kind":"trend_sound","bogus":1}]}]}`,
		"unknown field in track":  `{"tracks":[{"id":"a","type":"captions","bogus":1}]}`,
		"track field of another":  `{"tracks":[{"id":"a","type":"captions","items":[]}]}`,
		"trailing data":           `{"tracks":[]} {}`,
		"track not an object":     `{"tracks":["x"]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestRenderListIsSubsetOfValid(t *testing.T) {
	valid := map[string]bool{}
	for _, f := range fixtureFiles(t, "valid") {
		valid[filepath.Base(f)] = true
	}
	list := strings.Fields(string(readFile(t, filepath.Join(fixturesDir, "render.txt"))))
	if len(list) == 0 {
		t.Fatal("render.txt is empty")
	}
	for _, name := range list {
		if !valid[name] {
			t.Errorf("render.txt lists %s, which is not a valid fixture", name)
		}
	}
}
