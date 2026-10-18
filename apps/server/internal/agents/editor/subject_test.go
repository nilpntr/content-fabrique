package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// a person whose head starts at 22 % of the frame, in a 9:16 clip, for the asset with this key.
func headAt22For(key string) subject.Index {
	return subject.Index{key: {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}}}}
}

func headAt22() subject.Index { return headAt22For(assetA) }

func withTitle(t *testing.T, spec *timeline.Spec, mut func(*timeline.TextItem)) *timeline.Spec {
	t.Helper()
	for _, tr := range spec.Tracks {
		if tt, ok := tr.(*timeline.TextTrack); ok {
			mut(&tt.Items[0])
		}
	}
	return spec
}

func TestTextOverSubject(t *testing.T) {
	run := func(spec *timeline.Spec, ix subject.Index) Report {
		return RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t), Subjects: ix})
	}
	// One line at the top of the safe zone clears the head.
	r := run(buildingLats(t), headAt22())
	if c := wantStatus(t, r, CheckTextOverSubject, CheckPass); !strings.Contains(c.Detail, "none on the head") {
		t.Errorf("one line: %s", c.Detail)
	}
	// Two lines run into it: the renderer clears them (below the head), and the line says so with the numbers of the band.
	two := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK DAY\nBLUEPRINT" })
	c := wantStatus(t, run(two, headAt22()), CheckTextOverSubject, CheckPass)
	for _, want := range []string{"covers the head", "the renderer moves it", "the head starts at y 422 (22 % from the top)", "clear band above it is 182 px"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("moved: lacks %q: %s", want, c.Detail)
		}
	}
	// The same title with avoid_subject false is placed on the head: a failure with the fix.
	off := withTitle(t, buildingLats(t), func(it *timeline.TextItem) {
		it.Text = "BACK DAY\nBLUEPRINT"
		f := false
		it.AvoidSubject = &f
	})
	c = wantStatus(t, run(off, headAt22()), CheckTextOverSubject, CheckFail)
	if !strings.Contains(c.Detail, "set not to avoid it") || !strings.Contains(c.Detail, "one line") {
		t.Errorf("off: %s", c.Detail)
	}
	// A subject that fills the zone leaves nowhere to go: a failure that says to shrink or shorten.
	// two moments in the clip: the head high at one and lower at the other, so there is no clear band above it or below it for the whole title
	wall := subject.Index{assetA: {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 500, X: 0.2, Y: 0.1, W: 0.6, H: 0.8, HeadTopY: 0.12}, {AtMs: 1500, X: 0.2, Y: 0.45, W: 0.6, H: 0.5, HeadTopY: 0.5}}}}
	wide := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK DAY\nBLUEPRINT" })
	c = wantStatus(t, run(wide, wall), CheckTextOverSubject, CheckFail)
	if !strings.Contains(c.Detail, "cannot be moved off it") || !strings.Contains(c.Detail, "lower scale") {
		t.Errorf("wall: %s", c.Detail)
	}
	// No boxes at all: not applicable, and it says how to get them. A subject in another clip than the one the title is on: nothing to
	// check.
	wantStatus(t, run(buildingLats(t), nil), CheckTextOverSubject, CheckSkip)
	other := subject.Index{assetB: {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}}}}
	c = wantStatus(t, run(buildingLats(t), other), CheckTextOverSubject, CheckSkip)
	if !strings.Contains(c.Detail, "no text is on top of a clip with a subject box") {
		t.Errorf("other clip: %s", c.Detail)
	}
}

func TestTheFootageListSaysWhereTheHeadIs(t *testing.T) {
	id := newAssetID()
	rows := []store.ProjectFootageRow{{AssetID: id, Kind: store.AssetKindVideo}}
	ix := headAt22For(id.String())
	text := footageText(rows, footageOpts{Full: true, Subjects: ix})
	for _, want := range []string{"  subject: head top at about 22 % from the top (22 % at 1.0 s)", "check_edit says when a title covers the head"} {
		if !strings.Contains(text, want) {
			t.Errorf("the line lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(footageText(rows, footageOpts{Full: true}), "subject:") {
		t.Error("a subject line without a subject")
	}
}

// The style note names the creator's title format without quoting their words, and the block (untrusted data) holds the templates.
func TestTheStyleNoteNamesTheTitleTemplate(t *testing.T) {
	p := ginoProfile(t)
	note := styleNote(p)
	for _, want := range []string{"series format, <TOPIC> <number> in capitals (always ending in 101)", "do not invent a name", "Keep a title of up to 22 characters on one line"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "BUILDING") || strings.Contains(note, "BRACHIORADIALIS") {
		t.Errorf("the creator's words are in our note:\n%s", note)
	}
	data := styleBlocks(p)[1].Text
	if !strings.Contains(data, "title templates: a series title") || !strings.Contains(data, "'BUILDING LATS 101'") {
		t.Errorf("the block lacks the templates:\n%s", data)
	}
}

// A title that is not in a form the creator uses is borderline: the check passes and says so.
func TestAnOddTitleIsBorderlineNotAFailure(t *testing.T) {
	spec := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK BLUEPRINT" })
	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t)})
	c := wantStatus(t, r, CheckStyleMatch, CheckPass)
	if !strings.Contains(c.Detail, "borderline, not a failure: title_pattern") || !strings.Contains(c.Detail, "ending in 101") {
		t.Errorf("detail: %s", c.Detail)
	}
	if len(r.Failed()) != 0 {
		t.Errorf("failed: %v", r.Failed())
	}
}

// A title of a length that is one line for the creator is held to one line, including a line break of the editor's own.
func TestTheLinesAreEnforcedFromTheProfile(t *testing.T) {
	p := ginoProfile(t)
	for text, want := range map[string]string{
		"BACK DAY\nBLUEPRINT": "take the line break out",
		"BUILDING BACK 101":   "", // 17 characters at the style's size fits one line
	} {
		spec := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = text })
		r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p})
		if want == "" {
			wantStatus(t, r, CheckStyleMatch, CheckPass)
			continue
		}
		c := wantStatus(t, r, CheckStyleMatch, CheckFail)
		if !strings.Contains(c.Detail, "title_lines") || !strings.Contains(c.Detail, want) {
			t.Errorf("%q: %s", text, c.Detail)
		}
	}
	// A title that wraps at a big scale gets the scale that fits.
	spec := withTitle(t, buildingLats(t), func(it *timeline.TextItem) {
		it.Text = "BUILDING LATS 101 ROW"
		s := 1.4
		it.Scale = &s
	})
	c := wantStatus(t, RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: p}), CheckStyleMatch, CheckFail)
	if !strings.Contains(c.Detail, "title_lines") || !strings.Contains(c.Detail, "set the title's scale to") {
		t.Errorf("scale: %s", c.Detail)
	}
}

// The note does not tell the editor to follow a form the creator hardly uses.
func TestTheNoteFollowsOnlyARealTemplate(t *testing.T) {
	p := ginoProfile(t)
	p.Text.Patterns = patternsFor("BUILDING LATS 101", "how to bake a croissant tricep", "is this the best row?", "BACK DAY", "Heavy Lat Day")
	if note := styleNote(p); strings.Contains(note, "series format") {
		t.Errorf("a form at 20 %% in the note:\n%s", note)
	}
}

// five forms at 20 % each: none of them is the creator's template.
func patternsFor(_ ...string) []styleprofile.TitlePattern {
	var out []styleprofile.TitlePattern
	for _, k := range []string{styleprofile.KindSeries, styleprofile.KindLowercase, styleprofile.KindQuestion, styleprofile.KindCaps, styleprofile.KindOther} {
		out = append(out, styleprofile.TitlePattern{Kind: k, Share: 0.2})
	}
	return out
}
