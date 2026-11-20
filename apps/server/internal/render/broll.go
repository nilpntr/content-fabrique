package render

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The b-roll track (docs/design/broll-cutaways.md section 4): full-frame cutaways laid over the joined clips, under the motion graphics
// and the text. The stage is one more step of the video graph and touches nothing else:
//
//   - The audio graph reads main only. A cutaway's input is opened with -an and no audio pad is ever mapped from it, and its input comes
//     after every audio input, so the programme, the ducking, the bed and both loudnorm passes are byte for byte what they are without
//     the track.
//   - The captions follow the words of main (the transcript of the clip under a cutaway): nothing here reaches them.
//   - The time map keeps its main entries as they are, so an output time still resolves to exactly one main item; each cutaway adds an
//     entry of its own with track "broll".
//
// In this package "overlay" means the motion-graphics layer (graphics.go); the names here are broll and cutaway.

// RoleBroll is the Input role of a cutaway: its picture only (-an).
const RoleBroll = "broll"

// TrackBroll is the TimeMapEntry.Track of a cutaway's entry. A main item's entry, and a text or graphics item's, has no track.
const TrackBroll = string(timeline.TrackBroll)

// cutaway is a b-roll item with everything the compiler derives from it.
type cutaway struct {
	// k is the cutaway's place in time order: its pads are [b<k>] and [vb<k>].
	k     int
	item  timeline.BrollItem
	asset ResolvedAsset
	// startFrame is its first frame on the output and frames how many it shows, both snapped from at_ms and its end.
	startFrame, frames int64
	// input is its ffmpeg input index (cutawayInputs), after every clip and audio input.
	input int
}

// layoutCutaways snaps the cutaways to the frame grid, in time order, and resolves their assets. Compile does not validate (the validator
// owns the rules); it refuses what it could not draw: an empty source range, one under a frame, one past the end of the timeline, two
// sharing a frame or more than the timeline may hold.
func (c *compiler) layoutCutaways(resolver AssetResolver) error {
	items := slices.Clone(timeline.Cutaways(c.spec))
	if len(items) == 0 {
		return nil
	}
	if len(items) > timeline.MaxCutaways {
		return fmt.Errorf("%w: %d cutaways, at most %d", ErrInvalidSpec, len(items), timeline.MaxCutaways)
	}
	slices.SortStableFunc(items, func(a, b timeline.BrollItem) int { return cmp.Compare(a.AtMs, b.AtMs) })
	resolved := map[string]ResolvedAsset{}
	for _, cl := range c.clips {
		resolved[cl.item.AssetID] = cl.asset
	}
	for k, it := range items {
		if it.DurationMs() <= 0 {
			return fmt.Errorf("%w: cutaway %s plays nothing (in_ms %d, out_ms %d)", ErrInvalidSpec, it.ID, it.Source.InMs, it.Source.OutMs)
		}
		start, end := timeline.Frame(it.AtMs, c.fps), timeline.Frame(it.EndMs(), c.fps)
		switch {
		case end-start < 1:
			return fmt.Errorf("%w: cutaway %s is shorter than one frame at %d fps", ErrInvalidSpec, it.ID, c.fps)
		case end > c.total:
			return fmt.Errorf("%w: cutaway %s ends at frame %d, after the last frame of the timeline (%d)", ErrInvalidSpec, it.ID, end, c.total)
		case k > 0 && start < c.cutaways[k-1].startFrame+c.cutaways[k-1].frames:
			return fmt.Errorf("%w: cutaway %s starts at frame %d, before cutaway %s ends", ErrInvalidSpec, it.ID, start, c.cutaways[k-1].item.ID)
		}
		ra, seen := resolved[it.AssetID]
		if !seen {
			var err error
			ra, err = resolver.Resolve(it.AssetID, c.profile.Source)
			if err != nil {
				return fmt.Errorf("render: resolve asset %s for cutaway %s: %w", it.AssetID, it.ID, err)
			}
			resolved[it.AssetID] = ra
		}
		c.cutaways = append(c.cutaways, cutaway{k: k, item: it, asset: ra, startFrame: start, frames: end - start})
	}
	return nil
}

// cutawayInputs adds one input per cutaway after every input the audio graph added, so that no audio input changes its number.
func (c *compiler) cutawayInputs() {
	for i := range c.cutaways {
		cw := &c.cutaways[i]
		cw.input = len(c.inputs)
		c.inputs = append(c.inputs, Input{
			Index: cw.input, Role: RoleBroll, ItemID: cw.item.ID, AssetID: cw.item.AssetID, Location: cw.asset.Location,
			SeekMs: cw.item.Source.InMs, DurationMs: cw.item.DurationMs(),
		})
	}
}

// cutawayGraph lays the cutaways over the pad cur, ending in out. Each is the clips' own chain (pictureGraph: exact frame count, reframe,
// look) shifted to its first frame's time and overlaid full frame; before and after it the picture passes through untouched, the way
// overlayGraph lays the motion graphics.
func (c *compiler) cutawayGraph(cur, out string) ([]string, error) {
	var stmts []string
	for i, cw := range c.cutaways {
		pad := fmt.Sprintf("b%d", cw.k)
		chain, err := c.pictureGraph(picture{
			item: cw.item.AsVideoItem(), asset: cw.asset, frames: cw.frames,
			in: fmt.Sprintf("%d:v", cw.input), out: pad, pads: fmt.Sprintf("cb%d", cw.k),
			tail: []string{"setpts=PTS+" + c.frameSecs(cw.startFrame) + "/TB"},
		})
		if err != nil {
			return nil, err
		}
		next := fmt.Sprintf("vb%d", cw.k)
		if i == len(c.cutaways)-1 {
			next = out
		}
		stmts = append(stmts, chain...)
		stmts = append(stmts, fmt.Sprintf("[%s][%s]overlay=0:0:eof_action=pass:repeatlast=0[%s]", cur, pad, next))
		cur = next
	}
	return stmts, nil
}

// cutawayTimeMap adds an entry per cutaway: [its first frame, its last frame + 1), with its asset, source in point and speed 1, and
// track "broll". The main entries are left as they are.
func (c *compiler) cutawayTimeMap() []TimeMapEntry {
	var out []TimeMapEntry
	for _, cw := range c.cutaways {
		in, speed := cw.item.Source.InMs, 1.0
		out = append(out, TimeMapEntry{
			OutStartMs: timeline.FrameStartMs(cw.startFrame, c.fps),
			OutEndMs:   timeline.FrameStartMs(cw.startFrame+cw.frames, c.fps),
			ItemID:     cw.item.ID,
			AssetID:    cw.item.AssetID,
			SourceInMs: &in,
			Speed:      &speed,
			Track:      TrackBroll,
		})
	}
	return out
}
