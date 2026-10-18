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
	warnings  []Warning
}

func (b *builder) warn(code, format string, args ...any) {
	b.warnings = append(b.warnings, Warning{Code: code, Message: fmt.Sprintf(format, args...)})
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

// centre returns the centre of a box as the ASS tags write it.
func centre(box style.Rect) (x, y float64) {
	return float64(box.Left+box.Right) / 2, float64(box.Top+box.Bottom) / 2
}

// anchorOf is the ASS alignment tag and the x the \pos uses for the lines of a text in a box: \an5 at the centre of the box, or \an4 at
// the left edge of the ink (the box less its padding) or \an6 at the right edge, so the lines line up against that edge. Only the
// three fixed tags are ever written; the alignment itself comes from an enum the validator has checked.
func anchorOf(box style.Rect, pad int, align string) (an string, x float64) {
	cx, _ := centre(box)
	switch align {
	case style.AlignLeft:
		return `\an4`, float64(box.Left + pad)
	case style.AlignRight:
		return `\an6`, float64(box.Right - pad)
	}
	return `\an5`, cx
}

// addText adds a text item (or the watermark) at a box: the entrance and exit tags, then the lines. align is left, center or right (""
// is centre): how the lines sit against each other. Every tag is one of a fixed set with numbers in it; nothing of the spec's text is
// a tag.
func (b *builder) addText(layer int, start, end int64, lay style.Layout, zone style.Zone, box style.Rect, anim, exit, align string) {
	st := lay.Style
	an, cx := anchorOf(box, lay.Pad, align)
	_, cy := centre(box)
	durMs := frameMs(end, b.fps) - frameMs(start, b.fps)
	var tags strings.Builder
	tags.WriteString(an)
	pos := fmt.Sprintf(`\pos(%s,%s)`, num(cx), num(cy))
	body := escapeLines(lay.Lines)
	fadeIn, fadeOut := 0, 0
	moved := false // a \move is in the tags (an event has one \pos or one \move)
	var scale strings.Builder
	switch anim {
	case style.AnimFade:
		tags.WriteString(pos)
		fadeIn = fadeMs(durMs)
		if exit == "" || exit == style.ExitNone {
			fadeOut = fadeIn // the fade in and out, as it always was
		}
	case style.AnimPopIn:
		tags.WriteString(pos)
		scale.WriteString(`\fscx60\fscy60\t(0,180,0.6,\fscx100\fscy100)`)
		fadeIn = min(100, fadeMs(durMs))
	case style.AnimPop:
		// Up past its size and back: 70 % to 112 % in 140 ms, then settling to 100 % by 240 ms (an overshoot, the lively entrance).
		tags.WriteString(pos)
		up, settle := popTimes(durMs)
		fmt.Fprintf(&scale, `\fscx70\fscy70\t(0,%d,\fscx112\fscy112)\t(%d,%d,\fscx100\fscy100)`, up, up, settle)
		fadeIn = min(80, fadeMs(durMs))
	case style.AnimSlideUp:
		d := min(int(math.Floor(0.02*float64(b.h)+0.5)), zone.SlideRoom(box))
		if d > 0 {
			fmt.Fprintf(&tags, `\move(%s,%s,%s,%s,0,250)`, num(cx), num(cy+float64(d)), num(cx), num(cy))
			moved = true
		} else {
			tags.WriteString(pos)
		}
		fadeIn = min(120, fadeMs(durMs))
	case style.AnimTypewriter:
		tags.WriteString(pos)
		body = typewriter(lay, durMs)
	default:
		tags.WriteString(pos)
	}
	// The exit, at the end of the event.
	out := min(exitMs, fadeMs(durMs))
	switch exit {
	case style.ExitFade:
		fadeOut = fadeMs(durMs)
	case style.ExitPopOut:
		if out > 0 {
			fmt.Fprintf(&scale, `\t(%d,%d,\fscx80\fscy80)`, durMs-int64(out), durMs)
			fadeOut = out
		}
	case style.ExitSlideDown:
		d := min(int(math.Floor(0.015*float64(b.h)+0.5)), zone.SlideRoom(box))
		if d > 0 && !moved && out > 0 {
			// The \pos becomes a \move that sinks over the last moments (an event has one position tag).
			s := tags.String()
			tags.Reset()
			tags.WriteString(strings.Replace(s, pos, fmt.Sprintf(`\move(%s,%s,%s,%s,%d,%d)`, num(cx), num(cy), num(cx), num(cy+float64(d)), durMs-int64(out), durMs), 1))
		}
		fadeOut = max(out, 0)
	}
	tags.WriteString(scale.String())
	if fadeIn > 0 || fadeOut > 0 {
		fmt.Fprintf(&tags, `\fad(%d,%d)`, fadeIn, fadeOut)
	}
	b.add(layer, start, end, st, "{"+tags.String()+"}"+body)
	if layer == layerWatermark {
		b.watermark = true
	} else {
		b.counts.text++
	}
}

// exitMs is the length of an exit (pop_out, slide_down), at most a quarter of the event like a fade.
const exitMs = 180

// popTimes are the moments of a pop entrance, in ms: the overshoot peaks at up and settles by settle (scaled down for a very short text).
func popTimes(durMs int64) (up, settle int64) {
	up, settle = 140, 240
	if limit := durMs / 3; settle > limit {
		up, settle = max(limit*7/12, 1), max(limit, 2)
	}
	return up, settle
}

// fadeMs is the length of a fade in or out: 150 ms, at most a quarter of the event.
func fadeMs(durMs int64) int { return int(max(0, min(150, durMs/4))) }

// frameMs is the start of a frame in whole milliseconds.
func frameMs(frame int64, fps int) int64 { return (2*frame*1000 + int64(fps)) / (2 * int64(fps)) }

// typewriter reveals the characters one after the other: every character starts fully transparent and a \t switches it on at
// its time. The characters keep their places the whole time, so the block does not move while it types. A character takes at
// most 45 ms, and the last one starts within 60 % of the event whatever the length: a long text on a short event is typed
// faster than a millisecond a character, several characters switching on in the same millisecond.
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
	step := math.Min(45, float64(durMs)*0.6/float64(n)) // ms between two characters, below 1 for a long text on a short event
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
			t := int(math.Floor(float64(i) * step))
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
		sb.WriteString(styleLine(b.used[id], min(b.w, b.h)))
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

// styleLine writes the ASS Style line of a registry style for an output with the given shorter side.
func styleLine(st style.Style, shortSide int) string {
	size := st.FontSizePx(shortSide)
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
