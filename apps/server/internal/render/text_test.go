package render

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// dialogue is a parsed Dialogue line of a generated script.
type dialogue struct {
	layer      int
	start, end int64 // centiseconds
	style      string
	text       string
}

func parseDialogues(t testing.TB, ass string) []dialogue {
	t.Helper()
	var out []dialogue
	for _, l := range strings.Split(ass, "\n") {
		rest, ok := strings.CutPrefix(l, "Dialogue: ")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, ",", 10)
		if len(f) != 10 {
			t.Fatalf("bad Dialogue line %q", l)
		}
		layer, _ := strconv.Atoi(f[0])
		out = append(out, dialogue{layer: layer, start: parseCs(t, f[1]), end: parseCs(t, f[2]), style: f[3], text: f[9]})
	}
	return out
}

func parseCs(t testing.TB, s string) int64 {
	t.Helper()
	var h, m, sec, cs int64
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &cs); err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return ((h*60+m)*60+sec)*100 + cs
}

// firstFrameAt returns the first frame whose timestamp is at or after cs centiseconds: the frame an event that starts at cs
// first shows on, and the first frame it is gone on when cs is its end. The timestamp is computed the two ways ffmpeg versions
// differ in (truncated and rounded to a millisecond); both must agree.
func firstFrameAt(t testing.TB, cs int64, fps int) int64 {
	t.Helper()
	find := func(ts func(f int64) int64) int64 {
		f := int64(0)
		for ts(f) < cs*10 {
			f++
		}
		return f
	}
	trunc := find(func(f int64) int64 { return f * 1000 / int64(fps) })
	round := find(func(f int64) int64 { return int64(math.Floor(float64(f)*1000/float64(fps) + 0.5)) })
	if trunc != round {
		t.Fatalf("an event boundary at %d cs lies on a frame boundary at %d fps: truncating and rounding timestamps disagree (frames %d and %d)", cs, fps, trunc, round)
	}
	return trunc
}

func fixtureSpecAndPlan(t *testing.T, name string, prof Profile) (*timeline.Spec, Plan) {
	t.Helper()
	cat := testAssets(t)
	spec := loadFixture(t, name, cat)
	plan, err := Compile(spec, prof, fakeResolver(cat))
	if err != nil {
		t.Fatal(err)
	}
	return spec, plan
}

// expWord is a word placed on the output by the test's own mapping.
type expWord struct {
	capWord
	item string
}

// expectedWordFrames maps the fixture transcript through the plan's time map and the spec's clips, the way the design note
// says: each word of a clip's asset that the clip plays is spoken at out_start + (source time - in) / speed, snapped to a
// frame, and a clip owns the frames of its time map entry.
func expectedWordFrames(spec *timeline.Spec, plan Plan, cat timeline.MemoryCatalog) []expWord {
	main, _, _ := timeline.MainTrack(spec)
	items := map[string]timeline.VideoItem{}
	for _, it := range main.Items {
		items[it.ID] = it
	}
	var out []expWord
	for _, e := range plan.TimeMap {
		if e.SourceInMs == nil {
			continue
		}
		it := items[e.ItemID]
		in, srcOut, speed := it.Source.InMs, it.Source.OutMs, *e.Speed
		first, limit := timeline.Frame(e.OutStartMs, plan.FPS), timeline.Frame(timeline.EndMs(it), plan.FPS) // a clip words last as long as its own audio, through a transition
		for _, w := range fixtureTranscript(e.AssetID, cat[e.AssetID].DurationMs) {
			if w.EndMs <= in || w.StartMs >= srcOut {
				continue
			}
			s := e.OutStartMs + int64(math.Floor(float64(max(w.StartMs, in)-in)/speed+0.5))
			en := e.OutStartMs + int64(math.Floor(float64(min(w.EndMs, srcOut)-in)/speed+0.5))
			sf := max(timeline.Frame(s, plan.FPS), first)
			if sf >= limit {
				continue
			}
			ef := min(max(timeline.Frame(en, plan.FPS), sf+1), limit)
			out = append(out, expWord{capWord: capWord{text: w.Text, startF: sf, endF: ef}, item: e.ItemID})
		}
	}
	return out
}

// Captions stay in sync with the words within one frame (here: exactly), across speed changes, a cut and a crossfade. The
// expected frames come from the time map and the fixture transcript; the actual ones from parsing the generated script and
// asking which frame each event first shows on.
func TestCaptionSync(t *testing.T) {
	cat := testAssets(t)
	for _, name := range []string{"v34_captions_speed_cut_9x16", "v35_captions_highlight_16x9", "v23_captions_word_highlight"} {
		t.Run(name, func(t *testing.T) {
			spec, plan := fixtureSpecAndPlan(t, name, Final)
			if plan.Subtitles == nil || plan.Subtitles.CaptionEvents == 0 {
				t.Fatalf("no caption events: %+v", plan.Subtitles)
			}
			var captions *timeline.CaptionsTrack
			for _, tr := range spec.Tracks {
				if c, ok := tr.(*timeline.CaptionsTrack); ok {
					captions = c
				}
			}
			upper := captions.UppercaseOrDefault()
			// A caption event shows, for each word, the chunk's text with that word lit; match words by their frame range.
			words := expectedWordFrames(spec, plan, cat)
			evs := parseDialogues(t, plan.Subtitles.ASS)
			matched := 0
			for _, e := range evs {
				if e.layer != layerCaption {
					continue
				}
				gotStart := firstFrameAt(t, e.start, plan.FPS)
				gotEnd := firstFrameAt(t, e.end, plan.FPS)
				if inTransition(plan, gotStart) || inTransition(plan, gotEnd) {
					continue // both clips speak there; TestCaptionsDuringACrossfade covers it
				}
				if gotEnd <= gotStart {
					t.Errorf("event %q is empty: frames %d to %d", e.text, gotStart, gotEnd)
				}
				if captions.Style == style.CaptionWordHighlight {
					// The lit word is the text between \1c-on and \1c-off tags (yellow is &H0AD6FF&).
					m := regexp.MustCompile(`\{\\1c&H0AD6FF&\}([^{]*)\{`).FindStringSubmatch(e.text)
					if m == nil {
						continue // a gap event: nothing is lit
					}
					lit := m[1]
					found := false
					for _, w := range words {
						text := w.text
						if upper {
							text = strings.ToUpper(text)
						}
						if text == lit && w.startF == gotStart {
							found = true
							if w.endF != gotEnd {
								t.Errorf("word %q: ends on frame %d, the script ends it on %d", lit, w.endF, gotEnd)
							}
						}
					}
					if !found {
						t.Errorf("word %q: no expected word starts on frame %d (script)", lit, gotStart)
					}
					matched++
				}
			}
			if captions.Style == style.CaptionWordHighlight && matched == 0 {
				t.Fatal("no lit word events found")
			}
			if captions.Style == style.CaptionChunk {
				// Chunks: the first event starts with a word and the last ends with one; every chunk start and end is a word boundary.
				starts, ends := map[int64]bool{}, map[int64]bool{}
				for _, w := range words {
					starts[w.startF], ends[w.endF] = true, true
				}
				for _, e := range evs {
					if e.layer != layerCaption {
						continue
					}
					if inTransition(plan, firstFrameAt(t, e.start, plan.FPS)) || inTransition(plan, firstFrameAt(t, e.end, plan.FPS)) {
						continue
					}
					if s := firstFrameAt(t, e.start, plan.FPS); !starts[s] {
						t.Errorf("chunk %q starts on frame %d, which no word starts on", e.text, s)
					}
					if en := firstFrameAt(t, e.end, plan.FPS); !ends[en] && en != plan.TotalFrames {
						t.Errorf("chunk %q ends on frame %d, which no word ends on", e.text, en)
					}
					matched++
				}
			}
			if matched == 0 {
				t.Fatal("nothing matched")
			}
		})
	}
}

// The mapping of words to frames: with a speed change the word times scale, with a cut the second clip's words start at its own
// out_start, and a word outside the clip's source range is dropped.
func TestCaptionMapping(t *testing.T) {
	cat := testAssets(t)
	spec, plan := fixtureSpecAndPlan(t, "v34_captions_speed_cut_9x16", Final)
	words := expectedWordFrames(spec, plan, cat)
	// c2 plays asset B from 5000 ms at speed 2 and starts at 2700 ms (frame 81): the word at source 5040 ms (index 12) is
	// spoken at 2700 + 20 = 2720 ms, frame 82.
	found := false
	for _, w := range words {
		if w.startF == 82 {
			found = true
		}
	}
	if !found {
		t.Errorf("no word starts on frame 82 (c2, speed 2): %+v", words)
	}
	// c3 plays asset A from 20000 ms at half speed from 4200 ms (frame 126): words are twice as far apart there: 840 ms.
	var c3 []int64
	for _, w := range words {
		if w.item == "c3" {
			c3 = append(c3, w.startF)
		}
	}
	if len(c3) < 3 || c3[2]-c3[1] != 25 && c3[2]-c3[1] != 26 {
		t.Errorf("at half speed words are 840 ms (25 or 26 frames) apart, got %v", c3)
	}
}

// Text that tries to inject override tags is written literally: every backslash is followed by a word joiner, and braces are
// escaped, so the only tags in a Dialogue line are the ones the generator wrote.
func TestEscaping(t *testing.T) {
	spec := &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{
			&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 3000}, AtMs: 0}}},
			&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{
				{ID: "t1", AtMs: 0, DurationMs: 1000, Text: `{\pos(0,0)}{\fs200}x\Ny\hz\{}`, Style: "title_bold", Anchor: "center"},
				{ID: "t2", AtMs: 1000, DurationMs: 1000, Text: `{\an9}typed`, Style: "title_bold", Anchor: "center", Animation: "typewriter"},
			}},
		},
	}
	plan, err := Compile(spec, Final, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	tagRe := regexp.MustCompile(`\{[^{}]*\}`)
	for _, d := range parseDialogues(t, plan.Subtitles.ASS) {
		head := tagRe.FindString(d.text)
		if !strings.HasPrefix(head, `{\an5\pos(`) {
			t.Errorf("event does not start with the generator's tags: %q", d.text)
		}
		body := strings.TrimPrefix(d.text, head)
		// Remove what the generator itself writes: typewriter blocks and escaped sequences, then nothing may be left that
		// looks like a tag or an override.
		body = regexp.MustCompile(`\{\\1a&HFF&[^{}]*\}`).ReplaceAllString(body, "")
		body = strings.NewReplacer("\\\u2060", "", `\{`, "", `\}`, "", `\N`, "").Replace(body)
		if strings.ContainsAny(body, `{}\`) {
			t.Errorf("unescaped syntax characters left in %q (body %q)", d.text, body)
		}
	}
	if got := Escape("a{b}c\\Nd\n"); got != "a\\{b\\}c\\\u2060Nd\\N" {
		t.Errorf("Escape = %q", got)
	}
	// A transcript word is data too.
	if cleanWord("he\x00llo  \tworld") != "he llo world" {
		t.Errorf("cleanWord = %q", cleanWord("he\x00llo  \tworld"))
	}
}

// Property: for random text, anchor or position, style and format, a text item either gets unsafe_text from Validate or its
// box is inside the safe zone and clear of the blocked areas; and the renderer draws a safe item exactly where the
// validator measured it, and every position an animation passes through is safe too.
func TestPropertyTextIsSafeOrReported(t *testing.T) {
	cat := testAssets(t)
	r := rand.New(rand.NewPCG(7, 11))
	formats := []timeline.Format{timeline.Format9x16, timeline.Format1x1, timeline.Format4x5, timeline.Format16x9}
	anchors := []string{timeline.AnchorUpperThird, timeline.AnchorCenter, timeline.AnchorLowerThird}
	anims := []string{"", "none", "pop_in", "fade", "typewriter", "slide_up"}
	words := []string{"Back", "day", "WWWWWWWW", "i", "Squat", "deeper!", "Ünïcödé", "5x5", "a", "supercalifragilisticexpialidocious", "{x}", `\N`}
	styles := style.IDs()
	safeCount, unsafeCount := 0, 0
	for i := 0; i < 600; i++ {
		f := formats[r.IntN(len(formats))]
		w, h, _ := f.Size()
		var parts []string
		for l, lines := 0, 1+r.IntN(3); l < lines; l++ {
			var ws []string
			for n, k := 0, 1+r.IntN(6); n < k; n++ {
				ws = append(ws, words[r.IntN(len(words))])
			}
			parts = append(parts, strings.Join(ws, " "))
		}
		it := timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1500, Text: strings.Join(parts, "\n"), Style: styles[r.IntN(len(styles))], Animation: anims[r.IntN(len(anims))]}
		if r.IntN(2) == 0 {
			it.Anchor = anchors[r.IntN(len(anchors))]
		} else {
			it.Position = &timeline.Point{X: float64(r.IntN(101)) / 100, Y: float64(r.IntN(101)) / 100}
		}
		if len([]rune(it.Text)) > 120 {
			continue
		}
		spec := &timeline.Spec{
			Schema: timeline.SchemaVersion,
			Output: timeline.Output{Format: f, Width: w, Height: h, FPS: 30, Background: "#000000"},
			Tracks: []timeline.Track{
				&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 3000}, AtMs: 0}}},
				&timeline.TextTrack{ID: "text", Items: []timeline.TextItem{it}},
			},
		}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		res := timeline.Validate(data, cat)
		unsafe := slices.Contains(res.Codes(), timeline.CodeUnsafeText)
		for _, c := range res.Codes() {
			if c != timeline.CodeUnsafeText {
				t.Fatalf("case %d: unexpected issue %v in %s", i, res.Issues, data)
			}
		}
		pl, err := timeline.PlaceText(spec.Output, it)
		if err != nil {
			t.Fatal(err)
		}
		if safe := pl.Zone.Safe(pl.Box); safe == unsafe {
			t.Fatalf("case %d: Validate says unsafe=%v but the box %+v is safe=%v (zone %+v)\n%s", i, unsafe, pl.Box, safe, pl.Zone, data)
		}
		if unsafe {
			unsafeCount++
			continue
		}
		safeCount++
		plan, err := Compile(spec, Final, plainResolver())
		if err != nil {
			t.Fatal(err)
		}
		d := parseDialogues(t, plan.Subtitles.ASS)[0]
		cx, cy := centre(pl.Box)
		if !strings.Contains(d.text, fmt.Sprintf(`\an5\pos(%s,%s)`, num(cx), num(cy))) && !strings.Contains(d.text, fmt.Sprintf(`\move(%s,`, num(cx))) {
			t.Fatalf("case %d: the renderer moved a safe item: want centre (%v,%v) in %q", i, cx, cy, d.text)
		}
		// The slide starts lower and stays safe.
		if m := regexp.MustCompile(`\\move\(([\d.]+),([\d.]+),`).FindStringSubmatch(d.text); m != nil {
			y0, _ := strconv.ParseFloat(m[2], 64)
			dy := int(y0 - cy)
			moved := pl.Box
			moved.Top, moved.Bottom = moved.Top+dy, moved.Bottom+dy
			if !pl.Zone.Safe(moved) {
				t.Fatalf("case %d: slide_up starts outside the safe zone: %+v", i, moved)
			}
		}
	}
	if safeCount < 100 || unsafeCount < 100 {
		t.Errorf("the generator is lopsided: %d safe, %d unsafe", safeCount, unsafeCount)
	}
}

// An event boundary written for frame n is first seen on frame n, at every supported frame rate, whether the filter truncates or
// rounds the frame's timestamp to a millisecond.
func TestEventTimesLandOnTheirFrame(t *testing.T) {
	for _, fps := range []int{24, 25, 30, 60} {
		b := newBuilder(1080, 1920, fps, 1<<40)
		for f := int64(0); f < 20000; f++ {
			cs := parseCs(t, b.assTime(f))
			if got := firstFrameAt(t, cs, fps); got != f {
				t.Fatalf("%d fps: frame %d is written as %s, which is first seen on frame %d", fps, f, b.assTime(f), got)
			}
		}
	}
}
