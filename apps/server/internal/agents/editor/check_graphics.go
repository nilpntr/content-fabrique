package editor

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The checks of the motion-graphics overlays (D-35, docs/design/motion-graphics-v1.md section 9):
//
//   - overlay_readable: every overlay is fully on screen for at least minReadableMs before its exit starts (the times are the
//     template's own, motion.Schedule, so the check says what the render does);
//   - overlay_over_subject: the torso rule. An overlay is never drawn on the person's body: the renderer fits it like a text item
//     (subject.FitText, the same function: at its own time, else in the part of it that has room, at the nearest time that has, or not
//     at all), so the check fails what the editor did not mean: a graphic left out, a hook or chapter title shown at another time, and
//     one set not to avoid the subject that covers it;
//   - overlay_legibility: the letters stand out from the picture behind them. The picture is measured on the preview render, in the
//     overlay's box, on a frame of the same shot just before the overlay enters (or just after it leaves), where it is not drawn.

// The check ids.
const (
	CheckOverlayReadable    = "overlay_readable"
	CheckOverlayOverSubject = "overlay_over_subject"
	CheckOverlayLegibility  = "overlay_legibility"
)

// minReadableMs is how long an overlay must stay fully entered: about the time it takes to read a few words.
const minReadableMs = 1200

// Contrast floors (the WCAG ratio of the letters' luminance to the picture's): 3 is the floor for large text; letters with a shadow
// are read at a lower ratio, and an outline or a plate behind them gives them their own background.
const (
	legibleRatio       = 3.0
	legibleRatioShadow = 2.0
)

// RegionProbe is a place to measure on the preview: an overlay's box (fractions of the frame) on one frame.
type RegionProbe struct {
	ItemID string
	Frame  int64
	// When says where the frame is: "before" the overlay enters or "after" it leaves.
	When       string
	X, Y, W, H float64
}

// RegionLuma is the luma of a probed region: the 10th and 90th percentile and the mean, 8-bit limited range (16 to 235).
type RegionLuma struct {
	Probe          RegionProbe
	Low, High, Avg float64
}

// OverlaySampler measures regions of a version's preview render. *RenderMeasurer is one.
type OverlaySampler interface {
	RenderedRegions(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int, probes []RegionProbe) ([]RegionLuma, error)
}

func graphicsOf(spec *timeline.Spec) []timeline.GraphicsItem {
	for _, t := range spec.Tracks {
		if g, ok := t.(*timeline.GraphicsTrack); ok {
			return g.Items
		}
	}
	return nil
}

// fitGraphic is where and when the renderer draws a graphics item: subject.FitText on its text, the item given the fitted time (a moved
// one keeps its accents where they were in it), and its box from motion.Resolve with the subjects at that time.
func fitGraphic(spec *timeline.Spec, subjects subject.Index, g timeline.GraphicsItem, totalMs int64) (timeline.GraphicsItem, subject.Fit, motion.Resolved, error) {
	var lookup subject.Lookup
	if len(subjects) > 0 {
		lookup = subjects.Lookup
	}
	fit, err := subject.FitText(spec, lookup, g.AsText(), totalMs)
	if err != nil || fit.How == subject.FitDropped {
		return g, fit, motion.Resolved{}, err
	}
	if fit.How == subject.FitTrimmed || fit.How == subject.FitMoved {
		shift := int64(0)
		if fit.How == subject.FitMoved {
			shift = fit.AtMs - g.AtMs
		}
		var acc []int64
		for _, a := range g.Params.AccentsMs {
			if a += shift; a >= fit.AtMs && a < fit.AtMs+fit.DurationMs {
				acc = append(acc, a)
			}
		}
		g.AtMs, g.DurationMs, g.Params.AccentsMs = fit.AtMs, fit.DurationMs, acc
	}
	r, err := motion.Resolve(spec.Output, g, fit.Regions)
	return g, fit, r, err
}

// overlayProbes picks, for every overlay, a frame of the same shot where it is not drawn: the frame before it enters, else the one
// after it leaves; an overlay that fills its shot (it starts and ends on cuts, or at the ends of the video) has none and is not probed.
// The box and the time are the ones the renderer draws at (fitGraphic); a graphic the renderer leaves out is not probed.
func overlayProbes(spec *timeline.Spec, subjects subject.Index) []RegionProbe {
	out := spec.Output
	fps := out.FPS
	total := timeline.TotalDuration(spec)
	main, _, _ := timeline.MainTrack(spec)
	clipAt := func(ms int64) string {
		id := ""
		for _, it := range main.Items {
			if it.AtMs <= ms {
				id = it.ID
			}
		}
		return id
	}
	var probes []RegionProbe
	for _, item := range graphicsOf(spec) {
		if len(probes) >= media.MaxRegions {
			break
		}
		g, fit, r, err := fitGraphic(spec, subjects, item, total)
		if err != nil || fit.How == subject.FitDropped {
			continue
		}
		frameMs := int64(1000 / max(fps, 1))
		p := RegionProbe{ItemID: g.ID,
			X: float64(r.Box.Left) / float64(out.Width), Y: float64(r.Box.Top) / float64(out.Height),
			W: float64(r.Box.Width()) / float64(out.Width), H: float64(r.Box.Height()) / float64(out.Height)}
		inShot := clipAt(g.AtMs + r.Timing.FullyInMs)
		switch before, after := g.AtMs-frameMs, g.EndMs()+frameMs; {
		case before >= 0 && clipAt(before) == inShot:
			p.Frame, p.When = timeline.Frame(g.AtMs, fps)-1, "before"
		case after < total && clipAt(after) == inShot:
			p.Frame, p.When = timeline.Frame(g.EndMs(), fps)+1, "after"
		default:
			continue
		}
		probes = append(probes, p)
	}
	return probes
}

func (c *checker) overlayReadable() Check {
	items := graphicsOf(c.in.Spec)
	if len(items) == 0 {
		return c.skip(CheckOverlayReadable, "there are no motion graphics")
	}
	var bad []string
	shortest := int64(math.MaxInt64)
	for _, g := range items {
		lines := 1
		if pl, err := timeline.PlaceText(c.in.Spec.Output, g.AsText()); err == nil {
			lines = len(pl.Layout.Lines)
		}
		t := motion.Schedule(g, lines)
		read := t.ReadableMs()
		shortest = min(shortest, read)
		if read < minReadableMs {
			bad = append(bad, fmt.Sprintf("%s (%s) is fully on screen for %.1f s before it leaves (in at %.2f s, out from %.2f s): make duration_ms at least %d, or start it earlier, or give it fewer words or accents that come sooner",
				g.ID, g.Template, float64(read)/1000, float64(t.FullyInMs)/1000, float64(t.ExitMs)/1000, t.FullyInMs+minReadableMs+motion.FeelOf(g).ExitMs))
		}
	}
	if len(bad) > 0 {
		return c.fail(CheckOverlayReadable, "%s", strings.Join(bad[:min(3, len(bad))], "; "))
	}
	return c.pass(CheckOverlayReadable, "%d motion graphics, each readable for at least %.1f s (the shortest %.1f s)", len(items), float64(minReadableMs)/1000, float64(shortest)/1000)
}

func (c *checker) overlayOverSubject() Check {
	items := graphicsOf(c.in.Spec)
	switch {
	case len(items) == 0:
		return c.skip(CheckOverlayOverSubject, "there are no motion graphics")
	case len(c.in.Subjects) == 0:
		return c.skip(CheckOverlayOverSubject, "the clips have no subject boxes (the quick look reads them: run cfctl assets quicklook --subjects for older clips)")
	}
	out := c.in.Spec.Output
	var bad, notes []string
	checked := 0
	for _, g := range items {
		regions := subject.Regions(c.in.Spec, c.in.Subjects.Lookup, g.AtMs, g.EndMs())
		if len(regions) == 0 {
			continue
		}
		checked++
		txt := g.AsText()
		where := c.headBand(regions)
		if !g.AvoidsSubject() {
			if cover, err := timeline.TextCovers(out, txt, regions); err == nil && cover.Body {
				bad = append(bad, fmt.Sprintf("%s (%s) covers the person and is set not to avoid them (%s): remove avoid_subject false", g.ID, g.Template, where))
			}
			continue
		}
		_, fit, _, err := fitGraphic(c.in.Spec, c.in.Subjects, g, c.total)
		if err != nil {
			continue
		}
		// The hook title and the chapter titles must be where the editor put them; another graphic may move.
		essential := g.AtMs <= hookTitleByMs || g.Template == timeline.TemplateChapterCard || IsChapterTitle(g.DisplayText())
		switch fit.How {
		case subject.FitDropped:
			bad = append(bad, fmt.Sprintf("%s (%s) would cover the person's body at every time of the video, so the renderer leaves it out (%s): %s", g.ID, g.Template, where, c.titleFix(txt, fit)))
		case subject.FitMoved:
			msg := fmt.Sprintf("%s (%s) has no room clear of the person's body at %d-%d ms (%s), so the renderer shows it at %d-%d ms", g.ID, g.Template, g.AtMs, g.EndMs(), where, fit.AtMs, fit.AtMs+fit.DurationMs)
			if essential {
				bad = append(bad, msg+": "+c.titleFix(txt, fit))
			} else {
				notes = append(notes, msg+"; set at_ms there yourself, or use fewer words or a lower scale")
			}
		case subject.FitTrimmed:
			notes = append(notes, fmt.Sprintf("%s (%s) fits clear of the person's body only at %d-%d ms of its %d-%d ms (%s), so the renderer shows it then (shorter, its accents after the cut dropped); set those times yourself, or use fewer words or a lower scale",
				g.ID, g.Template, fit.AtMs, fit.AtMs+fit.DurationMs, g.AtMs, g.EndMs(), where))
		default:
			if av := fit.Avoidance; av.Overlapped {
				notes = append(notes, fmt.Sprintf("%s (%s) is placed %s the person (%s)", g.ID, g.Template, av.Where, where))
			}
		}
	}
	switch {
	case len(bad) > 0:
		return c.fail(CheckOverlayOverSubject, "never over the body: %s", strings.Join(bad[:min(3, len(bad))], "; "))
	case checked == 0:
		return c.skip(CheckOverlayOverSubject, "no motion graphic is on top of a clip with a subject box")
	case len(notes) > 0:
		return c.pass(CheckOverlayOverSubject, "%s", strings.Join(notes[:min(3, len(notes))], "; "))
	}
	return c.pass(CheckOverlayOverSubject, "%d motion graphics on clips with a person, none on the person", checked)
}

func (c *checker) overlayLegibility() Check {
	items := graphicsOf(c.in.Spec)
	switch {
	case len(items) == 0:
		return c.skip(CheckOverlayLegibility, "there are no motion graphics")
	case c.in.Overlay == nil:
		return c.skip(CheckOverlayLegibility, "%s", firstNonEmpty(c.in.OverlaySkip, "the preview render was not measured behind the graphics"))
	}
	out := c.in.Spec.Output
	var bad, notes []string
	measured := 0
	for _, g := range items {
		m, ok := c.in.Overlay[g.ID]
		if !ok {
			notes = append(notes, fmt.Sprintf("%s fills its shot, so the picture behind it could not be measured without it", g.ID))
			continue
		}
		r, err := motion.Resolve(out, g, nil)
		if err != nil {
			continue
		}
		measured++
		p := r.Params
		if p.Plate != nil || p.Outline != nil {
			continue // its own background: a plate or an outline around every letter
		}
		text := motion.Luminance(strings.ToUpper(p.Fill))
		// Light letters are hardest to read on the brightest part of the picture, dark ones on the darkest.
		bg, part := lumaToLuminance(m.High), "bright"
		if text < 0.4 {
			bg, part = lumaToLuminance(m.Low), "dark"
		}
		ratio := (max(text, bg) + 0.05) / (min(text, bg) + 0.05)
		floor := legibleRatio
		if p.Shadow != nil {
			floor = legibleRatioShadow
		}
		if ratio < floor {
			bad = append(bad, fmt.Sprintf("%s (%s, letters %s) stands out from the %s part of the picture behind it at only %.1f:1 (measured %s it, at least %.1f:1 wanted): give it backing plate, a fill that contrasts with that picture, shadow strong, or move it (another anchor or time) over a calmer part of the frame",
				g.ID, g.Template, p.Fill, part, ratio, m.Probe.When, floor))
		}
	}
	switch {
	case len(bad) > 0:
		return c.fail(CheckOverlayLegibility, "%s", strings.Join(bad[:min(3, len(bad))], "; "))
	case measured == 0:
		return c.skip(CheckOverlayLegibility, "%s", strings.Join(notes[:min(2, len(notes))], "; "))
	}
	if len(notes) > 0 {
		return c.pass(CheckOverlayLegibility, "%d motion graphics stand out from the picture behind them; %s", measured, strings.Join(notes[:min(2, len(notes))], "; "))
	}
	return c.pass(CheckOverlayLegibility, "%d motion graphics stand out from the picture behind them", measured)
}

// lumaToLuminance turns an 8-bit limited-range luma into an estimate of relative luminance (the sRGB curve on the normalised luma).
func lumaToLuminance(y float64) float64 {
	v := min(1, max(0, (y-16)/219))
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
