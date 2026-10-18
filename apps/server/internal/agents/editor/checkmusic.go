package editor

import (
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/music"
)

// The checks of an edit that has a song (P6-06): the cuts land on the music (a beat or a strong onset, within offBeatToleranceMs), and
// the hook ends on the drop. The references cut within 1 to 100 ms of an onset in a fast edit and put the first cut or the reveal on the
// drop 2 to 4.5 s in; a cut 60 ms off a hit is already felt as late.

// The check ids.
const (
	CheckOffBeatCut     = "off_beat_cut"
	CheckHookMissesDrop = "hook_misses_drop"
)

// offBeatToleranceMs is how far a cut may be from a beat or a strong onset of the song.
const offBeatToleranceMs = 60

func (c *checker) offBeatCut() Check {
	m := c.in.Music
	if m == nil {
		return c.skip(CheckOffBeatCut, "the edit has no song with a music analysis (without one the export is sound-ready and beat_sync judges the grid)")
	}
	if len(c.items) < 2 {
		return c.skip(CheckOffBeatCut, "there are no cuts")
	}
	hits := m.hits()
	if len(hits) == 0 {
		return c.skip(CheckOffBeatCut, "the part of the song that plays has no beat")
	}
	var off []string
	on := 0
	for _, it := range c.items[1:] {
		at, d, _ := music.Nearest(hits, it.AtMs)
		if isTransition(it) {
			// A transition switches the picture at its middle: that may be the moment on the beat instead of its start.
			if mAt, md, _ := music.Nearest(hits, it.AtMs+it.TransitionIn.DurationMs/2); md < d {
				at, d = mAt, md
			}
		}
		if d <= offBeatToleranceMs {
			on++
			continue
		}
		if len(off) < 3 {
			off = append(off, fmt.Sprintf("%s at %d ms (the nearest beat or hit is at %d ms, %d ms away)", it.ID, it.AtMs, at, d))
		}
	}
	cuts := len(c.items) - 1
	if on < cuts {
		return c.fail(CheckOffBeatCut, "%d of %d cuts land on the song's beats or hits (within %d ms); off: %s. Call get_music for the song's beats_ms, then snap_cuts_to_beats with that list as the grid, sound_id %s and max_shift_ms 80",
			on, cuts, offBeatToleranceMs, strings.Join(off, "; "), m.ItemID)
	}
	return c.pass(CheckOffBeatCut, "all %d cuts land on the song's beats or hits (within %d ms)", cuts, offBeatToleranceMs)
}

func (c *checker) hookMissesDrop() Check {
	m := c.in.Music
	switch {
	case m == nil:
		return c.skip(CheckHookMissesDrop, "the edit has no song with a music analysis")
	case !m.HasDrop:
		return c.skip(CheckHookMissesDrop, "the part of the song that plays has no clear drop")
	case len(c.items) < 2:
		return c.skip(CheckHookMissesDrop, "there are no cuts")
	}
	first := c.items[1].AtMs
	if it := c.items[1]; isTransition(it) {
		// A transition switches the picture at its middle: the hook ends there when that is nearer the drop.
		if mid := it.AtMs + it.TransitionIn.DurationMs/2; absI(mid-m.DropMs) < absI(first-m.DropMs) {
			first = mid
		}
	}
	d := first - m.DropMs
	if absI(d) <= offBeatToleranceMs {
		return c.pass(CheckHookMissesDrop, "the hook ends at %d ms, on the drop at %d ms", first, m.DropMs)
	}
	// Two ways to fix it: move the song so its drop lands on the first cut (a drop at at_ms + drop - offset is at the cut when the offset
	// is smaller by the distance), or make the hook end at the drop.
	fix := fmt.Sprintf("make the hook end at %d ms (trim the first shot)", m.DropMs)
	if offset := m.SourceOffsetMs - d; offset >= 0 {
		fix = fmt.Sprintf("set source_offset_ms of %s to %d so the drop lands on the first cut, or %s", m.ItemID, offset, fix)
	}
	return c.fail(CheckHookMissesDrop, "the hook ends at %d ms and the song drops at %d ms (%d ms apart): %s", first, m.DropMs, absI(d), fix)
}
