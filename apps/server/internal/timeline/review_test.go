package timeline

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFirstTransitionInTimeOrder(t *testing.T) {
	cross := &Transition{Type: TransitionCrossfade, DurationMs: 300}
	// The array starts with a clip that has no transition (so the schema is content), but the clip that is first in
	// time, c2, has a crossfade.
	c1 := clipAt("c1", 0, 3000, 3000)
	c2 := clipAt("c2", 5000, 8000, 0)
	c2.TransitionIn = cross
	res := validateSpec(t, withClips(baseSpec(), c1, c2))
	wantCodes(t, res, CodeFirstTransition)
	if len(res.Issues) != 1 || res.Issues[0].Path != "/tracks/0/items/1/transition_in" {
		t.Fatalf("got %+v", res.Issues)
	}
	// A cut, or none, on the first clip in time is fine, in any array order.
	c2.TransitionIn = &Transition{Type: TransitionCut}
	wantCodes(t, validateSpec(t, withClips(baseSpec(), c1, c2)))
	// The transition of a later clip is not a first transition.
	c3 := clipAt("c3", 0, 3000, 2700)
	c3.TransitionIn = cross
	wantCodes(t, validateSpec(t, withClips(baseSpec(), clipAt("c1", 0, 3000, 0), c3)))
	// Nor is the one on a clip that is first in the array but not first in time.
	first := clipAt("c1", 0, 3000, 2700)
	first.TransitionIn = &Transition{Type: TransitionCut}
	wantCodes(t, validateSpec(t, withClips(baseSpec(), first, clipAt("c2", 0, 2700, 0))))
}

func TestWrongAssetKind(t *testing.T) {
	onAudio := VideoItem{ID: "c1", AssetID: assetLicensed, Source: Source{OutMs: 3000}}
	res := validateSpec(t, withClips(baseSpec(), onAudio))
	wantCodes(t, res, CodeWrongAssetKind)
	if res.Issues[0].Path != "/tracks/0/items/0/asset_id" || !strings.Contains(res.Issues[0].Message, "audio asset") {
		t.Fatalf("got %+v", res.Issues)
	}
	// An unknown asset is unknown_asset, not a wrong kind.
	wantCodes(t, validateSpec(t, withClips(baseSpec(), VideoItem{ID: "c1", AssetID: assetUnknown, Source: Source{OutMs: 3000}})), CodeUnknownAsset)
	// A wrong-kind audio asset raises this code only, not also the rights code.
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), audioTrack(licensed("s1", assetVideo60, 0, 0, nil)))), CodeWrongAssetKind)
	// The sound of one of the creator's own videos may be the music (creator_audio, D-34).
	wantCodes(t, validateSpec(t, addTrack(baseSpec(), audioTrack(creator("s1", assetVideo30, 0, 0, nil)))))
}

func TestUnknownStyleIsAnError(t *testing.T) {
	s := addTrack(baseSpec(), textTrack(TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hi", Style: "comic_sans", Anchor: AnchorCenter}))
	res := validateSpec(t, s)
	wantCodes(t, res, CodeUnknownStyle)
	if res.Issues[0].Severity != SeverityError {
		t.Fatalf("severity %q", res.Issues[0].Severity)
	}
	if res.OK() || len(res.Errors()) != 1 {
		t.Fatal("an unknown style must block a version (P3-04 made it an error)")
	}
	// A second error next to it is reported as well.
	s = addTrack(s, audioTrack(creator("s1", assetNoRights, 0, 0, nil)))
	res = validateSpec(t, s)
	if res.OK() || len(res.Errors()) != 2 {
		t.Fatalf("got %+v", res.Issues)
	}
}

// keyCase rewrites the first occurrence of a JSON key in a valid fixture to another case.
func keyCase(t *testing.T, fixture, from, to string) []byte {
	t.Helper()
	raw := string(readFile(t, filepath.Join(fixturesDir, "valid", fixture)))
	needle := `"` + from + `":`
	if !strings.Contains(raw, needle) {
		t.Fatalf("%s has no key %q", fixture, from)
	}
	return []byte(strings.Replace(raw, needle, `"`+to+`":`, 1))
}

func TestDecodingIsCaseSensitive(t *testing.T) {
	cases := []struct {
		name, fixture, from, to string
		path                    string
	}{
		{"clip id", "v30_full_edit.json", "id", "ID", "/tracks/0/items/0/ID"},
		{"clip at_ms", "v30_full_edit.json", "at_ms", "At_Ms", "/tracks/0/items/0/At_Ms"},
		{"top-level schema", "v01_minimal_9x16.json", "schema", "Schema", "/Schema"},
		{"top-level tracks", "v01_minimal_9x16.json", "tracks", "TRACKS", "/TRACKS"},
		{"output fps", "v01_minimal_9x16.json", "fps", "FPS", "/output/FPS"},
		{"track type", "v30_full_edit.json", "type", "TYPE", "/tracks/0/TYPE"},
		{"audio kind", "v25_audio_trend_sound_cue_only.json", "kind", "Kind", "/tracks/1/items/0/Kind"},
		{"audio common field", "v25_audio_trend_sound_cue_only.json", "export", "Export", "/tracks/1/items/0/Export"},
		{"audio variant field", "v25_audio_trend_sound_cue_only.json", "trend_id", "Trend_ID", "/tracks/1/items/0/Trend_ID"},
		{"nested source", "v01_minimal_9x16.json", "in_ms", "In_Ms", "/tracks/0/items/0/source/In_Ms"},
		{"marker kind", "v29_markers_all_kinds.json", "strength", "Strength", "/markers/1/Strength"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := keyCase(t, tc.fixture, tc.from, tc.to)
			_, err := Parse(doc)
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("want a DecodeError, got %v", err)
			}
			if de.Path != tc.path && !strings.HasSuffix(de.Path, "/"+tc.to) {
				t.Fatalf("path %q, want %q", de.Path, tc.path)
			}
			if !strings.Contains(de.Message, "case-sensitive") || !strings.Contains(de.Message, `"`+tc.from+`"`) {
				t.Fatalf("message %q", de.Message)
			}
			// The schema rejects the same document (it is closed and case-sensitive), so Validate says so too.
			res := Validate(doc, fixtureCatalog(t))
			if res.OK() {
				t.Fatal("Validate accepted a key in the wrong case")
			}
		})
	}
}

func TestNumbersWrittenAsFloatsGetAPath(t *testing.T) {
	doc := func(outMs, fps string) []byte {
		return []byte(`{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":` + fps + `,"background":"#000000"},
		"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":` + outMs + `},"at_ms":0}]}],"markers":[]}`)
	}
	cases := []struct {
		name, outMs, fps, path, hint string
	}{
		{"decimal point", "3000.0", "30", "/tracks/0/items/0/source/out_ms", "write it as 3000"},
		{"exponent", "3e3", "30", "/tracks/0/items/0/source/out_ms", "write it as 3000"},
		{"fps with decimal point", "3000", "30.0", "/output/fps", "write it as 30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Validate(doc(tc.outMs, tc.fps), unitCatalog)
			wantCodes(t, res, CodeSchema)
			if len(res.Issues) != 1 || res.Issues[0].Path != tc.path || !strings.Contains(res.Issues[0].Message, tc.hint) {
				t.Fatalf("got %+v", res.Issues)
			}
			if strings.Contains(res.Issues[0].Message, "could not be read") {
				t.Fatal("the message should not be the generic one")
			}
			if _, err := Parse(doc(tc.outMs, tc.fps)); err == nil {
				t.Fatal("Parse accepted it")
			}
		})
	}
	// A float where the type is a float is fine.
	if _, err := Parse([]byte(strings.Replace(string(doc("3000", "30")), `"at_ms":0`, `"at_ms":0,"speed":2.0`, 1))); err != nil {
		t.Fatalf("speed 2.0: %v", err)
	}
}

func TestUnionFieldsBelongToTheirVariant(t *testing.T) {
	cases := map[string]string{
		"captions field on a video track": `{"tracks":[{"id":"main","type":"video","items":[],"enabled":true}]}`,
		"items on a captions track":       `{"tracks":[{"id":"c","type":"captions","from":"transcript","style":"chunk","enabled":true,"items":[]}]}`,
		"asset_id on a trend sound":       `{"tracks":[{"id":"a","type":"audio","items":[{"id":"s1","kind":"trend_sound","asset_id":"x"}]}]}`,
		"trend_id on a licensed track":    `{"tracks":[{"id":"a","type":"audio","items":[{"id":"s1","kind":"licensed_track","trend_id":"x"}]}]}`,
		"license_id on creator audio":     `{"tracks":[{"id":"a","type":"audio","items":[{"id":"s1","kind":"creator_audio","license_id":"x"}]}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// Track and AudioItem are satisfied by the pointer types only, so a value cannot be put in Spec.Tracks and a type
// switch on the pointer types sees every track.
func TestTrackInterfacesTakePointersOnly(t *testing.T) {
	for _, v := range []any{VideoTrack{}, TextTrack{}, CaptionsTrack{}, AudioTrack{}} {
		if reflect.TypeOf(v).Implements(trackIface) {
			t.Errorf("%T (a value) satisfies Track", v)
		}
		if !reflect.PointerTo(reflect.TypeOf(v)).Implements(trackIface) {
			t.Errorf("*%T does not satisfy Track", v)
		}
	}
	for _, v := range []any{TrendSound{}, LicensedTrack{}, CreatorAudio{}} {
		if reflect.TypeOf(v).Implements(audioIface) {
			t.Errorf("%T (a value) satisfies AudioItem", v)
		}
		if !reflect.PointerTo(reflect.TypeOf(v)).Implements(audioIface) {
			t.Errorf("*%T does not satisfy AudioItem", v)
		}
	}
	// Decoding hands out pointers, and MainTrack finds the main track among them.
	spec, err := Parse(readFile(t, filepath.Join(fixturesDir, "valid", "v30_full_edit.json")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range spec.Tracks {
		if reflect.TypeOf(tr).Kind() != reflect.Pointer {
			t.Errorf("%T is not a pointer", tr)
		}
	}
	if main, idx, ok := MainTrack(spec); !ok || idx != 0 || main.ID != MainTrackID {
		t.Fatalf("MainTrack: %v %d %v", main, idx, ok)
	}
	if _, _, ok := MainTrack(&Spec{}); ok {
		t.Fatal("no main track expected")
	}
}

// countingCatalog counts the lookups.
type countingCatalog struct {
	MemoryCatalog
	lookups map[string]int
}

func (c *countingCatalog) Lookup(id string) (Asset, bool) {
	c.lookups[id]++
	return c.MemoryCatalog.Lookup(id)
}

func TestOneLookupPerItem(t *testing.T) {
	cat := &countingCatalog{MemoryCatalog: unitCatalog, lookups: map[string]int{}}
	s := withClips(baseSpec(), clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 3000, 2000))
	s = addTrack(s, audioTrack(creator("s1", assetAttested, 0, 0, nil), licensed("s2", assetLicensed, 0, 0, nil), trend("s3", 0, 0, nil)))
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if res := Validate(b, cat); !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	// Two clips of one asset, one creator track, one licensed track; the trend sound has no asset.
	want := map[string]int{assetVideo60: 2, assetAttested: 1, assetLicensed: 1}
	if !reflect.DeepEqual(cat.lookups, want) {
		t.Fatalf("lookups %v, want %v", cat.lookups, want)
	}
}

func TestPlayableEndIsTheTimelineEnd(t *testing.T) {
	items := []VideoItem{clipAt("c1", 0, 2000, 0), clipAt("c2", 4000, 1000, 2000), clipAt("c3", 0, 1000, 2000)}
	if got := PlayableEnd(items); got != 3000 { // c2 has in > out and does not count
		t.Fatalf("got %d", got)
	}
	if PlayableEnd(nil) != 0 {
		t.Fatal("empty")
	}
	if !Playable(items[0]) || Playable(items[1]) || EndMs(items[2]) != 3000 {
		t.Fatal("Playable or EndMs")
	}
	// Validate and TotalDuration agree: the message of a too-long timeline quotes the shared total.
	s := withClips(baseSpec(), items...)
	if TotalDuration(s) != 3000 {
		t.Fatalf("TotalDuration %d", TotalDuration(s))
	}
}
