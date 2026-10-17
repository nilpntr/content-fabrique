package style

// Rect is a pixel rectangle: Left and Top are inclusive, Right and Bottom are the edges just outside it.
type Rect struct {
	Left, Top, Right, Bottom int
}

// Width is the rectangle's width in pixels.
func (r Rect) Width() int { return r.Right - r.Left }

// Height is the rectangle's height in pixels.
func (r Rect) Height() int { return r.Bottom - r.Top }

// Contains reports whether o lies inside r.
func (r Rect) Contains(o Rect) bool {
	return o.Left >= r.Left && o.Right <= r.Right && o.Top >= r.Top && o.Bottom <= r.Bottom
}

// Overlaps reports whether r and o share at least one pixel.
func (r Rect) Overlaps(o Rect) bool {
	return o.Left < r.Right && o.Right > r.Left && o.Top < r.Bottom && o.Bottom > r.Top
}

// Zone is where text may go in one format: inside Inner, and out of every rectangle in Blocked.
type Zone struct {
	Inner   Rect
	Blocked []Rect
}

// The output formats (D-25), spelled as in the spec.
const (
	Format9x16 = "9:16"
	Format1x1  = "1:1"
	Format4x5  = "4:5"
	Format16x9 = "16:9"
)

// ZoneFor returns the safe zone of a format at an output size (design note section 5, D-25):
//
//   - 9:16 follows research 3.4: out of the top 240 px, the bottom 660 px and 120 px on each side, and out of the
//     right-hand action column (x > 900 for y > 840). The numbers are for the 1080x1920 canvas and scale with the size.
//   - 1:1 and 4:5 keep a 5 % margin on every side.
//   - 16:9 keeps a 10 % title-safe margin and the bottom 15 % clear.
//
// Percentages are of the side they cut, rounded half up to a pixel. ok is false for an unknown format.
func ZoneFor(format string, w, h int) (z Zone, ok bool) {
	switch format {
	case Format9x16:
		sx, sy := func(v int) int { return scale(v, w, 1080) }, func(v int) int { return scale(v, h, 1920) }
		return Zone{
			Inner:   Rect{Left: sx(120), Top: sy(240), Right: w - sx(120), Bottom: h - sy(660)},
			Blocked: []Rect{{Left: sx(900), Top: sy(840), Right: w, Bottom: h}},
		}, true
	case Format1x1, Format4x5:
		mx, my := percent(w, 5), percent(h, 5)
		return Zone{Inner: Rect{Left: mx, Top: my, Right: w - mx, Bottom: h - my}}, true
	case Format16x9:
		mx, top, bottom := percent(w, 10), percent(h, 10), percent(h, 15)
		return Zone{Inner: Rect{Left: mx, Top: top, Right: w - mx, Bottom: h - bottom}}, true
	}
	return Zone{}, false
}

// percent is pct % of v, rounded half up.
func percent(v, pct int) int { return (v*pct + 50) / 100 }

// scale is v * n / d, rounded half up.
func scale(v, n, d int) int { return (v*n + d/2) / d }

// Column is the widest rectangle for centred text: the inner zone narrowed symmetrically about the vertical centre line so
// that it also keeps out of every blocked rectangle on either side of it. Text up to Column's width, centred on the centre
// line, is inside the zone at any height. For 9:16 it is x 180 to 900 (the action column starts at 900).
func (z Zone) Column(w int) Rect {
	c := w / 2
	half := min(c-z.Inner.Left, z.Inner.Right-c)
	for _, b := range z.Blocked {
		switch {
		case b.Left >= c:
			half = min(half, b.Left-c)
		case b.Right <= c:
			half = min(half, c-b.Right)
		}
	}
	half = max(half, 1)
	col := z.Inner
	col.Left, col.Right = c-half, c+half
	return col
}

// Fit moves box the shortest way that puts it inside the zone and out of the blocked rectangles. It returns the box
// unchanged when it is already safe and false when it cannot be made safe (it is larger than the zone, or no shift clears
// the blocked area). The renderer clamps with it; the validator reports an unclamped box that needs it.
func (z Zone) Fit(box Rect) (Rect, bool) {
	w, h := box.Width(), box.Height()
	in := z.Inner
	if w > in.Width() || h > in.Height() {
		return box, false
	}
	shift := func(r Rect, dx, dy int) Rect {
		return Rect{Left: r.Left + dx, Top: r.Top + dy, Right: r.Right + dx, Bottom: r.Bottom + dy}
	}
	box = shift(box, max(in.Left-box.Left, 0)+min(in.Right-box.Right, 0), max(in.Top-box.Top, 0)+min(in.Bottom-box.Bottom, 0))
	for _, b := range z.Blocked {
		if !b.Overlaps(box) {
			continue
		}
		// Two ways out: left of the blocked area, or above it. Take the smaller move that stays inside the zone.
		left := shift(box, b.Left-box.Right, 0)
		up := shift(box, 0, b.Top-box.Bottom)
		leftOK, upOK := in.Contains(left), in.Contains(up)
		switch {
		case leftOK && (!upOK || box.Right-b.Left <= box.Bottom-b.Top):
			box = left
		case upOK:
			box = up
		default:
			return box, false
		}
	}
	return box, z.Safe(box)
}

// Safe reports whether box is inside the zone and clear of the blocked rectangles.
func (z Zone) Safe(box Rect) bool {
	if !z.Inner.Contains(box) {
		return false
	}
	for _, b := range z.Blocked {
		if b.Overlaps(box) {
			return false
		}
	}
	return true
}

// SlideRoom is how far a box may move down and stay safe: the distance to the bottom of the zone, and to the top of a
// blocked rectangle below it that the box overlaps horizontally. It is never negative.
func (z Zone) SlideRoom(box Rect) int {
	room := z.Inner.Bottom - box.Bottom
	for _, b := range z.Blocked {
		if box.Left < b.Right && box.Right > b.Left && box.Bottom <= b.Top {
			room = min(room, b.Top-box.Bottom)
		}
	}
	return max(room, 0)
}
