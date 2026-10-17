package render

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Caption chunking (design note section 5): a chunk takes at most max_words consecutive words, and a new chunk starts
// earlier when the speaker pauses for longer than maxGapMs, when a word ends a sentence, or when the next word comes from
// another clip, so one caption never spans a cut or a pause.
const maxGapMs = 600

// mappedWord is a transcript word placed on the output: the frames it is spoken on and the clip it came from.
type mappedWord struct {
	capWord
	clip int
}

// subtitles builds the script of the plan: the text items, the captions of an enabled captions track and, in a watermarked
// profile, the watermark. It returns nil when there is nothing to draw.
func (c *compiler) subtitles() (*Subtitles, error) {
	b := newBuilder(c.spec.Output.Width, c.spec.Output.Height, c.fps, c.total)
	out := c.spec.Output
	for _, t := range c.spec.Tracks {
		switch tr := t.(type) {
		case *timeline.TextTrack:
			for _, it := range tr.Items {
				pl, err := timeline.PlaceText(out, it)
				if err != nil {
					return nil, fmt.Errorf("%w: text item %s: %w", ErrInvalidSpec, it.ID, err)
				}
				box, _ := textBox(pl.Zone, pl.Box)
				anim := it.Animation
				if anim == "" {
					anim = pl.Style.Animation
				}
				b.addText(layerText, timeline.Frame(it.AtMs, c.fps), timeline.Frame(it.AtMs+it.DurationMs, c.fps), pl.Layout, pl.Zone, box, anim)
			}
		case *timeline.CaptionsTrack:
			if tr.Enabled {
				if err := c.captions(b, tr); err != nil {
					return nil, err
				}
			}
		}
	}
	if c.profile.Watermark {
		zone, ok := style.ZoneFor(string(out.Format), out.Width, out.Height)
		if !ok {
			return nil, fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
		}
		st := style.Watermark()
		lay := style.Measure(st, out.Height, "PREVIEW", style.MaxBoxWidth(zone, out.Width))
		w, h := lay.Box()
		box, _ := textBox(zone, style.PlaceWatermark(zone, w, h))
		b.addText(layerWatermark, 0, c.total, lay, zone, box, style.AnimNone)
	}
	return b.subtitles(), nil
}

// captions adds the captions of a captions track. Each clip contributes the words of its asset's transcript that it plays,
// mapped through the time map: a word at source time s of a clip that starts at output time t0 with source in point in and
// speed v is spoken at t0 + (s - in) / v, and both ends of the word are snapped to frames like every other boundary.
func (c *compiler) captions(b *builder, tr *timeline.CaptionsTrack) error {
	kind := tr.Style
	st, ok := style.CaptionStyle(kind)
	if !ok {
		return fmt.Errorf("%w: captions style %q", ErrInvalidSpec, kind)
	}
	out := c.spec.Output
	zone, ok := style.ZoneFor(string(out.Format), out.Width, out.Height)
	if !ok {
		return fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
	}
	upper, maxWords := tr.UppercaseOrDefault(), tr.MaxWordsOrDefault()
	for _, chunk := range chunks(c.mapWords(), maxWords, c.fps) {
		words := make([]string, len(chunk))
		for i, w := range chunk {
			words[i] = w.text
			if upper {
				words[i] = strings.ToUpper(w.text)
			}
		}
		text := strings.Join(words, " ")
		lay := style.Measure(st, out.Height, text, style.MaxBoxWidth(zone, out.Width))
		w, h := lay.Box()
		box, _ := textBox(zone, style.PlaceCaption(zone, out.Width, w, h))
		caps := make([]capWord, len(chunk))
		for i, w := range chunk {
			caps[i] = w.capWord
			caps[i].text = words[i]
		}
		b.addCaption(lay, box, caps, text)
	}
	return nil
}

// mapWords places the transcript words of every clip on the output, in output order. Words are cut to the part of the clip
// that plays: a word that straddles the clip's in or out point keeps the part inside; one entirely outside is dropped. A word
// is at least one frame long, and a word never overlaps the one before it by a frame: the later word starts where the
// earlier one ends if the speech overlaps in the transcript.
func (c *compiler) mapWords() []mappedWord {
	var all []mappedWord
	for ci, cl := range c.clips {
		in, out := cl.item.Source.InMs, cl.item.Source.OutMs
		speed := cl.item.SpeedOrDefault()
		t0 := timeline.FrameStartMs(cl.startFrame, c.fps)
		limit := min(cl.ownedEnd, c.total) // the incoming clip owns a transition, like the time map
		for _, w := range cl.asset.Transcript {
			text := cleanWord(w.Text)
			if text == "" || w.EndMs <= in || w.StartMs >= out {
				continue
			}
			s, e := max(w.StartMs, in), min(w.EndMs, out)
			startF := timeline.Frame(t0+roundDiv(float64(s-in), speed), c.fps)
			endF := timeline.Frame(t0+roundDiv(float64(e-in), speed), c.fps)
			startF = max(startF, cl.startFrame)
			if startF >= limit {
				continue
			}
			endF = min(max(endF, startF+1), limit)
			all = append(all, mappedWord{capWord: capWord{text: text, startF: startF, endF: endF}, clip: ci})
		}
	}
	slices.SortStableFunc(all, func(x, y mappedWord) int {
		switch {
		case x.startF < y.startF:
			return -1
		case x.startF > y.startF:
			return 1
		}
		return 0
	})
	for i := 1; i < len(all); i++ {
		prev, cur := &all[i-1], &all[i]
		if cur.startF < prev.endF {
			// Overlapping speech: the earlier word ends where the later one starts, and keeps at least a frame.
			prev.endF = max(cur.startF, prev.startF+1)
			if cur.startF < prev.endF {
				cur.startF = prev.endF
				cur.endF = max(cur.endF, cur.startF+1)
			}
		}
	}
	return all
}

// roundDiv is x / v rounded half up, in milliseconds.
func roundDiv(x, v float64) int64 { return int64(math.Floor(x/v + 0.5)) }

// cleanWord makes a transcript word safe to put in a script: control characters and line separators become spaces, runs of
// spaces collapse, the ends are trimmed. (Escape still guards the script syntax; this keeps odd transcripts from adding
// blank lines and double spaces to the layout.)
func cleanWord(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F || (r >= 0x80 && r < 0xA0) || r == 0x2028 || r == 0x2029 || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// chunks splits words into caption chunks (see maxGapMs).
func chunks(words []mappedWord, maxWords, fps int) [][]mappedWord {
	var out [][]mappedWord
	var cur []mappedWord
	for _, w := range words {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			gapMs := timeline.FrameStartMs(w.startF, fps) - timeline.FrameStartMs(prev.endF, fps)
			if len(cur) >= maxWords || gapMs > maxGapMs || w.clip != prev.clip || endsSentence(prev.text) {
				out = append(out, cur)
				cur = nil
			}
		}
		cur = append(cur, w)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// endsSentence reports whether a word ends with sentence punctuation.
func endsSentence(w string) bool {
	return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "!") || strings.HasSuffix(w, "?") || strings.HasSuffix(w, "…")
}
