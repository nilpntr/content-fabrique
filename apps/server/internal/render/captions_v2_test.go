package render

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

const captionAsset = "ast_01j3er3kwcgrasdgd4aznmxxe0"

// talkingSpec is a 6 s talking clip in a format, with an enabled captions track.
func talkingSpec(format timeline.Format, caps *timeline.CaptionsTrack) *timeline.Spec {
	out := timeline.Output{Format: format, FPS: 30, Background: "#000000"}
	switch format {
	case timeline.Format9x16:
		out.Width, out.Height = 1080, 1920
	case timeline.Format1x1:
		out.Width, out.Height = 1080, 1080
	case timeline.Format4x5:
		out.Width, out.Height = 1080, 1350
	case timeline.Format16x9:
		out.Width, out.Height = 1920, 1080
	}
	if caps.ID == "" {
		caps.ID, caps.From, caps.Enabled = "captions", "transcript", true
	}
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: out,
		Tracks: []timeline.Track{
			&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: captionAsset, Source: timeline.Source{InMs: 0, OutMs: 6000}, AtMs: 0}}},
			caps,
		},
	}
}

// speakerAt is the subject of a talking clip whose head and shoulders cover the rows from top to bottom (fractions of the frame height), in
// the middle of the frame.
func speakerAt(aspect, top, bottom float64) *subject.Track {
	return &subject.Track{Aspect: aspect, Samples: []subject.Sample{{
		AtMs: 1000, X: 0.3, Y: top, W: 0.4, H: min(0.45, 1-top), HeadTopY: top, HeadX: 0.4, HeadW: 0.2, ShoulderY: bottom,
	}}}
}

func captionResolver(tr *subject.Track) AssetResolver {
	return ResolverFunc(func(id string, _ Variant) (ResolvedAsset, error) {
		return ResolvedAsset{Location: "/tmp/" + id + ".mp4", HasAudio: true, Subject: tr, Transcript: fixtureTranscript(id, 6000)}, nil
	})
}

// captionEvents are the caption dialogues of a script (layer 0) with the y of their centre.
func captionEvents(t *testing.T, ass string) (events []dialogue, ys []float64) {
	t.Helper()
	for _, d := range parseDialogues(t, ass) {
		if d.layer != 0 {
			continue
		}
		m := posRe.FindStringSubmatch(d.text)
		if m == nil {
			t.Fatalf("no position in %q", d.text)
		}
		var yy float64
		if _, err := fmt.Sscanf(m[1], "%g", &yy); err != nil {
			t.Fatal(err)
		}
		events = append(events, d)
		ys = append(ys, yy)
	}
	return events, ys
}

// A golden render of a talking clip has no caption over the face box in any format: with the face where the lower band is, the captions
// are in the upper band, inside the safe zone, and the renderer says it moved them.
func TestCaptionsKeepOffTheFaceInEveryFormat(t *testing.T) {
	for _, format := range []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9} {
		t.Run(string(format), func(t *testing.T) {
			spec := talkingSpec(format, &timeline.CaptionsTrack{Style: "chunk"})
			out := spec.Output
			zone, _ := out.Zone()
			st, _ := style.CaptionStyle("chunk")
			lower := style.CaptionBand(zone, out.Width, out.ShortSide(), st)
			h := float64(out.Height)
			aspect := float64(out.Width) / h
			tr := speakerAt(aspect, float64(lower.Top-20)/h, float64(lower.Bottom+20)/h)
			plan, err := Compile(spec, Final, captionResolver(tr))
			if err != nil {
				t.Fatal(err)
			}
			regions := subject.Regions(spec, subject.Index{captionAsset: *tr}.Lookup, 0, 6000)
			if len(regions) == 0 {
				t.Fatal("no subject regions")
			}
			upper := style.CaptionBandAt(zone, out.Width, out.ShortSide(), st, style.CaptionUpper)
			if !zone.Safe(upper) {
				t.Fatalf("the upper band %+v is not inside the %s safe zone", upper, format)
			}
			events, ys := captionEvents(t, plan.Subtitles.ASS)
			if len(events) == 0 {
				t.Fatal("no caption events")
			}
			for i, y := range ys {
				if int(y) < upper.Top || int(y) > upper.Bottom {
					t.Errorf("caption %d is at y %v, outside the upper band %d to %d", i, y, upper.Top, upper.Bottom)
				}
				for _, r := range regions {
					if y >= float64(r.Head.Top) && y <= float64(r.Head.Bottom) {
						t.Errorf("caption %d (y %v) is over the face box %+v", i, y, r.Head)
					}
				}
			}
			for _, r := range regions {
				if upper.Overlaps(r.Head) {
					t.Errorf("the upper band %+v overlaps the face %+v", upper, r.Head)
				}
			}
			var moved bool
			for _, w := range plan.Warnings {
				moved = moved || w.Code == "captions_moved"
				if w.Code == "caption_over_face" {
					t.Errorf("warning %+v", w)
				}
			}
			if !moved {
				t.Errorf("no captions_moved warning: %+v", plan.Warnings)
			}
		})
	}
}

// With the face out of the lower band, or no subject at all, the captions stay where they were (the lower band, no warning).
func TestCaptionsStayLowWhenTheFaceIsClear(t *testing.T) {
	spec := talkingSpec(timeline.Format9x16, &timeline.CaptionsTrack{Style: "chunk"})
	for name, res := range map[string]AssetResolver{
		"face high": captionResolver(speakerAt(0.5625, 0.20, 0.34)),
		"no boxes":  captionResolver(nil),
	} {
		plan, err := Compile(spec, Final, res)
		if err != nil {
			t.Fatal(name, err)
		}
		zone, _ := spec.Output.Zone()
		st, _ := style.CaptionStyle("chunk")
		lower := style.CaptionBand(zone, 1080, 1080, st)
		_, ys := captionEvents(t, plan.Subtitles.ASS)
		for _, y := range ys {
			if int(y) < lower.Top || int(y) > lower.Bottom {
				t.Errorf("%s: caption at y %v, outside the lower band %d to %d", name, y, lower.Top, lower.Bottom)
			}
		}
		if len(plan.Warnings) != 0 {
			t.Errorf("%s: warnings %+v", name, plan.Warnings)
		}
	}
}

// A face that fills the frame is in both bands: the captions cannot be moved clear, and the renderer says so.
func TestCaptionsOverAFaceThatFillsTheFrameWarn(t *testing.T) {
	spec := talkingSpec(timeline.Format9x16, &timeline.CaptionsTrack{Style: "chunk"})
	plan, err := Compile(spec, Final, captionResolver(speakerAt(0.5625, 0.12, 0.95)))
	if err != nil {
		t.Fatal(err)
	}
	var warned bool
	for _, w := range plan.Warnings {
		warned = warned || w.Code == "caption_over_face"
	}
	if !warned {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}

// The creator's fill and accent reach the script as a caption style of their own; keyword emphasis draws the key words in the accent
// colour for as long as the group is up, the listed ones, or the longest word of the group when none are listed.
func TestKeywordEmphasisInTheCreatorsColours(t *testing.T) {
	caps := &timeline.CaptionsTrack{Style: "keyword_emphasis", Fill: "#F5F0DC", Accent: "#33CC66", Keywords: []string{"squat"}, MaxWords: ptr(3)}
	plan, err := Compile(talkingSpec(timeline.Format9x16, caps), Final, captionResolver(nil))
	if err != nil {
		t.Fatal(err)
	}
	ass := plan.Subtitles.ASS
	if !strings.Contains(ass, "caption_keyword~fF5F0DC~a33CC66") {
		t.Errorf("no style with the creator's colours:\n%s", ass)
	}
	accent := "&H66CC33&" // &HBBGGRR&
	events, _ := captionEvents(t, ass)
	var lit, plain int
	for _, d := range events {
		if strings.Contains(d.text, `{\1c`+accent+`}`) {
			lit++
			if !strings.Contains(strings.ToUpper(d.text), "SQUAT") {
				t.Errorf("an accent colour on a group without the key word: %q", d.text)
			}
		} else {
			plain++
		}
	}
	if lit == 0 || plain == 0 {
		t.Errorf("%d groups with the key word in the accent colour, %d without", lit, plain)
	}
	// No listed words: the longest word of each group is emphasised.
	caps.Keywords = nil
	plan, err = Compile(talkingSpec(timeline.Format9x16, caps), Final, captionResolver(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Subtitles.ASS, `{\1c`+accent+`}`) {
		t.Error("the fallback emphasised nothing")
	}
}

// A correction shows in the next render, from the spec alone (no model, no new transcript): the word is replaced, the timing is the
// transcript's, and the other words are untouched.
func TestACorrectionShowsInTheNextRender(t *testing.T) {
	words := fixtureTranscript(captionAsset, 6000)
	if len(words) < 4 {
		t.Fatal("transcript too short")
	}
	before, err := Compile(talkingSpec(timeline.Format9x16, &timeline.CaptionsTrack{Style: "chunk", MaxWords: ptr(1)}), Final, captionResolver(nil))
	if err != nil {
		t.Fatal(err)
	}
	fix := &timeline.CaptionsTrack{Style: "chunk", MaxWords: ptr(1), Corrections: []timeline.CaptionCorrection{{AssetID: captionAsset, Word: 2, Text: "Creatine"}}}
	after, err := Compile(talkingSpec(timeline.Format9x16, fix), Final, captionResolver(nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := captionEvents(t, before.Subtitles.ASS)
	a, _ := captionEvents(t, after.Subtitles.ASS)
	if len(a) != len(b) {
		t.Fatalf("%d events after, %d before", len(a), len(b))
	}
	for i := range a {
		if a[i].start != b[i].start || a[i].end != b[i].end {
			t.Errorf("event %d moved in time: %d-%d against %d-%d", i, a[i].start, a[i].end, b[i].start, b[i].end)
		}
		want := strings.ToUpper(strings.Trim(words[i].Text, "."))
		if i == 2 {
			want = "CREATINE"
		}
		if !strings.Contains(a[i].text, want) {
			t.Errorf("event %d = %q, want %q", i, a[i].text, want)
		}
	}
}

// Hostile corrections, key words and colours are refused by the compiler, whatever the schema said: they would otherwise reach the script.
func TestHostileCaptionFieldsAreRefusedByTheCompiler(t *testing.T) {
	long := strings.Repeat("a", 41)
	many := make([]timeline.CaptionCorrection, 201)
	for i := range many {
		many[i] = timeline.CaptionCorrection{AssetID: captionAsset, Word: i, Text: "ok"}
	}
	fix := func(text string) *timeline.CaptionsTrack {
		return &timeline.CaptionsTrack{Style: "chunk", Corrections: []timeline.CaptionCorrection{{AssetID: captionAsset, Word: 1, Text: text}}}
	}
	for name, caps := range map[string]*timeline.CaptionsTrack{
		"override tags":      fix(`{\an8\fs400}hi`),
		"new line":           fix("a\\Nb"),
		"control":            fix("a\nb"),
		"bidi override":      fix("exe\u202efdp"),
		"zero width":         fix("a\u200bb"),
		"markup":             fix("<b>hi</b>"),
		"too long":           fix(long),
		"empty":              fix(""),
		"double space":       fix("a  b"),
		"negative word":      {Style: "chunk", Corrections: []timeline.CaptionCorrection{{AssetID: captionAsset, Word: -1, Text: "x"}}},
		"same word twice":    {Style: "chunk", Corrections: []timeline.CaptionCorrection{{AssetID: captionAsset, Word: 1, Text: "x"}, {AssetID: captionAsset, Word: 1, Text: "y"}}},
		"too many":           {Style: "chunk", Corrections: many},
		"fill not a colour":  {Style: "chunk", Fill: "red"},
		"accent with tags":   {Style: "keyword_emphasis", Accent: `#FFFFFF}{\fs999`},
		"keyword with tags":  {Style: "keyword_emphasis", Keywords: []string{`{\fs9}`}},
		"keyword two words":  {Style: "keyword_emphasis", Keywords: []string{"two words"}},
		"too many key words": {Style: "keyword_emphasis", Keywords: strings.Split("a b c d e f g h i j k l m", " ")},
	} {
		_, err := Compile(talkingSpec(timeline.Format9x16, caps), Final, captionResolver(nil))
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v, want ErrInvalidSpec", name, err)
		}
	}
}
