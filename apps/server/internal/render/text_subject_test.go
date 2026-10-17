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
	if y1With <= y1Without+100 {
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
	if len(moved) != 1 || !strings.Contains(moved[0], "t1") {
		t.Errorf("warnings %+v", with.Warnings)
	}
	if len(without.Warnings) != 0 {
		t.Errorf("a render without a subject warned: %+v", without.Warnings)
	}
}

func boolPtr(v bool) *bool { return &v }
