package motion

import (
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// When an item can be read. The text is readable once its last element has landed (its entrance move is 95 % done: opaque, unblurred
// and in place, give or take an overshoot that settles) and until its exit has visibly begun (the first element to leave is 10 % of
// the way out). Not FullyInMs to ExitMs: the entrances end on a settle that changes nothing a reader needs, and every exit starts on
// a strong ease-in that hardly moves the letters at first. The curves are the templates' own (bundle/templates/*.js); the docker test
// TestIntegrationReadableWindow holds the two to the rendered frames.

// bezier is a CSS cubic-bezier(x1, y1, x2, y2) timing function.
type bezier struct{ x1, y1, x2, y2 float64 }

// The easings the templates' entrances and exits use.
var (
	easeLand    = bezier{0.2, 0.9, 0.3, 1} // title_pop's words and chapter_card's number, to their peak
	easeOutExpo = bezier{0.16, 1, 0.3, 1}  // the sweeps, lines and letters
	easeExit    = bezier{0.7, 0, 0.84, 0}  // every exit
	landedAt    = 0.95                     // an entrance is done for reading at 95 % of its move
	leavingAt   = 0.10                     // an exit has begun for reading at 10 % of its move
	landPeak    = 0.6                      // title_pop's words peak at 60 % of their entrance
	numberPeak  = 0.55                     // chapter_card's number peaks at 55 % of its entrance
	sweepFactor = 1.1                      // chapter_card's words take 1.1 times the entrance duration
	lineFactor  = 1.6                      // blur_in's lines take 1.6 times
	textFactor  = 1.2                      // highlight_word's lines take 1.2 times
	wordsExit   = 0.6                      // title_pop and chapter_card: each word leaves in 60 % of the exit
	textExitAt  = 0.3                      // highlight_word: the text leaves from 30 % of the exit
	textExitDur = 0.7                      // ... over 70 % of it
	labelExit   = 0.5                      // lower_label: the lines leave in 50 % of the exit
)

// at is the point of the curve at parameter s.
func (b bezier) at(s float64) (x, y float64) {
	u := 1 - s
	x = 3*u*u*s*b.x1 + 3*u*s*s*b.x2 + s*s*s
	y = 3*u*u*s*b.y1 + 3*u*s*s*b.y2 + s*s*s
	return x, y
}

// timeFor is the fraction of the duration at which the eased progress reaches p (the curves the templates use rise monotonically).
func (b bezier) timeFor(p float64) float64 {
	lo, hi := 0.0, 1.0
	for range 60 {
		mid := (lo + hi) / 2
		if _, y := b.at(mid); y < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	x, _ := b.at(hi)
	return x
}

// frac is ms * f, rounded.
func frac(ms int64, f float64) int64 { return int64(f*float64(ms) + 0.5) }

// readableWindow sets t.LegibleMs and t.LeavingMs from the template's elements and curves (t's entrances and exit set already).
func (t *Timing) readableWindow(it timeline.GraphicsItem, f Feel, lines int) {
	land := func(start, d int64, b bezier, part float64) int64 {
		return start + frac(frac(d, part), b.timeFor(landedAt))
	}
	leave := func(start, d int64) int64 { return start + frac(d, easeExit.timeFor(leavingAt)) }
	last := func(xs []int64) int64 {
		if len(xs) == 0 {
			return 0
		}
		return xs[len(xs)-1]
	}
	e := t.ExitDurMs
	switch it.Template {
	case timeline.TemplateTitlePop:
		t.LegibleMs = land(last(t.EnterMs), f.DurMs, easeLand, landPeak)
		t.LeavingMs = leave(t.ExitMs, frac(e, wordsExit))
	case timeline.TemplateChapterCard:
		restWords := max(1, len(it.Words())-1)
		step := max(25, min(70, f.StaggerMs/2))
		num := land(t.EnterMs[0], f.DurMs, easeLand, numberPeak)
		words := land(t.EnterMs[1]+int64(restWords-1)*step, frac(f.DurMs, sweepFactor), easeOutExpo, 1)
		t.LegibleMs = max(num, words)
		t.LeavingMs = leave(t.ExitMs, frac(e, wordsExit))
	case timeline.TemplateBlurIn:
		t.LegibleMs = land(last(t.EnterMs), frac(f.DurMs, lineFactor), easeOutExpo, 1)
		t.LeavingMs = leave(t.ExitMs, e)
	case timeline.TemplateHighlightWord:
		t.LegibleMs = land(last(t.EnterMs), frac(f.DurMs, textFactor), easeOutExpo, 1)
		t.LeavingMs = leave(t.ExitMs+frac(e, textExitAt), frac(e, textExitDur))
	case timeline.TemplateCounter:
		// The number counts until it lands; the label lands with the entrance.
		t.LegibleMs = max(t.LandMs, land(t.EnterMs[1], f.DurMs, easeOutExpo, 1))
		t.LeavingMs = leave(t.ExitMs, e)
	case timeline.TemplateLowerLabel:
		t.LegibleMs = land(t.EnterMs[1]+int64(max(1, lines)-1)*f.StaggerMs, f.DurMs, easeOutExpo, 1)
		t.LeavingMs = leave(t.ExitMs, frac(e, labelExit))
	case timeline.TemplateOutroCard:
		letters := int64(0)
		for _, w := range it.Words() {
			letters += int64(len([]rune(w)))
		}
		per := max(12, min(45, f.StaggerMs/3))
		t.LegibleMs = land(t.EnterMs[0]+max(0, letters-1)*per, f.DurMs, easeOutExpo, 1)
		t.LeavingMs = leave(t.ExitMs, e)
	default:
		t.LegibleMs, t.LeavingMs = t.FullyInMs, t.ExitMs
	}
	t.LegibleMs = min(t.LegibleMs, t.FullyInMs)
	t.LeavingMs = max(t.LeavingMs, t.ExitMs)
}
