package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func withLook(preset string, match bool) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) {
		it.Look = &timeline.Look{Preset: preset}
		if match {
			it.Look.Match = ptr(true)
		}
	}
}

func withEV(ev float64) func(*timeline.VideoItem) {
	return func(it *timeline.VideoItem) { it.ExposureEv = &ev }
}

func TestLooksAndExposureInTheGraph(t *testing.T) {
	graph := func(opts ...func(*timeline.VideoItem)) string {
		return mustCompile(t, specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, opts...)}), Final).FilterComplex
	}
	for preset, want := range map[string]string{
		timeline.LookAuto:     "curves=all='0/0 0.25/0.235 0.75/0.765 1/1'",
		timeline.LookGymMoody: "vignette=angle=0.28:dither=0",
	} {
		if g := graph(withLook(preset, false)); !strings.Contains(g, want) {
			t.Errorf("%s: want %s in %s", preset, want, g)
		}
	}
	// auto is a fixed curve: it has no measuring filter that could follow the picture's mean or extremes.
	if g := graph(withLook(timeline.LookAuto, false)); strings.Contains(g, "normalize") || strings.Contains(g, "histeq") {
		t.Error("auto must not normalise the picture")
	}
	t.Run("the hint is applied as its opposite, limited to 0.7 stops", func(t *testing.T) {
		for ev, want := range map[float64]string{
			0.5:  "lt(((val-16)/219),0.5),((val-16)/219)*0.707107,",
			-0.5: "lt(((val-16)/219),0.5),((val-16)/219)*1.414214,",
			1:    "((val-16)/219)*0.615572,", // clamped to 0.7 stops
			-1:   "((val-16)/219)*1.624505,",
		} {
			if g := graph(withLook("none", true), withEV(ev)); !strings.Contains(g, want) {
				t.Errorf("ev %v: want %s in %s", ev, want, g)
			}
		}
	})
	t.Run("a hint under 0.15 stops is left alone", func(t *testing.T) {
		for _, ev := range []float64{0.14, -0.14, 0, 0.004} {
			if g := graph(withLook("none", true), withEV(ev)); strings.Contains(g, "lutyuv") {
				t.Errorf("ev %v: the graph has an exposure step", ev)
			}
		}
		if g := graph(withLook("none", true), withEV(0.15)); !strings.Contains(g, "lutyuv") {
			t.Error("0.15 stops is the smallest hint that is applied")
		}
	})
	t.Run("the exposure step is before the preset", func(t *testing.T) {
		g := graph(withLook("punchy", true), withEV(0.5))
		if iExp, iLook := strings.Index(g, "lutyuv"), strings.Index(g, "eq=contrast=1.12"); iExp < 0 || iExp > iLook {
			t.Errorf("exposure at %d, look at %d", iExp, iLook)
		}
	})
	t.Run("match, or the preset auto, applies the hint; nothing else does", func(t *testing.T) {
		for name, tc := range map[string]struct {
			g    string
			want bool
		}{
			"auto without match":      {graph(withLook("auto", false), withEV(0.5)), true},
			"match with another look": {graph(withLook("punchy", true), withEV(0.5)), true},
			"match false":             {graph(withLook("punchy", false), withEV(0.5)), false},
			"no hint":                 {graph(withLook("punchy", true)), false},
			"a hint, no look":         {graph(withEV(0.5)), false},
		} {
			if has := strings.Contains(tc.g, "lutyuv"); has != tc.want {
				t.Errorf("%s: exposure step present = %v", name, has)
			}
		}
	})
	t.Run("hints outside -1 to 1 are refused, and so are unknown presets", func(t *testing.T) {
		for _, ev := range []float64{1.01, -1.5, 3, math.NaN(), math.Inf(1)} {
			spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withLook("none", true), withEV(ev))})
			if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("ev %v: err = %v", ev, err)
			}
		}
		for _, p := range []string{"auto;drawtext=text=x", "gym_moody,scale=1:1", "AUTO", "cinematic"} {
			spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, withLook(p, false))})
			if _, err := Compile(spec, Final, plainResolver()); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("look %q: err = %v", p, err)
			}
		}
	})
	t.Run("only numbers and fixed words in the graph", func(t *testing.T) {
		cat := testAssets(t)
		for _, name := range []string{"v41_looks_sfx_9x16", "v42_looks_sfx_1x1", "v43_looks_sfx_4x5", "v44_looks_sfx_16x9"} {
			plan, err := Compile(loadFixture(t, name, cat), Final, fakeResolver(cat))
			if err != nil {
				t.Fatal(err)
			}
			if !graphAlphabet.MatchString(plan.FilterComplex) {
				t.Errorf("%s: a character outside the alphabet: %q", name, regexpOutside.FindString(plan.FilterComplex))
			}
		}
	})
}

// frameLuma is the average luma (0 to 255) of a region of the render's frames: crop is an ffmpeg crop argument (w:h:x:y).
func frameLuma(t *testing.T, ffmpeg, video, crop string) float64 {
	t.Helper()
	stats := filepath.Join(t.TempDir(), "stats.txt")
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-i", video, //nolint:gosec // test helper
		"-vf", "crop="+crop+",signalstats,metadata=mode=print:key=lavfi.signalstats.YAVG:file="+stats, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signalstats: %v\n%s", err, stderr.String())
	}
	data, err := os.ReadFile(stats) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	sum, n := 0.0, 0
	for _, line := range strings.Split(string(data), "\n") {
		if _, v, ok := strings.Cut(line, "lavfi.signalstats.YAVG="); ok {
			y, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				t.Fatal(err)
			}
			sum += y
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no statistics in %q", data)
	}
	return sum / float64(n)
}

// percentile is the p-th percentile (0 to 100) of the luma of the middle frame of a render.
func percentile(t *testing.T, ffmpeg, video string, w, h int, p float64) float64 {
	t.Helper()
	frames := grayFrames(t, ffmpeg, video, w, h)
	px := slices.Clone(frames[len(frames)/2])
	slices.Sort(px)
	return float64(px[int(p/100*float64(len(px)-1))])
}

// TestLooksOnTheScreen renders flat and low-contrast sources and checks what each look does to the pixels: the exposure hint
// moves the luma by the right amount, a low-key frame keeps its blacks and a dark frame is lifted, auto is a fixed curve that
// does not chase the mean, gym_moody pulls the blacks down and darkens the corners, and nothing is random.
func TestLooksOnTheScreen(t *testing.T) {
	ffmpeg, _ := ffmpegTools(t)
	_, _ = ensureMedia(t)
	bare := Preview
	bare.Watermark = false
	// Flat mid grey (luma 126 in limited range) and a dim ramp (luma 40 to 90 left to right).
	grey := genSource(t, ffmpeg, "grey", "color=c=gray:s=640x360:r=30:d=3")
	dim := genSource(t, ffmpeg, "dim", "color=c=black:s=640x360:r=30:d=3,format=yuv420p,geq=lum='40+50*X/W':cb=128:cr=128")
	// A low-key frame: a black wall (luma 18) and a lit disc (luma 150), the way gym footage is shot. Its mean is low, and it is
	// correctly exposed. And a frame that is really underexposed: no blacks, everything between luma 50 and 90.
	lowKey := genSource(t, ffmpeg, "lowkey", "color=c=black:s=640x360:r=30:d=3,format=yuv420p,geq=lum='if(lt(hypot(X-320,Y-180),80),150,18)':cb=128:cr=128")
	under := genSource(t, ffmpeg, "under", "color=c=black:s=640x360:r=30:d=3,format=yuv420p,geq=lum='50+40*X/W':cb=128:cr=128")
	render := func(src string, opts ...func(*timeline.VideoItem)) string {
		spec := specOf(30, []timeline.VideoItem{clipAt("c1", assetA, 0, 2000, 0, opts...)})
		out, _ := renderSpec(t, spec, bare, oneFileResolver(src))
		return out
	}
	const whole = "iw:ih:0:0"
	base := frameLuma(t, ffmpeg, render(grey), whole)

	t.Run("the hint moves the luma along the curve limited to 0.7 stops", func(t *testing.T) {
		x := (base - 16) / 219
		curve := func(ev float64) float64 {
			g := math.Exp2(-math.Max(-0.7, math.Min(0.7, ev)))
			if x < 0.5 {
				return 16 + 219*x*g
			}
			return 16 + 219*(0.5*g+(1-0.5*g)*(x-0.5)/0.5)
		}
		for _, ev := range []float64{0.5, -0.5, -0.9, 0.9} {
			got := frameLuma(t, ffmpeg, render(grey, withLook("none", true), withEV(ev)), whole)
			if want := curve(ev); math.Abs(got-want) > 2.5 {
				t.Errorf("ev %v: luma %.1f, want %.1f (from %.1f)", ev, got, want, base)
			}
		}
		if got := frameLuma(t, ffmpeg, render(grey, withLook("none", false), withEV(0.5)), whole); math.Abs(got-base) > 1 {
			t.Errorf("a hint without match changed the luma: %.1f from %.1f", got, base)
		}
		if got := frameLuma(t, ffmpeg, render(grey, withLook("none", true), withEV(0.1)), whole); math.Abs(got-base) > 1 {
			t.Errorf("a hint of 0.1 stops changed the luma: %.1f from %.1f", got, base)
		}
	})

	t.Run("a low-key frame keeps its blacks", func(t *testing.T) {
		before := render(lowKey)
		w, h := 540, 960
		p5, p20 := percentile(t, ffmpeg, before, w, h, 5), percentile(t, ffmpeg, before, w, h, 20)
		// The hint of a clip like this, as the analysis must give it (from a percentile, so a nudge), and the strongest one the
		// spec allows. The black wall is 18 and stays within a few levels of it: the 5th and the 20th percentile are the wall.
		for _, ev := range []float64{-0.7, -1} {
			for name, look := range map[string]func(*timeline.VideoItem){"auto": withLook("auto", false), "none with match": withLook("none", true), "gym_moody with match": withLook("gym_moody", true)} {
				out := render(lowKey, look, withEV(ev))
				got5, got20 := percentile(t, ffmpeg, out, w, h, 5), percentile(t, ffmpeg, out, w, h, 20)
				// gym_moody pulls the toe down on purpose, so its blacks may go down; no look may lift them.
				lowest := p5 - 6
				if name == "gym_moody with match" {
					lowest = 0
				}
				if got5 > p5+6 || got5 < lowest {
					t.Errorf("%s, ev %v: the 5th percentile went from %.0f to %.0f", name, ev, p5, got5)
				}
				if got20 > p20+6 {
					t.Errorf("%s, ev %v: the 20th percentile went from %.0f to %.0f", name, ev, p20, got20)
				}
			}
		}
		// auto alone, on a frame with no hint: a fixed curve, the wall does not move.
		out := render(lowKey, withLook("auto", false))
		if got := percentile(t, ffmpeg, out, w, h, 5); math.Abs(got-p5) > 3 {
			t.Errorf("auto without a hint moved the blacks: %.0f from %.0f", got, p5)
		}
		// The lit subject is nudged up, not clipped: the brightest pixel stays under white.
		lit := render(lowKey, withLook("auto", false), withEV(-0.7))
		if got := percentile(t, ffmpeg, lit, w, h, 100); got >= 235 {
			t.Errorf("the highlights are clipped: %.0f", got)
		}
		if after := frameLuma(t, ffmpeg, lit, "100:100:220:430"); after <= frameLuma(t, ffmpeg, before, "100:100:220:430") {
			t.Error("the lit subject was not lifted by a hint that says it is underexposed")
		}
	})

	t.Run("a really underexposed frame is lifted", func(t *testing.T) {
		before := frameLuma(t, ffmpeg, render(under), whole)
		after := frameLuma(t, ffmpeg, render(under, withLook("auto", false), withEV(-0.7)), whole)
		// 70 -> about 70 + 54 * 0.62 = x1.62 on (70-16): a lift of 30 levels or so.
		if after < before+20 {
			t.Errorf("the underexposed frame went from luma %.1f to %.1f", before, after)
		}
		if got := frameLuma(t, ffmpeg, render(under, withLook("auto", false)), whole); math.Abs(got-before) > 5 {
			t.Errorf("auto without a hint changed the frame: %.1f from %.1f", got, before)
		}
	})

	t.Run("auto does not chase the mean", func(t *testing.T) {
		// Two clips of very different brightness come out as far apart as they went in: only a hint moves a clip.
		a, b := frameLuma(t, ffmpeg, render(dim, withLook("auto", false)), whole), frameLuma(t, ffmpeg, render(grey, withLook("auto", false)), whole)
		a0, b0 := frameLuma(t, ffmpeg, render(dim), whole), frameLuma(t, ffmpeg, render(grey), whole)
		if math.Abs((b-a)-(b0-a0)) > 6 {
			t.Errorf("auto moved the gap between two clips from %.1f to %.1f", b0-a0, b-a)
		}
	})

	t.Run("gym_moody darkens the corners and the blacks", func(t *testing.T) {
		out := render(grey, withLook("gym_moody", false))
		centre := frameLuma(t, ffmpeg, out, "100:100:220:430") // 540x960 frame
		corner := frameLuma(t, ffmpeg, out, "60:60:0:0")
		if corner > centre-15 {
			t.Errorf("corner luma %.1f is not darker than the centre %.1f", corner, centre)
		}
		// The shadows are left alone: a dim source (luma 40 to 90) does not get darker, and not noticeably lighter.
		after, before := frameLuma(t, ffmpeg, render(dim, withLook("gym_moody", false)), "100:100:220:430"), frameLuma(t, ffmpeg, render(dim), "100:100:220:430")
		if after > before+3 || after < before*0.93 {
			t.Errorf("the shadows moved: luma %.1f from %.1f", after, before)
		}
	})

	// gym_moody is the grade for gym footage, which is low-key: it must not crush it. The first version took the rendered YAVG of a
	// real clip from 17 to 5 of 255 (a toe from 0.12 to 0.07, eq contrast 1.08). On low-key frames the median and the 90th percentile
	// of the luma may not drop by more than 12 %.
	t.Run("gym_moody does not crush a low-key frame", func(t *testing.T) {
		// A dark gym: a dim gradient (luma 28 to 60) behind a lit subject (luma 110), and the black wall with a lit disc.
		gym := genSource(t, ffmpeg, "gym", "color=c=black:s=640x360:r=30:d=3,format=yuv420p,geq=lum='if(lt(hypot(X-320,Y-200),90),110,28+32*X/W)':cb=128:cr=128")
		w, h := 540, 960
		for name, src := range map[string]string{"black wall and disc": lowKey, "dark gym": gym} {
			before := render(src)
			after := render(src, withLook("gym_moody", false))
			// The median, the 90th percentile and the mean may not fall by more than 12 %; the 5th percentile is the corners, which the
			// vignette darkens on purpose, so it has more room.
			for _, c := range []struct{ p, keep float64 }{{5, 0.75}, {50, 0.88}, {90, 0.88}} {
				b, a := percentile(t, ffmpeg, before, w, h, c.p), percentile(t, ffmpeg, after, w, h, c.p)
				if a < b*c.keep {
					t.Errorf("%s: the %.0fth percentile of the luma fell from %.0f to %.0f (more than %.0f %%)", name, c.p, b, a, 100*(1-c.keep))
				}
			}
			if b, a := frameLuma(t, ffmpeg, before, whole), frameLuma(t, ffmpeg, after, whole); a < b*0.88 {
				t.Errorf("%s: the mean luma fell from %.1f to %.1f (more than 12 %%)", name, b, a)
			}
		}
	})

	t.Run("the same frames on every render", func(t *testing.T) {
		for _, p := range []string{"auto", "gym_moody"} {
			sums := [2][32]byte{}
			for i := range sums {
				out := render(dim, withLook(p, true), withEV(-0.7))
				data, err := os.ReadFile(out) //nolint:gosec // test file
				if err != nil {
					t.Fatal(err)
				}
				sums[i] = sha256.Sum256(data)
			}
			if sums[0] != sums[1] {
				t.Errorf("%s: two renders differ", p)
			}
		}
	})
}
