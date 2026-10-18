package motion

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Laying out the graphics track: where and when each overlay is drawn. The renderer and the editor's checks both call Layout, so what a
// check says is what the render does.
//
//  1. Never over the body (P6-06): each item is fitted like a text item (subject.FitText): at its own time, else in the part of it that
//     has room, else at the nearest time that has, else it is left out.
//  2. Never over another overlay: the text items and the graphics already laid out that are on screen at the same time are obstacles.
//     An item whose box would touch one is stacked below the obstacles, else above them (inside the safe zone and still off the person);
//     else it is moved to the nearest time next to them where it is clear; else it is left out. Titles (title_pop, blur_in,
//     chapter_card) are laid out before the other templates, so a counter or a label makes way for a chapter card, never the reverse.

// How a clash with another overlay was resolved (Laid.Clash).
const (
	ClashNone    = ""
	ClashBelow   = "below"
	ClashAbove   = "above"
	ClashMoved   = "moved"
	ClashDropped = "dropped"
)

// Laid is one graphics item as Layout laid it out.
type Laid struct {
	// Original is the item as the spec has it; Item is the item as drawn (its time changed by the fit or the clash).
	Original, Item timeline.GraphicsItem
	// Fit is subject.FitText's result for the item (its own time and the subjects there).
	Fit subject.Fit
	// NotPlaceable says its box cannot be put inside the safe zone at all; it is left out.
	NotPlaceable bool
	// Clash says how the item was kept off another overlay (a Clash constant), With which one (an item id), and the time that
	// other one is on screen.
	Clash               string
	With                string
	WithAtMs, WithEndMs int64
	// Resolved is the item ready to render (its Box moved when it was stacked). Empty when the item is left out.
	Resolved Resolved
}

// Drawn says whether the item is drawn.
func (l Laid) Drawn() bool {
	return l.Fit.How != subject.FitDropped && !l.NotPlaceable && l.Clash != ClashDropped
}

// obstacle is an overlay on screen: its box and its time.
type obstacle struct {
	id         string
	start, end int64
	box        style.Rect
}

// maxClashTimes bounds the times tried for an item that clashes.
const maxClashTimes = 48

// Layout lays out the spec's graphics track (see above). lookup gives the subjects of the clips (nil: none known); totalMs is the length
// of the timeline. The result is in the order of the track. An item with invalid params is an error (the validator refuses it first).
func Layout(spec *timeline.Spec, lookup subject.Lookup, totalMs int64) ([]Laid, error) {
	var items []timeline.GraphicsItem
	for _, t := range spec.Tracks {
		if g, ok := t.(*timeline.GraphicsTrack); ok {
			items = g.Items
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := spec.Output
	gap := gapPx(out)
	obs, err := textObstacles(spec, lookup, totalMs)
	if err != nil {
		return nil, err
	}
	// Titles first, then by time, then in the order of the track.
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ta, tb := items[a].IsTitle(), items[b].IsTitle()
		if ta != tb {
			if ta {
				return -1
			}
			return 1
		}
		return cmp.Compare(items[a].AtMs, items[b].AtMs)
	})
	laid := make([]Laid, len(items))
	for _, i := range order {
		it := items[i]
		l := Laid{Original: it, Item: it}
		if field, err := it.CheckParams(); err != nil {
			return nil, fmt.Errorf("motion: graphics item %s: %s: %w", it.ID, field, err)
		}
		fit, err := subject.FitText(spec, lookup, it.AsText(), totalMs)
		if err != nil {
			return nil, fmt.Errorf("motion: graphics item %s: %w", it.ID, err)
		}
		l.Fit = fit
		if fit.How == subject.FitDropped {
			laid[i] = l
			continue
		}
		if fit.How == subject.FitTrimmed || fit.How == subject.FitMoved {
			l.Item = it.Retimed(fit.AtMs, fit.DurationMs, fit.How == subject.FitMoved)
		}
		r, err := Resolve(out, l.Item, fit.Regions)
		if errors.Is(err, ErrNotPlaceable) {
			l.NotPlaceable = true
			laid[i] = l
			continue
		}
		if err != nil {
			return nil, err
		}
		if hit, ok := clash(r.Box, l.Item.AtMs, l.Item.EndMs(), obs, gap); ok {
			l.With, l.WithAtMs, l.WithEndMs = hit.id, hit.start, hit.end
			if box, where, ok := stack(r, fit.Regions, l.Item, obs, gap); ok {
				r.Box, l.Clash = box, where
			} else if moved, mr, ok := shiftInTime(spec, lookup, l.Item, obs, gap, totalMs); ok {
				l.Item, r, l.Clash = moved, mr, ClashMoved
			} else {
				l.Clash = ClashDropped
				laid[i] = l
				continue
			}
		}
		l.Resolved = r
		laid[i] = l
		obs = append(obs, obstacle{id: it.ID, start: l.Item.AtMs, end: l.Item.EndMs(), box: r.Box})
	}
	return laid, nil
}

// textObstacles are the text items where and when the renderer draws them (subject.FitText, as render/text.go).
func textObstacles(spec *timeline.Spec, lookup subject.Lookup, totalMs int64) ([]obstacle, error) {
	var obs []obstacle
	out := spec.Output
	for _, t := range spec.Tracks {
		// The band of enabled captions, for the whole video. A graphic there is refused by the validator (text_overlaps_captions);
		// this keeps a stacked one out of it. It is shrunk by the gap, so touching it is not a clash.
		if ct, ok := t.(*timeline.CaptionsTrack); ok && ct.Enabled {
			zone, zok := out.Zone()
			st, sok := style.CaptionStyle(ct.Style)
			if zok && sok {
				b := style.CaptionBand(zone, out.Width, out.ShortSide(), st)
				g := gapPx(out)
				obs = append(obs, obstacle{id: ct.ID, start: 0, end: totalMs, box: style.Rect{Left: b.Left + g, Top: b.Top + g, Right: b.Right - g, Bottom: b.Bottom - g}})
			}
			continue
		}
		tt, ok := t.(*timeline.TextTrack)
		if !ok {
			continue
		}
		for _, it := range tt.Items {
			fit, err := subject.FitText(spec, lookup, it, totalMs)
			if err != nil {
				return nil, fmt.Errorf("motion: text item %s: %w", it.ID, err)
			}
			if fit.How == subject.FitDropped {
				continue
			}
			box, ok := fit.Placement.Zone.Fit(fit.Placement.Box)
			if !ok {
				continue
			}
			obs = append(obs, obstacle{id: it.ID, start: fit.AtMs, end: fit.AtMs + fit.DurationMs, box: box})
		}
	}
	return obs, nil
}

// clash returns the first obstacle on screen during [start, end) whose box comes within gap px of box.
func clash(box style.Rect, start, end int64, obs []obstacle, gap int) (obstacle, bool) {
	grown := style.Rect{Left: box.Left - gap, Top: box.Top - gap, Right: box.Right + gap, Bottom: box.Bottom + gap}
	for _, o := range obs {
		if o.start < end && start < o.end && grown.Overlaps(o.box) {
			return o, true
		}
	}
	return obstacle{}, false
}

// stack moves the box straight down below the obstacles it meets, else straight up above them, to the first place that is inside the
// safe zone, off the blocked parts of the zone, off the subject (when the item avoids them) and clear of every obstacle on screen.
func stack(r Resolved, regions []timeline.Subject, it timeline.GraphicsItem, obs []obstacle, gap int) (style.Rect, string, bool) {
	zone := r.Placement.Zone
	ok := func(b style.Rect) bool {
		if !zone.Inner.Contains(b) {
			return false
		}
		for _, bl := range zone.Blocked {
			if bl.Overlaps(b) {
				return false
			}
		}
		if it.AvoidsSubject() {
			if _, body := timeline.BoxCovers(b, regions); body {
				return false
			}
		}
		_, hit := clash(b, it.AtMs, it.EndMs(), obs, gap)
		return !hit
	}
	h := r.Box.Height()
	for _, dir := range []string{ClashBelow, ClashAbove} {
		b := r.Box
		for range len(obs) + 1 {
			o, hit := clash(b, it.AtMs, it.EndMs(), obs, gap)
			if !hit {
				break
			}
			// Even, as the clips of the overlay frames are, and rounded away from the obstacle.
			if dir == ClashBelow {
				b.Top = o.box.Bottom + gap
				b.Top += b.Top & 1
			} else {
				b.Top = o.box.Top - gap - h
				b.Top -= b.Top & 1
			}
			b.Bottom = b.Top + h
		}
		if ok(b) {
			return b, dir, true
		}
	}
	return style.Rect{}, "", false
}

// shiftInTime moves the item in time, to the nearest start next to an obstacle it meets (just after one ends, or so that it ends just before
// one starts) where it is clear of the subject and of every obstacle, inside the timeline. Stacking is tried at each time too.
func shiftInTime(spec *timeline.Spec, lookup subject.Lookup, it timeline.GraphicsItem, obs []obstacle, gap int, totalMs int64) (timeline.GraphicsItem, Resolved, bool) {
	dur := it.DurationMs
	var times []int64
	for _, o := range obs {
		for _, t := range []int64{o.end, o.start - dur} {
			if t >= 0 && t+dur <= totalMs && t != it.AtMs && !slices.Contains(times, t) {
				times = append(times, t)
			}
		}
	}
	slices.SortFunc(times, func(a, b int64) int {
		return cmp.Or(cmp.Compare(absMs(a-it.AtMs), absMs(b-it.AtMs)), cmp.Compare(a, b))
	})
	for _, t := range times[:min(len(times), maxClashTimes)] {
		cand := it.Retimed(t, dur, true)
		var regions []timeline.Subject
		if cand.AvoidsSubject() && lookup != nil {
			regions = subject.Regions(spec, lookup, t, t+dur)
		}
		r, err := Resolve(spec.Output, cand, regions)
		if err != nil || (len(regions) > 0 && !r.Avoid.Cleared) {
			continue
		}
		if _, hit := clash(r.Box, t, t+dur, obs, gap); !hit {
			return cand, r, true
		}
		if box, _, ok := stack(r, regions, cand, obs, gap); ok {
			r.Box = box
			return cand, r, true
		}
	}
	return it, Resolved{}, false
}

// gapPx is the clear space kept between two overlays: 1 % of the output's height, as between a title and the person.
func gapPx(out timeline.Output) int { return max(2, out.Height/100) }

func absMs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
