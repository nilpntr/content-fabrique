package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// cfctl eval editor prints the style distance when the workspace has a profile.
func TestEvalPrintsTheStyleDistance(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "Row")
	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--assets", a.ID.String(), "--yes")
	if code != 0 {
		t.Fatalf("create: exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)
	spec := `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
		"tracks":[{"type":"video","id":"main","items":[
			{"id":"c1","asset_id":"` + a.ID.String() + `","source":{"in_ms":0,"out_ms":1500},"at_ms":0},
			{"id":"c2","asset_id":"` + a.ID.String() + `","source":{"in_ms":0,"out_ms":1000},"at_ms":1500}]}],"markers":[]}`
	if _, err := r.st.AddTimelineVersion(context.Background(), r.ws.ID, pid, store.NewTimelineVersion{
		Version: 1, Parent: 0, Spec: json.RawMessage(spec), Summary: []string{"first cut"}, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = r.runEval("editor", "--workspace", r.ws.ID.String(), "--project", pid.String())
	if code != 0 || strings.Contains(out, "OUT  ") || !strings.Contains(out, "n/a  style_match") || !strings.Contains(out, "no creator style profile") {
		t.Fatalf("without a profile: %d\n%s%s", code, out, errOut)
	}
	if _, err := styleprofile.Save(context.Background(), r.st, r.ws.ID, referenceMeasures(t)); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = r.runEval("editor", "--workspace", r.ws.ID.String(), "--project", pid.String())
	if code != 0 || !strings.Contains(out, "style distance:") || !strings.Contains(out, "OUT  long_takes") || !strings.Contains(out, "ok   transitions") {
		t.Fatalf("with a profile: %d\n%s%s", code, out, errOut)
	}
}
