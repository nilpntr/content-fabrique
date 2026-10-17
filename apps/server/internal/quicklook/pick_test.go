package quicklook

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"slices"
	"testing"
)

func TestCandidateTimes(t *testing.T) {
	times := CandidateTimes(12_000)
	if len(times) != Frames*CandidatesPerFrame {
		t.Fatalf("a 12 s clip gives %d candidates, want %d", len(times), Frames*CandidatesPerFrame)
	}
	if times[0] != 500 || times[len(times)-1] != 11_500 {
		t.Errorf("candidates are the middles of equal slices: first %d last %d, want 500 and 11500", times[0], times[len(times)-1])
	}
	if !slices.IsSorted(times) {
		t.Errorf("not in time order: %v", times)
	}

	for _, d := range []int64{1, 40, 100, 500, 1500} {
		got := CandidateTimes(d)
		if len(got) == 0 {
			t.Errorf("a %d ms clip gives no candidates", d)
		}
		for i, at := range got {
			if at < 0 || at > max(d-40, 0) {
				t.Errorf("%d ms clip: candidate %d at %d ms is outside the clip", d, i, at)
			}
			if i > 0 && at <= got[i-1] {
				t.Errorf("%d ms clip: candidates repeat or go back: %v", d, got)
			}
		}
	}
	if CandidateTimes(0) != nil || CandidateTimes(-5) != nil {
		t.Error("a clip without a length must give no candidates")
	}
}

func cands(sharp ...float64) []Candidate {
	out := make([]Candidate, len(sharp))
	for i, s := range sharp {
		out[i] = Candidate{AtMs: int64(i+1) * 1000, JPEG: []byte{byte(i)}, Sharpness: s}
	}
	return out
}

func atOf(cs []Candidate) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.AtMs
	}
	return out
}

func TestPickTakesTheSharpestOfEachWindow(t *testing.T) {
	// Twelve candidates in four windows of three.
	got := Pick(cands(
		10, 90, 40, // window 1: the second
		5, 6, 70, //  window 2: the third
		80, 20, 30, // window 3: the first
		1, 2, 3, //   window 4: the third
	))
	want := []int64{2000, 6000, 7000, 12000}
	if !slices.Equal(atOf(got), want) {
		t.Errorf("picked %v, want %v", atOf(got), want)
	}
}

func TestPickFallsBackToTheMiddleWhenNothingCanBeTold(t *testing.T) {
	for name, in := range map[string][]Candidate{
		"all flat":      cands(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"unmeasurable":  cands(-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1),
		"flat and none": cands(0, -1, 0, -1, 0, -1, 0, -1, 0, -1, 0, -1),
	} {
		got := Pick(in)
		want := []int64{2000, 5000, 8000, 11000} // the middle candidate of each window keeps the spread
		if !slices.Equal(atOf(got), want) {
			t.Errorf("%s: picked %v, want the evenly spaced %v", name, atOf(got), want)
		}
	}
	// One window measured, the others not: the measured one is taken on its merits, the rest fall back.
	got := Pick(cands(1, 50, 2, -1, -1, -1, 0, 0, 0, -1, 3, -1))
	if want := []int64{2000, 5000, 8000, 11000}; !slices.Equal(atOf(got), want) {
		t.Errorf("mixed windows: picked %v, want %v", atOf(got), want)
	}
}

func TestPickWithFewCandidates(t *testing.T) {
	for n := 1; n <= Frames; n++ {
		in := cands(make([]float64, n)...)
		got := Pick(in)
		if len(got) != n || !slices.Equal(atOf(got), atOf(in)) {
			t.Errorf("%d candidates: picked %v", n, atOf(got))
		}
	}
	if got := Pick(nil); len(got) != 0 {
		t.Errorf("no candidates: picked %v", got)
	}
	// Five to seven candidates still give four windows, each non-empty, in time order.
	for n := Frames + 1; n < 12; n++ {
		got := Pick(cands(make([]float64, n)...))
		if len(got) != Frames || !slices.IsSorted(atOf(got)) {
			t.Errorf("%d candidates: picked %v", n, atOf(got))
		}
	}
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSharpness(t *testing.T) {
	const size = 96
	checker := func(cell int) *image.Gray {
		img := image.NewGray(image.Rect(0, 0, size, size))
		for y := range size {
			for x := range size {
				v := uint8(20)
				if (x/cell+y/cell)%2 == 0 {
					v = 235
				}
				img.SetGray(x, y, color.Gray{Y: v})
			}
		}
		return img
	}
	// Blur by averaging a few passes of a 3x3 box.
	blur := func(src *image.Gray, passes int) *image.Gray {
		cur := src
		for range passes {
			next := image.NewGray(src.Bounds())
			for y := range size {
				for x := range size {
					sum, n := 0, 0
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							if xx, yy := x+dx, y+dy; xx >= 0 && yy >= 0 && xx < size && yy < size {
								sum += int(cur.GrayAt(xx, yy).Y)
								n++
							}
						}
					}
					next.SetGray(x, y, color.Gray{Y: uint8(sum / n)})
				}
			}
			cur = next
		}
		return cur
	}
	flat := image.NewGray(image.Rect(0, 0, size, size))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}

	sharp, err := Sharpness(encodeJPEG(t, checker(4)))
	if err != nil {
		t.Fatal(err)
	}
	soft, err := Sharpness(encodeJPEG(t, blur(checker(4), 2)))
	if err != nil {
		t.Fatal(err)
	}
	blurry, err := Sharpness(encodeJPEG(t, blur(checker(4), 5)))
	if err != nil {
		t.Fatal(err)
	}
	none, err := Sharpness(encodeJPEG(t, flat))
	if err != nil {
		t.Fatal(err)
	}
	if !(sharp > soft && soft > blurry && blurry > none) || none > 1 {
		t.Errorf("sharpness should fall with blur: sharp %.1f, soft %.1f, blurry %.1f, flat %.1f", sharp, soft, blurry, none)
	}

	// A colour picture is measured on its luma.
	rgb := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			c := color.RGBA{R: 200, G: 30, B: 30, A: 255}
			if (x/4+y/4)%2 == 0 {
				c = color.RGBA{R: 30, G: 200, B: 30, A: 255}
			}
			rgb.SetRGBA(x, y, c)
		}
	}
	if v, err := Sharpness(encodeJPEG(t, rgb)); err != nil || v <= none {
		t.Errorf("a colour checkerboard measured %.1f, %v", v, err)
	}

	for name, in := range map[string][]byte{"garbage": []byte("not a jpeg"), "empty": nil, "truncated": encodeJPEG(t, checker(4))[:40]} {
		if v, err := Sharpness(in); err == nil || v >= 0 {
			t.Errorf("%s: want an error and a negative value, got %v, %v", name, v, err)
		}
	}
	tiny := image.NewGray(image.Rect(0, 0, 2, 2))
	if _, err := Sharpness(encodeJPEG(t, tiny)); err == nil {
		t.Error("a 2x2 picture cannot be measured")
	}
}
