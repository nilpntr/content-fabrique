package timeline

import (
	"cmp"
	"fmt"
	"slices"
)

// ---- b-roll track (docs/design/broll-cutaways.md section 3) -----------------------------------------------------------
//
// Every rule here fires only when the spec has a b-roll track: render jobs re-validate stored versions, and a spec without the track
// must get exactly the issues it always got.

// checkBroll checks the cutaways: their count, assets and source ranges, length in frames, place inside the timeline, that no two
// share a frame, that no shot is shown twice, and (a warning) that there is speech under each.
func (c *checker) checkBroll() {
	bt, ti, ok := BrollTrackOf(c.spec)
	if !ok {
		return
	}
	tp := fmt.Sprintf("/tracks/%d", ti)
	if n := len(bt.Items); n > MaxCutaways {
		c.add(CodeTooManyCutaways, tp+"/items",
			"the b-roll track has %d cutaways; a timeline holds at most %d (each is a separate input of the render). Remove %d, keeping the ones that cover a jump cut or show what is being said",
			n, MaxCutaways, n-MaxCutaways)
	}
	fps := c.spec.Output.FPS
	var cuts []placedCutaway
	for i, it := range bt.Items {
		path := fmt.Sprintf("%s/items/%d", tp, i)
		asset, known := c.checkBrollAsset(it, path)
		if it.Source.InMs >= it.Source.OutMs {
			c.add(CodeSourceOutOfRange, path+"/source",
				"cutaway %s has source.in_ms %d, which is not below source.out_ms %d; a cutaway needs in_ms < out_ms",
				it.ID, it.Source.InMs, it.Source.OutMs)
			continue
		}
		if known && it.Source.OutMs > asset.DurationMs {
			c.add(CodeSourceOutOfRange, path+"/source/out_ms",
				"cutaway %s ends at %d ms of asset %s, which is %d ms long; set source.out_ms to at most %d",
				it.ID, it.Source.OutMs, it.AssetID, asset.DurationMs, asset.DurationMs)
		}
		if fps <= 0 {
			continue // the schema refuses such an fps; nothing can be counted in frames
		}
		start, end := Frame(it.AtMs, fps), Frame(it.EndMs(), fps)
		if (end-start)*1000 < MinShotMs*int64(fps) {
			c.add(CodeShotTooShort, path,
				"cutaway %s is on screen for %d frames at %d fps (source %d to %d ms, at %d ms); a cutaway must show for at least %d ms. Lengthen its source range",
				it.ID, end-start, fps, it.Source.InMs, it.Source.OutMs, it.AtMs, MinShotMs)
		}
		if c.hasTimeline() && it.EndMs() > c.total {
			c.add(CodeOutsideTimeline, path,
				"cutaway %s runs from %d to %d ms but the timeline is only %d ms long; start it earlier or shorten its source range by %d ms",
				it.ID, it.AtMs, it.EndMs(), c.total, it.EndMs()-c.total)
		}
		cuts = append(cuts, placedCutaway{item: it, index: i, start: start, end: end})
	}
	c.checkCutawayOverlap(tp, cuts)
	c.checkSourceReused(tp, bt.Items)
	if !c.hasTimeline() {
		return
	}
	for _, p := range cuts {
		c.checkCutawaySpeech(p.item, fmt.Sprintf("%s/items/%d", tp, p.index))
	}
}

// placedCutaway is a playable cutaway with its frames on the output, [start, end), and its index in the track's items.
type placedCutaway struct {
	item       BrollItem
	index      int
	start, end int64
}

// checkBrollAsset looks the cutaway's asset up: unknown_asset when the catalog does not know it, wrong_asset_kind when it is not video.
func (c *checker) checkBrollAsset(it BrollItem, path string) (Asset, bool) {
	asset, ok := c.assets.Lookup(it.AssetID)
	if !ok {
		c.add(CodeUnknownAsset, path+"/asset_id",
			"cutaway %s uses asset %s, which is not in the catalog; use the id of a video asset of this project", it.ID, it.AssetID)
		return Asset{}, false
	}
	if asset.Kind != AssetVideo {
		c.add(CodeWrongAssetKind, path+"/asset_id",
			"cutaway %s shows asset %s, which is not a video asset (its kind is %s); a cutaway is a picture, so use a video asset", it.ID, it.AssetID, asset.Kind)
	}
	return asset, true
}

// checkCutawayOverlap refuses two cutaways that share a frame. Each is compared with the one that reaches furthest among those that start
// before it, so one cutaway placed over two others is one issue.
func (c *checker) checkCutawayOverlap(tp string, cuts []placedCutaway) {
	order := slices.Clone(cuts)
	var reach placedCutaway
	slices.SortStableFunc(order, func(a, b placedCutaway) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.index, b.index))
	})
	for n := 1; n < len(order); n++ {
		// reach is the cutaway that ends latest among those before this one.
		if prev := order[n-1]; n == 1 || prev.end > reach.end {
			reach = prev
		}
		cur := order[n]
		if cur.start < reach.end && cur.end > cur.start {
			fps := c.spec.Output.FPS
			c.add(CodeOverlap, fmt.Sprintf("%s/items/%d/at_ms", tp, cur.index),
				"cutaway %s starts at %d ms, while cutaway %s is still on screen (until %d ms): two cutaways cannot share a frame. Set at_ms to %d or later, or shorten %s",
				cur.item.ID, cur.item.AtMs, reach.item.ID, FrameStartMs(reach.end, fps), FrameStartMs(reach.end, fps), reach.item.ID)
		}
	}
}

// checkSourceReused refuses a cutaway that shows a part of an asset that another item already shows: a clip of main, or an earlier
// cutaway of the track (the later one in the array is blamed). The ranges are the source ranges in the asset's own time, end-exclusive.
func (c *checker) checkSourceReused(tp string, items []BrollItem) {
	var mainItems []VideoItem
	if main, _, ok := MainTrack(c.spec); ok {
		mainItems = main.Items
	}
	overlaps := func(a, b Source) bool { return a.InMs < b.OutMs && b.InMs < a.OutMs }
	for i, it := range items {
		if it.Source.InMs >= it.Source.OutMs {
			continue
		}
		path := fmt.Sprintf("%s/items/%d/source", tp, i)
		other, kind := "", ""
		var src Source
		for _, m := range mainItems {
			if m.AssetID == it.AssetID && overlaps(m.Source, it.Source) {
				other, kind, src = m.ID, "clip", m.Source
				break
			}
		}
		if other == "" {
			for _, prev := range items[:i] {
				if prev.AssetID == it.AssetID && overlaps(prev.Source, it.Source) {
					other, kind, src = prev.ID, "cutaway", prev.Source
					break
				}
			}
		}
		if other != "" {
			c.add(CodeSourceReused, path,
				"cutaway %s shows %d to %d ms of asset %s, but %s %s already shows %d to %d ms of it: a shot is shown once. Pick another part of the footage, outside %d to %d ms, or another clip",
				it.ID, it.Source.InMs, it.Source.OutMs, it.AssetID, kind, other, src.InMs, src.OutMs, src.InMs, src.OutMs)
		}
	}
}

// checkCutawaySpeech warns about a cutaway with no main clip in audio mode full under it: a cutaway covers the speaker while their speech
// goes on, and over a silent stretch it is just another shot.
func (c *checker) checkCutawaySpeech(it BrollItem, path string) {
	for _, cl := range c.clips {
		if cl.item.AtMs < it.EndMs() && it.AtMs < cl.end && ClipAudioMode(cl.item.Audio) == AudioModeFull {
			return
		}
	}
	c.addSeverity(SeverityWarning, CodeCutawayOverSilence, path,
		"cutaway %s (%d to %d ms) has no clip with its sound in full mode under it, so nothing is said while it is on screen; a cutaway covers a speaker who keeps talking. Put it over speech, or make it a clip of the main track",
		it.ID, it.AtMs, it.EndMs())
}
