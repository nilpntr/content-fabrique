package camshift

import (
	"math"
	"testing"
)

// texture is a smooth pattern that can be sampled at any position, like a lit wall with detail.
func texture(x, y float64) byte {
	v := 128 + 50*math.Sin(x*0.7) + 40*math.Cos(y*0.55) + 25*math.Sin((x+y)*0.31)
	return byte(math.Max(0, math.Min(255, v)))
}

func frames(n, size int, at func(i int) (ox, oy float64), block func(i int) (bx, by int)) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		f := make([]byte, size*size)
		ox, oy := at(i)
		for y := range size {
			for x := range size {
				f[y*size+x] = texture(float64(x)+ox, float64(y)+oy)
			}
		}
		if block != nil { // a person moving in front of the camera: a dark block that walks across
			bx, by := block(i)
			for y := by; y < min(by+10, size); y++ {
				for x := bx; x < min(bx+8, size); x++ {
					f[y*size+x] = 20
				}
			}
		}
		out[i] = f
	}
	return out
}

func TestATripodShotWithAMovingPersonIsStatic(t *testing.T) {
	fr := frames(60, 48, func(int) (float64, float64) { return 0, 0 }, func(i int) (int, int) { return 10 + i%20, 18 })
	m := Measure(fr, 48, 10)
	if m.Verdict() != Static || m.ShiftPctPerS > 0.2 {
		t.Errorf("%+v %s", m, m.Verdict())
	}
}

func TestADriftingCameraIsHandheld(t *testing.T) {
	// 0.06 px a frame at 10 fps on 48 px: 1.25 % of the frame a second, with a slow sway.
	fr := frames(80, 48, func(i int) (float64, float64) { return 0.06 * float64(i), 0.4 * math.Sin(float64(i)/6) }, nil)
	m := Measure(fr, 48, 10)
	if m.Verdict() != Handheld {
		t.Errorf("%+v %s", m, m.Verdict())
	}
	if m.Pairs == 0 || (Motion{}).Verdict() != "" {
		t.Error("pairs")
	}
}
