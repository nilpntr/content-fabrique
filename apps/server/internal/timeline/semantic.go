package timeline

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// checker runs layer 2: what the schema cannot express. All arithmetic is int64 milliseconds. Each check raises its
// own code, and a problem is reported by the check that owns it only, so a fixture, or a model's edit, that breaks one
// rule sees one code.
type checker struct {
	spec    *Spec
	assets  AssetCatalog
	issues  []Issue
	total   int64
	mainIdx int
	// clips are the playable clips of the main track (in_ms < out_ms), in timeline order.
	clips []clip
	// skipped counts the clips left out of the length checks because in_ms >= out_ms.
	skipped int
	// tooLong holds the array index of every clip whose transition_in already raised transition_too_long.
	tooLong map[int]bool
}

type clip struct {
	item  VideoItem
	index int   // position in the track's items array, for paths
	dur   int64 // output duration
	end   int64 // at_ms + dur
}

func checkSemantics(spec *Spec, assets AssetCatalog) []Issue {
	c := &checker{spec: spec, assets: assets}
	main, mainIdx, ok := MainTrack(spec)
	c.mainIdx = mainIdx
	c.checkDuplicateIDs()
	if ok {
		c.checkMain(main)
	}
	c.checkTotal()
	c.checkText()
	c.checkAudio()
	c.checkMarkers()
	return c.issues
}

func (c *checker) add(code Code, path, format string, args ...any) {
	c.addSeverity(SeverityError, code, path, format, args...)
}

func (c *checker) addSeverity(sev Severity, code Code, path, format string, args ...any) {
	c.issues = append(c.issues, Issue{
		Code: code, Path: path, Severity: sev, Message: fmt.Sprintf(format, args...),
	})
}

func (c *checker) mainPath(i int) string {
	return fmt.Sprintf("/tracks/%d/items/%d", c.mainIdx, i)
}

// ---- duplicate_id -----------------------------------------------------------------------------------------------

func (c *checker) checkDuplicateIDs() {
	first := map[string]string{} // id -> path of the first use
	use := func(id, path string) {
		if prev, dup := first[id]; dup {
			c.add(CodeDuplicateID, path+"/id",
				"id %q is already used at %s; ids are unique across the whole spec (tracks and items), so give this one another id",
				id, prev)
			return
		}
		first[id] = path
	}
	for ti, t := range c.spec.Tracks {
		tp := fmt.Sprintf("/tracks/%d", ti)
		use(t.TrackID(), tp)
		switch t := t.(type) {
		case *VideoTrack:
			for i, it := range t.Items {
				use(it.ID, fmt.Sprintf("%s/items/%d", tp, i))
			}
		case *TextTrack:
			for i, it := range t.Items {
				use(it.ID, fmt.Sprintf("%s/items/%d", tp, i))
			}
		case *AudioTrack:
			for i, it := range t.Items {
				use(it.Common().ID, fmt.Sprintf("%s/items/%d", tp, i))
			}
		}
	}
}

// ---- main track -------------------------------------------------------------------------------------------------

func (c *checker) checkMain(main *VideoTrack) {
	for i, it := range main.Items {
		path := c.mainPath(i)
		asset, known := c.checkClipAsset(it, path)
		if !Playable(it) {
			c.add(CodeSourceOutOfRange, path+"/source",
				"item %s has source.in_ms %d, which is not below source.out_ms %d; a clip needs in_ms < out_ms",
				it.ID, it.Source.InMs, it.Source.OutMs)
			c.skipped++
			continue // left out of the length, contiguity and total-duration checks
		}
		dur := OutputDuration(it)
		if known && it.Source.OutMs > asset.DurationMs {
			c.add(CodeSourceOutOfRange, path+"/source/out_ms",
				"item %s ends at %d ms but asset %s is %d ms long; set source.out_ms to at most %d",
				it.ID, it.Source.OutMs, it.AssetID, asset.DurationMs, asset.DurationMs)
		}
		if dur < MinShotMs && it.HasRamp() {
			c.add(CodeShotTooShort, path,
				"item %s plays for %d ms on a speed ramp (source %d to %d ms); a shot must play for at least %d ms. Lengthen its source range, or lower the speeds of the ramp points (set_speed would replace the ramp)",
				it.ID, dur, it.Source.InMs, it.Source.OutMs, MinShotMs)
		} else if dur < MinShotMs {
			c.add(CodeShotTooShort, path,
				"item %s plays for %d ms (source %d to %d ms at speed %s); a shot must play for at least %d ms. Lengthen its source range or slow it down",
				it.ID, dur, it.Source.InMs, it.Source.OutMs, num(it.SpeedOrDefault()), MinShotMs)
		}
		c.checkSpeedRamp(it, path)
		c.checkMotion(it, path, dur)
		c.clips = append(c.clips, clip{item: it, index: i, dur: dur, end: EndMs(it)})
	}
	// Timeline order is at_ms order; equal at_ms keeps the order of the array.
	slices.SortStableFunc(c.clips, func(a, b clip) int {
		switch {
		case a.item.AtMs < b.item.AtMs:
			return -1
		case a.item.AtMs > b.item.AtMs:
			return 1
		}
		return a.index - b.index
	})
	c.total = PlayableEnd(main.Items)
	c.checkContiguity()
	c.checkTransitionFit()
}

// ---- speed ramps and motion (P3-10) ------------------------------------------------------------------------------

// checkSpeedRamp checks a clip's speed ramp: it replaces speed, so the clip may not have both; its points are sorted by
// at_ms, and at_ms is a source offset, so it must lie inside the clip's own source range. The speed of each point is
// range-checked by the schema. The clip's source audio does not follow a ramp (atempo cannot ramp), so it is muted, which
// is a warning.
func (c *checker) checkSpeedRamp(it VideoItem, path string) {
	if !it.HasRamp() {
		return
	}
	if it.Speed != nil {
		c.add(CodeInvalidSpeedRamp, path+"/speed_ramp",
			"item %s has both speed and speed_ramp; the ramp replaces the speed, so give only one. Remove speed (the ramp sets the speed along the clip), or remove speed_ramp",
			it.ID)
	}
	span := it.Source.OutMs - it.Source.InMs
	for i, p := range it.SpeedRamp {
		pp := fmt.Sprintf("%s/speed_ramp/%d/at_ms", path, i)
		if i > 0 && p.AtMs <= it.SpeedRamp[i-1].AtMs {
			c.add(CodeInvalidSpeedRamp, pp,
				"speed_ramp point %d of item %s is at %d ms, not after point %d at %d ms; the points must be sorted with strictly increasing at_ms",
				i, it.ID, p.AtMs, i-1, it.SpeedRamp[i-1].AtMs)
		}
		if p.AtMs > span {
			c.add(CodeInvalidSpeedRamp, pp,
				"speed_ramp point %d of item %s is at %d ms, but at_ms counts from source.in_ms and the source range is only %d ms long (%d to %d); use an at_ms from 0 to %d",
				i, it.ID, p.AtMs, span, it.Source.InMs, it.Source.OutMs, span)
		}
	}
	if a := it.Audio; a != nil && ClipAudioMode(a) == AudioModeFull {
		field := "/audio/use_source"
		if a.Mode != "" {
			field = "/audio/mode"
		}
		c.addSeverity(SeverityWarning, CodeRampAudioMuted, path+field,
			"item %s has a speed ramp, so its own audio is muted in full mode: speech cannot follow a changing speed. Set the mode to mute or bed (a bed plays at the clip's average speed under the ramp), or give the clip a constant speed to keep its speech",
			it.ID)
	}
}

// checkMotion checks a clip's motion preset: the clip must play for at least MinMotionShotMs, at_ms (where punch_in lands or
// shake starts, in ms from the clip's first frame) must lie inside the clip and is only for punch_in and shake.
func (c *checker) checkMotion(it VideoItem, path string, dur int64) {
	m := it.Motion
	if m == nil {
		return
	}
	mp := path + "/motion"
	if !m.Active() {
		if m.AtMs != nil || m.Strength != nil {
			c.add(CodeInvalidMotion, mp,
				"item %s has motion preset none with at_ms or strength; none takes no parameters. Leave them out, or choose punch_in, push_in, pull_back or shake",
				it.ID)
		}
		return
	}
	if dur < MinMotionShotMs {
		c.add(CodeInvalidMotion, mp+"/preset",
			"item %s plays for %d ms, too short for the motion preset %s, which needs a shot of at least %d ms. Lengthen the clip or remove the motion",
			it.ID, dur, m.Preset, MinMotionShotMs)
	}
	if m.AtMs == nil {
		return
	}
	if m.Preset != MotionPunchIn && m.Preset != MotionShake {
		c.add(CodeInvalidMotion, mp+"/at_ms",
			"item %s has at_ms on the motion preset %s, which runs over the whole clip and has no moment to land on; at_ms is only for punch_in and shake. Remove at_ms",
			it.ID, m.Preset)
		return
	}
	if *m.AtMs >= dur {
		c.add(CodeInvalidMotion, mp+"/at_ms",
			"item %s has motion at_ms %d, but the clip plays for only %d ms and at_ms counts from the clip's first frame; use an at_ms from 0 to %d",
			it.ID, *m.AtMs, dur, dur-1)
	}
}

// checkClipAsset looks the clip's asset up once. It raises unknown_asset when the catalog does not know the asset and
// wrong_asset_kind when it is not a video asset, and returns the asset for the range check.
func (c *checker) checkClipAsset(it VideoItem, path string) (Asset, bool) {
	asset, ok := c.assets.Lookup(it.AssetID)
	if !ok {
		c.add(CodeUnknownAsset, path+"/asset_id",
			"item %s uses asset %s, which is not in the catalog; use the id of an asset of this project",
			it.ID, it.AssetID)
		return Asset{}, false
	}
	if asset.Kind != AssetVideo {
		c.add(CodeWrongAssetKind, path+"/asset_id",
			"item %s is a video clip but asset %s is a %s asset; use a video asset",
			it.ID, it.AssetID, asset.Kind)
	}
	return asset, true
}

// checkContiguity checks the main track against the design note's formula: the first clip starts at 0, and each next
// clip starts where the previous one ends minus its own transition. Each clip is compared with the real end of the
// one before it, so one mistake is one issue, not a cascade.
func (c *checker) checkContiguity() {
	for n, cl := range c.clips {
		it := cl.item
		path := c.mainPath(cl.index) + "/at_ms"
		if n == 0 {
			if it.AtMs != 0 {
				c.add(CodeGap, path,
					"the first item %s starts at %d ms; the main track starts at 0, so set at_ms to 0",
					it.ID, it.AtMs)
			}
			if it.TransitionIn != nil && it.TransitionIn.Type != TransitionCut {
				c.add(CodeFirstTransition, c.mainPath(cl.index)+"/transition_in",
					"item %s is the first clip in time (at_ms %d) but has a %s transition_in; nothing comes before it to transition from. Remove transition_in or make it a cut",
					it.ID, it.AtMs, it.TransitionIn.Type)
			}
			continue
		}
		prev := c.clips[n-1]
		tr := it.TransitionDurationMs()
		want := prev.end - tr
		switch {
		case it.AtMs > want:
			c.add(CodeGap, path,
				"item %s starts at %d ms but item %s ends at %d ms%s, leaving a gap of %d ms; set at_ms to %d",
				it.ID, it.AtMs, prev.item.ID, prev.end, transitionNote(it), it.AtMs-want, want)
		case it.AtMs < want:
			if tr == 0 {
				c.add(CodeOverlap, path,
					"item %s starts at %d ms, %d ms before item %s ends at %d ms, and has no transition that explains the overlap; set at_ms to %d",
					it.ID, it.AtMs, want-it.AtMs, prev.item.ID, prev.end, want)
			} else {
				c.add(CodeOverlap, path,
					"item %s starts at %d ms but its %d ms %s should start it at %d ms (item %s ends at %d ms); set at_ms to %d",
					it.ID, it.AtMs, tr, it.TransitionIn.Type, want, prev.item.ID, prev.end, want)
			}
		}
		if tr > 0 && (tr >= cl.dur || tr >= prev.dur) {
			if c.tooLong == nil {
				c.tooLong = map[int]bool{}
			}
			c.tooLong[cl.index] = true
			c.add(CodeTransitionTooLong, c.mainPath(cl.index)+"/transition_in/duration_ms",
				"the %s into item %s is %d ms, but a transition must be shorter than both clips it joins (item %s plays %d ms, item %s plays %d ms); shorten it to under %d ms",
				it.TransitionIn.Type, it.ID, tr, prev.item.ID, prev.dur, it.ID, cl.dur, min(cl.dur, prev.dur))
		}
	}
}

// checkTransitionFit checks what the renderer needs after snapping to frames, which the millisecond check above cannot
// see: a transition must overlap fewer frames than either clip it joins, and the transition into a clip and the one out of
// it must together leave at least one frame of the clip on its own. render.Compile refuses the same specs.
func (c *checker) checkTransitionFit() {
	fps := c.spec.Output.FPS
	if fps <= 0 {
		return
	}
	frames := func(ms int64) int64 { return Frame(ms, fps) }
	for n, cl := range c.clips {
		own := frames(cl.end) - frames(cl.item.AtMs)
		var in, out int64
		if n > 0 && cl.item.TransitionDurationMs() > 0 {
			in = frames(c.clips[n-1].end) - frames(cl.item.AtMs)
		}
		var next clip
		if n+1 < len(c.clips) && c.clips[n+1].item.TransitionDurationMs() > 0 {
			next = c.clips[n+1]
			out = frames(cl.end) - frames(next.item.AtMs)
		}
		if in == 0 && out == 0 {
			continue
		}
		if in+out < own && in < own && out < own {
			continue
		}
		// Blame the later transition: the one out of this clip if it has one, else the one into it.
		blame := cl
		if out > 0 {
			blame = next
		}
		if c.tooLong[blame.index] {
			continue
		}
		c.add(CodeTransitionTooLong, c.mainPath(blame.index)+"/transition_in/duration_ms",
			"item %s plays %d frames at %d fps, but the transitions into and out of it take %d and %d frames: together they must leave at least one frame of the clip, and each must be shorter than the clips it joins. Shorten the %s into item %s",
			cl.item.ID, own, fps, in, out, blame.item.TransitionIn.Type, blame.item.ID)
	}
}

func transitionNote(it VideoItem) string {
	if tr := it.TransitionDurationMs(); tr > 0 {
		return fmt.Sprintf(" minus its %d ms transition", tr)
	}
	return ""
}

// ---- total duration ---------------------------------------------------------------------------------------------

func (c *checker) checkTotal() {
	path := fmt.Sprintf("/tracks/%d/items", c.mainIdx)
	if len(c.clips) == 0 && c.skipped > 0 {
		return // every clip has in_ms >= out_ms and source_out_of_range already says so; that is the one code
	}
	switch {
	case len(c.clips) == 0:
		c.add(CodeDurationOutOfRange, path,
			"the main track has no playable clip, so the timeline is 0 ms long; it must be between %d and %d ms. Add a clip",
			MinTotalMs, MaxTotalMs)
	case c.total < MinTotalMs:
		c.add(CodeDurationOutOfRange, path,
			"the timeline is %d ms long; it must be at least %d ms. Add or lengthen clips", c.total, MinTotalMs)
	case c.total > MaxTotalMs:
		c.add(CodeDurationOutOfRange, path,
			"the timeline is %d ms long; it must be at most %d ms. Remove or shorten clips", c.total, MaxTotalMs)
	}
}

// hasTimeline reports whether there is a timeline for other items to be inside of. With no playable clip, a text item
// is not "outside the timeline", the empty main track is the problem, and duration_out_of_range already says so.
func (c *checker) hasTimeline() bool { return len(c.clips) > 0 }

// ---- text -------------------------------------------------------------------------------------------------------

func (c *checker) checkText() {
	band, captionsOn := c.captionBand()
	for ti, t := range c.spec.Tracks {
		tt, ok := t.(*TextTrack)
		if !ok {
			continue
		}
		for i, it := range tt.Items {
			path := fmt.Sprintf("/tracks/%d/items/%d", ti, i)
			st, styleOK := style.Lookup(it.Style)
			if !styleOK {
				c.add(CodeUnknownStyle, path+"/style",
					"text item %s uses style %q, which is not in the style registry; use one of: %s",
					it.ID, it.Style, strings.Join(style.IDs(), ", "))
			}
			// The overrides: each field is checked on its own, at its own path, whatever the style.
			for _, f := range []struct {
				field string
				err   error
			}{
				{"fill", style.Overrides{Fill: it.Fill}.Validate()},
				{"scale", style.Overrides{Scale: it.Scale}.Validate()},
				{"shadow", style.Overrides{Shadow: it.Shadow}.Validate()},
				{"align", alignErr(it.Align)},
			} {
				if f.err != nil {
					c.add(CodeInvalidTextStyle, path+"/"+f.field, "text item %s: %v", it.ID, f.err)
					styleOK = false
				}
			}
			if c.hasTimeline() {
				if end := it.AtMs + it.DurationMs; end > c.total {
					c.add(CodeOutsideTimeline, path,
						"text item %s runs from %d to %d ms but the timeline is only %d ms long; shorten it by %d ms or start it earlier",
						it.ID, it.AtMs, end, c.total, end-c.total)
				}
			}
			if styleOK {
				st, _ = st.With(it.StyleOverrides()) // the font is the base style's, so what the character check reads does not change
				if bad := st.Unsupported(it.Text); len(bad) > 0 {
					c.add(CodeUnsupportedCharacter, path+"/text",
						"text item %s has characters the font of style %s has no glyph for (%s), so they cannot be drawn or measured; use plain Latin or Cyrillic letters, digits and common punctuation",
						it.ID, it.Style, quoteRunes(bad))
				} else {
					c.checkTextBox(it, path, band, captionsOn)
				}
			}
		}
	}
}

func alignErr(a string) error {
	if style.ValidAlign(a) {
		return nil
	}
	return fmt.Errorf("align %q is not one of left, center, right", a)
}

// checkTextBox lays the item out with the renderer's own layout (real font advance widths, word wrap at the safe column,
// outline and shadow included) and requires the box to be inside the safe zone and clear of the blocked areas.
func (c *checker) checkTextBox(it TextItem, path string, band style.Rect, captionsOn bool) {
	out := c.spec.Output
	pl, err := PlaceText(out, it)
	if err != nil {
		return // unknown style and format are reported by their own checks
	}
	box, zone := pl.Box, pl.Zone
	field, what := "/position", ""
	if it.Position != nil {
		what = fmt.Sprintf("at position (%s, %s)", num(it.Position.X), num(it.Position.Y))
	} else {
		field, what = "/anchor", "at anchor "+it.Anchor
	}
	fix := "Move the position inward, or use an anchor (upper_third, center, lower_third)"
	if it.Position == nil {
		fix = "Shorten the text, split it over two items or use a smaller style"
	}
	in := zone.Inner
	if !in.Contains(box) {
		var over []string
		if box.Left < in.Left {
			over = append(over, fmt.Sprintf("its left edge is at x %d, left of the zone (x %d)", box.Left, in.Left))
		}
		if box.Right > in.Right {
			over = append(over, fmt.Sprintf("its right edge is at x %d, right of the zone (x %d)", box.Right, in.Right))
		}
		if box.Top < in.Top {
			over = append(over, fmt.Sprintf("its top edge is at y %d, above the zone (y %d)", box.Top, in.Top))
		}
		if box.Bottom > in.Bottom {
			over = append(over, fmt.Sprintf("its bottom edge is at y %d, below the zone (y %d)", box.Bottom, in.Bottom))
		}
		c.add(CodeUnsafeText, path+field,
			"text item %s %s takes a %d x %d px box in style %s (%d lines, laid out with the font's real widths) and that is outside the %s safe zone (x %d to %d, y %d to %d px): %s. %s",
			it.ID, what, box.Width(), box.Height(), it.Style, len(pl.Layout.Lines), out.Format, in.Left, in.Right, in.Top, in.Bottom,
			strings.Join(over, "; "), fix)
		return
	}
	for _, b := range zone.Blocked {
		if b.Overlaps(box) {
			c.add(CodeUnsafeText, path+field,
				"text item %s %s covers x %d to %d and y %d to %d px, which runs into the %s action column (x above %d where y is below %d px); move it left or up, or use an anchor",
				it.ID, what, box.Left, box.Right, box.Top, box.Bottom, out.Format, b.Left, b.Top)
			return
		}
	}
	if captionsOn && band.Overlaps(box) {
		c.add(CodeTextOverlapsCaptions, path+field,
			"text item %s %s covers y %d to %d px, where the captions are drawn (y %d to %d px, the band of the enabled captions track); move it above or below the band, or use upper_third or lower_third, or turn the captions off",
			it.ID, what, box.Top, box.Bottom, band.Top, band.Bottom)
	}
}

// ---- audio ------------------------------------------------------------------------------------------------------

func (c *checker) checkAudio() {
	c.checkEffectBudget()
	for ti, t := range c.spec.Tracks {
		at, ok := t.(*AudioTrack)
		if !ok {
			continue
		}
		for i, item := range at.Items {
			path := fmt.Sprintf("/tracks/%d/items/%d", ti, i)
			com := item.Common()
			eff := com.EffectiveDurationMs(c.total)
			if c.hasTimeline() {
				c.checkAudioInside(com, path)
			}
			if fadeIn, fadeOut := com.FadeMs(); eff > 0 && fadeIn+fadeOut > eff {
				c.add(CodeFadeTooLong, path,
					"audio item %s fades in for %d ms and out for %d ms, %d ms in total, but it plays for only %d ms; shorten the fades to %d ms together or less",
					com.ID, fadeIn, fadeOut, fadeIn+fadeOut, eff, eff)
			}
			if assetID, hasAsset := item.AssetRef(); hasAsset {
				c.checkAudioAsset(item, assetID, eff, path)
			}
		}
	}
}

func (c *checker) checkAudioInside(com AudioCommon, path string) {
	switch {
	case com.AtMs >= c.total:
		c.add(CodeOutsideTimeline, path+"/at_ms",
			"audio item %s starts at %d ms but the timeline is only %d ms long; start it before the end", com.ID, com.AtMs, c.total)
	case com.DurationMs != nil && com.AtMs+*com.DurationMs > c.total:
		end := com.AtMs + *com.DurationMs
		c.add(CodeOutsideTimeline, path,
			"audio item %s runs from %d to %d ms but the timeline is only %d ms long; shorten duration_ms to at most %d or drop it to play to the end",
			com.ID, com.AtMs, end, c.total, c.total-com.AtMs)
	}
}

func (c *checker) checkAudioAsset(item AudioItem, assetID string, eff int64, path string) {
	com := item.Common()
	asset, known := c.assets.Lookup(assetID)
	if !known {
		c.add(CodeUnknownAsset, path+"/asset_id",
			"audio item %s uses asset %s, which is not in the catalog; use the id of an asset of this project", com.ID, assetID)
		return
	}
	if eff > 0 && com.SourceOffsetMs+eff > asset.DurationMs {
		c.add(CodeSourceOutOfRange, path+"/source_offset_ms",
			"audio item %s needs the asset from %d to %d ms (source_offset_ms plus the %d ms it plays) but asset %s is %d ms long; lower source_offset_ms to at most %d or shorten the item",
			com.ID, com.SourceOffsetMs, com.SourceOffsetMs+eff, eff, assetID, asset.DurationMs, max(asset.DurationMs-eff, 0))
	}
	if asset.Kind != AssetAudio {
		c.add(CodeWrongAssetKind, path+"/asset_id",
			"audio item %s (%s) uses asset %s, which is a %s asset; use an audio asset",
			com.ID, item.AudioKind(), assetID, asset.Kind)
		return // its rights say nothing about audio
	}
	switch item.AudioKind() {
	case AudioLicensedTrack:
		if asset.Rights != RightsLicensed {
			c.add(CodeUnlicensedAudio, path+"/license_id",
				"audio item %s is a licensed_track but asset %s has no licence record (rights: %s); use a track with a licence, or a trend_sound as a cue (it is not mixed into the export)",
				com.ID, assetID, asset.Rights)
		}
	case AudioCreatorAudio:
		if asset.Rights != RightsAttested {
			c.add(CodeUnlicensedAudio, path+"/asset_id",
				"audio item %s is creator_audio but asset %s has no rights attestation from the creator (rights: %s); the creator must attest that they own it first",
				com.ID, assetID, asset.Rights)
		}
	}
}

// ---- markers ----------------------------------------------------------------------------------------------------

func (c *checker) checkMarkers() {
	if !c.hasTimeline() {
		return
	}
	for i, m := range c.spec.Markers {
		if m.AtMs >= c.total {
			c.add(CodeOutsideTimeline, fmt.Sprintf("/markers/%d/at_ms", i),
				"marker %d (%s) is at %d ms but the timeline is only %d ms long; move it before the end or remove it",
				i, m.Kind, m.AtMs, c.total)
		}
	}
}

// num formats a number without trailing zeros, for messages.
func num(f float64) string {
	return fmt.Sprintf("%g", f)
}

// checkEffectBudget refuses a timeline whose sound effects would make the render's filter graph too long for one process
// argument: the effects are the only part of a graph that grows with a count the editor controls freely.
func (c *checker) checkEffectBudget() {
	var n, bytes int
	path := ""
	for ti, t := range c.spec.Tracks {
		at, ok := t.(*AudioTrack)
		if !ok {
			continue
		}
		for _, item := range at.Items {
			if s, ok := item.(*SFX); ok {
				n++
				bytes += SFXGraphBytes(s.Sfx)
				if path == "" {
					path = fmt.Sprintf("/tracks/%d/items", ti)
				}
			}
		}
	}
	if bytes > SFXBudgetBytes {
		c.add(CodeTooManyEffects, path,
			"the timeline has %d sound effects, which would add about %d kB to the render's filter graph (the limit is %d kB, about 30 effects): remove some with remove_sound; the edit style asks for at most 4 per 10 seconds",
			n, bytes/1000, SFXBudgetBytes/1000)
	}
}
