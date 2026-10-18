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
	if c := wantStatus(t, r, CheckTextOverSubject, CheckPass); !strings.Contains(c.Detail, "none on the person") {
		t.Errorf("one line: %s", c.Detail)
	}
	// Two lines run into it: the renderer clears them (above the head, smaller), and the line says so with the numbers of the band.
	two := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK DAY\nBLUEPRINT" })
	c := wantStatus(t, run(two, headAt22()), CheckTextOverSubject, CheckPass)
	for _, want := range []string{"covers the subject", "the renderer moves it above the head at scale", "the head starts at y 422 (22 % from the top)", "clear band above it is 182 px"} {
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
	if !strings.Contains(c.Detail, "set not to avoid them") || !strings.Contains(c.Detail, "remove avoid_subject false") {
		t.Errorf("off: %s", c.Detail)
	}
	// Two moments in the clip: the head high at one and lower at the other, so there is no clear band for the whole title; the second half
	// of its time has room, so the renderer shows it then, and the check says so.
	// (At the first moment the arms are raised above the head, so there is no room beside it either.)
	wall := subject.Index{assetA: {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 500, X: 0.2, Y: 0.05, W: 0.6, H: 0.85, HeadTopY: 0.12}, {AtMs: 1500, X: 0.2, Y: 0.45, W: 0.6, H: 0.5, HeadTopY: 0.5}}}}
	wide := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK DAY\nBLUEPRINT" })
	c = wantStatus(t, run(wide, wall), CheckTextOverSubject, CheckPass)
	if !strings.Contains(c.Detail, "fits clear of the person's body only at 1000-2000 ms") || !strings.Contains(c.Detail, "scale down to 0.6") {
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

// The body is protected like the head (P6-06): a title with no room clear of the body at its own time is moved by the renderer, and the
// check fails for a hook or chapter title that ends up elsewhere, notes it for any other text, and fails any text set not to avoid the
// person that covers their torso.
func TestTextOverTheBodyNeverStays(t *testing.T) {
	// The head starts at 14.6 % (280 px): 40 px of band above it, too little for the title at any legible size, the person is wide and
	// the arms are raised above the head, so there is no background beside it either.
	track := subject.Index{assetA: {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.05, Y: 0.08, W: 0.9, H: 0.91, HeadTopY: 0.146}}}}
	run := func(spec *timeline.Spec) Report {
		return RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t), Subjects: track})
	}
	// The hook title: the renderer shows it on the last shot (another clip, with nobody in its box); that is not a hook any more.
	c := wantStatus(t, run(buildingLats(t)), CheckTextOverSubject, CheckFail)
	for _, want := range []string{"has no room clear of the person's body at 0-2000 ms", "the renderer shows it at", "the head starts at y 280", "tried: above the head",
		"beside it", "smallest legible scale"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("the hook: lacks %q: %s", want, c.Detail)
		}
	}
	// A later label in the same place is a note: the renderer moves it.
	later := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.AtMs, it.Text = 2500, "ROWS" })
	c = wantStatus(t, run(later), CheckTextOverSubject, CheckPass)
	if !strings.Contains(c.Detail, "the renderer shows it at") {
		t.Errorf("a label: %s", c.Detail)
	}
	// A chapter title is essential wherever it is.
	chapter := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.AtMs, it.Text = 2500, "1. ROWS" })
	wantStatus(t, run(chapter), CheckTextOverSubject, CheckFail)
	// A title set not to avoid the subject that covers the torso only: a failure.
	off := withTitle(t, buildingLats(t), func(it *timeline.TextItem) {
		f := false
		it.AvoidSubject = &f
		it.Anchor = timeline.AnchorCenter // over the chest: the torso, and not the head
	})
	c = wantStatus(t, run(off), CheckTextOverSubject, CheckFail)
	if !strings.Contains(c.Detail, "covers the person's torso and is set not to avoid them") {
		t.Errorf("off: %s", c.Detail)
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
	for _, want := range []string{"series format, <TOPIC> <number> in capitals (always ending in 101)", "do not invent a name", "never reuse one of their titles", "Keep a title of up to 22 characters on one line"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "BUILDING") || strings.Contains(note, "BRACHIORADIALIS") {
		t.Errorf("the creator's words are in our note:\n%s", note)
	}
	data := styleBlocks(p)[1].Text
	if !strings.Contains(data, `title templates: a series title, "<TOPIC> 101" in capitals`) || strings.Contains(data, "BUILDING LATS 101") || strings.Contains(data, "their own words") {
		t.Errorf("the block lacks the templates, or quotes a title:\n%s", data)
	}
}

// A title that is not in a form the creator uses is borderline: the check passes and says so.
func TestAnOddTitleIsBorderlineNotAFailure(t *testing.T) {
	spec := withTitle(t, buildingLats(t), func(it *timeline.TextItem) { it.Text = "BACK BLUEPRINT" })
	r := RunChecks(CheckInput{Spec: spec, Plan: workoutPlan(), Style: ginoProfile(t), Vibe: VibeCalm})
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
