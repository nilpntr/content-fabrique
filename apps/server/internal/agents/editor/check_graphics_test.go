package editor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

const graphicsAsset = "ast_01j3er3kwcgrasdgd4aznmxxe0"

// graphicsSpec is a 9:16 edit of two 4 s clips of one asset with the given graphics items (JSON).
func graphicsSpec(t *testing.T, items string) *timeline.Spec {
	t.Helper()
	js := `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
	"tracks":[{"id":"main","type":"video","items":[
	  {"id":"c1","asset_id":"` + graphicsAsset + `","source":{"in_ms":0,"out_ms":4000},"at_ms":0},
	  {"id":"c2","asset_id":"` + graphicsAsset + `","source":{"in_ms":10000,"out_ms":14000},"at_ms":4000}]},
	 {"id":"graphics","type":"graphics","items":[` + items + `]}],"markers":[]}`
	spec, err := timeline.Parse([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func runGraphicsChecks(t *testing.T, in CheckInput) map[string]Check {
	t.Helper()
	r := RunChecks(in)
	out := map[string]Check{}
	for _, c := range r.Checks {
		out[c.ID] = c
	}
	return out
}

func TestOverlayReadable(t *testing.T) {
	short := graphicsSpec(t, `{"id":"g1","template":"title_pop","at_ms":0,"duration_ms":1200,"anchor":"upper_third","params":{"text":"BACK DAY 101"}}`)
	c := runGraphicsChecks(t, CheckInput{Spec: short})[CheckOverlayReadable]
	if c.Status != CheckFail || !strings.Contains(c.Detail, "make duration_ms at least") {
		t.Fatalf("a 1.2 s title: %+v", c)
	}
	long := graphicsSpec(t, `{"id":"g1","template":"title_pop","at_ms":0,"duration_ms":3000,"anchor":"upper_third","params":{"text":"BACK DAY 101"}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: long})[CheckOverlayReadable]; c.Status != CheckPass {
		t.Fatalf("a 3 s title: %+v", c)
	}
	// The fix it names is enough.
	need := strings.TrimRight(strings.Fields(strings.SplitAfter(c.Detail, "at least ")[1])[0], ",;")
	fixed := graphicsSpec(t, `{"id":"g1","template":"title_pop","at_ms":0,"duration_ms":`+need+`,"anchor":"upper_third","params":{"text":"BACK DAY 101"}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: fixed})[CheckOverlayReadable]; c.Status != CheckPass {
		t.Fatalf("the duration the check asked for (%s) still fails: %+v", need, c)
	}
	if _, ok := runGraphicsChecks(t, CheckInput{Spec: graphicsSpec(t, "")})[CheckOverlayReadable]; ok {
		t.Fatal("an edit without graphics got the graphics checks")
	}
}

// A motion-graphics title is the hook title, and chapter cards are the chapter titles (the checks of P6-06 count them).
func TestGraphicsCountAsTitles(t *testing.T) {
	spec := graphicsSpec(t, `{"id":"g1","template":"title_pop","at_ms":0,"duration_ms":2400,"anchor":"upper_third","params":{"text":"BACK DAY 101"}},
		{"id":"g2","template":"chapter_card","at_ms":2400,"duration_ms":2500,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}},
		{"id":"g3","template":"chapter_card","at_ms":5000,"duration_ms":2500,"anchor":"upper_third","params":{"text":"LAT PULLDOWN","number":2}}`)
	plan := &Plan{ContentType: TypeWorkout, Shots: []PlanShot{{Role: "hook"}, {Role: "step", Chapter: 1}, {Role: "step", Chapter: 2}}}
	got := runGraphicsChecks(t, CheckInput{Spec: spec, Plan: plan})
	if c := got[CheckHookTitle]; c.Status != CheckPass {
		t.Errorf("hook_title: %+v", c)
	}
	if c := got[CheckChapterTitles]; c.Status != CheckPass {
		t.Errorf("chapter_titles: %+v", c)
	}
}

func subjects(headTop, top float64) subject.Index {
	return subject.Index{graphicsAsset: {Aspect: 0.5625, Samples: []subject.Sample{
		{AtMs: 0, X: 0.1, Y: top, W: 0.8, H: 1 - top, HeadTopY: headTop},
		{AtMs: 14000, X: 0.1, Y: top, W: 0.8, H: 1 - top, HeadTopY: headTop},
	}}}
}

// The torso rule: an overlay on the body fails, wherever the person is; a person low in the frame leaves the top band free.
func TestOverlayNeverOverTheBody(t *testing.T) {
	spec := graphicsSpec(t, `{"id":"g1","template":"chapter_card","at_ms":0,"duration_ms":3000,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}}`)
	high := runGraphicsChecks(t, CheckInput{Spec: spec, Subjects: subjects(0.06, 0.05)})[CheckOverlayOverSubject]
	if high.Status != CheckFail || !strings.Contains(high.Detail, "never over the body") || !strings.Contains(high.Detail, "leaves it out") {
		t.Fatalf("a person filling the frame: %+v", high)
	}
	// The person fills the frame in the first shot (source 0-4 s) and stands low in the second (source 10-14 s): the renderer moves a
	// graphic of the first shot to the second (subject.FitText). A label may move (a note); a chapter card must be where it was put.
	shots := subject.Index{graphicsAsset: {Aspect: 0.5625, Samples: []subject.Sample{
		{AtMs: 0, X: 0.02, Y: 0.05, W: 0.96, H: 0.95, HeadTopY: 0.06},
		{AtMs: 4000, X: 0.02, Y: 0.05, W: 0.96, H: 0.95, HeadTopY: 0.06},
		{AtMs: 10000, X: 0.1, Y: 0.44, W: 0.8, H: 0.56, HeadTopY: 0.45},
		{AtMs: 14000, X: 0.1, Y: 0.44, W: 0.8, H: 0.56, HeadTopY: 0.45},
	}}}
	label := graphicsSpec(t, `{"id":"g1","template":"lower_label","at_ms":1000,"duration_ms":2000,"anchor":"upper_third","params":{"text":"TEMPO 3-1-1"}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: label, Subjects: shots})[CheckOverlayOverSubject]; c.Status != CheckPass || !strings.Contains(c.Detail, "the renderer shows it at") {
		t.Fatalf("a label moved to the next shot: %+v", c)
	}
	card := graphicsSpec(t, `{"id":"g1","template":"chapter_card","at_ms":1000,"duration_ms":2500,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: card, Subjects: shots})[CheckOverlayOverSubject]; c.Status != CheckFail || !strings.Contains(c.Detail, "the renderer shows it at") {
		t.Fatalf("a chapter card moved to the next shot: %+v", c)
	}
	// One set not to avoid the subject stays where it is, so on the person it fails.
	pinned := graphicsSpec(t, `{"id":"g1","template":"lower_label","at_ms":1000,"duration_ms":2000,"anchor":"center","avoid_subject":false,"params":{"text":"TEMPO 3-1-1"}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: pinned, Subjects: shots})[CheckOverlayOverSubject]; c.Status != CheckFail || !strings.Contains(c.Detail, "avoid_subject false") {
		t.Fatalf("a graphic set not to avoid the person: %+v", c)
	}
	low := runGraphicsChecks(t, CheckInput{Spec: spec, Subjects: subjects(0.45, 0.44)})[CheckOverlayOverSubject]
	if low.Status != CheckPass {
		t.Fatalf("a person low in the frame: %+v", low)
	}
	if c := runGraphicsChecks(t, CheckInput{Spec: spec})[CheckOverlayOverSubject]; c.Status != CheckSkip {
		t.Fatalf("no subject boxes: %+v", c)
	}
}

func TestOverlayLegibility(t *testing.T) {
	// Cream letters with no shadow (title_lowercase has a soft one, so force none) on a bright and on a dark picture.
	spec := graphicsSpec(t, `{"id":"g1","template":"blur_in","at_ms":500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"how it feels","style":"title_series","fill":"#F2E9A0","shadow":"none"}}`)
	probe := RegionProbe{ItemID: "g1", When: "before"}
	bright := runGraphicsChecks(t, CheckInput{Spec: spec, Overlay: map[string]RegionLuma{"g1": {Probe: probe, Low: 120, High: 225, Avg: 190}}})[CheckOverlayLegibility]
	if bright.Status != CheckFail || !strings.Contains(bright.Detail, "backing plate") {
		t.Fatalf("cream on a bright picture: %+v", bright)
	}
	dark := runGraphicsChecks(t, CheckInput{Spec: spec, Overlay: map[string]RegionLuma{"g1": {Probe: probe, Low: 16, High: 60, Avg: 30}}})[CheckOverlayLegibility]
	if dark.Status != CheckPass {
		t.Fatalf("cream on a dark picture: %+v", dark)
	}
	plated := graphicsSpec(t, `{"id":"g1","template":"blur_in","at_ms":500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"how it feels","style":"title_series","fill":"#F2E9A0","shadow":"none","backing":"plate"}}`)
	if c := runGraphicsChecks(t, CheckInput{Spec: plated, Overlay: map[string]RegionLuma{"g1": {Probe: probe, Low: 120, High: 225}}})[CheckOverlayLegibility]; c.Status != CheckPass {
		t.Fatalf("a plate behind the letters: %+v", c)
	}
	if c := runGraphicsChecks(t, CheckInput{Spec: spec, OverlaySkip: "no preview"})[CheckOverlayLegibility]; c.Status != CheckSkip || c.Detail != "no preview" {
		t.Fatalf("not measured: %+v", c)
	}
}

// A probe is taken on a frame of the same shot where the overlay is not drawn: before it when it starts inside a shot, after it when
// it starts on a cut, and none when it fills its shot.
func TestOverlayProbes(t *testing.T) {
	spec := graphicsSpec(t, `{"id":"g1","template":"title_pop","at_ms":500,"duration_ms":2000,"anchor":"upper_third","params":{"text":"A"}},
		{"id":"g2","template":"title_pop","at_ms":4000,"duration_ms":2000,"anchor":"upper_third","params":{"text":"B"}},
		{"id":"g3","template":"title_pop","at_ms":0,"duration_ms":4000,"anchor":"upper_third","params":{"text":"C"}}`)
	got := map[string]RegionProbe{}
	for _, p := range overlayProbes(spec, nil) {
		got[p.ItemID] = p
	}
	if p := got["g1"]; p.When != "before" || p.Frame != 14 || p.W <= 0 || p.X < 0 || p.X+p.W > 1 {
		t.Errorf("g1 %+v", p)
	}
	if p := got["g2"]; p.When != "after" || p.Frame != 181 {
		t.Errorf("g2 %+v", p)
	}
	if _, ok := got["g3"]; ok {
		t.Errorf("g3 fills its shot and was probed: %+v", got["g3"])
	}
}

func TestRegionStats(t *testing.T) {
	l, err := ParseRegionStats([]byte("frame:0 pts:0\nlavfi.signalstats.YLOW=19\nlavfi.signalstats.YAVG=45.3\nlavfi.signalstats.YHIGH=79\n"))
	if err != nil || l.Low != 19 || l.High != 79 || l.Avg != 45.3 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := ParseRegionStats([]byte("frame:0\nlavfi.signalstats.YLOW=300\nlavfi.signalstats.YAVG=1\nlavfi.signalstats.YHIGH=2\n")); err == nil {
		t.Fatal("a luma over 255 was accepted")
	}
	if _, err := ParseRegionStats(nil); !errors.Is(err, errNoRegionFrame) {
		t.Fatalf("no frame: %v", err)
	}
	args, err := media.RegionStatsArgs("/in/0", []media.RegionCrop{{Frame: 14, X: 0.25, Y: 0.1, W: 0.5, H: 0.08}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "select='eq(n\\,14)',crop=w='iw*0.5000':h='ih*0.0800':x='iw*0.2500':y='ih*0.1000'") || !strings.Contains(joined, "-map [o0] -f null -") {
		t.Fatalf("args %s", joined)
	}
	if _, err := media.RegionStatsArgs("/in/0", make([]media.RegionCrop, media.MaxRegions+1)); err == nil {
		t.Fatal("too many regions were accepted")
	}
}

// list_motion_templates lists every template, and shows a still only when asked, as a built-in reference the resolver serves from the
// binary; no other reference under that prefix resolves.
func TestListMotionTemplates(t *testing.T) {
	var d Deps
	res, err := d.listMotionTemplates(context.Background(), runtime.RunContext{}, listMotionArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("without a template the answer has %d blocks", len(res.Content))
	}
	text := res.Content[0].Text
	for _, tpl := range motion.Templates() {
		if !strings.Contains(text, tpl.Name+" (v1)") {
			t.Errorf("the list lacks %s", tpl.Name)
		}
	}
	res, err = d.listMotionTemplates(context.Background(), runtime.RunContext{}, listMotionArgs{Template: "chapter_card"})
	if err != nil {
		t.Fatal(err)
	}
	img := res.Content[len(res.Content)-1]
	if img.Type != llm.BlockImage || img.Image.Ref != "builtin/motion-previews/chapter_card.png" {
		t.Fatalf("the still: %+v", img)
	}
	mt, data, err := ImageResolver{}.ResolveImage(context.Background(), img.Image.Ref)
	if err != nil || mt != "image/png" || len(data) < 100 {
		t.Fatalf("the still does not resolve: %v", err)
	}
	for _, ref := range []string{"builtin/motion-previews/../motion-previews/chapter_card.png", "builtin/motion-previews/evil.png", "builtin/motion-previews/chapter_card.PNG"} {
		if _, _, err := (ImageResolver{}).ResolveImage(context.Background(), ref); err == nil {
			t.Errorf("%s resolved", ref)
		}
	}
	if _, err := d.listMotionTemplates(context.Background(), runtime.RunContext{}, listMotionArgs{Template: "spin"}); err == nil {
		t.Fatal("an unknown template was accepted")
	}
}
