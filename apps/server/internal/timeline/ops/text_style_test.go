package ops

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func textItemsByID(t *testing.T, spec timeline.Spec) map[string]timeline.TextItem {
	t.Helper()
	out := map[string]timeline.TextItem{}
	for _, tr := range spec.Tracks {
		if tt, ok := tr.(*timeline.TextTrack); ok {
			for _, it := range tt.Items {
				out[it.ID] = it
			}
		}
	}
	return out
}

// add_text and update_text carry the per-item style overrides (fill, scale, shadow, align), update changes only what it names, and a
// value outside the schema never gets as far as the timeline.
func TestTextStyleOverridesOps(t *testing.T) {
	base := baseSpec(t)
	out := mustApply(t, base, `[{"op":"add_text","id":"t9","at_ms":0,"duration_ms":1500,"text":"BUILDING LATS 101","style":"title_series","anchor":"upper_third","fill":"#F2E9A0","scale":0.85,"shadow":"none","align":"left"}]`)
	it := textItemsByID(t, out)["t9"]
	if it.Fill != "#F2E9A0" || it.Scale == nil || *it.Scale != 0.85 || it.Shadow != "none" || it.Align != "left" {
		t.Fatalf("added: %+v", it)
	}
	// Update only the scale: the others stay, and the scale is a copy.
	out = mustApply(t, out, `[{"op":"update_text","id":"t9","scale":1.2}]`)
	it = textItemsByID(t, out)["t9"]
	if it.Fill != "#F2E9A0" || *it.Scale != 1.2 || it.Shadow != "none" || it.Align != "left" {
		t.Fatalf("updated: %+v", it)
	}
	out = mustApply(t, out, `[{"op":"update_text","id":"t9","fill":"#FFFFFF","shadow":"soft","align":"center"}]`)
	it = textItemsByID(t, out)["t9"]
	if it.Fill != "#FFFFFF" || it.Shadow != "soft" || it.Align != "center" || *it.Scale != 1.2 {
		t.Fatalf("updated again: %+v", it)
	}
	// A new style starts from its own look: the overrides chosen for the old one are dropped, unless the same op sets them again.
	changed := mustApply(t, out, `[{"op":"update_text","id":"t9","style":"title_lowercase"}]`)
	it = textItemsByID(t, changed)["t9"]
	if it.Style != "title_lowercase" || it.Fill != "" || it.Scale != nil || it.Shadow != "" || it.Align != "" {
		t.Errorf("a new style kept the old overrides: %+v", it)
	}
	kept := mustApply(t, out, `[{"op":"update_text","id":"t9","style":"title_lowercase","fill":"#101010"}]`)
	if it = textItemsByID(t, kept)["t9"]; it.Fill != "#101010" || it.Scale != nil || it.Align != "" {
		t.Errorf("an op that sets a fill with a new style: %+v", it)
	}
	// The result validates, and the original spec is untouched.
	if r := timeline.Validate(mustJSON(t, out), fixtureCatalog(t)); !r.OK() {
		t.Errorf("the result does not validate: %+v", r.Issues)
	}
	if _, ok := textItemsByID(t, base)["t9"]; ok {
		t.Error("the input spec changed")
	}
	// Values outside the schema are refused at the parse, with the field named.
	for _, bad := range []string{
		`{"op":"update_text","id":"t1","fill":"red"}`,
		`{"op":"update_text","id":"t1","fill":"#F2E9A0\\an8"}`,
		`{"op":"update_text","id":"t1","scale":0.5}`,
		`{"op":"update_text","id":"t1","scale":1.5}`,
		`{"op":"update_text","id":"t1","shadow":"big"}`,
		`{"op":"update_text","id":"t1","align":"justify"}`,
	} {
		if op, issues := ParseOp([]byte(bad), 0); op != nil || len(issues) == 0 {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// A caption that keeps off the face only (P6-05d): add_text and update_text take avoid; body, the default, is stored as nothing.
func TestTextAvoidModeOps(t *testing.T) {
	out := mustApply(t, baseSpec(t), `[{"op":"add_text","id":"cap1","at_ms":0,"duration_ms":1500,"text":"And I kept going","style":"subtitle_clean","anchor":"center","avoid":"face"}]`)
	if it := textItemsByID(t, out)["cap1"]; it.Avoid != timeline.AvoidFace || !it.AvoidsFaceOnly() {
		t.Fatalf("added: %+v", it)
	}
	out = mustApply(t, out, `[{"op":"update_text","id":"cap1","avoid":"body"}]`)
	if it := textItemsByID(t, out)["cap1"]; it.Avoid != "" || it.AvoidsFaceOnly() {
		t.Fatalf("updated to body: %+v", it)
	}
	if r := timeline.Validate(mustJSON(t, out), fixtureCatalog(t)); !r.OK() {
		t.Errorf("the result does not validate: %+v", r.Issues)
	}
}
