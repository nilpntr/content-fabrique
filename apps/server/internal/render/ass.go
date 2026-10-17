package render

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The ASS script of a render (design note section 5): every text item, every caption and the preview watermark is a Dialogue
// event of one generated script, burned in with the ass filter.
//
//   - PlayRes is the spec's output size, whatever the profile: the preview profile renders at half size and libass scales the
//     script to the frame, so the layout (and the safe-zone check that goes with it) is the same in every profile.
//   - Every event places its text with \an5\pos at the centre of the box the shared layout computed (timeline.PlaceText), and
//     breaks lines itself with \N (WrapStyle 2 turns libass's own wrapping off), so what libass draws is what the validator
//     measured.
//   - The text of the spec and the words of a transcript are data. Escape writes them so that \ { and } cannot start an
//     override tag, whatever they hold.
//   - Event times are written between two frames, not on them, so that the centisecond rounding of the format and the
//     millisecond rounding of the filter's timestamps can never move an event by a frame.

const (
	subtitleFile = "subs.ass"
	fontsDirName = "fonts"
)

// Subtitles is the generated script of a render and what the sandbox needs next to it. Execute writes ASS to File in the work
// directory and copies Fonts into FontsDir there; the ass filter in the graph reads them by those relative names.
type Subtitles struct {
	File     string   `json:"file"`
	FontsDir string   `json:"fonts_dir"`
	Fonts    []string `json:"fonts"`
	ASS      string   `json:"ass"`
	// TextEvents, CaptionEvents and Watermark count the events by kind.
	TextEvents    int  `json:"text_events"`
	CaptionEvents int  `json:"caption_events"`
	Watermark     bool `json:"watermark"`
}

// event is one Dialogue line before it is written.
type event struct {
	layer      int
	start, end int64 // frames; the event shows on [start, end)
	style      string
	text       string
	seq        int
}

// builder collects the events of a script.
type builder struct {
	w, h, fps int
	total     int64
	used      map[string]style.Style
	events    []event
	counts    struct{ text, caption int }
	watermark bool
}

func newBuilder(w, h, fps int, total int64) *builder {
	return &builder{w: w, h: h, fps: fps, total: total, used: map[string]style.Style{}}
}

// Layers: the watermark above the text above the captions.
const (
	layerCaption   = 0
	layerText      = 1
	layerWatermark = 2
)

func (b *builder) add(layer int, start, end int64, st style.Style, text string) {
	end = min(end, b.total)
	if end <= start || start >= b.total || start < 0 {
		return
	}
	b.used[st.ID] = st
	b.events = append(b.events, event{layer: layer, start: start, end: end, style: st.ID, text: text, seq: len(b.events)})
}

// textBox gives the centre of the box the renderer draws: the validator's box, clamped into the safe zone the shortest way.
// The clamp is a safety net: Validate reports a position that needs it, and a spec that passed validation is never moved.
func textBox(zone style.Zone, box style.Rect) (style.Rect, bool) {
	if fit, ok := zone.Fit(box); ok {
		return fit, true
	}
	return box, false
}

// centre returns the centre of a box as the ASS tags write it.
func centre(box style.Rect) (x, y float64) {
	return float64(box.Left+box.Right) / 2, float64(box.Top+box.Bottom) / 2
}

// addText adds a text item (or the watermark) at a box: the animation tags, then the lines.
func (b *builder) addText(layer int, start, end int64, lay style.Layout, zone style.Zone, box style.Rect, anim string) {
	st := lay.Style
	cx, cy := centre(box)
	durMs := frameMs(end, b.fps) - frameMs(start, b.fps)
	var tags strings.Builder
	fmt.Fprintf(&tags, `\an5`)
	pos := fmt.Sprintf(`\pos(%s,%s)`, num(cx), num(cy))
	body := escapeLines(lay.Lines)
	switch anim {
	case style.AnimFade:
		fad := fadeMs(durMs)
		fmt.Fprintf(&tags, `%s\fad(%d,%d)`, pos, fad, fad)
	case style.AnimPopIn:
		fmt.Fprintf(&tags, `%s\fscx60\fscy60\t(0,180,0.6,\fscx100\fscy100)\fad(%d,0)`, pos, min(100, fadeMs(durMs)))
	case style.AnimSlideUp:
		d := min(int(math.Floor(0.02*float64(b.h)+0.5)), zone.SlideRoom(box))
		if d > 0 {
			fmt.Fprintf(&tags, `\move(%s,%s,%s,%s,0,250)\fad(%d,0)`, num(cx), num(cy+float64(d)), num(cx), num(cy), min(120, fadeMs(durMs)))
		} else {
			fmt.Fprintf(&tags, `%s\fad(%d,0)`, pos, min(120, fadeMs(durMs)))
		}
	case style.AnimTypewriter:
		tags.WriteString(pos)
		body = typewriter(lay, durMs)
	default:
		tags.WriteString(pos)
	}
	b.add(layer, start, end, st, "{"+tags.String()+"}"+body)
	if layer == layerWatermark {
		b.watermark = true
	} else {
		b.counts.text++
	}
}

// fadeMs is the length of a fade in or out: 150 ms, at most a quarter of the event.
func fadeMs(durMs int64) int { return int(max(0, min(150, durMs/4))) }

// frameMs is the start of a frame in whole milliseconds.
func frameMs(frame int64, fps int) int64 { return (2*frame*1000 + int64(fps)) / (2 * int64(fps)) }

// typewriter reveals the characters one after the other: every character starts fully transparent and a \t switches it on at
// its time. The characters keep their places the whole time, so the block does not move while it types. Typing takes at most
// 60 % of the event and at most 45 ms a character.
func typewriter(lay style.Layout, durMs int64) string {
	st := lay.Style
	n := 0
	for _, l := range lay.Lines {
		for _, r := range l {
			if r != ' ' {
				n++
			}
		}
	}
	if n == 0 {
		return escapeLines(lay.Lines)
	}
	step := max(20, min(45, int(float64(durMs)*0.6)/n))
	hide := `\1a&HFF&\3a&HFF&\4a&HFF&`
	show := fmt.Sprintf(`\1a%s\3a%s\4a%s`, alpha(st.Fill), alpha(st.Outline), alpha(st.Shadow))
	var sb strings.Builder
	i := 0
	for li, l := range lay.Lines {
		if li > 0 {
			sb.WriteString(`\N`)
		}
		for _, r := range l {
			if r == ' ' {
				sb.WriteRune(' ')
				continue
			}
			t := i * step
			fmt.Fprintf(&sb, `{%s\t(%d,%d,%s)}%s`, hide, t, t+1, show, escape(r))
			i++
		}
	}
	return sb.String()
}

// alpha is the ASS alpha override value of a colour: &HAA&, where AA is the transparency.
func alpha(c style.Colour) string { return fmt.Sprintf("&H%02X&", 255-c.A) }

// colourRGB is the ASS colour override value without alpha: &HBBGGRR&.
func colourRGB(c style.Colour) string { return fmt.Sprintf("&H%02X%02X%02X&", c.B, c.G, c.R) }

// escape writes one character of user text for the ASS Text field. A backslash would start an override or a line break
// (\N, \h), so it is followed by a word joiner, which is invisible and has no width, and no backslash is ever followed by
// anything that could make it a tag; braces are escaped the way libass reads them (\{ and \}); control characters and the
// Unicode line separators, which a script cannot hold or which end a line, become spaces.
func escape(r rune) string {
	switch {
	case r == '\\':
		return "\\\u2060"
	case r == '{':
		return `\{`
	case r == '}':
		return `\}`
	case r < 0x20, r >= 0x7F && r < 0xA0, r == 0x2028, r == 0x2029:
		return " "
	}
	return string(r)
}

// Escape writes text for the ASS Text field (see escape); a newline becomes the ASS line break.
func Escape(text string) string {
	var sb strings.Builder
	for _, r := range text {
		if r == '\n' {
			sb.WriteString(`\N`)
			continue
		}
		sb.WriteString(escape(r))
	}
	return sb.String()
}

func escapeLines(lines []string) string {
	esc := make([]string, len(lines))
	for i, l := range lines {
		esc[i] = Escape(l)
	}
	return strings.Join(esc, `\N`)
}

// addCaption adds the events of one caption chunk. For a word-highlight style there is one event per word (the chunk with
// that word in the highlight colour) plus an event for each gap between two words with nothing highlighted, so the chunk is
// on screen from its first word to its last and the highlight follows the voice; otherwise the chunk is a single event.
func (b *builder) addCaption(lay style.Layout, box style.Rect, words []capWord, text string) {
	if len(words) == 0 {
		return
	}
	cx, cy := centre(box)
	head := fmt.Sprintf(`{\an5\pos(%s,%s)}`, num(cx), num(cy))
	st := lay.Style
	first, last := words[0].startF, words[len(words)-1].endF
	if st.Highlight == (style.Colour{}) {
		b.add(layerCaption, first, last, st, head+escapeLines(lay.Lines))
		b.counts.caption++
		return
	}
	spans := wordSpans(words)
	for i, w := range words {
		b.add(layerCaption, w.startF, w.endF, st, head+highlighted(lay.Lines, text, spans, i, st))
		b.counts.caption++
		if i+1 < len(words) && words[i+1].startF > w.endF {
			b.add(layerCaption, w.endF, words[i+1].startF, st, head+escapeLines(lay.Lines))
			b.counts.caption++
		}
	}
}

// capWord is a caption word placed on the output: its text and the frames it is spoken on, [startF, endF).
type capWord struct {
	text         string
	startF, endF int64
}

// wordSpans returns, for each word, the rune range [from, to) it occupies in text (the words joined by single spaces).
func wordSpans(words []capWord) [][2]int {
	spans := make([][2]int, len(words))
	pos := 0
	for i, w := range words {
		n := len([]rune(w.text))
		spans[i] = [2]int{pos, pos + n}
		pos += n + 1
	}
	return spans
}

// highlighted writes the lines with word k in the highlight colour. The lines are the wrapped text: they hold the same
// characters in the same order as text, minus the spaces the wrap swallowed, so the text is walked alongside them.
func highlighted(lines []string, text string, spans [][2]int, k int, st style.Style) string {
	runes := []rune(text)
	on, off := colourRGB(st.Highlight), colourRGB(st.Fill)
	p := 0
	var sb strings.Builder
	for li, l := range lines {
		if li > 0 {
			sb.WriteString(`\N`)
		}
		lr := []rune(l)
		// Skip the spaces the wrap swallowed before this line.
		for p < len(runes) && (len(lr) == 0 || runes[p] != lr[0]) && runes[p] == ' ' {
			p++
		}
		lit := false
		for _, r := range lr {
			in := p >= spans[k][0] && p < spans[k][1]
			if in != lit {
				if in {
					fmt.Fprintf(&sb, `{\1c%s}`, on)
				} else {
					fmt.Fprintf(&sb, `{\1c%s}`, off)
				}
				lit = in
			}
			sb.WriteString(escape(r))
			p++
		}
		if lit {
			fmt.Fprintf(&sb, `{\1c%s}`, off)
		}
	}
	return sb.String()
}

// render writes the script.
func (b *builder) render() string {
	var sb strings.Builder
	sb.WriteString("[Script Info]\nScriptType: v4.00+\n")
	fmt.Fprintf(&sb, "PlayResX: %d\nPlayResY: %d\n", b.w, b.h)
	sb.WriteString("WrapStyle: 2\nScaledBorderAndShadow: yes\nYCbCr Matrix: None\n\n")
	sb.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	ids := make([]string, 0, len(b.used))
	for id := range b.used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sb.WriteString(styleLine(b.used[id], b.h))
		sb.WriteString("\n")
	}
	sb.WriteString("\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	evs := slices.Clone(b.events)
	slices.SortStableFunc(evs, func(x, y event) int {
		switch {
		case x.start != y.start:
			return int(x.start - y.start)
		case x.layer != y.layer:
			return x.layer - y.layer
		}
		return x.seq - y.seq
	})
	for _, e := range evs {
		fmt.Fprintf(&sb, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", e.layer, b.assTime(e.start), b.assTime(e.end), e.style, e.text)
	}
	return sb.String()
}

// styleLine writes the ASS Style line of a registry style at an output height.
func styleLine(st style.Style, outH int) string {
	size := st.FontSizePx(outH)
	f := st.FontOf()
	bold := 0
	if f.Bold {
		bold = -1
	}
	border, outline := 1, st.Outline
	if st.Box {
		border = 3
	}
	return fmt.Sprintf("Style: %s,%s,%d,%s,%s,%s,%s,%d,0,0,0,100,100,0,0,%d,%d,%d,5,0,0,0,1",
		st.ID, f.Family, size, st.Fill.ASS(), st.Fill.ASS(), outline.ASS(), st.Shadow.ASS(), bold, border, st.OutlinePx(size), st.ShadowPx(size))
}

// assTime writes the time of an event boundary, in the H:MM:SS.cc form of ASS. A boundary on frame n is written half a frame
// before the frame's own time, rounded to a centisecond: libass shows an event on [start, end) of the frame's timestamp, so
// the event starts on frame n and ends before frame n only if the written time lies between the timestamps of frames n-1
// and n, and the middle of that gap is further than a centisecond's rounding from either end at every supported frame rate
// (the gap is at least 16.7 ms at 60 fps).
func (b *builder) assTime(frame int64) string {
	period := 1000.0 / float64(b.fps)
	ms := float64(frame)*period - period/2
	cs := int64(math.Floor(ms/10 + 0.5))
	cs = max(cs, 0)
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

// subtitles finishes the script, or returns nil when there are no events.
func (b *builder) subtitles() *Subtitles {
	if len(b.events) == 0 {
		return nil
	}
	fonts := map[string]bool{}
	for _, st := range b.used {
		fonts[st.FontOf().File] = true
	}
	files := make([]string, 0, len(fonts))
	for f := range fonts {
		files = append(files, f)
	}
	sort.Strings(files)
	return &Subtitles{
		File: subtitleFile, FontsDir: fontsDirName, Fonts: files, ASS: b.render(),
		TextEvents: b.counts.text, CaptionEvents: b.counts.caption, Watermark: b.watermark,
	}
}

// filter is the ass filter that burns the script into the video.
func (s *Subtitles) filter() string {
	return fmt.Sprintf("ass=filename=%s:fontsdir=%s", s.File, s.FontsDir)
}
