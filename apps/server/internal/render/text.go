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

// Warning is something the renderer left out of the picture because it could not be drawn safely. Compile does not fail for it:
// the rest of the render is fine, and the caller shows the warning (the editor agent reads it).
type Warning struct {
	// Code is text_dropped or caption_dropped.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// subtitles builds the script of the plan: the text items, the captions of an enabled captions track and, in a watermarked
// profile, the watermark. It returns nil when there is nothing to draw. Nothing is ever drawn outside the safe zone: a box
// that cannot be put inside it is left out, with a warning.
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
				// A character the font cannot draw is left out of the text, as the layout never measured it.
				if bad := pl.Style.Unsupported(it.Text); len(bad) > 0 {
					it.Text = dropUnsupported(pl.Style, it.Text)
					if pl, err = timeline.PlaceText(out, it); err != nil {
						return nil, fmt.Errorf("%w: text item %s: %w", ErrInvalidSpec, it.ID, err)
					}
				}
				box, ok := pl.Zone.Fit(pl.Box)
				if !ok {
					b.warn("text_dropped", "text item %s is left out: its %d x %d px box cannot be put inside the %s safe zone", it.ID, pl.Box.Width(), pl.Box.Height(), out.Format)
					continue
				}
				b.addText(layerText, timeline.Frame(it.AtMs, c.fps), timeline.Frame(it.AtMs+it.DurationMs, c.fps), pl.Layout, pl.Zone, box, it.AnimationOrDefault())
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
		zone, ok := out.Zone()
		if !ok {
			return nil, fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
		}
		st := style.Watermark()
		lay := style.Measure(st, out.ShortSide(), "PREVIEW", style.MaxBoxWidth(zone, out.Width))
		w, h := lay.Box()
		if box, ok := zone.Fit(style.PlaceWatermark(zone, w, h)); ok {
			b.addText(layerWatermark, 0, c.total, lay, zone, box, style.AnimNone)
		}
	}
	c.plan.Warnings = b.warnings
	return b.subtitles(), nil
}

// dropUnsupported removes the characters the style's font has no glyph for.
func dropUnsupported(st style.Style, s string) string {
	return strings.Map(func(r rune) rune {
		if st.Supports(r) {
			return r
		}
		return -1
	}, s)
}

// captionFloor is how small a caption may be shrunk to fit its band: 70 % of the style's size.
const captionFloor = 0.7

// captions adds the captions of a captions track. Each clip contributes the words of its asset's transcript that it plays,
// mapped through the time map: a word at source time s of a clip that starts at output time t0 with source in point in and
// speed v is spoken at t0 + (s - in) / v, and both ends of the word are snapped to frames like every other boundary.
//
// A chunk is laid out in the caption style and must fit the caption band (style.CaptionBand: the centre column, three lines).
// One that does not is split in two and each half tried again; a single word that is still too big is shrunk down to
// captionFloor of the size, and left out with a warning if even that does not fit. So a caption is never drawn outside the zone.
func (c *compiler) captions(b *builder, tr *timeline.CaptionsTrack) error {
	st, ok := style.CaptionStyle(tr.Style)
	if !ok {
		return fmt.Errorf("%w: captions style %q", ErrInvalidSpec, tr.Style)
	}
	out := c.spec.Output
	zone, ok := out.Zone()
	if !ok {
		return fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
	}
	upper, maxWords := tr.UppercaseOrDefault(), tr.MaxWordsOrDefault()
	band := style.CaptionBand(zone, out.Width, out.ShortSide(), st)
	var place func(chunk []mappedWord)
	place = func(chunk []mappedWord) {
		words := make([]string, 0, len(chunk))
		kept := make([]capWord, 0, len(chunk))
		for _, w := range chunk {
			text := w.text
			if upper {
				text = strings.ToUpper(text)
			}
			if text = dropUnsupported(st, text); text != "" {
				words = append(words, text)
				kept = append(kept, capWord{text: text, startF: w.startF, endF: w.endF})
			}
		}
		if len(kept) == 0 {
			return
		}
		text := strings.Join(words, " ")
		if lay, box, ok := fitCaption(st, zone, band, out, text, 1); ok {
			b.addCaption(lay, box, kept, text)
			return
		}
		if len(chunk) > 1 {
			place(chunk[:len(chunk)/2])
			place(chunk[len(chunk)/2:])
			return
		}
		for _, scale := range []float64{0.9, 0.8, captionFloor} {
			if lay, box, ok := fitCaption(st, zone, band, out, text, scale); ok {
				b.addCaption(lay, box, kept, text)
				return
			}
		}
		b.warn("caption_dropped", "the caption %q at %d ms is left out: it does not fit the caption band even at %d %% of the size", text, timeline.FrameStartMs(kept[0].startF, c.fps), int(captionFloor*100))
	}
	for _, chunk := range chunks(c.mapWords(), maxWords, c.fps) {
		place(chunk)
	}
	return nil
}

// fitCaption lays text out in the caption style scaled by scale, and puts it in the zone at the caption position. ok is false
// when it takes more lines than the band holds or does not fit the zone. A scaled style has its own id (the script needs a Style
// line for it).
func fitCaption(st style.Style, zone style.Zone, band style.Rect, out timeline.Output, text string, scale float64) (style.Layout, style.Rect, bool) {
	if scale != 1 {
		st.ID = fmt.Sprintf("%s_%d", st.ID, int(scale*100))
		st.EmPct *= scale
	}
	lay := style.Measure(st, out.ShortSide(), text, style.MaxBoxWidth(zone, out.Width))
	w, h := lay.Box()
	if h > band.Height() {
		return lay, style.Rect{}, false
	}
	box, ok := zone.Fit(style.PlaceCaption(zone, out.Width, w, h))
	return lay, box, ok
}

// mapWords places the transcript words of every clip on the output, in output order. Words are cut to the part of the clip
// that plays: a word that straddles the clip's in or out point keeps the part inside; one entirely outside is dropped. A word
// is at least one frame long, and a word never overlaps the one before it by a frame: the later word starts where the
// earlier one ends if the speech overlaps in the transcript.
func (c *compiler) mapWords() []mappedWord {
	var all []mappedWord
	for ci, cl := range c.clips {
		in, out := cl.item.Source.InMs, cl.item.Source.OutMs
		it := cl.item
		t0 := timeline.FrameStartMs(cl.startFrame, c.fps)
		limit := min(cl.endFrame, c.total) // the clip's own audio plays to its end, also through a transition
		for _, w := range cl.asset.Transcript {
			text := cleanWord(w.Text)
			if text == "" || w.EndMs <= in || w.StartMs >= out {
				continue
			}
			s, e := max(w.StartMs, in), min(w.EndMs, out)
			startF := timeline.Frame(t0+int64(math.Floor(it.OutOffsetMs(float64(s-in))+0.5)), c.fps)
			endF := timeline.Frame(t0+int64(math.Floor(it.OutOffsetMs(float64(e-in))+0.5)), c.fps)
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
