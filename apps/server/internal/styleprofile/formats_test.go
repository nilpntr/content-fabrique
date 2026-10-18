package styleprofile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// References in our own words: two series titles, a count title with chapters, a story told in lines, and one with no text read.
func formatRefs() []Measures {
	m := func(name string, text *TextStyle, cuts ...float64) Measures {
		shots := []float64{}
		prev := 0.0
		for _, c := range cuts {
			shots = append(shots, c-prev)
			prev = c
		}
		shots = append(shots, 20-prev)
		return Measures{Name: name, DurationS: 20, CutsS: cuts, ShotsS: shots, Text: text}
	}
	return []Measures{
		m("a.mp4", &TextStyle{TitlePresent: true, Text: "UPPER CHEST 101"}, 2.5, 9),
		m("b.mp4", &TextStyle{TitlePresent: true, Text: "REAR DELTS 101"}, 2.2, 12),
		m("c.mp4", &TextStyle{TitlePresent: true, Text: "3 KEYS TO WIDER LATS", Chapters: []string{"1. PULL-UPS", "2. ROWS"}}, 2, 5, 8, 11, 14),
		m("d.mp4", &TextStyle{ShotsRead: true, ShotTexts: []ShotText{{AtS: 1, Text: "what I changed this month"}, {AtS: 4, Text: "I train legs first now"}, {AtS: 8, Text: "and I sleep more"}}}, 3, 7, 12),
		m("e.mp4", nil),
	}
}

func TestAggregateFormats(t *testing.T) {
	refs := formatRefs()
	if tags := FormatTags(refs[0]); len(tags) == 0 || tags[0].ID != "tutorial_101" {
		t.Errorf("a series title: %v", tags)
	}
	if tags := FormatTags(refs[3]); len(tags) == 0 || tags[0].ID != "story_captions" {
		t.Errorf("a story in lines: %v", tags)
	}
	f := AggregateFormats(refs)
	if f.Library != formats.Default().Version || f.Read != 4 {
		t.Errorf("formats %+v", f)
	}
	want := []formats.Share{{ID: "tutorial_101", Videos: 2, Share: 0.4}, {ID: "numbered_listicle", Videos: 1, Share: 0.2}, {ID: "one_take", Videos: 1, Share: 0.2}, {ID: "story_captions", Videos: 1, Share: 0.2}}
	if len(f.Shares) != len(want) {
		t.Fatalf("shares %v", f.Shares)
	}
	for i := range want {
		if f.Shares[i] != want[i] {
			t.Errorf("share %d = %+v, want %+v", i, f.Shares[i], want[i])
		}
	}
	p := Aggregate(refs)
	if p.Formats == nil || len(p.Formats.Shares) != 4 {
		t.Fatalf("the profile's formats: %+v", p.Formats)
	}
	var line string
	for _, l := range p.PlainLines() {
		if l.ID == "formats" {
			line = l.Label + ": " + l.Value
		}
	}
	if line != "Your go-to formats: 101 tutorials (2 of 5 videos), numbered lists (1 of 5), one-take sets (1 of 5), caption stories (1 of 5)" {
		t.Errorf("the plain line: %q", line)
	}
	if b := p.CompactBlock(); !strings.Contains(b, "formats: the creator's go-to formats are tutorial_101 (2 of 5 videos)") {
		t.Errorf("the editor's block lacks the formats:\n%s", b)
	}
	// No creator text reaches the plain line or the block through the formats.
	for _, s := range []string{line, p.formatsLine()} {
		if strings.Contains(s, "CHEST") || strings.Contains(s, "legs first") {
			t.Errorf("creator text in %q", s)
		}
	}
	if AggregateFormats(nil) != nil {
		t.Error("formats of nothing")
	}
}

type rowStore struct{ row store.CreatorStyle }

func (r rowStore) GetCreatorStyle(context.Context, ids.WorkspaceID) (store.CreatorStyle, error) {
	return r.row, nil
}

// A profile stored before formats (or with an older library) is tagged again from its stored measures when it is read.
func TestLoadTagsAnOlderProfile(t *testing.T) {
	refs := formatRefs()
	p := Aggregate(refs)
	p.Formats = nil
	pj, _ := json.Marshal(p)
	mj, _ := json.Marshal(refs)
	got, err := LoadProfile(context.Background(), rowStore{store.CreatorStyle{Profile: pj, Measures: mj}}, ids.WorkspaceID{})
	if err != nil || got.Formats == nil || got.Formats.Library != formats.Default().Version || len(got.Formats.Shares) != 4 {
		t.Fatalf("the read profile: %v %+v", err, got)
	}
	p.Formats = &FormatsProfile{Library: "formats@old", Shares: []formats.Share{{ID: "pov", Videos: 5, Share: 1}}}
	pj, _ = json.Marshal(p)
	st, err := LoadStyle(context.Background(), rowStore{store.CreatorStyle{Profile: pj, Measures: mj}}, ids.WorkspaceID{})
	if err != nil || st.Profile.Formats.Shares[0].ID != "tutorial_101" {
		t.Fatalf("an older library is not tagged again: %v %+v", err, st.Profile.Formats)
	}
	// Measures that cannot be read leave the profile as it is.
	got, err = LoadProfile(context.Background(), rowStore{store.CreatorStyle{Profile: pj, Measures: json.RawMessage(`{`)}}, ids.WorkspaceID{})
	if err != nil || got.Formats.Library != "formats@old" {
		t.Errorf("unreadable measures: %v %+v", err, got.Formats)
	}
}
