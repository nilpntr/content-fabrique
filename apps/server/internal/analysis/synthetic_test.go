package analysis

import (
	"math"
	"math/rand"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// A synthetic clip for the tests: a bright box on a dark background, seen as the 32x32 thumbnails the ffmpeg pass produces, 10 per
// second. The box's vertical position follows a function of time, so the tests know where the reps are.

type synth struct {
	seconds int
	// pos gives the box's centre (0..1 of the picture height) at time t seconds.
	pos func(t float64) float64
	// noise is the amplitude of the uniform noise added to every pixel (grey levels).
	noise float64
	// bg and fg are the grey levels of the background and the box.
	bg, fg float64
	seed   int64
	// scene is the scene-change score per sample (nil: zero everywhere).
	scene func(i int) float64
	// sharp is the edge energy per sample (nil: 5 everywhere).
	sharp func(i int) float64
}

func (s synth) raw() Raw {
	rng := rand.New(rand.NewSource(s.seed + 1))
	n := s.seconds * 10
	const px = media.ThumbPixels
	thumbs := make([]byte, n*px)
	bg, fg := s.bg, s.fg
	if bg == 0 && fg == 0 {
		bg, fg = 40, 200
	}
	for i := 0; i < n; i++ {
		t := float64(i) / 10
		cy := s.pos(t) * media.ThumbSize
		for y := 0; y < media.ThumbSize; y++ {
			for x := 0; x < media.ThumbSize; x++ {
				v := bg
				// a box 12 wide and 8 tall, with soft edges (the thumbnail is area-averaged)
				dy := math.Abs(float64(y)+0.5-cy) - 4
				dx := math.Abs(float64(x)+0.5-16) - 6
				cover := math.Min(math.Max(0.5-dy, 0), 1) * math.Min(math.Max(0.5-dx, 0), 1)
				v += (fg - bg) * cover
				if s.noise > 0 {
					v += (rng.Float64()*2 - 1) * s.noise
				}
				thumbs[i*px+y*media.ThumbSize+x] = byte(math.Max(0, math.Min(255, v)))
			}
		}
	}
	r := Raw{Thumbs: thumbs, Sharp: make([]float64, n), Scene: make([]float64, n)}
	for i := 0; i < n; i++ {
		r.Sharp[i] = 5
		if s.sharp != nil {
			r.Sharp[i] = s.sharp(i)
		}
		if s.scene != nil {
			r.Scene[i] = s.scene(i)
		}
	}
	return r
}

// sine is a box that moves up and down with the given period between 0.3 and 0.7 of the height.
func sine(period float64) func(t float64) float64 {
	return func(t float64) float64 { return 0.5 + 0.2*math.Sin(2*math.Pi*t/period) }
}

// phases builds a position function from consecutive stretches: each is still (at 0.5) or moves with the period.
type phase struct {
	until  float64 // seconds, end of the stretch
	period float64 // 0 = still
}

func phases(ps ...phase) func(t float64) float64 {
	return func(t float64) float64 {
		start := 0.0
		for _, p := range ps {
			if t < p.until {
				if p.period == 0 {
					return 0.5
				}
				return 0.5 + 0.2*math.Sin(2*math.Pi*(t-start)/p.period)
			}
			start = p.until
		}
		return 0.5
	}
}

// walk is a box that wanders without any period: a smoothed random walk.
func walk(seed int64, seconds int) func(t float64) float64 {
	rng := rand.New(rand.NewSource(seed))
	n := seconds*10 + 2
	x := make([]float64, n)
	v := 0.0
	for i := 1; i < n; i++ {
		v = 0.9*v + (rng.Float64()-0.5)*0.03
		x[i] = x[i-1] + v
		x[i] = math.Max(-0.3, math.Min(0.3, x[i]))
	}
	return func(t float64) float64 { return 0.5 + x[int(t*10)] }
}
