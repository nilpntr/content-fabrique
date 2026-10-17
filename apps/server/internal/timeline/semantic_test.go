package timeline

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The assets of the unit tests. They mirror fixtures/assets.json but are written out, so that these tests read on
// their own.
const (
	assetVideo60  = "ast_01j3er3kwcgrasdgd4aznmxxe0" // video, 60 s
	assetVideo30  = "ast_01jfbemsc6way7dnfebdf8tf3g" // video, 30 s
	assetLicensed = "ast_01j0h9rew727hk2pt0v5b31h44" // audio, 90 s, licensed
	assetAttested = "ast_01jwbx4exrs98bh4pyzkx8020y" // audio, 60 s, attested
	assetNoRights = "ast_01j5ntfevbyp8fy4vwn751jmjq" // audio, 90 s, no rights
	assetUnknown  = "ast_01jz6b8v83t2vr2qa03wpcnhnd" // not in the catalog
	licenseID     = "lic_01jagyb5y1g8z8tykznb1pntq6"
	trendID       = "trd_01jcmevy4tr3fffvjc82eh7rjm"
)

var unitCatalog = MemoryCatalog{
	assetVideo60:  {DurationMs: 60000, Kind: AssetVideo, Rights: RightsAttested},
	assetVideo30:  {DurationMs: 30000, Kind: AssetVideo, Rights: RightsAttested},
	assetLicensed: {DurationMs: 90000, Kind: AssetAudio, Rights: RightsLicensed},
	assetAttested: {DurationMs: 60000, Kind: AssetAudio, Rights: RightsAttested},
	assetNoRights: {DurationMs: 90000, Kind: AssetAudio, Rights: RightsNone},
}

// clipAt is a clip of asset60 from in to out at at_ms.
func clipAt(id string, in, out, at int64) VideoItem {
	return VideoItem{ID: id, AssetID: assetVideo60, Source: Source{InMs: in, OutMs: out}, AtMs: at}
}

// baseSpec is a valid 9:16 spec: one 5 s clip.
func baseSpec() *Spec {
	return &Spec{
		Schema:  SchemaVersion,
		Output:  Output{Format: Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks:  []Track{VideoTrack{ID: "main", Items: []VideoItem{clipAt("c1", 0, 5000, 0)}}},
		Markers: []Marker{},
	}
}

// withFormat switches the output to another format and its size.
func withFormat(s *Spec, f Format) *Spec {
	w, h, _ := f.Size()
	s.Output.Format, s.Output.Width, s.Output.Height = f, w, h
	return s
}

func withClips(s *Spec, clips ...VideoItem) *Spec {
	s.Tracks[0] = VideoTrack{ID: "main", Items: clips}
	return s
}

func addTrack(s *Spec, t Track) *Spec {
	s.Tracks = append(s.Tracks, t)
	return s
}

func textTrack(items ...TextItem) TextTrack { return TextTrack{ID: "text", Items: items} }

func anchored(id string, at, dur int64) TextItem {
	return TextItem{ID: id, AtMs: at, DurationMs: dur, Text: "Hello", Style: "title_bold", Anchor: AnchorCenter}
}

func audioTrack(items ...AudioItem) AudioTrack { return AudioTrack{ID: "sound", Items: items} }

func ptr[T any](v T) *T { return &v }

func trend(id string, at, offset int64, dur *int64) TrendSound {
	return TrendSound{
		AudioCommon: AudioCommon{ID: id, AtMs: at, SourceOffsetMs: offset, Export: ExportCueOnly, DurationMs: dur},
		TrendID:     trendID,
	}
}

func licensed(id, asset string, at, offset int64, dur *int64) LicensedTrack {
	return LicensedTrack{
		AudioCommon: AudioCommon{ID: id, AtMs: at, SourceOffsetMs: offset, Export: ExportLicensed, DurationMs: dur},
		AssetID:     asset, LicenseID: licenseID,
	}
}

func creator(id, asset string, at, offset int64, dur *int64) CreatorAudio {
	return CreatorAudio{
		AudioCommon: AudioCommon{ID: id, AtMs: at, SourceOffsetMs: offset, Export: ExportLicensed, DurationMs: dur},
		AssetID:     asset,
	}
}

func validateSpec(t *testing.T, s *Spec) Result {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return Validate(b, unitCatalog)
}

// wantCodes asserts the exact set of distinct codes of a result.
func wantCodes(t *testing.T, res Result, want ...Code) {
	t.Helper()
	got := res.Codes()
	slices.Sort(got)
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Fatalf("want codes %v, got %+v", w, res.Issues)
	}
}

func TestBaseSpecIsValid(t *testing.T) {
	wantCodes(t, validateSpec(t, baseSpec()))
}

func TestMainTrackBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		clips []VideoItem
		want  []Code
	}{
		{"total exactly 1000 ms", []VideoItem{clipAt("c1", 0, 1000, 0)}, nil},
		{"total 999 ms", []VideoItem{clipAt("c1", 0, 999, 0)}, []Code{CodeDurationOutOfRange}},
		{"total exactly 180000 ms", []VideoItem{clipAt("c1", 0, 60000, 0), clipAt("c2", 0, 60000, 60000), clipAt("c3", 0, 60000, 120000)}, nil},
		{"total 180300 ms", []VideoItem{clipAt("c1", 0, 60000, 0), clipAt("c2", 0, 60000, 60000), clipAt("c3", 0, 60000, 120000), clipAt("c4", 0, 300, 180000)}, []Code{CodeDurationOutOfRange}},
		{"shot exactly 300 ms", []VideoItem{clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 300, 2000)}, nil},
		{"shot 299 ms", []VideoItem{clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 299, 2000)}, []Code{CodeShotTooShort}},
		{"source out exactly the asset length", []VideoItem{clipAt("c1", 55000, 60000, 0)}, nil},
		{"source out one ms past the asset", []VideoItem{clipAt("c1", 55000, 60001, 0)}, []Code{CodeSourceOutOfRange}},
		{"source in equals out", []VideoItem{clipAt("c1", 1000, 1000, 0), clipAt("c2", 0, 2000, 0)}, []Code{CodeSourceOutOfRange}},
		{"source in above out", []VideoItem{clipAt("c1", 2000, 1000, 0), clipAt("c2", 0, 2000, 0)}, []Code{CodeSourceOutOfRange}},
		// A lone clip with in >= out raises source_out_of_range only: it is left out of the total-duration check too.
		{"only clip has in above out", []VideoItem{clipAt("c1", 2000, 1000, 0)}, []Code{CodeSourceOutOfRange}},
		{"first item not at 0", []VideoItem{clipAt("c1", 0, 2000, 1)}, []Code{CodeGap}},
		{"gap of 1 ms", []VideoItem{clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 2000, 2001)}, []Code{CodeGap}},
		{"overlap of 1 ms", []VideoItem{clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 2000, 1999)}, []Code{CodeOverlap}},
		{"items listed out of order are sorted by at_ms", []VideoItem{clipAt("c2", 0, 2000, 2000), clipAt("c1", 0, 2000, 0)}, nil},
		{"unknown asset", []VideoItem{{ID: "c1", AssetID: assetUnknown, Source: Source{OutMs: 2000}}}, []Code{CodeUnknownAsset}},
		{"unknown asset is not also out of range", []VideoItem{{ID: "c1", AssetID: assetUnknown, Source: Source{OutMs: 70000}}}, []Code{CodeUnknownAsset}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCodes(t, validateSpec(t, withClips(baseSpec(), tc.clips...)), tc.want...)
		})
	}
}

func TestTransitions(t *testing.T) {
	cross := func(ms int64) *Transition { return &Transition{Type: TransitionCrossfade, DurationMs: ms} }
	second := func(at int64, tr *Transition, dur int64) VideoItem {
		c := clipAt("c2", 0, dur, at)
		c.TransitionIn = tr
		return c
	}
	cases := []struct {
		name string
		c2   VideoItem
		want []Code
	}{
		{"crossfade starts where the previous ends minus the transition", second(1700, cross(300), 2000), nil},
		{"one ms late is a gap", second(1701, cross(300), 2000), []Code{CodeGap}},
		{"one ms early is an overlap", second(1699, cross(300), 2000), []Code{CodeOverlap}},
		{"a cut at the previous end", second(2000, &Transition{Type: TransitionCut}, 2000), nil},
		{"transition shorter than both clips", second(1601, cross(399), 400), nil},
		{"transition as long as the next clip", second(1600, cross(400), 400), []Code{CodeTransitionTooLong}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := withClips(baseSpec(), clipAt("c1", 0, 2000, 0), tc.c2)
			wantCodes(t, validateSpec(t, s), tc.want...)
		})
	}
	t.Run("transition as long as the previous clip", func(t *testing.T) {
		// c2 starts at 1000 - 1000 = 0, where c1 starts; the transition is not shorter than c1.
		s := withClips(baseSpec(), clipAt("c1", 0, 1000, 0), second(0, cross(1000), 3000))
		res := validateSpec(t, s)
		wantCodes(t, res, CodeTransitionTooLong)
		if got := res.Issues[0].Path; got != "/tracks/0/items/1/transition_in/duration_ms" {
			t.Fatalf("path %q", got)
		}
	})
}

func TestOutputDurationInValidation(t *testing.T) {
	// 600 ms of source at speed 2 plays for 300 ms (exactly the minimum); 598 ms plays for 299 ms.
	slow := func(out int64, speed float64) VideoItem {
		c := clipAt("c2", 0, out, 2000)
		c.Speed = &speed
		return c
	}
	wantCodes(t, validateSpec(t, withClips(baseSpec(), clipAt("c1", 0, 2000, 0), slow(600, 2))))
	wantCodes(t, validateSpec(t, withClips(baseSpec(), clipAt("c1", 0, 2000, 0), slow(598, 2))), CodeShotTooShort)
	// 3001 ms at speed 2 is 1500.5 ms, which rounds up to 1501: the next clip must start at 1501, not 1500.
	first := clipAt("c1", 0, 3001, 0)
	first.Speed = ptr(2.0)
	wantCodes(t, validateSpec(t, withClips(baseSpec(), first, clipAt("c2", 0, 2000, 1501))))
	wantCodes(t, validateSpec(t, withClips(baseSpec(), first, clipAt("c2", 0, 2000, 1500))), CodeOverlap)
}

func TestOutsideTimeline(t *testing.T) { // the timeline of baseSpec is 5000 ms
	cases := []struct {
		name string
		spec *Spec
		want []Code
	}{
		{"text ends exactly at the end", addTrack(baseSpec(), textTrack(anchored("t1", 4000, 1000))), nil},
		{"text ends 1 ms after the end", addTrack(baseSpec(), textTrack(anchored("t1", 4000, 1001))), []Code{CodeOutsideTimeline}},
		{"text starts at the end", addTrack(baseSpec(), textTrack(anchored("t1", 5000, 300))), []Code{CodeOutsideTimeline}},
		{"marker 1 ms before the end", func() *Spec { s := baseSpec(); s.Markers = []Marker{{AtMs: 4999, Kind: MarkerBeat}}; return s }(), nil},
		{"marker at the end", func() *Spec { s := baseSpec(); s.Markers = []Marker{{AtMs: 5000, Kind: MarkerBeat}}; return s }(), []Code{CodeOutsideTimeline}},
		{"audio ends exactly at the end", addTrack(baseSpec(), audioTrack(trend("s1", 1000, 0, ptr(int64(4000))))), nil},
		{"audio ends 1 ms after the end", addTrack(baseSpec(), audioTrack(trend("s1", 1000, 0, ptr(int64(4001))))), []Code{CodeOutsideTimeline}},
		{"audio starts at the end", addTrack(baseSpec(), audioTrack(trend("s1", 5000, 0, nil))), []Code{CodeOutsideTimeline}},
		{"audio with no duration plays to the end", addTrack(baseSpec(), audioTrack(trend("s1", 4999, 0, nil))), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { wantCodes(t, validateSpec(t, tc.spec), tc.want...) })
	}
}

func TestAudioChecks(t *testing.T) {
	cases := []struct {
		name string
		item AudioItem
		want []Code
	}{
		{"licensed track with a licence", licensed("s1", assetLicensed, 0, 0, nil), nil},
		{"licensed track on an asset without a licence record", licensed("s1", assetNoRights, 0, 0, nil), []Code{CodeUnlicensedAudio}},
		{"licensed track on an attested asset", licensed("s1", assetAttested, 0, 0, nil), []Code{CodeUnlicensedAudio}},
		{"licensed track on a video asset", licensed("s1", assetVideo60, 0, 0, nil), []Code{CodeUnlicensedAudio}},
		{"licensed track on an unknown asset", licensed("s1", assetUnknown, 0, 0, nil), []Code{CodeUnknownAsset}},
		{"creator audio with an attestation", creator("s1", assetAttested, 0, 0, nil), nil},
		{"creator audio without an attestation", creator("s1", assetNoRights, 0, 0, nil), []Code{CodeUnlicensedAudio}},
		{"creator audio on a licensed asset needs the attestation, not the licence", creator("s1", assetLicensed, 0, 0, nil), []Code{CodeUnlicensedAudio}},
		{"creator audio on an unknown asset", creator("s1", assetUnknown, 0, 0, nil), []Code{CodeUnknownAsset}},
		{"trend sound needs no licence and has no range", trend("s1", 0, 9999999, nil), nil},
		// asset60 attested is 60000 ms; the timeline is 5000 ms.
		{"source range ends exactly at the asset end", creator("s1", assetAttested, 0, 55000, nil), nil},
		{"source range ends 1 ms past the asset end", creator("s1", assetAttested, 0, 55001, nil), []Code{CodeSourceOutOfRange}},
		{"explicit duration, range exactly the asset", creator("s1", assetAttested, 0, 58000, ptr(int64(2000))), nil},
		{"explicit duration, range 1 ms past the asset", creator("s1", assetAttested, 0, 58001, ptr(int64(2000))), []Code{CodeSourceOutOfRange}},
		{"fades add up to the duration", func() AudioItem {
			a := trend("s1", 0, 0, ptr(int64(2000)))
			a.FadeInMs, a.FadeOutMs = ptr(int64(1000)), ptr(int64(1000))
			return a
		}(), nil},
		{"fades 1 ms longer than the duration", func() AudioItem {
			a := trend("s1", 0, 0, ptr(int64(2000)))
			a.FadeInMs, a.FadeOutMs = ptr(int64(1000)), ptr(int64(1001))
			return a
		}(), []Code{CodeFadeTooLong}},
		{"fades against the implied duration (to the end)", func() AudioItem {
			a := trend("s1", 4000, 0, nil) // plays 1000 ms
			a.FadeInMs, a.FadeOutMs = ptr(int64(600)), ptr(int64(401))
			return a
		}(), []Code{CodeFadeTooLong}},
		{"fade of zero", func() AudioItem {
			a := trend("s1", 0, 0, nil)
			a.FadeInMs, a.FadeOutMs = ptr(int64(0)), ptr(int64(0))
			return a
		}(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCodes(t, validateSpec(t, addTrack(baseSpec(), audioTrack(tc.item))), tc.want...)
		})
	}
	t.Run("source range uses the implied duration", func(t *testing.T) {
		// Starting at 1000 ms on a 5000 ms timeline plays 4000 ms; 56000 + 4000 = 60000 fits, 56001 does not.
		wantCodes(t, validateSpec(t, addTrack(baseSpec(), audioTrack(creator("s1", assetAttested, 1000, 56000, nil)))))
		wantCodes(t, validateSpec(t, addTrack(baseSpec(), audioTrack(creator("s1", assetAttested, 1000, 56001, nil)))), CodeSourceOutOfRange)
	})
}

func TestDuplicateIDs(t *testing.T) {
	t.Run("track id equals an item id", func(t *testing.T) {
		s := addTrack(baseSpec(), TextTrack{ID: "c1", Items: nil})
		wantCodes(t, validateSpec(t, s), CodeDuplicateID)
	})
	t.Run("three uses report two issues", func(t *testing.T) {
		s := withClips(baseSpec(), clipAt("c1", 0, 2000, 0), clipAt("c1", 0, 2000, 2000), clipAt("c1", 0, 2000, 4000))
		res := validateSpec(t, s)
		wantCodes(t, res, CodeDuplicateID)
		if len(res.Issues) != 2 {
			t.Fatalf("want 2 issues, got %+v", res.Issues)
		}
	})
	t.Run("audio and text share an id", func(t *testing.T) {
		s := addTrack(addTrack(baseSpec(), textTrack(anchored("x1", 0, 1000))), audioTrack(trend("x1", 0, 0, nil)))
		wantCodes(t, validateSpec(t, s), CodeDuplicateID)
	})
}

func TestUnknownStyle(t *testing.T) {
	s := addTrack(baseSpec(), textTrack(TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hi", Style: "comic_sans", Anchor: AnchorCenter}))
	res := validateSpec(t, s)
	wantCodes(t, res, CodeUnknownStyle)
	if !strings.Contains(res.Issues[0].Message, "title_bold") {
		t.Fatalf("the message should list the known styles: %s", res.Issues[0].Message)
	}
	if res.Issues[0].Path != "/tracks/1/items/0/style" {
		t.Fatalf("path %q", res.Issues[0].Path)
	}
	// An unknown style cannot be measured, so a position is not also flagged as unsafe.
	s = addTrack(baseSpec(), textTrack(TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: "Hi", Style: "comic_sans", Position: &Point{X: 0.5, Y: 0.01}}))
	wantCodes(t, validateSpec(t, s), CodeUnknownStyle)
}

func TestMessagesAreActionable(t *testing.T) {
	clip := VideoItem{ID: "c3", AssetID: assetVideo30, Source: Source{InMs: 28000, OutMs: 31000}, AtMs: 0}
	res := validateSpec(t, withClips(baseSpec(), clip))
	wantCodes(t, res, CodeSourceOutOfRange)
	want := fmt.Sprintf("item c3 ends at 31000 ms but asset %s is 30000 ms long", assetVideo30)
	if !strings.Contains(res.Issues[0].Message, want) {
		t.Fatalf("message %q lacks %q", res.Issues[0].Message, want)
	}
}

func TestIssuesAreCombined(t *testing.T) {
	// Independent semantic problems are all reported, not just the first.
	s := withClips(baseSpec(), clipAt("c1", 0, 2000, 0), clipAt("c2", 0, 2000, 2500)) // gap
	s = addTrack(s, textTrack(anchored("t1", 4000, 1000)))                            // outside (the timeline is 4500)
	wantCodes(t, validateSpec(t, s), CodeGap, CodeOutsideTimeline)
}

func TestStructureShortCircuitsSemantics(t *testing.T) {
	// A schema error and a gap: only the schema error is reported, because layer 2 only runs on a well-formed spec.
	doc := `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":31,"background":"#000000"},
	"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":2000},"at_ms":500}]}],"markers":[]}`
	res := Validate([]byte(doc), unitCatalog)
	wantCodes(t, res, CodeSchema)
	if len(res.Issues) != 1 || res.Issues[0].Path != "/output/fps" {
		t.Fatalf("got %+v", res.Issues)
	}
}

func TestNilCatalogKnowsNoAsset(t *testing.T) {
	b, _ := json.Marshal(baseSpec())
	wantCodes(t, Validate(b, nil), CodeUnknownAsset)
}

func TestJunkInput(t *testing.T) {
	deep := strings.Repeat("[", 100000)
	cases := map[string]string{
		"empty":             "",
		"whitespace":        "  \n",
		"null":              "null",
		"array":             "[]",
		"string":            `"x"`,
		"number":            "1",
		"empty object":      "{}",
		"trailing garbage":  `{} x`,
		"two documents":     `{} {}`,
		"truncated":         `{"schema":`,
		"deeply nested":     deep,
		"nul byte":          "{\"schema\":\"\x00\"}",
		"invalid utf-8":     "{\"schema\":\"\xff\xfe\"}",
		"huge number":       `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1e999999,"height":1920,"fps":30,"background":"#000000"},"tracks":[],"markers":[]}`,
		"track is a string": `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":["x"],"markers":[]}`,
		"track is null":     `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":[null],"markers":[]}`,
		"track type number": `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":[{"type":5}],"markers":[]}`,
		"audio kind null":   `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":[{"id":"a","type":"audio","items":[{"kind":null}]}],"markers":[]}`,
		"whole number as float": `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
			"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"` + assetVideo60 + `","source":{"in_ms":0,"out_ms":3000.0},"at_ms":0}]}],"markers":[]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			res := Validate([]byte(doc), unitCatalog)
			if res.OK() {
				t.Fatal("junk was accepted")
			}
			for _, i := range res.Issues {
				if i.Code != CodeSchema || i.Message == "" {
					t.Fatalf("bad issue %+v", i)
				}
			}
		})
	}
}

func TestSchemaIssueCapAndPaths(t *testing.T) {
	// Many problems at once: the answer is capped, and an escaped path is a valid JSON Pointer.
	var items []string
	for i := 0; i < 200; i++ {
		items = append(items, `{"bogus":1}`)
	}
	doc := `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},` +
		`"tracks":[{"id":"main","type":"video","items":[` + strings.Join(items, ",") + `]}],"markers":[]}`
	res := Validate([]byte(doc), unitCatalog)
	if len(res.Issues) == 0 || len(res.Issues) > maxSchemaIssues+1 {
		t.Fatalf("got %d issues", len(res.Issues))
	}
	if got := pointer([]string{"a/b", "c~d", "0"}); got != "/a~1b/c~0d/0" {
		t.Fatalf("pointer %q", got)
	}
}

func TestMarshalNormalisesNilSlices(t *testing.T) {
	b, err := json.Marshal(&Spec{Schema: SchemaVersion, Output: Output{Format: Format1x1, Width: 1080, Height: 1080, FPS: 30, Background: "#ffffff"},
		Tracks: []Track{VideoTrack{ID: "main"}, TextTrack{ID: "text"}, AudioTrack{ID: "sound"}, CaptionsTrack{ID: "caps", From: "transcript", Style: "chunk", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"markers":[]`, `"type":"video","id":"main","items":[]`, `"type":"text"`, `"type":"audio"`, `"type":"captions"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	if _, err := Parse(b); err != nil {
		t.Fatalf("what Marshal writes must Parse: %v", err)
	}
}
