package beats

import (
	"errors"
	"math"
	"testing"
)

func TestMsToFrameTable(t *testing.T) {
	tests := []struct {
		fps   int
		ms    int64
		frame int64
	}{
		{30, 0, 0},
		{30, 16, 0}, // 0.48
		{30, 17, 1}, // 0.51
		{30, 50, 2}, // exactly 1.5 rounds up
		{30, 49, 1},
		{24, 20, 0}, // 0.48
		{24, 21, 1}, // 0.504
		{24, 62, 1}, // 1.488
		{24, 63, 2}, // 1.512
		{25, 20, 1}, // exactly 0.5 rounds up
		{25, 19, 0}, // 0.475
		{60, 8, 0},  // 0.48
		{60, 9, 1},  // 0.54
		{60, 25, 2}, // exactly 1.5
		{30, 1000, 30},
		{24, 1000, 24},
	}
	for _, tc := range tests {
		if got := MsToFrame(tc.ms, tc.fps); got != tc.frame {
			t.Errorf("MsToFrame(%d, %d) = %d, want %d", tc.ms, tc.fps, got, tc.frame)
		}
	}
}

func TestFrameFormulaMatchesNote(t *testing.T) {
	// frame(ms) = floor(ms × fps / 1000 + 0.5), the formula in the design note.
	for _, fps := range SupportedFPS {
		for ms := int64(0); ms <= 300000; ms++ {
			want := int64(math.Floor(float64(ms)*float64(fps)/1000 + 0.5))
			if got := MsToFrame(ms, fps); got != want {
				t.Fatalf("fps %d ms %d: got %d, want %d", fps, ms, got, want)
			}
		}
	}
}

func TestFrameRoundTrip(t *testing.T) {
	for _, fps := range SupportedFPS {
		for f := int64(0); f <= 200000; f++ {
			ms := FrameToMs(f, fps)
			if MsToFrame(ms, fps) != f {
				t.Fatalf("fps %d frame %d -> %d ms -> frame %d", fps, f, ms, MsToFrame(ms, fps))
			}
			if !IsAligned(ms, fps) {
				t.Fatalf("fps %d frame %d: %d ms not aligned", fps, f, ms)
			}
			if d := math.Abs(float64(ms) - float64(f)*1000/float64(fps)); d > 0.5 {
				t.Fatalf("fps %d frame %d: %d ms is %v off", fps, f, ms, d)
			}
		}
	}
}

func TestFloorDivNegative(t *testing.T) {
	if got := floorDiv(-1, 2000); got != -1 {
		t.Errorf("floorDiv(-1, 2000) = %d, want -1", got)
	}
	if got := MsToFrame(-100, 30); got != -3 { // -3.0 + 0.5 = -2.5 -> floor -3
		t.Errorf("MsToFrame(-100, 30) = %d, want -3", got)
	}
}

func TestCheckFPS(t *testing.T) {
	for _, fps := range []int{0, 23, 29, 50, 120} {
		if err := checkFPS(fps); !errors.Is(err, ErrInvalidFPS) {
			t.Errorf("fps %d: err = %v, want ErrInvalidFPS", fps, err)
		}
	}
	for _, fps := range SupportedFPS {
		if err := checkFPS(fps); err != nil {
			t.Errorf("fps %d: %v", fps, err)
		}
	}
}
