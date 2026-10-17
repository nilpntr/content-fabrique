package analysis

import "math"

// Shots (P2-02): boundaries from the scene-change scores, with a minimum length, and per shot the signals the editor and the
// renderer care about: exposure (and the correction that would bring it to a target), sharpness and motion.

// Exposure targets and limits. Luma here is the 8-bit grey of the proxy, which is limited range: 16 is black, 235 is white.
const (
	blackLevel = 16.0
	whiteRange = 219.0
	// TargetLuma is the mean brightness (0 black .. 1 white) the exposure hint aims at: a bit under the middle, where phone
	// footage of a person looks right without clipping the lamps.
	TargetLuma = 0.42
)

// ExposureHint says how far a shot's mean brightness is from the target and what would bring it there. The gain is a linear
// multiplier on the picture, the gamma is the value for ffmpeg's eq filter (out = in^(1/gamma): above 1 lifts the shadows). Both
// are clamped to what looks acceptable, so a very dark shot gets the most the clamp allows and Level still says "dark".
type ExposureHint struct {
	// MeanY is the shot's mean brightness, 0 (black) to 1 (white).
	MeanY   float64 `json:"mean_y"`
	TargetY float64 `json:"target_y"`
	Gain    float64 `json:"gain"`
	Gamma   float64 `json:"gamma"`
	// Level is "dark", "ok" or "bright".
	Level string `json:"level"`
}

// NormLuma converts an 8-bit limited-range grey level to 0..1.
func NormLuma(v float64) float64 { return clamp01((v - blackLevel) / whiteRange) }

// KeyLuma is the brightness the exposure is judged by: the mean, unless the lit part of the picture is bright, which a dark
// background (a posing wall, a black gym) only drags the mean down from. Both are 0..1.
func KeyLuma(meanY, highY float64) float64 { return math.Max(meanY, 0.6*highY) }

// NewExposureHint computes the hint for a key brightness meanY (0..1, see KeyLuma).
func NewExposureHint(meanY float64) ExposureHint {
	y := math.Max(meanY, 0.02)
	h := ExposureHint{MeanY: round2(meanY), TargetY: TargetLuma, Level: "ok"}
	h.Gain = round2(math.Min(math.Max(TargetLuma/y, 0.7), 2.5))
	h.Gamma = round2(math.Min(math.Max(math.Log(y)/math.Log(TargetLuma), 0.6), 2.2))
	switch {
	case meanY < 0.25:
		h.Level = "dark"
	case meanY > 0.65:
		h.Level = "bright"
	}
	return h
}

// ExposureScore is 1 for a shot at the target brightness and falls to 0 at black and at white.
func ExposureScore(meanY float64) float64 {
	return clamp01(1 - math.Abs(meanY-TargetLuma)/TargetLuma)
}

// Shot is one continuous take of a clip.
type Shot struct {
	Idx     int `json:"idx"`
	StartMs int `json:"start_ms"`
	EndMs   int `json:"end_ms"`
	// From and To are the sample range [From, To) of the shot in the series.
	From int `json:"-"`
	To   int `json:"-"`

	Exposure ExposureHint `json:"exposure"`
	// Contrast is the mean 10th-to-90th percentile spread of brightness (0..255): low for a flat or foggy picture.
	Contrast float64 `json:"contrast"`
	// Sharpness is the mean edge energy; SharpnessRel is the shot's sharpness against the sharpest 10 % of the whole clip
	// (1 = as sharp as the clip gets), which is what is comparable between shots of one clip.
	Sharpness    float64 `json:"sharpness"`
	SharpnessRel float64 `json:"sharpness_rel"`
	// Motion is the mean motion energy of the shot (0..255, mostly well under 5).
	Motion float64 `json:"motion"`
}

// ShotParams are the numbers of the shot detector.
type ShotParams struct {
	// SceneThreshold is the scdet score (0..100) from which a sample is a cut.
	SceneThreshold float64
	// MinShotMs is the shortest shot; a cut that would make a shorter one is dropped (the stronger of two close cuts wins).
	MinShotMs int
}

// DefaultShotParams are the product's.
func DefaultShotParams() ShotParams { return ShotParams{SceneThreshold: 12, MinShotMs: 1000} }

// DetectShots cuts the series into shots at the scene-change scores that are local maxima above the threshold. A clip with no
// cut is one shot.
func DetectShots(s Series, sp ShotParams) []Shot {
	n := s.Len()
	if n == 0 {
		return nil
	}
	minLen := max(sp.MinShotMs/s.StepMs, 1)
	type cut struct {
		i     int
		score float64
	}
	var cuts []cut
	const near = 3 // a cut is the highest score within 0.3 s on each side
	for i := 1; i < n; i++ {
		v := s.Scene[i]
		if v < sp.SceneThreshold {
			continue
		}
		peak := true
		for j := max(i-near, 0); j <= min(i+near, n-1); j++ {
			if s.Scene[j] > v || (s.Scene[j] == v && j < i) {
				peak = false
				break
			}
		}
		if peak {
			cuts = append(cuts, cut{i, v})
		}
	}
	// Enforce the minimum length: while two neighbouring boundaries (the ends of the clip count) are closer than minLen, drop the
	// weaker cut of the pair.
	for {
		worst := -1
		prev := 0
		for k := 0; k <= len(cuts); k++ {
			end := n
			if k < len(cuts) {
				end = cuts[k].i
			}
			if end-prev < minLen {
				// the cut at the start of the short shot is cuts[k-1], at its end cuts[k]
				switch {
				case k == 0:
					worst = 0
				case k == len(cuts):
					worst = k - 1
				case cuts[k-1].score < cuts[k].score:
					worst = k - 1
				default:
					worst = k
				}
				break
			}
			prev = end
		}
		if worst < 0 || len(cuts) == 0 {
			break
		}
		cuts = append(cuts[:worst], cuts[worst+1:]...)
	}

	bounds := []int{0}
	for _, c := range cuts {
		bounds = append(bounds, c.i)
	}
	bounds = append(bounds, n)
	sharpRef := quantile(s.Sharp, 0.9)
	shots := make([]Shot, 0, len(bounds)-1)
	for k := 0; k+1 < len(bounds); k++ {
		a, b := bounds[k], bounds[k+1]
		sh := Shot{Idx: k, StartMs: a * s.StepMs, EndMs: b * s.StepMs, From: a, To: b}
		sh.Exposure = NewExposureHint(KeyLuma(NormLuma(mean(s.Luma[a:b])), NormLuma(mean(s.High[a:b]))))
		sh.Contrast = round1(mean(s.Contrast[a:b]))
		sh.Sharpness = round2(mean(s.Sharp[a:b]))
		if sharpRef > 0 {
			sh.SharpnessRel = round2(math.Min(mean(s.Sharp[a:b])/sharpRef, 1.5))
		}
		sh.Motion = round2(mean(s.Motion[a:b]))
		shots = append(shots, sh)
	}
	return shots
}

// SharpnessScore turns a sharpness relative to the clip's sharpest (1 = as sharp as it gets) into a score: 1 at 90 % and above,
// 0 at 60 % and below.
func SharpnessScore(rel float64) float64 { return clamp01((rel - 0.6) / 0.3) }
