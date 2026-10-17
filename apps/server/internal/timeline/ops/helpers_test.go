package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fixturesDir is packages/schemas/timeline/fixtures, relative to this package (apps/server/internal/timeline/ops).
const fixturesDir = "../../../../../packages/schemas/timeline/fixtures"

// The assets of fixtures/assets.json.
const (
	vid60         = "ast_01j3er3kwcgrasdgd4aznmxxe0" // video, 60 s
	vid45         = "ast_01jex6xx15ayjx8epfhfjj9s11" // video, 45 s
	vid30         = "ast_01jfbemsc6way7dnfebdf8tf3g" // video, 30 s
	musicLicensed = "ast_01j0h9rew727hk2pt0v5b31h44" // audio, 90 s, licensed
	musicMine     = "ast_01jwbx4exrs98bh4pyzkx8020y" // audio, 60 s, attested
	trend         = "trd_01jcmevy4tr3fffvjc82eh7rjm"
	licence       = "lic_01jg1bfdc146rw2b0vngmx6p6f"
)

func fixtureFiles(t testing.TB, sub string) []string {
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

func fixtureCatalog(t testing.TB) timeline.MemoryCatalog {
	t.Helper()
	cat, err := timeline.LoadMemoryCatalog(filepath.Join(fixturesDir, "assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func parseSpec(t testing.TB, data []byte) timeline.Spec {
	t.Helper()
	s, err := timeline.Parse(data)
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return *s
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// batchOf parses a JSON array of ops and fails the test if any op is invalid.
func batchOf(t testing.TB, js string) []Op {
	t.Helper()
	batch, res := ParseBatch([]byte(js))
	if !res.OK() {
		t.Fatalf("parse batch %s: %+v", js, res.Issues)
	}
	return batch
}

// baseSpec is the timeline most unit tests start from: three clips of 3000, 4000 and 3000 ms (total 10 s), one text
// item and one trend sound.
func baseSpec(t testing.TB) timeline.Spec {
	t.Helper()
	return parseSpec(t, []byte(`{
  "schema": "contentfabrique.timeline/v1",
  "output": {"format": "9:16", "width": 1080, "height": 1920, "fps": 30, "background": "#000000"},
  "tracks": [
    {"id": "main", "type": "video", "items": [
      {"id": "c1", "asset_id": "`+vid60+`", "source": {"in_ms": 0, "out_ms": 3000}, "at_ms": 0},
      {"id": "c2", "asset_id": "`+vid45+`", "source": {"in_ms": 1000, "out_ms": 5000}, "at_ms": 3000},
      {"id": "c3", "asset_id": "`+vid30+`", "source": {"in_ms": 0, "out_ms": 3000}, "at_ms": 7000}
    ]},
    {"id": "text", "type": "text", "items": [
      {"id": "t1", "at_ms": 500, "duration_ms": 2000, "text": "Hello", "style": "title_bold", "anchor": "center"}
    ]},
    {"id": "sound", "type": "audio", "items": [
      {"id": "s1", "kind": "trend_sound", "trend_id": "`+trend+`", "at_ms": 0, "source_offset_ms": 0, "export": "cue_only"}
    ]}
  ],
  "markers": []
}`))
}

// apply runs a batch given as JSON on spec and returns the new spec and the result.
func apply(t testing.TB, spec timeline.Spec, js string) (timeline.Spec, Result) {
	t.Helper()
	return Apply(spec, batchOf(t, js), fixtureCatalog(t))
}

// mustApply is apply for a batch that has to work.
func mustApply(t testing.TB, spec timeline.Spec, js string) timeline.Spec {
	t.Helper()
	out, res := apply(t, spec, js)
	if !res.OK() {
		t.Fatalf("batch %s failed: %+v", js, res.Issues)
	}
	return out
}

// clips returns the main track items.
func clips(t testing.TB, spec timeline.Spec) []timeline.VideoItem {
	t.Helper()
	main, _, ok := timeline.MainTrack(&spec)
	if !ok {
		t.Fatal("no main track")
	}
	return main.Items
}

// clipTimes returns "id:at-end" for each clip, for compact assertions.
func clipTimes(t testing.TB, spec timeline.Spec) string {
	t.Helper()
	var parts []string
	for _, c := range clips(t, spec) {
		parts = append(parts, c.ID+":"+itoa(c.AtMs)+"-"+itoa(timeline.EndMs(c)))
	}
	return strings.Join(parts, " ")
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func textItems(spec timeline.Spec) []timeline.TextItem {
	for _, tr := range spec.Tracks {
		if tt, ok := tr.(*timeline.TextTrack); ok {
			return tt.Items
		}
	}
	return nil
}

func soundItems(spec timeline.Spec) []timeline.AudioItem {
	for _, tr := range spec.Tracks {
		if at, ok := tr.(*timeline.AudioTrack); ok {
			return at.Items
		}
	}
	return nil
}

// lastCaptions returns the captions track, which the tests expect to be the last track.
func lastCaptions(t testing.TB, spec timeline.Spec) *timeline.CaptionsTrack {
	t.Helper()
	ct, ok := spec.Tracks[len(spec.Tracks)-1].(*timeline.CaptionsTrack)
	if !ok {
		t.Fatalf("the last track is %T, not captions", spec.Tracks[len(spec.Tracks)-1])
	}
	return ct
}

// errorCodes returns the codes of the error issues.
func errorCodes(res Result) []string {
	var out []string
	for _, is := range res.Errors() {
		out = append(out, string(is.Code))
	}
	return out
}

// sameSpec reports whether two specs encode to the same JSON.
func sameSpec(t testing.TB, a, b timeline.Spec) bool {
	t.Helper()
	return string(mustJSON(t, a)) == string(mustJSON(t, b))
}
