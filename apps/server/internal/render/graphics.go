package render

import (
	"context"
	"errors"
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

// motionBundleDir is the directory in the work directory that the template bundle is staged into (and mounted read-only from).
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
// it fills c.plan.Motion and c.overlays.
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
	if c.profile.MotionFallback {
		cp := *c.spec
		cp.Tracks = slices.Clone(c.spec.Tracks)
		tt := &timeline.TextTrack{ID: "graphics_fallback"}
		for _, it := range items {
			tt.Items = append(tt.Items, it.AsText())
		}
		cp.Tracks = append(cp.Tracks, tt)
		c.spec = &cp
		return nil
	}
	subjects := subject.Index{}
	for _, cl := range c.clips {
		if cl.asset.Subject != nil {
			subjects[cl.item.AssetID] = *cl.asset.Subject
		}
	}
	var resolved []motion.Resolved
	var lookup subject.Lookup
	if len(subjects) > 0 {
		lookup = subjects.Lookup
	}
	for _, it := range items {
		// Never over the person's body, like a text item (P6-06): where there is no room at the item's own time it is shown in the part
		// of its time that has room, or at the nearest time that has, or it is left out (subject.FitText, the renderer's and the check's).
		fit, err := subject.FitText(c.spec, lookup, it.AsText(), c.totalMs)
		if err != nil {
			return fmt.Errorf("%w: graphics item %s: %w", ErrInvalidSpec, it.ID, err)
		}
		switch fit.How {
		case subject.FitDropped:
			c.warn("text_over_subject", "graphics item %s is left out: it would cover the subject and there is no room above the head, beside it or below the person at any time of the video", it.ID)
			continue
		case subject.FitTrimmed, subject.FitMoved:
			c.warn("text_retimed", "graphics item %s was %s to %d-%d ms, where it fits clear of the subject (at %d-%d ms it would cover their body)",
				it.ID, map[string]string{subject.FitTrimmed: "shortened", subject.FitMoved: "moved"}[fit.How], fit.AtMs, fit.AtMs+fit.DurationMs, it.AtMs, it.EndMs())
			it = retime(it, fit.AtMs, fit.DurationMs, fit.How == subject.FitMoved)
		}
		r, err := motion.Resolve(c.spec.Output, it, fit.Regions)
		if errors.Is(err, motion.ErrNotPlaceable) {
			c.plan.Warnings = append(c.plan.Warnings, Warning{Code: "text_dropped", Message: fmt.Sprintf("graphics item %s is left out: its box cannot be put inside the %s safe zone", it.ID, c.spec.Output.Format)})
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: graphics item %s: %w", ErrInvalidSpec, it.ID, err)
		}
		av := r.Avoid
		switch {
		case av.OverHead:
			c.warn("text_over_subject", "graphics item %s covers the subject's head and could not be moved off it inside the safe zone", it.ID)
		case av.OverBody:
			c.warn("text_over_subject", "graphics item %s is off the subject's head but still covers their body", it.ID)
		case av.Overlapped && av.Where == timeline.WhereSmaller:
			c.warn("text_moved", "graphics item %s was shrunk to scale %g to keep it clear of the subject", it.ID, av.Scale)
		case av.Overlapped:
			c.warn("text_moved", "graphics item %s was moved %s the subject to keep it clear of them", it.ID, av.Where)
		}
		if it.AtMs >= c.totalMs {
			continue
		}
		resolved = append(resolved, r)
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

// retime gives an item a new time: a moved item keeps its accents where they were in it (shifted with it), a shortened one keeps the
// accents its new time still holds.
func retime(it timeline.GraphicsItem, at, dur int64, moved bool) timeline.GraphicsItem {
	shift := int64(0)
	if moved {
		shift = at - it.AtMs
	}
	var acc []int64
	for _, a := range it.Params.AccentsMs {
		if a += shift; a >= at && a < at+dur {
			acc = append(acc, a)
		}
	}
	it.AtMs, it.DurationMs, it.Params.AccentsMs = at, dur, acc
	return it
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
