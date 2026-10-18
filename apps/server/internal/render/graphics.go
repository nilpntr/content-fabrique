package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The graphics track (D-35, docs/design/motion-graphics-v1.md). This file is the seam between the renderer and the motion-graphics
// layer, kept apart from the text, camera and audio code on purpose:
//
//   - With motion graphics on (the default), every graphics item is resolved by internal/motion (its box is placed like a text item,
//     in the safe zone and off the subject), and the plan carries the job cf-motion renders (Plan.Motion). The frames it writes are
//     image-sequence inputs of the render, one per segment, laid over the joined clips before the ASS script (so the watermark and
//     the captions stay on top).
//   - With motion graphics off (Profile.MotionFallback), each graphics item becomes a text item with the template's fallback
//     animation: the same words in the same box, drawn by libass. Nothing is lost but the motion.

// MotionPlan is what Execute needs to render a plan's overlays.
type MotionPlan struct {
	Job motion.Job `json:"job"`
	// BundleHash identifies the template bundle the plan was compiled against.
	BundleHash string `json:"bundle_hash"`
}

// MotionOptions configure the motion-graphics run of Execute.
type MotionOptions struct {
	// Chrome is the chrome-headless-shell binary for the local provider (the media-tools image has its own); empty means the
	// CF_MOTION_CHROME environment variable.
	Chrome string
	// Report, if set, receives what the run did (frames, the runner's timing), for logs and benchmarks.
	Report func(motion.Report)
}

// motionBundleDir is the directory in the work directory that the template bundle is staged into (and mounted read-only from):
// beside the motion run's own writable directory (motion.OutDir), never inside it.
const motionBundleDir = "motion-bundle"

// runMotion renders the plan's overlays into the work directory, through the same runner as ffmpeg (the media-tools image has
// cf-motion and Chrome). A failure fails the render: a title that silently went missing would be worse.
func runMotion(ctx context.Context, plan Plan, runner media.Runner, work string, opts ExecuteOptions, inImage bool) error {
	if plan.Motion == nil {
		return nil
	}
	if plan.Motion.BundleHash != motion.BundleHash() {
		return fmt.Errorf("render: the plan was compiled against another template bundle (%.12s, this build has %.12s): compile it again", plan.Motion.BundleHash, motion.BundleHash())
	}
	bundle := filepath.Join(work, motionBundleDir)
	_ = os.RemoveAll(bundle)
	if err := os.MkdirAll(bundle, 0o755); err != nil { //nolint:gosec // read by the sandbox user
		return fmt.Errorf("render: motion bundle: %w", err)
	}
	o := motion.Options{InImage: inImage, Chrome: opts.Motion.Chrome}
	if !inImage {
		if o.Chrome == "" {
			o.Chrome = os.Getenv("CF_MOTION_CHROME")
		}
		var files []string
		for _, f := range plan.Motion.Job.Fonts {
			files = append(files, f.File)
		}
		dir, err := findFontsDir(opts.FontsDir, files)
		if err != nil {
			return err
		}
		o.FontsDir = dir
	}
	rep, err := motion.Render(ctx, runner, plan.Motion.Job, work, bundle, o)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if opts.Motion.Report != nil {
		opts.Motion.Report(rep)
	}
	return nil
}

// graphicsItems returns the items of the spec's graphics track.
func (c *compiler) graphicsItems() []timeline.GraphicsItem {
	for _, t := range c.spec.Tracks {
		if g, ok := t.(*timeline.GraphicsTrack); ok {
			return g.Items
		}
	}
	return nil
}

// graphics prepares the graphics track. In the fallback it replaces c.spec by a copy with the items added as text items; otherwise
// it fills c.plan.Motion and c.overlays. Either way the items are laid out by motion.Layout first (never over the body, never over
// another overlay or a text on screen at the same time), the function the editor's checks call too.
func (c *compiler) graphics() error {
	items := c.graphicsItems()
	if len(items) == 0 {
		return nil
	}
	for _, it := range items {
		if field, err := it.CheckParams(); err != nil {
			return fmt.Errorf("%w: graphics item %s: %s: %w", ErrInvalidSpec, it.ID, field, err)
		}
	}
	subjects := subject.Index{}
	for _, cl := range c.clips {
		if cl.asset.Subject != nil {
			subjects[cl.item.AssetID] = *cl.asset.Subject
		}
	}
	var lookup subject.Lookup
	if len(subjects) > 0 {
		lookup = subjects.Lookup
	}
	laid, err := motion.Layout(c.spec, lookup, c.totalMs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSpec, err)
	}
	var resolved []motion.Resolved
	var fallback []timeline.TextItem
	for _, l := range laid {
		if !c.warnLaid(l) {
			continue
		}
		if l.Item.AtMs >= c.totalMs {
			continue
		}
		if c.profile.MotionFallback {
			fallback = append(fallback, fallbackText(l, c.spec.Output))
			continue
		}
		resolved = append(resolved, l.Resolved)
	}
	if c.profile.MotionFallback {
		cp := *c.spec
		cp.Tracks = slices.Clone(c.spec.Tracks)
		cp.Tracks = append(cp.Tracks, &timeline.TextTrack{ID: "graphics_fallback", Items: fallback})
		c.spec = &cp
		return nil
	}
	if len(resolved) == 0 {
		return nil
	}
	job, err := motion.BuildJob(c.spec.Output, c.fps, c.total, resolved)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSpec, err)
	}
	c.plan.Motion = &MotionPlan{Job: job, BundleHash: motion.BundleHash()}
	return nil
}

// warnLaid adds the warnings of a laid-out item and says whether it is drawn.
func (c *compiler) warnLaid(l motion.Laid) bool {
	it, id := l.Original, l.Original.ID
	switch {
	case l.Fit.How == subject.FitDropped:
		c.warn("text_over_subject", "graphics item %s is left out: it would cover the subject and there is no room above the head, beside it or below the person at any time of the video", id)
		return false
	case l.NotPlaceable:
		c.warn("text_dropped", "graphics item %s is left out: its box cannot be put inside the %s safe zone", id, c.spec.Output.Format)
		return false
	case l.Clash == motion.ClashDropped:
		c.warn("graphics_overlap", "graphics item %s is left out: it would be drawn over %s (on screen %d-%d ms) and there is no room below or above it or at a time next to it", id, l.With, l.WithAtMs, l.WithEndMs)
		return false
	}
	if l.Fit.How == subject.FitTrimmed || l.Fit.How == subject.FitMoved {
		c.warn("text_retimed", "graphics item %s was %s to %d-%d ms, where it fits clear of the subject (at %d-%d ms it would cover their body)",
			id, map[string]string{subject.FitTrimmed: "shortened", subject.FitMoved: "moved"}[l.Fit.How], l.Fit.AtMs, l.Fit.AtMs+l.Fit.DurationMs, it.AtMs, it.EndMs())
	}
	switch l.Clash {
	case motion.ClashBelow, motion.ClashAbove:
		c.warn("graphics_overlap", "graphics item %s was moved %s %s, which is on screen at the same time in the same place", id, l.Clash, l.With)
	case motion.ClashMoved:
		c.warn("graphics_overlap", "graphics item %s was moved to %d-%d ms: at its own time it would be drawn over %s (on screen %d-%d ms)", id, l.Item.AtMs, l.Item.EndMs(), l.With, l.WithAtMs, l.WithEndMs)
	}
	switch av := l.Resolved.Avoid; {
	case av.OverHead:
		c.warn("text_over_subject", "graphics item %s covers the subject's head and could not be moved off it inside the safe zone", id)
	case av.OverBody:
		c.warn("text_over_subject", "graphics item %s is off the subject's head but still covers their body", id)
	case av.Overlapped && av.Where == timeline.WhereSmaller:
		c.warn("text_moved", "graphics item %s was shrunk to scale %g to keep it clear of the subject", id, av.Scale)
	case av.Overlapped:
		c.warn("text_moved", "graphics item %s was moved %s the subject to keep it clear of them", id, av.Where)
	}
	return true
}

// fallbackText is a laid-out item as the text item the fallback draws: at the time it was laid out at, and where it was stacked to
// keep it off another overlay, at the centre of its box (placed already, so it does not avoid the subject again).
func fallbackText(l motion.Laid, out timeline.Output) timeline.TextItem {
	t := l.Item.AsText()
	if l.Clash == motion.ClashBelow || l.Clash == motion.ClashAbove {
		b := l.Resolved.Box
		w, h := float64(out.Width), float64(out.Height)
		t.Anchor, t.Position = "", &timeline.Point{X: float64(b.Left+b.Right) / 2 / w, Y: float64(b.Top+b.Bottom) / 2 / h}
		if sc := l.Resolved.Avoid.Scale; sc > 0 {
			t.Scale = &sc
		}
		no := false
		t.AvoidSubject = &no
	}
	return t
}

// graphicsTimeMap adds the overlays to the time map like text items: [frame(at_ms), frame(end)). (In the fallback they are text items
// of the spec the compiler draws, and collectMaps has them already.)
func (c *compiler) graphicsTimeMap() {
	if c.plan.Motion == nil {
		return
	}
	for _, it := range c.plan.Motion.Job.Items {
		start, end := timeline.Frame(it.StartMs, c.fps), min(timeline.Frame(it.EndMs, c.fps), c.total)
		c.plan.TimeMap = append(c.plan.TimeMap, TimeMapEntry{
			OutStartMs: timeline.FrameStartMs(start, c.fps), OutEndMs: timeline.FrameStartMs(end, c.fps), ItemID: it.ID,
		})
	}
}

func (c *compiler) warn(code, format string, args ...any) {
	c.plan.Warnings = append(c.plan.Warnings, Warning{Code: code, Message: fmt.Sprintf(format, args...)})
}

// overlayInputArgs are the image-sequence inputs of the overlay frames, one per segment, after every other input. They are read from
// the work directory (ffmpeg's working directory in every provider), where Execute has the motion job write them.
func (c *compiler) overlayInputArgs() []string {
	if c.plan.Motion == nil {
		return nil
	}
	var args []string
	for k := range c.plan.Motion.Job.Segments {
		args = append(args, "-protocol_whitelist", "file", "-f", "image2", "-framerate", strconv.Itoa(c.fps), "-start_number", "0",
			"-i", motion.FramePattern(k))
	}
	return args
}

// overlayGraph lays the overlay segments over the pad cur, ending in out: each segment's frames are shifted to its first frame's time
// and overlaid at its clip; before and after a segment the picture passes through untouched.
func (c *compiler) overlayGraph(cur, out string) []string {
	m := c.plan.Motion
	if m == nil {
		return nil
	}
	var stmts []string
	base := len(c.inputs)
	d := c.profile.SizeDivisor
	for k, s := range m.Job.Segments {
		in := fmt.Sprintf("%d:v", base+k)
		ov := "mo" + strconv.Itoa(k)
		filters := []string{"setpts=PTS-STARTPTS+" + c.frameSecs(s.StartFrame) + "/TB"}
		if d > 1 {
			// The frames are the output's own size (motion.BuildJob); a smaller profile scales them, area-averaged.
			filters = append(filters, fmt.Sprintf("scale=%d:%d:flags=area", max(1, s.Clip.W/d), max(1, s.Clip.H/d)))
		}
		stmts = append(stmts, chain([]string{in}, filters, ov))
		next := "mv" + strconv.Itoa(k)
		if k == len(m.Job.Segments)-1 {
			next = out
		}
		stmts = append(stmts, chain([]string{cur, ov}, []string{fmt.Sprintf("overlay=x=%d:y=%d:eof_action=pass:repeatlast=0:format=auto", s.Clip.X/d, s.Clip.Y/d)}, next))
		cur = next
	}
	return stmts
}
