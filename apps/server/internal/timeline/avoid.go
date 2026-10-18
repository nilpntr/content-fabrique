package timeline

import (
	"math"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Avoiding the subject: a title that sits on the head of the person in the footage is moved off it, by the same function in the renderer and
// in the editor's check, so what the check says is what the render does. The obstacles are boxes in output pixels (package subject makes
// them from the stored subject boxes of the clips on screen); this file knows nothing about where they come from.

// AvoidsSubject says whether the renderer keeps this item off the subject: it does unless the item says avoid_subject false.
func (t TextItem) AvoidsSubject() bool { return t.AvoidSubject == nil || *t.AvoidSubject }

// Avoidance says what PlaceTextAvoiding did.
type Avoidance struct {
	// Overlapped is true when the item as placed (its anchor or position, at its scale) covers an obstacle.
	Overlapped bool
	// Cleared is true when the item now clears every obstacle: it did not overlap, or it was moved or shrunk until it did not.
	Cleared bool
	// Where says how it was cleared: "above" the obstacles, "below" them or "" for not moved. Scale is the scale it was placed at when the
	// avoidance shrank it (0 when the size is the item's own).
	Where string
	Scale float64
}

// avoidFactors are the shrinks tried, in order, within the allowed scale range.
var avoidFactors = []float64{1, 0.9, 0.8, 0.7, 0.6}

// avoidGapPct is the clear space kept between a title and an obstacle, as a percentage of the output height.
const avoidGapPct = 1.0

// PlaceTextAvoiding places a text item like PlaceText and, when its box covers an obstacle, tries to clear it without leaving the safe
// zone: first above the obstacles (the band above the head) at the item's own size and then at smaller ones down to the lowest scale the
// item may have (0.6 of its style's), and only then below them, again from the item's own size down. When nothing clears, it returns the item as PlaceText placed it and Cleared
// false. An item that does not avoid the subject, or a call with no obstacles, is PlaceText's result untouched.
func PlaceTextAvoiding(out Output, it TextItem, obstacles []style.Rect) (TextPlacement, Avoidance, error) {
	pl, err := PlaceText(out, it)
	if err != nil {
		return pl, Avoidance{}, err
	}
	if !it.AvoidsSubject() || len(obstacles) == 0 {
		return pl, Avoidance{Cleared: true}, nil
	}
	box, ok := pl.Zone.Fit(pl.Box)
	if !ok || !overlapsAny(box, obstacles) {
		return pl, Avoidance{Cleared: true}, nil
	}
	res := Avoidance{Overlapped: true}
	gap := int(math.Round(avoidGapPct / 100 * float64(out.Height)))
	base := 1.0
	if it.Scale != nil {
		base = *it.Scale
	}
	// The scales to try: the item's own, then smaller ones down to the lowest allowed.
	var scales []float64
	seen := map[float64]bool{}
	for _, f := range avoidFactors {
		if sc := math.Max(math.Round(base*f*100)/100, style.MinScale); !seen[sc] {
			seen[sc] = true
			scales = append(scales, sc)
		}
	}
	// A smaller size that no longer reaches the obstacle clears it where it is: no move at all.
	for _, sc := range scales[1:] {
		cand := it
		cand.Scale = &sc
		cpl, err := PlaceText(out, cand)
		if err != nil {
			continue
		}
		if box, ok := cpl.Zone.Fit(cpl.Box); ok {
			padded := box
			padded.Bottom += gap // the same gap under the text as for a move
			padded.Top -= gap
			if !overlapsAny(padded, obstacles) {
				return cpl, Avoidance{Overlapped: true, Cleared: true, Where: "smaller", Scale: sc}, nil
			}
		}
	}
	// Above the obstacles first, at every scale (the clear band above the head is where a title belongs), and only then below them.
	for _, where := range []string{"above", "below"} {
		for _, sc := range scales {
			cand := it
			applied := 0.0
			if sc != base {
				cand.Scale, applied = &sc, sc
			}
			cpl, err := PlaceText(out, cand)
			if err != nil {
				continue
			}
			bx := cpl.Box
			w, h := bx.Width(), bx.Height()
			var top int
			if where == "above" {
				// The box's bottom a gap over the highest obstacle that is in its way.
				top = obstacleTop(bx, obstacles) - gap - h
				if top < cpl.Zone.Inner.Top {
					continue
				}
			} else {
				// The box's top a gap under the lowest obstacle.
				top = obstacleBottom(bx, obstacles) + gap
				if top+h > cpl.Zone.Inner.Bottom {
					continue
				}
			}
			nb := style.Rect{Left: bx.Left, Top: top, Right: bx.Left + w, Bottom: top + h}
			if cpl.Zone.Safe(nb) && !overlapsAny(nb, obstacles) {
				cpl.Box = nb
				return cpl, Avoidance{Overlapped: true, Cleared: true, Where: where, Scale: applied}, nil
			}
		}
	}
	return pl, res, nil
}

// TextOverlaps says whether the item, placed where its anchor or position puts it (and clamped into the zone), covers an obstacle:
// whatever the item's own avoid_subject says.
func TextOverlaps(out Output, it TextItem, obstacles []style.Rect) (bool, error) {
	pl, err := PlaceText(out, it)
	if err != nil {
		return false, err
	}
	box, ok := pl.Zone.Fit(pl.Box)
	if !ok {
		return false, nil // it cannot be put inside the safe zone at all (unsafe_text, and the renderer leaves it out)
	}
	return overlapsAny(box, obstacles), nil
}

func overlapsAny(box style.Rect, obstacles []style.Rect) bool {
	for _, o := range obstacles {
		if box.Overlaps(o) {
			return true
		}
	}
	return false
}

// obstacleTop is the top of the highest obstacle that overlaps the box horizontally (the box's own top when none does).
func obstacleTop(box style.Rect, obstacles []style.Rect) int {
	top := box.Top
	first := true
	for _, o := range obstacles {
		if o.Left < box.Right && o.Right > box.Left {
			if first || o.Top < top {
				top, first = o.Top, false
			}
		}
	}
	return top
}

// obstacleBottom is the bottom of the lowest obstacle that overlaps the box horizontally.
func obstacleBottom(box style.Rect, obstacles []style.Rect) int {
	bottom := box.Bottom
	first := true
	for _, o := range obstacles {
		if o.Left < box.Right && o.Right > box.Left {
			if first || o.Bottom > bottom {
				bottom, first = o.Bottom, false
			}
		}
	}
	return bottom
}
