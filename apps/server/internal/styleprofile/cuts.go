package styleprofile

import (
	"context"
	"math"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/media"
)

// What happens at the cuts (P6-05c): the kind of transition at each cut, measured from the picture, and where each cut sits against the
// movement, from the rep detection of the clip analysis run on the reference.

// TransitionStats counts the cuts of a reference by how the picture changes at them. Hard is a plain cut; Blend a change spread over
// several frames (a dissolve, a slide, a whip); Flash a flash of light at the cut; Zoom a quick zoom at the cut (a punch or a
// zoom-through). A cut is counted once, in the first kind that fits in that order: flash, zoom, blend, hard. The measure is a proxy from
// the thumbnails and the scene scores, not a reading of the effect the creator picked.
type TransitionStats struct {
	Cuts  int `json:"cuts"`
	Hard  int `json:"hard"`
	Blend int `json:"blend"`
	Flash int `json:"flash"`
	Zoom  int `json:"zoom"`
}

// The zoom transition: a zoom-through or a punch scales the picture into the cut and out of it, blurred where the picture switches. A
// push-in of the camera inside a shot, or a hand-held camera, scales or shakes the picture too, so a cut counts as a zoom only when
//
//   - the picture scales on both sides of the cut (zoomSideS before it and after it, zoomSide each way, every step the same way on a
//     side): a push that runs into a plain cut scales on one side only;
//   - the frames at the cut are blurred: their sharpness is at most zoomBlur of the sharpness of the same shots further from the cut;
//   - the shots are quiet before that: the scale of the second before the zoom window adds up to less than zoomSide (a camera that keeps
//     moving is hand-held or a long push, not a transition).
const (
	zoomSideS  = 0.35
	zoomSide   = 0.03 // ln(1.03): the scale a side of a zoom transition adds up to at least
	zoomBlur   = 0.75
	zoomQuietS = 1.0
)

// zoomTransitionAt says whether the cut at t (seconds) is a zoom transition, from the grey thumbnails at media.StyleThumbFPS.
func zoomTransitionAt(frames [][]byte, t float64) bool {
	fps := float64(media.StyleThumbFPS)
	n := len(frames)
	cut := int(math.Round(t * fps)) // the first frame of the new shot
	side := int(math.Ceil(zoomSideS * fps))
	quiet := int(math.Ceil(zoomQuietS * fps))
	if cut-side-quiet < 1 || cut+side+quiet >= n {
		return false
	}
	// scaled adds up the scale steps of the frame pairs [from, to) and says whether every step that scaled went the same way.
	scaled := func(from, to int) (float64, bool) {
		var sum float64
		sign := 0
		for i := from; i < to; i++ {
			st := scaleStep(frames[i-1], frames[i])
			if st == 0 {
				continue
			}
			sg := 1
			if st < 0 {
				sg = -1
			}
			if sign != 0 && sg != sign {
				return sum, false
			}
			sign = sg
			sum += st
		}
		return sum, true
	}
	before, okB := scaled(cut-side+1, cut-1) // the pairs inside the outgoing shot, up to the frame before the cut
	after, okA := scaled(cut+1, cut+side)    // the pairs inside the incoming shot
	if !okB || !okA || math.Abs(before) < zoomSide || math.Abs(after) < zoomSide {
		return false
	}
	if q, _ := scaled(cut-side-quiet+1, cut-side); math.Abs(q) >= zoomSide {
		return false // the outgoing shot was already moving: a hand-held camera or a push
	}
	if q, _ := scaled(cut+side+1, cut+side+quiet); math.Abs(q) >= zoomSide {
		return false
	}
	at := (sharpness(frames[cut-1]) + sharpness(frames[cut])) / 2
	far := (sharpness(frames[cut-side-quiet/2]) + sharpness(frames[cut+side+quiet/2])) / 2
	return far > 0 && at <= zoomBlur*far
}

// sharpness is the mean absolute difference between neighbouring pixels of a thumbnail: a blurred picture has less of it.
func sharpness(f []byte) float64 {
	var sum float64
	for y := range tsz - 1 {
		for x := range tsz - 1 {
			v := int(f[y*tsz+x])
			sum += math.Abs(float64(v-int(f[y*tsz+x+1]))) + math.Abs(float64(v-int(f[(y+1)*tsz+x])))
		}
	}
	return sum / float64(2*(tsz-1)*(tsz-1))
}

// The windows of the transition proxies.
const (
	blendWindowS = 0.35 // scene scores within this of a cut that spread the change
	blendMinRun  = 3    // scores of at least blendScore besides the cut's own frame and its neighbours make a blend
	blendScore   = CutThreshold * 0.4
	fxWindowS    = 0.45 // a flash or a quick zoom this close to a cut is the cut's transition
)

// transitionsOf classifies the cuts. points are the scene scores (StyleSceneFPS), frames the gray thumbnails (StyleThumbFPS).
func transitionsOf(cuts []float64, points []scorePoint, frames [][]byte) TransitionStats {
	ts := TransitionStats{Cuts: len(cuts)}
	if len(cuts) == 0 {
		return ts
	}
	fps := float64(media.StyleThumbFPS)
	n := len(frames)
	means := make([]float64, n)
	for i, f := range frames {
		means[i] = meanOf(f)
	}
	flashAt := func(t float64) bool {
		for i := max(2, int((t-fxWindowS)*fps)); i <= min(n-5, int((t+fxWindowS)*fps)); i++ {
			before := (means[i-1] + means[i-2]) / 2
			after := (means[i+3] + means[i+4]) / 2
			if means[i]-before >= flashRise && means[i]-after >= flashRise {
				return true
			}
		}
		return false
	}
	zoomAt := func(t float64) bool { return zoomTransitionAt(frames, t) }
	blendAt := func(t float64) bool {
		var near []scorePoint
		for _, p := range points {
			if math.Abs(p.TimeS-t) <= blendWindowS {
				near = append(near, p)
			}
		}
		if len(near) == 0 {
			return false
		}
		peak := slices.IndexFunc(near, func(p scorePoint) bool {
			return p.Score == slices.MaxFunc(near, func(a, b scorePoint) int { return cmpFloat(a.Score, b.Score) }).Score
		})
		run := 0
		for i, p := range near {
			if i >= peak-1 && i <= peak+1 {
				continue
			}
			if p.Score >= blendScore {
				run++
			}
		}
		return run >= blendMinRun
	}
	for _, c := range cuts {
		switch {
		case flashAt(c):
			ts.Flash++
		case zoomAt(c):
			ts.Zoom++
		case blendAt(c):
			ts.Blend++
		default:
			ts.Hard++
		}
	}
	return ts
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// MovementStats is where the cuts of a reference sit against the movement, from the reps the clip analysis finds in its shots. Only the
// cuts after a shot in which reps were found are measured (the rep detection needs a few seconds of a set, so the cuts between short
// shots are not). OnBoundary is a cut at the end of a rep (within 150 ms) or after the set; MidRep a cut that stops the movement
// before the rep it is in ends; InsideSet a cut with reps at a similar pace on both sides, which is most likely a switch of angle within
// one exercise (the analysis does not know which exercise a shot shows, so it is a proxy).
type MovementStats struct {
	CutsMeasured int `json:"cuts_measured"`
	OnBoundary   int `json:"on_boundary"`
	MidRep       int `json:"mid_rep"`
	InsideSet    int `json:"inside_set"`
}

// The tolerances of the movement measure.
const (
	repEdgeMs      = 150 // a cut this close to the end of a rep is on its boundary
	repLookMs      = 10_000
	sameSetPace    = 0.25 // reps on both sides of a cut within this share of each other's period are one set
	midRepFraction = 0.8  // a cut later than this share of a period after the last rep, but before the next would end, is mid-rep
)

// cutMovement measures the cuts against the reps of the analysis.
func cutMovement(cutsS []float64, moments []analysis.Moment) MovementStats {
	var reps []analysis.Moment
	for _, m := range moments {
		if m.Kind == "rep" && m.EndMs > m.StartMs {
			reps = append(reps, m)
		}
	}
	var ms MovementStats
	period := func(rs []analysis.Moment) float64 {
		d := make([]float64, len(rs))
		for i, r := range rs {
			d[i] = float64(r.EndMs - r.StartMs)
		}
		slices.Sort(d)
		return median(d)
	}
	for _, cs := range cutsS {
		c := int(cs * 1000)
		var before, after []analysis.Moment
		spans := false
		for _, r := range reps {
			switch {
			case r.StartMs < c-100 && r.StartMs > c-repLookMs:
				if r.EndMs > c+repEdgeMs {
					spans = true // the analysis did not split the shot here: the cut is inside a rep
				}
				before = append(before, r)
			case r.StartMs >= c-repEdgeMs && r.StartMs < c+repLookMs:
				after = append(after, r)
			}
		}
		if len(before) == 0 {
			continue
		}
		ms.CutsMeasured++
		pb := period(before)
		lastEnd := 0
		for _, r := range before {
			if r.EndMs <= c+repEdgeMs {
				lastEnd = max(lastEnd, r.EndMs)
			}
		}
		gap := float64(c - lastEnd)
		switch {
		case spans || (lastEnd > 0 && gap > repEdgeMs && gap < midRepFraction*pb):
			ms.MidRep++
		default:
			ms.OnBoundary++
		}
		if len(after) > 0 && pb > 0 && math.Abs(period(after)-pb)/pb <= sameSetPace {
			ms.InsideSet++
		}
	}
	return ms
}

// measureMovement runs the clip analysis on the reference and measures its cuts against the reps; nil when the pass cannot run (the
// measure is then unknown, which does not fail the reference).
func measureMovement(ctx context.Context, runner media.Runner, tempDir, path string, durationS float64, cutsS []float64) *MovementStats {
	if len(cutsS) == 0 {
		return &MovementStats{}
	}
	an, err := analysis.AnalyzeFile(ctx, runner, tempDir, path, int64(durationS*1000), analysis.DefaultParams(), analysis.DefaultShotParams())
	if err != nil {
		return nil
	}
	ms := cutMovement(cutsS, an.Moments)
	return &ms
}
