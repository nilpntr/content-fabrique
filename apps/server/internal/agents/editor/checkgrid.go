package editor

import (
	"fmt"
	"maps"
	"math"
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
	gridEndingBeats = 4
	gridBarBeats    = 4
	// reuseSlackMs is the overlap of two source ranges of one clip that still counts as two moments (a frame or two of handle).
	reuseSlackMs = 100
)

// beatGrid is the spec's beat markers, sorted, and the real tempo of the grid (the span from the first to the last beat over the number of
// gaps, so a rounded gap does not drift over a long song). Zero tempo: fewer than two beats.
type beatGrid struct {
	beats  []int64
	beatMs float64
}

func (c *checker) gridBeats() beatGrid { return c.gridBeatsOf(c.in.Spec.Markers) }

// gridBeatsOf is the grid of a list of markers.
func (c *checker) gridBeatsOf(markers []timeline.Marker) beatGrid {
	var g beatGrid
	for _, m := range markers {
		if m.Kind == timeline.MarkerBeat || m.Kind == timeline.MarkerDownbeat {
			g.beats = append(g.beats, m.AtMs)
		}
	}
	slices.Sort(g.beats)
	g.beats = slices.Compact(g.beats)
	if n := len(g.beats); n >= 2 {
		g.beatMs = float64(g.beats[n-1]-g.beats[0]) / float64(n-1)
	}
	return g
}

// index is the beat number of time t: the index of the nearest beat marker when one is within tol, else, before the first or past the last
// marker, extrapolated from that marker at the grid's tempo (when the result lands within tol of a beat). False when t sits between beats.
func (g beatGrid) index(t, tol int64) (int, bool) {
	if len(g.beats) == 0 {
		return 0, false
	}
	i := sort.Search(len(g.beats), func(i int) bool { return g.beats[i] >= t })
	best, bestD := -1, int64(0)
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(g.beats) {
			if d := abs64(g.beats[j] - t); best < 0 || d < bestD {
				best, bestD = j, d
			}
		}
	}
	if bestD <= tol {
		return best, true
	}
	if g.beatMs <= 0 {
		return 0, false
	}
	if last := len(g.beats) - 1; t > g.beats[last] {
		n := int(math.Round(float64(t-g.beats[last]) / g.beatMs))
		if abs64(g.beats[last]+int64(math.Round(float64(n)*g.beatMs))-t) <= tol {
			return last + n, true
		}
	} else if t < g.beats[0] {
		n := int(math.Round(float64(g.beats[0]-t) / g.beatMs))
		if abs64(g.beats[0]-int64(math.Round(float64(n)*g.beatMs))-t) <= tol {
			return -n, true
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

// onFixedGrid says the creator's style declares the fixed grid (the cut_grid setting is fixed_2). It is a personal rule: without it the
// edit is held to the usual shot-length rules.
func (c *checker) onFixedGrid() bool {
	return c.in.Settings != nil && c.in.Settings.CutGrid == styleprofile.CutGridFixed2
}

// beatGridFixed holds the edit to the beat arithmetic: every cut on an even beat (counted from the first shot), the last shot 4 beats and
// the total a multiple of 4. It applies to an edit whose style declares the fixed grid and has a beat grid: not to one with no music or
// cuts known to sit off a grid, and not where no grid is expected (a plan with tempo 0).
func (c *checker) beatGridFixed() Check {
	if !c.onFixedGrid() {
		return c.skip(CheckBeatGridFixed, "the creator's style does not declare a fixed 2-beat grid (cut_grid)")
	}
	if c.lim.looseBeat || c.in.Settings.Music == styleprofile.MusicNever {
		return c.skip(CheckBeatGridFixed, "the edit has no music beat to cut on (the style has no music, or its cuts sit off a grid)")
	}
	g := c.gridBeats()
	if len(g.beats) < 2 {
		if !c.lim.beatGrid {
			return c.skip(CheckBeatGridFixed, "no beat grid is expected (a %s video, or a plan with tempo 0)", c.typ)
		}
		return c.fail(CheckBeatGridFixed, "the style asks for a fixed 2-beat grid and the edit has no beat grid; write one with snap_cuts_to_beats (write_markers true)")
	}
	tol := int64(beatToleranceFrames) * 1000 / int64(max(c.in.Spec.Output.FPS, 1))
	n := len(c.items)
	// The first shot starts the video. It is beat 0 of the count; when the song's first beat is a little after the start of the video (a
	// grid that does not begin at 0), the video's start still counts as that beat.
	start, startOK := g.index(c.items[0].AtMs, tol)
	if !startOK && c.items[0].AtMs == 0 && float64(g.beats[0]) < g.beatMs {
		start, startOK = 0, true
	}
	var bad []string
	if !startOK {
		bad = append(bad, fmt.Sprintf("%s starts between beats", c.items[0].ID))
	}
	pos, found := make([]int, n), make([]bool, n)
	pos[0], found[0] = start, startOK
	for i := 1; i < n; i++ {
		p, ok := g.index(c.items[i].AtMs, tol)
		pos[i], found[i] = p, ok
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s starts between beats", c.items[i].ID))
		case startOK && (p-start)%2 != 0:
			bad = append(bad, fmt.Sprintf("%s cuts on beat %d, an odd beat", c.items[i].ID, p-start))
		}
	}
	endPos, endOK := g.index(c.total, tol)
	if !endOK {
		bad = append(bad, "the video does not stop on a beat")
	} else {
		if k := endPos - pos[n-1]; found[n-1] && k != gridEndingBeats {
			bad = append(bad, fmt.Sprintf("the last shot %s is %d beats, not %d", c.items[n-1].ID, k, gridEndingBeats))
		}
		if startOK {
			if total := endPos - start; total%gridBarBeats != 0 {
				bad = append(bad, fmt.Sprintf("the total is %d beats, not a multiple of %d", total, gridBarBeats))
			}
		}
	}
	if len(bad) > 0 {
		return c.fail(CheckBeatGridFixed, "%s. Cut on even beats, make the last shot 4 beats and the total a multiple of 4; shorter is better than a broken rhythm", strings.Join(bad[:min(4, len(bad))], "; "))
	}
	return c.pass(CheckBeatGridFixed, "%d cuts on even beats, the last shot 4 beats, %d beats in all", n-1, endPos-start)
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
			dup = append(dup, fmt.Sprintf("%s come from one tripod clip, which gives one shot: keep one of them and replace the others with another clip, or drop them", strings.Join(ids, " and ")))
		}
	}
	if len(dup) > 0 {
		return c.fail(CheckShotReused, "%d shots repeat a moment: %s. Use every shot once; replace the repeat with another clip, or cut the video shorter", len(dup), strings.Join(dup[:min(3, len(dup))], "; "))
	}
	return c.pass(CheckShotReused, "every shot is a different moment, and no tripod clip is used twice")
}
