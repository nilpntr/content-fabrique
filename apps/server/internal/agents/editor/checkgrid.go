package editor

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/camshift"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Gino's grid and "every shot once" rules (docs/design/gino-edit-rules.md, rules 1 and 6; his video-edit skill, step 4).
const (
	CheckBeatGridFixed = "beat_grid_fixed"
	CheckShotReused    = "shot_reused"
)

const (
	gridShotBeats   = 2
	gridEndingBeats = 4
	gridBarBeats    = 4
	// reuseSlackMs is the overlap of two source ranges of one clip that still counts as two moments (a frame or two of handle).
	reuseSlackMs = 100
)

// gridBeats are the beat and downbeat markers of the spec, sorted, with the beat length (the median gap).
func (c *checker) gridBeats() (beats []int64, beatMs int64) {
	for _, m := range c.in.Spec.Markers {
		if m.Kind == timeline.MarkerBeat || m.Kind == timeline.MarkerDownbeat {
			beats = append(beats, m.AtMs)
		}
	}
	slices.Sort(beats)
	beats = slices.Compact(beats)
	if len(beats) < 2 {
		return beats, 0
	}
	gaps := make([]int64, 0, len(beats)-1)
	for i := 1; i < len(beats); i++ {
		gaps = append(gaps, beats[i]-beats[i-1])
	}
	return beats, medianOf(gaps)
}

// beatIndex is the beat number of time t on the grid: the index of the beat within the tolerance, or, past the last beat, extrapolated
// at the beat length. False when t sits between beats.
func beatIndex(beats []int64, beatMs, t, tol int64) (int, bool) {
	i := sort.Search(len(beats), func(i int) bool { return beats[i] >= t })
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(beats) && abs64(beats[j]-t) <= tol {
			return j, true
		}
	}
	if last := len(beats) - 1; t > beats[last] && beatMs > 0 {
		n := (t - beats[last] + beatMs/2) / beatMs
		if abs64(beats[last]+n*beatMs-t) <= tol {
			return last + int(n), true
		}
	}
	return 0, false
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// gridPositions is the beat number of each shot start and, last, of the end of the video, and whether each was found on the grid.
func (c *checker) gridPositions(beats []int64, beatMs int64) (pos []int, ok []bool) {
	tol := int64(beatToleranceFrames) * 1000 / int64(max(c.in.Spec.Output.FPS, 1))
	pos, ok = make([]int, len(c.items)+1), make([]bool, len(c.items)+1)
	for i, it := range c.items {
		pos[i], ok[i] = beatIndex(beats, beatMs, it.AtMs, tol)
	}
	pos[len(c.items)], ok[len(c.items)] = beatIndex(beats, beatMs, c.total, tol)
	return pos, ok
}

// onFixedGrid says the creator's style declares the fixed grid (the cut_grid setting is fixed_2). It is a personal rule: without it the
// edit is held to the usual shot-length rules.
func (c *checker) onFixedGrid() bool {
	return c.in.Settings != nil && c.in.Settings.CutGrid == styleprofile.CutGridFixed2
}

// beatGridFixed holds the edit to the beat arithmetic: every cut on an even beat (counted from the first shot), the last shot 4 beats and
// the total a multiple of 4. It applies to an edit that declares the fixed grid or works to it.
func (c *checker) beatGridFixed() Check {
	if !c.onFixedGrid() {
		return c.skip(CheckBeatGridFixed, "the creator's style does not declare a fixed 2-beat grid (cut_grid)")
	}
	beats, beatMs := c.gridBeats()
	if len(beats) < 2 {
		return c.fail(CheckBeatGridFixed, "the style asks for a fixed 2-beat grid and the edit has no beat grid; write one with snap_cuts_to_beats (write_markers true)")
	}
	pos, ok := c.gridPositions(beats, beatMs)
	end, last := len(c.items), len(c.items)-1
	var bad []string
	for i := 1; i < end; i++ {
		switch {
		case !ok[i]:
			bad = append(bad, fmt.Sprintf("%s starts between beats", c.items[i].ID))
		case ok[0] && (pos[i]-pos[0])%2 != 0:
			bad = append(bad, fmt.Sprintf("%s cuts on beat %d, an odd beat", c.items[i].ID, pos[i]-pos[0]))
		}
	}
	switch {
	case !ok[0]:
		bad = append(bad, fmt.Sprintf("%s starts between beats", c.items[0].ID))
	case !ok[end]:
		bad = append(bad, "the video does not stop on a beat")
	default:
		if n := pos[end] - pos[last]; ok[last] && n != gridEndingBeats {
			bad = append(bad, fmt.Sprintf("the last shot %s is %d beats, not %d", c.items[last].ID, n, gridEndingBeats))
		}
		if total := pos[end] - pos[0]; total%gridBarBeats != 0 {
			bad = append(bad, fmt.Sprintf("the total is %d beats, not a multiple of %d", total, gridBarBeats))
		}
	}
	if len(bad) > 0 {
		return c.fail(CheckBeatGridFixed, "%s. Cut on even beats, make the last shot 4 beats and the total a multiple of 4; shorter is better than a broken rhythm", strings.Join(bad[:min(4, len(bad))], "; "))
	}
	return c.pass(CheckBeatGridFixed, "%d cuts on even beats, the last shot 4 beats, %d beats in all", end-1, pos[end]-pos[0])
}

// shotReused fails a source moment shown twice: two shots of one clip whose source ranges overlap, and a tripod clip (camera static) used
// more than once, which gives one shot.
func (c *checker) shotReused() Check {
	if c.in.Settings == nil || c.in.Settings.ShotReuse != styleprofile.ReuseOnce {
		return c.skip(CheckShotReused, "the creator's style does not ask for every shot once (shot_reuse)")
	}
	var dup []string
	for i := range c.items {
		for j := i + 1; j < len(c.items); j++ {
			a, b := c.items[i], c.items[j]
			if a.AssetID != b.AssetID {
				continue
			}
			if over := min(a.Source.OutMs, b.Source.OutMs) - max(a.Source.InMs, b.Source.InMs); over > reuseSlackMs {
				dup = append(dup, fmt.Sprintf("%s and %s show the same moment of one clip (%d ms overlap)", a.ID, b.ID, over))
			}
		}
	}
	uses := map[string][]string{}
	for _, it := range c.items {
		if a, ok := c.in.Assets[it.AssetID]; ok && a.Camera == camshift.Static {
			uses[it.AssetID] = append(uses[it.AssetID], it.ID)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(uses)) {
		if ids := uses[id]; len(ids) > 1 {
			dup = append(dup, fmt.Sprintf("%s come from one tripod clip, which gives one shot", strings.Join(ids, " and ")))
		}
	}
	if len(dup) > 0 {
		return c.fail(CheckShotReused, "%d shots repeat a moment: %s. Use every shot once; replace the repeat with another clip, or cut the video shorter", len(dup), strings.Join(dup[:min(3, len(dup))], "; "))
	}
	return c.pass(CheckShotReused, "every shot is a different moment, and no tripod clip is used twice")
}
