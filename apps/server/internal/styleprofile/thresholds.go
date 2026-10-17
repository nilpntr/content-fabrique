package styleprofile

import "math"

// The thresholds a profile sets for an edit. They come from robust statistics, not from the extremes: a profile of five videos in which
// one is a single 44 s take must not let every edit of a montage creator pass. Each bound is a quartile (the 25th and 75th percentile)
// with room, and is capped by a multiple of the median and by hard limits, so one odd reference moves a bound by a rank and never opens
// it. The shot-length statistics are those of the references that have a cut; the long-take style is carried by Pacing.Style and the
// longest-shot statistics, not by a maximum.
const (
	// HookFloorS and HookCapS bound the longest first shot the checks accept: never under the default 2 s, never over 4 s.
	HookFloorS = 2.0
	HookCapS   = 4.0
	// FirstCutCapS bounds the latest first cut style_match accepts.
	FirstCutCapS = 4.5
	// A bound is at most this many times the median of what it bounds.
	medianCap = 2.5
)

// Thresholds are the numbers the checks and the distance use; all in seconds, counts or per-minute rates.
type Thresholds struct {
	// HookMaxS is the longest first shot, and FirstCutLoS / FirstCutHiS the window of the first cut (zero when no reference has a cut).
	HookMaxS, FirstCutLoS, FirstCutHiS float64
	// CutsLo and CutsHi bound the number of cuts, CutsPerMinHi their rate.
	CutsLo, CutsHi, CutsPerMinHi float64
	// LongTakeNeedS is the length of the longest shot an edit needs (0 when the creator does not let takes run).
	LongTakeNeedS float64
	// MedianShotMaxS is the longest median shot the shot_pacing check accepts; ShotLenLoS / ShotLenHiS the window of the median shot for
	// a creator who does not let takes run.
	MedianShotMaxS, ShotLenLoS, ShotLenHiS float64
	// FxPerMinHi is the most effects a minute.
	FxPerMinHi float64
	// DurLoS and DurHiS are the length range (0 when the profile has none).
	DurLoS, DurHiS float64
}

// Thresholds derives the bounds from the profile.
func (p *Profile) Thresholds() Thresholds {
	var t Thresholds
	pc := p.Pacing

	// The hook.
	t.HookMaxS = HookFloorS
	if fc := p.Hook.FirstCutS; fc.Max > 0 {
		// Quartile with room, kept within a step of the median: with few references that have a cut one late first cut would
		// otherwise pull the quartile up.
		t.HookMaxS = math.Max(math.Min(math.Min(fc.Upper()+0.5, fc.Median+1.5), HookCapS), HookFloorS)
		t.FirstCutHiS = math.Min(math.Min(fc.Upper()+1.2, fc.Median+2.5), FirstCutCapS)
		t.FirstCutLoS = math.Min(math.Max(fc.Lower()-0.8, 0.5), t.FirstCutHiS-0.5) // never a window that is empty
	} else if pc.Style == PaceLongTake {
		// No reference has a cut (every video is one take): the opening shot has no cut to end on, so the hook takes the cap, not the
		// floor. A creator who does cut gets the tighter value above.
		t.HookMaxS = HookCapS
	}

	// The cuts: the count of a video (a video with none counts as zero) and the pace.
	t.CutsLo = math.Max(pc.CutsPerVideo.Lower()-1, 0)
	t.CutsHi = math.Min(pc.CutsPerVideo.Upper()*1.5+2, medianCap*pc.CutsPerVideo.Median+3)
	t.CutsPerMinHi = math.Min(pc.CutsPerMinute.Upper()*1.5+2, medianCap*pc.CutsPerMinute.Median+3)

	// The shots.
	if pc.HasLongTakes {
		t.LongTakeNeedS = math.Min(math.Max(math.Min(pc.LongestShotS.Lower()*0.75, pc.LongestShotS.Median*0.5), 4), 12)
	}
	if pc.Style == PaceLongTake {
		t.MedianShotMaxS = pc.LongestShotS.Upper() // a median shot longer than the creator's longest take would be odd
	} else {
		t.MedianShotMaxS = math.Min(pc.ShotMedianS.Upper()*1.5, medianCap*pc.ShotMedianS.Median)
	}
	t.ShotLenLoS = pc.ShotMedianS.Lower() * 0.7
	t.ShotLenHiS = math.Min(pc.ShotMedianS.Upper()*1.4, medianCap*pc.ShotMedianS.Median)

	// The effects and the length.
	t.FxPerMinHi = math.Min(math.Max(p.Effects.PerMinute.Upper()*1.5, 3), medianCap*p.Effects.PerMinute.Median+3)
	if d := p.Format.DurationS; d.Max > 0 {
		t.DurLoS = math.Max(d.Lower()*0.7, 0.4*d.Median)
		t.DurHiS = math.Min(d.Upper()*1.4, medianCap*d.Median)
	}
	return t
}
