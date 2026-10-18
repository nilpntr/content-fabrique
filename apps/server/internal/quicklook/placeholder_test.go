package quicklook

import (
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// placeholderAnswer is what the model answered for the founder's slow-motion hammer-curl clip (2026-10-18): it saw four clear frames of
// a man curling dumbbells and wrote "placeholder", which was stored as the clip's title. The frames were fine (the clip is 30 fps as
// stored; its 120 fps base rate did not change the sampling) and the file name played no part: the model filled the fields instead of
// describing the pictures.
const placeholderAnswer = `{"title":"placeholder","description":"placeholder","exercise":"","type":"unsure","body":[],"movement":"unsure","angle":[],"equipment":[],"subjects":[]}`

// A placeholder answer is asked once more, with a note, inside the same budget; the second answer is stored.
func TestAPlaceholderAnswerIsAskedOnceMore(t *testing.T) {
	api := &fakeAPI{tokens: 1750, reply: func(n int) (int, string) {
		if n == 1 {
			return toolReply(placeholderAnswer, 1750, 60)
		}
		return goodReply(n)
	}}
	r := newRig(t, rigOptions{api: api})
	res := mustRun(t, r, Options{})
	if res.Outcome != OutcomeDone || res.Title != "Barbell row" {
		t.Fatalf("result = %+v", res)
	}
	calls := api.messageCalls()
	if len(calls) != 2 {
		t.Fatalf("%d model calls, want 2", len(calls))
	}
	if strings.Contains(string(calls[0].Body), "placeholder words instead of describing") || !strings.Contains(string(calls[1].Body), "placeholder words instead of describing") {
		t.Error("only the second request carries the note")
	}
	// Both calls are metered under the asset's run, and the result counts what both used.
	if rows := r.ledger.snapshot(); len(rows) != 2 || rows[0].RunID != r.asset.ID.UUID || rows[1].RunID != r.asset.ID.UUID {
		t.Errorf("ledger rows = %+v", rows)
	}
	if res.Usage.InputTokens != 2*1750 || res.Usage.OutputTokens != 60+110 {
		t.Errorf("usage = %+v, want both calls", res.Usage)
	}
	if a := r.store.snapshot(); a.Title != "Barbell row" || a.Exercise != "barbell row" {
		t.Errorf("asset = %q %q", a.Title, a.Exercise)
	}
}

// Two placeholder answers fail the quick look; "placeholder" never becomes the title, and the clip keeps its file name.
func TestPlaceholderIsNeverStored(t *testing.T) {
	api := &fakeAPI{tokens: 1750, reply: func(int) (int, string) { return toolReply(placeholderAnswer, 1750, 60) }}
	r := newRig(t, rigOptions{api: api})
	res := mustRun(t, r, Options{})
	if res.Outcome != OutcomeFailed || res.Reason != FailInvalidOutput {
		t.Fatalf("result = %+v", res)
	}
	if n := len(api.messageCalls()); n != 2 {
		t.Errorf("%d model calls, want 2 (one repeat, no more)", n)
	}
	a := r.store.snapshot()
	if a.Title != "IMG_0042" || a.Description != "" || len(a.Facets) != 0 || *a.QuickLookStatus != store.QuickLookFailed {
		t.Errorf("asset = title %q, description %q, facets %v, status %s", a.Title, a.Description, a.Facets, *a.QuickLookStatus)
	}
}

// Slow motion comes from the probe (the frame rate), on top of what the model said; never from the model.
func TestSlowMotionComesFromTheProbe(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.store.edit(func(a *store.Asset) {
		a.Probe = []byte(`{"fps":{"num":21525,"den":716,"value":30.06},"baseFps":{"num":120,"den":1,"value":120}}`)
	})
	mustRun(t, r, Options{})
	if got := r.store.snapshot().Facets; !slices.Equal(got, []string{"type:lift", "type:slow_motion", "body:back", "movement:row", "angle:side", "equipment:barbell"}) {
		t.Errorf("facets = %v", got)
	}

	// A 30 fps clip is not slow motion, whatever the model says.
	api := &fakeAPI{tokens: 1750, reply: func(int) (int, string) {
		return toolReply(`{"title":"Curls","description":"Slow curls.","exercise":"hammer curl","type":"slow_motion","body":["arms"],"movement":"curl","angle":["front"],"equipment":["dumbbell"]}`, 1750, 80)
	}}
	r = newRig(t, rigOptions{api: api})
	r.store.edit(func(a *store.Asset) { a.Probe = []byte(`{"fps":{"num":30,"den":1,"value":30}}`) })
	mustRun(t, r, Options{})
	if got := r.store.snapshot().Facets; !slices.Equal(got, []string{"type:lift", "body:arms", "movement:curl", "angle:front", "equipment:dumbbell"}) {
		t.Errorf("facets = %v", got)
	}
}
