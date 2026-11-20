package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// speaking is a clip of asset60 from in to out at at_ms with its sound in full mode (speech).
func speaking(id string, in, out, at int64) VideoItem {
	c := clipAt(id, in, out, at)
	c.Audio = &VideoAudio{Mode: AudioModeFull}
	return c
}

func cutaway(id, asset string, in, out, at int64) BrollItem {
	return BrollItem{ID: id, AssetID: asset, Source: Source{InMs: in, OutMs: out}, AtMs: at}
}

// talking is a valid spec of 6 s of speech (two clips of asset60) with the given cutaways.
func talking(cuts ...BrollItem) *Spec {
	s := withClips(baseSpec(), speaking("c1", 0, 3000, 0), speaking("c2", 10000, 13000, 3000))
	return addTrack(s, &BrollTrack{ID: BrollTrackID, Items: cuts})
}

func TestBrollTrackIsValid(t *testing.T) {
	res := validateSpec(t, talking(cutaway("b1", assetVideo30, 0, 1200, 2400), cutaway("b2", assetVideo30, 5000, 5600, 4800)))
	wantCodes(t, res)
	// An empty track is allowed (an op that removed the last cutaway leaves it).
	wantCodes(t, validateSpec(t, talking()))
}

func TestBrollRules(t *testing.T) {
	cases := []struct {
		name string
		cuts []BrollItem
		want Code
		path string
	}{
		{"past the end", []BrollItem{cutaway("b1", assetVideo30, 0, 1000, 5001)}, CodeOutsideTimeline, "/tracks/1/items/0"},
		{"in not below out", []BrollItem{cutaway("b1", assetVideo30, 1000, 1000, 500)}, CodeSourceOutOfRange, "/tracks/1/items/0/source"},
		{"past the asset", []BrollItem{cutaway("b1", assetVideo30, 29900, 30200, 500)}, CodeSourceOutOfRange, "/tracks/1/items/0/source/out_ms"},
		{"unknown asset", []BrollItem{cutaway("b1", assetUnknown, 0, 1000, 500)}, CodeUnknownAsset, "/tracks/1/items/0/asset_id"},
		{"audio asset", []BrollItem{cutaway("b1", assetLicensed, 0, 1000, 500)}, CodeWrongAssetKind, "/tracks/1/items/0/asset_id"},
		{"8 frames", []BrollItem{cutaway("b1", assetVideo30, 0, 280, 500)}, CodeShotTooShort, "/tracks/1/items/0"},
		{"share a frame", []BrollItem{cutaway("b1", assetVideo30, 0, 1000, 500), cutaway("b2", assetVideo30, 2000, 3000, 1480)}, CodeOverlap, "/tracks/1/items/1/at_ms"},
		{"inside another", []BrollItem{cutaway("b1", assetVideo30, 0, 3000, 500), cutaway("b2", assetVideo30, 4000, 4500, 1000), cutaway("b3", assetVideo30, 6000, 6500, 2000)}, CodeOverlap, "/tracks/1/items/1/at_ms"},
		{"shown on main", []BrollItem{cutaway("b1", assetVideo60, 12900, 13500, 500)}, CodeSourceReused, "/tracks/1/items/0/source"},
		{"shown by another cutaway", []BrollItem{cutaway("b1", assetVideo30, 0, 1000, 500), cutaway("b2", assetVideo30, 999, 1600, 3000)}, CodeSourceReused, "/tracks/1/items/1/source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := validateSpec(t, talking(tc.cuts...))
			wantCodes(t, res, tc.want)
			if res.Issues[0].Path != tc.path {
				t.Fatalf("want the issue at %s, got %+v", tc.path, res.Issues)
			}
			t.Log(res.Issues[0].Message)
		})
	}
}

// TestBrollBoundaries pins the edges: a cutaway may end exactly at the end of the timeline, touch the next one, show the frame range
// right after a clip of the same asset, and be exactly 300 ms long where that is a whole number of frames.
func TestBrollBoundaries(t *testing.T) {
	wantCodes(t, validateSpec(t, talking(
		cutaway("b1", assetVideo30, 0, 300, 0),
		cutaway("b2", assetVideo30, 1000, 2000, 300),
		cutaway("b3", assetVideo60, 3000, 4000, 2300), // c1 shows 0 to 3000: the range after it is free
		cutaway("b4", assetVideo30, 3000, 4000, 5000), // ends at 6000, the end of the timeline
	)))
	// At 24 fps 300 ms is 7.2 frames: the cutaway is on screen for 7 (292 ms) and too short; 334 ms is 8 frames.
	s := talking(cutaway("b1", assetVideo30, 0, 300, 500))
	s.Output.FPS = 24
	wantCodes(t, validateSpec(t, s), CodeShotTooShort)
	s = talking(cutaway("b1", assetVideo30, 0, 334, 500))
	s.Output.FPS = 24
	wantCodes(t, validateSpec(t, s))
}

func TestTooManyCutaways(t *testing.T) {
	var cuts []BrollItem
	for k := range MaxCutaways {
		cuts = append(cuts, cutaway("b"+string(rune('a'+k)), assetVideo30, int64(k)*1000, int64(k)*1000+400, int64(k)*450))
	}
	wantCodes(t, validateSpec(t, talking(cuts...)))
	cuts = append(cuts, cutaway("bz", assetVideo30, 20000, 20300, 5600))
	res := validateSpec(t, talking(cuts...))
	wantCodes(t, res, CodeTooManyCutaways)
	if res.Issues[0].Path != "/tracks/1/items" {
		t.Fatalf("want the issue on the items, got %+v", res.Issues)
	}
}

// TestCutawayOverSilence: a cutaway with no clip in full mode under it is a warning, never an error; one over a clip that speaks for any
// part of it is fine.
func TestCutawayOverSilence(t *testing.T) {
	s := withClips(baseSpec(), speaking("c1", 0, 3000, 0), clipAt("c2", 10000, 13000, 3000))
	s = addTrack(s, &BrollTrack{ID: BrollTrackID, Items: []BrollItem{
		cutaway("b1", assetVideo30, 0, 1000, 3500),    // over c2 only, which is muted
		cutaway("b2", assetVideo30, 2000, 3000, 2500), // over c1's speech, then c2
	}})
	res := validateSpec(t, s)
	wantCodes(t, res, CodeCutawayOverSilence)
	if !res.OK() || len(res.Issues) != 1 || res.Issues[0].Severity != SeverityWarning || res.Issues[0].Path != "/tracks/1/items/0" {
		t.Fatalf("want one warning on b1, got %+v", res.Issues)
	}
	// A bed is not speech either; use_source true is full.
	bed := clipAt("c2", 10000, 13000, 3000)
	bed.Audio = &VideoAudio{Mode: AudioModeBed}
	wantCodes(t, validateSpec(t, addTrack(withClips(baseSpec(), speaking("c1", 0, 3000, 0), bed), &BrollTrack{ID: BrollTrackID, Items: []BrollItem{cutaway("b1", assetVideo30, 0, 1000, 3500)}})), CodeCutawayOverSilence)
	on := clipAt("c2", 10000, 13000, 3000)
	on.Audio = &VideoAudio{UseSource: ptr(true)}
	wantCodes(t, validateSpec(t, addTrack(withClips(baseSpec(), speaking("c1", 0, 3000, 0), on), &BrollTrack{ID: BrollTrackID, Items: []BrollItem{cutaway("b1", assetVideo30, 0, 1000, 3500)}})))
}

// TestBrollIdsAreUnique: a cutaway's id is unique across the whole spec, like every other id.
func TestBrollIdsAreUnique(t *testing.T) {
	wantCodes(t, validateSpec(t, talking(cutaway("c1", assetVideo30, 0, 1000, 500))), CodeDuplicateID)
}

// TestBrollRefusesClipFields: a cutaway has no sound, speed, ramp, motion or transition of its own; the schema refuses each, at the item.
func TestBrollRefusesClipFields(t *testing.T) {
	for _, field := range []string{`"audio":{"mode":"full"}`, `"speed":2`, `"speed_ramp":[{"at_ms":0,"speed":1},{"at_ms":500,"speed":2}]`, `"motion":{"preset":"push_in"}`, `"transition_in":{"type":"cut"}`, `"moment_id":"mom_01jcmevy4tr3fffvjc82eh7rjm"`, `"exposure_ev":0.3`} {
		b, err := json.Marshal(talking(cutaway("b1", assetVideo30, 0, 1000, 500)))
		if err != nil {
			t.Fatal(err)
		}
		doc := strings.Replace(string(b), `"at_ms":500`, `"at_ms":500,`+field, 1)
		res := Validate([]byte(doc), unitCatalog)
		if got := res.Codes(); len(got) != 1 || got[0] != CodeSchema || res.Issues[0].Path != "/tracks/1/items/0" {
			t.Fatalf("%s: want one schema issue at the cutaway, got %+v", field, res.Issues)
		}
	}
	// The track's id is always broll.
	b, _ := json.Marshal(talking())
	doc := strings.Replace(string(b), `"id":"broll"`, `"id":"cutaways"`, 1)
	if res := Validate([]byte(doc), unitCatalog); res.OK() {
		t.Fatal("a b-roll track with another id was accepted")
	}
}

// TestBrollRoundTrip: a cutaway survives decoding and encoding, its optional fields left out or kept as they were.
func TestBrollRoundTrip(t *testing.T) {
	in := cutaway("b1", assetVideo30, 0, 1000, 500)
	in.Reframe = &Reframe{Mode: ReframeCover, Zoom: ptr(1.2)}
	in.Look = &Look{Preset: "warm"}
	in.Why = ptr("")
	b, err := json.Marshal(talking(in, cutaway("b2", assetVideo30, 2000, 2500, 3000)))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	got := Cutaways(spec)
	if len(got) != 2 || got[0].Why == nil || *got[0].Why != "" || got[0].Look.Preset != "warm" || got[1].Reframe != nil || got[0].EndMs() != 1500 {
		t.Fatalf("round trip: %+v", got)
	}
	if v := got[0].AsVideoItem(); v.ID != "b1" || v.Source != in.Source || v.AtMs != 500 || v.Reframe != got[0].Reframe || v.Speed != nil || v.Audio != nil {
		t.Fatalf("AsVideoItem: %+v", v)
	}
}

// TestBrollRulesNeedTheTrack is the back-compat promise: every rule fires only when a b-roll track exists. A spec that reuses a source
// range on main is still valid (only cutaways are held to "each shot once"), and every stored fixture without the track validates
// exactly as before (TestValidFixtures and TestInvalidFixtures hold the rest).
func TestBrollRulesNeedTheTrack(t *testing.T) {
	wantCodes(t, validateSpec(t, withClips(baseSpec(), speaking("c1", 0, 3000, 0), speaking("c2", 1000, 4000, 3000))))
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // test fixtures
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"broll"`) {
			continue
		}
		for _, i := range Validate(b, fixtureCatalog(t)).Issues {
			switch i.Code {
			case CodeSourceReused, CodeTooManyCutaways, CodeCutawayOverSilence:
				t.Errorf("%s: a b-roll code without a b-roll track: %+v", filepath.Base(f), i)
			}
		}
	}
}
