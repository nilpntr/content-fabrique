package render

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/nilpntr/content-fabrique/internal/subject"
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
	// Code is text_dropped, caption_dropped, text_moved, text_retimed (shown at another time, where it is clear of the person) or
	// text_over_subject (left out: it would cover the person at every time).
	Code    string `json:"code"`
	Message string `json:"message"`
}

// subtitles builds the script of the plan: the text items, the captions of an enabled captions track and, in a watermarked
// profile, the watermark. It returns nil when there is nothing to draw. Nothing is ever drawn outside the safe zone: a box
// that cannot be put inside it is left out, with a warning.
func (c *compiler) subtitles() (*Subtitles, error) {
	b := newBuilder(c.spec.Output.Width, c.spec.Output.Height, c.fps, c.total)
	out := c.spec.Output
	// The subject of each asset the clips play, for keeping titles off the head.
	lookup := c.subjectLookup()
	for _, t := range c.spec.Tracks {
		switch tr := t.(type) {
		case *timeline.TextTrack:
			for _, it := range tr.Items {
				// A character the font cannot draw is left out of the text, as the layout never measured it.
				if pl, err := timeline.PlaceText(out, it); err == nil {
					if bad := pl.Style.Unsupported(it.Text); len(bad) > 0 {
						it.Text = dropUnsupported(pl.Style, it.Text)
					}
				}
				// Never over the person's body (P6-06): where there is no room at the item's own time, it is shown in the part of its time
				// that has room, or at the nearest time that has, or it is left out.
				fit, err := subject.FitText(c.spec, lookup, it, c.totalMs)
				if err != nil {
					return nil, fmt.Errorf("%w: text item %s: %w", ErrInvalidSpec, it.ID, err)
				}
				pl, av := fit.Placement, fit.Avoidance
				switch fit.How {
				case subject.FitDropped:
					what := "body"
					if av.OverHead {
						what = "head"
					}
					b.warn("text_over_subject", "text item %s is left out: it would cover the subject's %s and there is no room above the head, beside it or below the person at any time of the video", it.ID, what)
					continue
				case subject.FitTrimmed, subject.FitMoved:
					verb := "shortened"
					if fit.How == subject.FitMoved {
						verb = "moved"
					}
					b.warn("text_retimed", "text item %s was %s to %d-%d ms, where it fits clear of the subject (at %d-%d ms it would cover their body)",
						it.ID, verb, fit.AtMs, fit.AtMs+fit.DurationMs, it.AtMs, it.AtMs+it.DurationMs)
					it.AtMs, it.DurationMs = fit.AtMs, fit.DurationMs
				}
				switch {
				case av.Overlapped && av.Where == timeline.WhereSmaller:
					b.warn("text_moved", "text item %s was shrunk to scale %g to keep it clear of the subject", it.ID, av.Scale)
				case av.Overlapped:
					b.warn("text_moved", "text item %s was moved %s the subject to keep it clear of them", it.ID, av.Where)
				}
				box, ok := pl.Zone.Fit(pl.Box)
				if !ok {
					b.warn("text_dropped", "text item %s is left out: its %d x %d px box cannot be put inside the %s safe zone", it.ID, pl.Box.Width(), pl.Box.Height(), out.Format)
					continue
				}
				b.addText(layerText, timeline.Frame(it.AtMs, c.fps), timeline.Frame(it.AtMs+it.DurationMs, c.fps), pl.Layout, pl.Zone, box, it.AnimationOrDefault(), it.Exit, pl.Align)
			}
		case *timeline.CaptionsTrack:
			if tr.Enabled {
				if err := c.captions(b, tr, lookup); err != nil {
					return nil, err
				}
			}
		}
	}
	// The shot numbers of a proof go below the watermark, which takes the top right corner first.
	numbersBelow := 0
	if c.profile.Watermark {
		zone, ok := out.Zone()
		if !ok {
			return nil, fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
		}
		st := style.Watermark()
		lay := style.Measure(st, out.ShortSide(), "PREVIEW", style.MaxBoxWidth(zone, out.Width))
		w, h := lay.Box()
		if box, ok := zone.Fit(style.PlaceWatermark(zone, w, h)); ok {
			b.addText(layerWatermark, 0, c.total, lay, zone, box, style.AnimNone, style.ExitNone, style.AlignCenter)
			numbersBelow = box.Bottom - zone.Inner.Top
		}
	}
	if c.profile.ShotNumbers {
		if err := c.shotNumbers(b, numbersBelow); err != nil {
			return nil, err
		}
	}
	c.plan.Warnings = append(c.plan.Warnings, b.warnings...)
	return b.subtitles(), nil
}

// shotNumbers adds the number of every shot of a proof edit (docs/design/proof-edit-v1.md): 1, 2, 3 ... in the order the clips play
// (the order of timeline.Shots, which the shot list and the clip feedback use), each from the first frame of its shot to the first frame
// of the next, small and white with a thin dark outline, at the top right of the safe zone. below is how far down the watermark reaches
// from the top of the zone (0 without one): the numbers go under it. A number that cannot be put inside the safe zone is left out with a
// warning, like a text.
func (c *compiler) shotNumbers(b *builder, below int) error {
	out := c.spec.Output
	zone, ok := out.Zone()
	if !ok {
		return fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
	}
	st := style.ShotNumber()
	gap := 0
	if below > 0 {
		gap = int(math.Round(st.EmPx(out.ShortSide()) / 4))
	}
	for i, cl := range c.clips {
		n := strconv.Itoa(i + 1)
		lay := style.Measure(st, out.ShortSide(), n, style.MaxBoxWidth(zone, out.Width))
		w, h := lay.Box()
		top := zone.Inner.Top + below + gap
		box, ok := zone.Fit(style.Rect{Left: zone.Inner.Right - w, Top: top, Right: zone.Inner.Right, Bottom: top + h})
		if !ok {
			b.warn("shot_number_dropped", "the number of shot %s (%s) is left out: it does not fit inside the %s safe zone", n, cl.item.ID, out.Format)
			continue
		}
		b.addText(layerShotNumber, cl.startFrame, cl.ownedEnd, lay, zone, box, style.AnimNone, style.ExitNone, style.AlignRight)
	}
	return nil
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
// The words are the transcript's, with the creator's corrections in place of the words they fix (the timing stays the transcript's).
// The captions of a clip are drawn in the lower band, or in the upper one when the speaker's face is in the lower
// (subject.CaptionPlaces); both bands are inside the safe zone.
//
// A chunk is laid out in the caption style and must fit the caption band (style.CaptionBand: the centre column, two lines).
// One that does not is split in two and each half tried again; a single word that is still too big is shrunk down to
// captionFloor of the size, and left out with a warning if even that does not fit. So a caption is never drawn outside the zone.
func (c *compiler) captions(b *builder, tr *timeline.CaptionsTrack, lookup subject.Lookup) error {
	st, ok := style.CaptionStyleOf(tr.Style, tr.Fill, tr.Accent)
	if !ok {
		return fmt.Errorf("%w: captions style %q", ErrInvalidSpec, tr.Style)
	}
	// Hostile fields are refused here too: a spec that never went through Validate must not put markup into the script.
	if bad := timeline.ValidateCaptionFields(tr.Fill, tr.Accent, tr.Keywords, tr.Corrections); len(bad) > 0 {
		return fmt.Errorf("%w: captions track %s: %s", ErrInvalidSpec, tr.ID, strings.Join(bad, "; "))
	}
	out := c.spec.Output
	zone, ok := out.Zone()
	if !ok {
		return fmt.Errorf("%w: unknown format %q", ErrInvalidSpec, out.Format)
	}
	fixes := make(map[correctionKey]timeline.CaptionCorrection, len(tr.Corrections))
	for _, cor := range tr.Corrections {
		fixes[correctionKey{cor.AssetID, cor.Word}] = cor
	}
	c.warnStaleCorrections(b, fixes)
	upper, maxWords := tr.UppercaseOrDefault(), tr.MaxWordsOrDefault()
	// The band of each clip's captions, by the clip's id.
	places := map[string]subject.CaptionPlace{}
	for _, p := range subject.CaptionPlaces(c.spec, lookup, st) {
		places[p.ClipID] = p
		switch {
		case p.OverFace:
			b.warn("caption_over_face", "the captions of clip %s cover the speaker's face in both bands (%d to %d ms): reframe the clip or use another shot", p.ClipID, p.AtMs, p.EndMs)
		case p.Moved:
			b.warn("captions_moved", "the captions of clip %s are in the upper band, because the speaker's face is in the lower one (%d to %d ms)", p.ClipID, p.AtMs, p.EndMs)
		}
	}
	keys := keywordSet(tr.Keywords)
	var place func(chunk []mappedWord)
	place = func(chunk []mappedWord) {
		pl, ok := places[c.clips[chunk[0].clip].item.ID]
		if !ok {
			pl = subject.CaptionPlace{Pos: style.CaptionLower, Band: style.CaptionBand(zone, out.Width, out.ShortSide(), st)}
		}
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
		if st.Keyword {
			markKeywords(kept, keys)
		}
		text := strings.Join(words, " ")
		if lay, box, ok := fitCaption(st, zone, pl, out, text, 1); ok {
			b.addCaption(lay, box, kept, text)
			return
		}
		if len(chunk) > 1 {
			place(chunk[:len(chunk)/2])
			place(chunk[len(chunk)/2:])
			return
		}
		for _, scale := range []float64{0.9, 0.8, captionFloor} {
			if lay, box, ok := fitCaption(st, zone, pl, out, text, scale); ok {
				b.addCaption(lay, box, kept, text)
				return
			}
		}
		b.warn("caption_dropped", "the caption %q at %d ms is left out: it does not fit the caption band even at %d %% of the size", text, timeline.FrameStartMs(kept[0].startF, c.fps), int(captionFloor*100))
	}
	for _, chunk := range chunks(c.mapWordsOf(nil, fixes), maxWords, c.fps) {
		place(chunk)
	}
	return nil
}

// fixApplies says a fix still belongs to the word the transcript has there: it remembers the word it was made for (Was), and a
// transcript that was made again may have another word at that position. A fix without Was always applies.
func fixApplies(fix timeline.CaptionCorrection, heard string) bool {
	return fix.Was == "" || cleanWord(fix.Was) == heard
}

// warnStaleCorrections warns once for the fixes that name a word the transcripts of the clips no longer have (another word, or none).
func (c *compiler) warnStaleCorrections(b *builder, fixes map[correctionKey]timeline.CaptionCorrection) {
	seen := map[string]bool{}
	for _, cl := range c.clips {
		id := cl.item.AssetID
		if seen[id] {
			continue
		}
		seen[id] = true
		for wi, w := range cl.asset.Transcript {
			if fix, ok := fixes[correctionKey{id, wi}]; ok && !fixApplies(fix, cleanWord(w.Text)) {
				b.warn("correction_stale", "the fix of word %d of %s (%q) is left out: the transcript now has %q there, not %q", wi, id, fix.Text, cleanWord(w.Text), fix.Was)
			}
		}
	}
}

// correctionKey names one word of one asset's transcript.
type correctionKey struct {
	assetID string
	word    int
}

// keywordSet is the key words of a keyword_emphasis track as a set of lower-case words.
func keywordSet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		if w = normaliseWord(w); w != "" {
			set[w] = true
		}
	}
	return set
}

// normaliseWord lower-cases a word and strips what is not a letter or a digit from its ends, so "Heavy," matches "heavy".
func normaliseWord(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
}

// minKeywordRunes is the shortest word the fallback emphasises when the track names no key words.
const minKeywordRunes = 5

// markKeywords marks the words of a chunk that keyword emphasis draws in the accent colour: those the track lists, or, when it lists
// none, the longest word of the chunk (the first of equals) if it has at least minKeywordRunes letters.
func markKeywords(words []capWord, keys map[string]bool) {
	if len(keys) > 0 {
		for i := range words {
			words[i].emph = keys[normaliseWord(words[i].text)]
		}
		return
	}
	best, bestLen := -1, minKeywordRunes-1
	for i, w := range words {
		if n := len([]rune(normaliseWord(w.text))); n > bestLen {
			best, bestLen = i, n
		}
	}
	if best >= 0 {
		words[best].emph = true
	}
}

// fitCaption lays text out in the caption style scaled by scale, and puts it in the zone in the band of the place. ok is false
// when it takes more lines than the band holds or does not fit the zone. A scaled style has its own id (the script needs a Style
// line for it).
func fitCaption(st style.Style, zone style.Zone, pl subject.CaptionPlace, out timeline.Output, text string, scale float64) (style.Layout, style.Rect, bool) {
	if scale != 1 {
		st.ID = fmt.Sprintf("%s_%d", st.ID, int(scale*100))
		st.EmPct *= scale
	}
	lay := style.Measure(st, out.ShortSide(), text, style.MaxBoxWidth(zone, out.Width))
	w, h := lay.Box()
	if h > pl.Band.Height() {
		return lay, style.Rect{}, false
	}
	box, ok := zone.Fit(style.PlaceCaptionAt(zone, out.Width, w, h, pl.Pos))
	return lay, box, ok
}

// mapWords places the transcript words of every clip on the output, in output order. Words are cut to the part of the clip
// that plays: a word that straddles the clip's in or out point keeps the part inside; one entirely outside is dropped. A word
// is at least one frame long, and a word never overlaps the one before it by a frame: the later word starts where the
// earlier one ends if the speech overlaps in the transcript.

// mapWordsOf is mapWords for the clips keep accepts (nil: all of them), with the creator's fixes in place of the words they correct
// (nil: the transcript as it is; only the captions pass fixes, so the timing readers such as the sound effects never see them). The overlap of two words is resolved among the words that
// are kept, so a clip that is left out does not shorten the words of the others.
func (c *compiler) mapWordsOf(keep func(clipIndex int) bool, fixes map[correctionKey]timeline.CaptionCorrection) []mappedWord {
	var all []mappedWord
	for ci, cl := range c.clips {
		if keep != nil && !keep(ci) {
			continue
		}
		in, out := cl.item.Source.InMs, cl.item.Source.OutMs
		it := cl.item
		t0 := timeline.FrameStartMs(cl.startFrame, c.fps)
		limit := min(cl.endFrame, c.total) // the clip's own audio plays to its end, also through a transition
		for wi, w := range cl.asset.Transcript {
			text := cleanWord(w.Text)
			if fix, ok := fixes[correctionKey{cl.item.AssetID, wi}]; ok && fixApplies(fix, text) {
				text = cleanWord(fix.Text) // the creator's fix of a misheard word; the timing stays the transcript's
			}
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

// subjectLookup is the subject of every asset on screen, the clips' and the cutaways' (a cutaway covers the clip under it, and
// subject.Regions reads its asset there), or nil when no asset has one.
func (c *compiler) subjectLookup() subject.Lookup {
	subjects := subject.Index{}
	for _, cl := range c.clips {
		if cl.asset.Subject != nil {
			subjects[cl.item.AssetID] = *cl.asset.Subject
		}
	}
	for _, cw := range c.cutaways {
		if cw.asset.Subject != nil {
			subjects[cw.item.AssetID] = *cw.asset.Subject
		}
	}
	if len(subjects) == 0 {
		return nil
	}
	return subjects.Lookup
}
