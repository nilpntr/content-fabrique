package render

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

var posRe = regexp.MustCompile(`\\pos\([0-9.]+,([0-9.]+)\)`)

func subjectResolver() AssetResolver {
	tr := subject.Track{Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.2, Y: 0.2, W: 0.6, H: 0.7, HeadTopY: 0.22}}}
	return ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true, Subject: &tr}, nil
	})
}

func eventAt(t *testing.T, ass, id string) (an string, y float64) {
	t.Helper()
	for _, d := range parseDialogues(t, ass) {
		if strings.Contains(d.text, id) {
			head := strings.SplitN(d.text, "}", 2)[0]
			m := posRe.FindStringSubmatch(head)
			if m == nil {
				t.Fatalf("no position in %q", d.text)
			}
			y, _ = strconv.ParseFloat(m[1], 64)
			return head, y
		}
	}
	t.Fatalf("no event with %q", id)
	return "", 0
}

// A title that would cover the head of the person in the clip is moved off it; a title that clears it, and one that says
// avoid_subject false, stay where their anchor puts them; and without a subject nothing is moved.
func TestTitlesAvoidTheSubjectInTheScript(t *testing.T) {
	items := []timeline.TextItem{
		{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "AAA\nBBB", Style: "title_series", Anchor: "upper_third"},
		{ID: "t2", AtMs: 1500, DurationMs: 1500, Text: "CCC", Style: "title_series", Anchor: "upper_third"},
		{ID: "t3", AtMs: 3000, DurationMs: 1500, Text: "DDD\nEEE", Style: "title_series", Anchor: "upper_third", AvoidSubject: boolPtr(false)},
	}
	spec := overridesSpec(items...)
	with, err := Compile(spec, Final, subjectResolver())
	if err != nil {
		t.Fatal(err)
	}
	without, err := Compile(spec, Final, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	_, y1With := eventAt(t, with.Subtitles.ASS, "AAA")
	_, y1Without := eventAt(t, without.Subtitles.ASS, "AAA")
	// The head starts at 22 % (422 px) and the title is at the top of the zone: it goes above the head, up into the margin the empty space
	// of its line box may use, at a smaller size.
	if y1With >= y1Without-30 {
		t.Errorf("the two-line title was not moved off the head: y %v with the subject, %v without", y1With, y1Without)
	}
	_, y2With := eventAt(t, with.Subtitles.ASS, "CCC")
	_, y2Without := eventAt(t, without.Subtitles.ASS, "CCC")
	if y2With != y2Without {
		t.Errorf("a title that clears the head moved: %v against %v", y2With, y2Without)
	}
	_, y3With := eventAt(t, with.Subtitles.ASS, "DDD")
	_, y3Without := eventAt(t, without.Subtitles.ASS, "DDD")
	if y3With != y3Without {
		t.Errorf("avoid_subject false moved the title: %v against %v", y3With, y3Without)
	}
	// The renderer says what it did.
	var moved []string
	for _, w := range with.Warnings {
		if w.Code == "text_moved" {
			moved = append(moved, w.Message)
		}
	}
	if len(moved) != 1 || !strings.Contains(moved[0], "t1") || !strings.Contains(moved[0], "above") {
		t.Errorf("warnings %+v", with.Warnings)
	}
	if len(without.Warnings) != 0 {
		t.Errorf("a render without a subject warned: %+v", without.Warnings)
	}
}

func boolPtr(v bool) *bool { return &v }

// The hook of the back-day video: the head starts at 17 % of the frame, and the title goes above it, smaller, up into the margin its empty
// line space may use. It is not put under the head over the back. A head too high for any title above it gets the body warning.
func TestTheTitleGoesAboveAHeadAt17PercentAndWarnsWhenItEndsOverTheBody(t *testing.T) {
	resolver := func(headTop, y, h float64) AssetResolver {
		tr := subject.Track{Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.2, Y: y, W: 0.6, H: h, HeadTopY: headTop}}}
		return ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
			return ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true, Subject: &tr}, nil
		})
	}
	spec := overridesSpec(timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "BACK DAY 101", Style: "title_series", Anchor: "upper_third"})
	plan, err := Compile(spec, Final, resolver(0.17, 0.165, 0.78))
	if err != nil {
		t.Fatal(err)
	}
	_, y := eventAt(t, plan.Subtitles.ASS, "BACK")
	if y >= 0.17*1920 {
		t.Errorf("the title is at y %v, not above the head (326)", y)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "text_moved" || !strings.Contains(plan.Warnings[0].Message, "above") {
		t.Errorf("warnings %+v", plan.Warnings)
	}
	// A head at 14.6 %: no room above at any size, the person is all around, and the clip is the whole video: the title is never put over
	// the body (P6-06), so it is left out, and the render says so.
	plan, err = Compile(spec, Final, resolver(0.146, 0.14, 0.78))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "text_over_subject" || !strings.Contains(plan.Warnings[0].Message, "left out") {
		t.Errorf("warnings %+v", plan.Warnings)
	}
	if plan.Subtitles != nil && strings.Contains(plan.Subtitles.ASS, "BACK") {
		t.Error("the title was drawn over the body")
	}
}

// A title over two clips, one a close-up with no room clear of the body and one a wide shot with room above the head, is shown only over
// the wide shot; a title over the close-up alone is moved to the wide shot (P6-06). Neither is drawn over the body.
const (
	fxClose = "ast_01j3er3kwcgrasdgd4aznmxxe0"
	fxWide  = "ast_01j3er3kwcgrasdgd4aznmxxe1"
)

func TestATitleWithNoRoomIsShownWhereItFits(t *testing.T) {
	tracks := map[string]subject.Track{
		"close": {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.05, Y: 0.14, W: 0.9, H: 0.86, HeadTopY: 0.146}}},
		"wide":  {Aspect: 0.5625, Samples: []subject.Sample{{AtMs: 1000, X: 0.3, Y: 0.34, W: 0.4, H: 0.6, HeadTopY: 0.35}}},
	}
	resolver := ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		name := "wide"
		if id == fxClose {
			name = "close"
		}
		tr := tracks[name]
		return ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true, Subject: &tr}, nil
	})
	spec := func(text timeline.TextItem) *timeline.Spec {
		s := overridesSpec(text)
		main := s.Tracks[0].(*timeline.VideoTrack)
		first := main.Items[0]
		first.AssetID, first.Source = fxClose, timeline.Source{InMs: 0, OutMs: 2000}
		second := first
		second.ID, second.AssetID, second.AtMs = "c2", fxWide, 2000
		main.Items = []timeline.VideoItem{first, second}
		return s
	}
	plan, err := Compile(spec(timeline.TextItem{ID: "t1", AtMs: 1000, DurationMs: 2000, Text: "BACK DAY 101", Style: "title_series", Anchor: "upper_third"}), Final, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 || plan.Warnings[0].Code != "text_retimed" || !strings.Contains(plan.Warnings[0].Message, "shortened to 2000-3000 ms") {
		t.Errorf("over two clips: %+v", plan.Warnings)
	}
	plan, err = Compile(spec(timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "BACK DAY 101", Style: "title_series", Anchor: "upper_third"}), Final, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 || plan.Warnings[0].Code != "text_retimed" || !strings.Contains(plan.Warnings[0].Message, "moved to 2000-3500 ms") {
		t.Errorf("over the close-up: %+v", plan.Warnings)
	}
}
