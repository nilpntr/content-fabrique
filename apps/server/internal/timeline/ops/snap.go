package ops

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/beats"
)

// build turns the description into a beat grid.
func (g BeatGrid) build() (beats.Grid, error) {
	constant := g.BPM != nil || g.FirstBeatMs != nil || g.DownbeatOffset != nil
	list := len(g.BeatsMs) > 0 || len(g.DownbeatsMs) > 0
	bpb := 0
	if g.BeatsPerBar != nil {
		bpb = *g.BeatsPerBar
	}
	switch {
	case constant && list:
		return beats.Grid{}, errors.New("grid is either a constant tempo (bpm and first_beat_ms) or a list of beats (beats_ms), not a mix of both")
	case constant:
		if g.BPM == nil || g.FirstBeatMs == nil {
			return beats.Grid{}, errors.New("a constant-tempo grid needs bpm and first_beat_ms")
		}
		off := 0
		if g.DownbeatOffset != nil {
			off = *g.DownbeatOffset
		}
		return beats.NewConstant(beats.ConstantSpec{BPM: *g.BPM, FirstBeatMs: *g.FirstBeatMs, BeatsPerBar: bpb, DownbeatOffset: off})
	case list:
		return beats.NewList(beats.ListSpec{BeatsMs: g.BeatsMs, DownbeatsMs: g.DownbeatsMs, BeatsPerBar: bpb})
	}
	return beats.Grid{}, errors.New("grid is empty: give bpm and first_beat_ms, or beats_ms")
}

func (a *applier) snapCuts(o *SnapCutsToBeats) bool {
	a.ensureSorted()
	items := a.main.Items
	n := len(items)
	grid, err := o.Grid.build()
	if err != nil {
		return a.errf(CodeBadArgument, "the beat grid is not usable: %v", err)
	}
	if o.SoundID != "" {
		i, ok := a.soundIndex(o.SoundID)
		if !ok {
			return false
		}
		c := a.audioTrack().Items[i].Common()
		p := beats.Placement{AtMs: c.AtMs, SourceOffsetMs: c.SourceOffsetMs}
		if c.DurationMs != nil {
			p.DurationMs = *c.DurationMs
		}
		if grid, err = grid.Place(p); err != nil {
			return a.errf(CodeBadArgument, "cannot place the grid with sound %q: %v", o.SoundID, err)
		}
	}
	fps := a.spec.Output.FPS
	opts := beats.SnapOpts{FPS: fps, StartMs: 0, EndMs: timeline.PlayableEnd(items)}
	var write bool
	if so := o.Options; so != nil {
		if so.MaxShiftMs != nil {
			opts.MaxShiftMs = *so.MaxShiftMs
		}
		if so.MaxShiftBeats != nil {
			opts.MaxShiftBeats = *so.MaxShiftBeats
		}
		if so.MinShotMs != nil {
			opts.MinShotMs = *so.MinShotMs
		}
		opts.PreferDownbeats = so.PreferDownbeats
		write = so.WriteMarkers
	}

	if n >= 2 {
		cuts := make([]int64, n-1)
		for i := 1; i < n; i++ {
			cuts[i-1] = items[i].AtMs
		}
		snaps, err := beats.SnapCuts(cuts, grid, opts)
		if err != nil {
			return a.errf(CodeBadArgument, "the cuts cannot be snapped: %v", err)
		}
		a.rollCuts(snaps, cuts)
	} else {
		a.warnf(CodeNoCuts, "the main track has %d clip(s), so there is no cut between clips to snap", n)
	}

	if write {
		if err := a.writeMarkers(grid, fps); err != nil {
			return a.errf(CodeBadArgument, "cannot write the markers: %v", err)
		}
	}
	return true
}

// rollCuts moves each snapped cut by trimming the end of the clip before it and the start of the clip after it (a
// rolling edit), so the total length stays. A cut whose move would take a clip outside its asset stays where it is, and
// is reported with the ones the beat utilities left alone.
func (a *applier) rollCuts(snaps []beats.Snap, cuts []int64) {
	items := slices.Clone(a.main.Items)
	target := make([]int64, len(cuts))
	for i, s := range snaps {
		target[i] = s.AtMs // unsnapped cuts stay where they are
	}
	reverted := map[int]string{}
	for {
		_, bad := a.rolled(items, cuts, target)
		if len(bad) == 0 {
			break
		}
		for c, why := range bad {
			target[c] = cuts[c]
			reverted[c] = why
		}
	}
	rolled, _ := a.rolled(items, cuts, target)

	for i, s := range snaps {
		name := fmt.Sprintf("the cut before %s (at %d ms)", items[i+1].ID, cuts[i])
		switch {
		case reverted[i] != "":
			a.warnf(CodeCutNotSnapped, "%s stays where it is: moving it to the beat at %d ms would %s", name, s.AtMs, reverted[i])
		case !s.Snapped && s.Reason == beats.ReasonMinShot:
			a.warnf(CodeCutNotSnapped, "%s stays where it is: moving it to a beat would make a shot shorter than the minimum", name)
		case !s.Snapped:
			a.warnf(CodeCutNotSnapped, "%s stays where it is: no beat within the maximum shift of it", name)
		}
	}
	for i := range rolled {
		if rolled[i].Source != items[i].Source {
			a.touch(rolled[i].ID)
		}
	}
	a.main.Items = rolled
	a.ripple()
}

// rolled computes the clips after the cuts have moved to target. It returns the new clips and, for a cut that has to
// stay (its move would take a clip outside its asset or leave it empty), the reason.
//
// The clips are walked in order and the start of each is taken from the end of the one before it, so a rounding error
// (a speed that does not divide the move) is absorbed by the next clip and never adds up. Of the two edges of a clip, the
// one at a cut that did not move stays where it is, and the other is chosen to give the clip the length the new cuts
// ask for.
func (a *applier) rolled(orig []timeline.VideoItem, cuts, target []int64) ([]timeline.VideoItem, map[int]string) {
	n := len(orig)
	out := slices.Clone(orig)
	bad := map[int]string{}
	origEnd := timeline.EndMs(orig[n-1])
	var prevEnd int64
	for j := range orig {
		it := orig[j]
		var at int64
		if j > 0 {
			at = max(0, prevEnd-it.TransitionDurationMs())
		}
		leftMoved := j > 0 && target[j-1] != cuts[j-1]
		rightMoved := j < n-1 && target[j] != cuts[j]
		next := it
		if it.HasRamp() && (leftMoved || rightMoved) {
			// A speed ramp is tied to the clip's source range (its points are offsets from the in point), so the source range
			// cannot be rolled to fit a new length without changing the ramp: the cuts at this clip stay where they are.
			why := "change the length of " + it.ID + ", which has a speed ramp that a snap cannot lengthen or shorten"
			if leftMoved {
				bad[j-1] = why
			}
			if rightMoved {
				bad[j] = why
			}
		} else if leftMoved || rightMoved {
			targetEnd := origEnd
			if j < n-1 {
				targetEnd = target[j] + orig[j+1].TransitionDurationMs()
			}
			want := targetEnd - at
			speed := it.SpeedOrDefault()
			switch {
			case !leftMoved: // only the end moves: keep the in point
				next.Source.OutMs = it.Source.InMs + spanFor(want, speed)
			case rightMoved: // both edges move: shift the in point by the move, fit the out point
				next.Source.InMs = it.Source.InMs + int64(math.Round(float64(at-it.AtMs)*speed))
				next.Source.OutMs = next.Source.InMs + spanFor(want, speed)
			default: // only the start moves: keep the out point
				next.Source.InMs = it.Source.OutMs - spanFor(want, speed)
			}
			if why := a.cannotPlay(next); why != "" {
				if leftMoved {
					bad[j-1] = fmt.Sprintf("leave %s %s", it.ID, why)
				}
				if rightMoved {
					bad[j] = fmt.Sprintf("leave %s %s", it.ID, why)
				}
			}
		}
		out[j] = next
		prevEnd = at + timeline.OutputDuration(next)
	}
	return out, bad
}

// cannotPlay says why a clip cannot have its new source range, or "" if it can.
func (a *applier) cannotPlay(it timeline.VideoItem) string {
	switch {
	case it.Source.InMs < 0:
		return "start before the beginning of its asset"
	case it.Source.InMs >= it.Source.OutMs:
		return "with nothing left to play"
	}
	if asset, ok := a.catalog.Lookup(it.AssetID); ok && it.Source.OutMs > asset.DurationMs {
		return "run past the end of its asset"
	}
	return ""
}

// spanFor is the length of source range that plays for want ms at speed: the one whose output duration is closest to
// want (equal to it when the speed allows).
func spanFor(want int64, speed float64) int64 {
	if want <= 0 {
		return 0
	}
	base := int64(math.Round(float64(want) * speed))
	best, bestErr := base, int64(math.MaxInt64)
	window := int64(math.Ceil(speed)) + 2
	for d := range window + 1 {
		for _, s := range []int64{base - d, base + d} {
			if s < 1 {
				continue
			}
			dur := timeline.OutputDuration(timeline.VideoItem{Source: timeline.Source{OutMs: s}, Speed: &speed})
			if e := absInt(dur - want); e < bestErr {
				best, bestErr = s, e
			}
		}
		if bestErr == 0 {
			break
		}
	}
	return best
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// writeMarkers replaces the beat and downbeat markers of the timeline with the beats of grid inside it; markers of
// other kinds stay.
func (a *applier) writeMarkers(grid beats.Grid, fps int) error {
	total := timeline.PlayableEnd(a.main.Items)
	ms, err := grid.Markers(0, total, beats.MarkerOpts{FPS: fps})
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(a.spec.Markers), func(m timeline.Marker) bool {
		return m.Kind == timeline.MarkerBeat || m.Kind == timeline.MarkerDownbeat
	})
	for _, m := range ms {
		s := m.Strength
		kept = append(kept, timeline.Marker{AtMs: m.AtMs, Kind: m.Kind, Strength: &s})
	}
	slices.SortStableFunc(kept, func(x, y timeline.Marker) int { return cmp.Compare(x.AtMs, y.AtMs) })
	a.spec.Markers = kept
	a.touched["markers"] = a.idx
	return nil
}
