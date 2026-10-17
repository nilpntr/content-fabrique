package styleprofile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// The video side of a reference: cuts, shot lengths, camera motion and zoom proxies, light and colour.

// Thresholds of the picture measures. CutThreshold is the scene-change score (ffmpeg's 0 to 1) from which a frame is a cut: tuned on
// five finished videos of one creator, where the cuts sit at 0.13 to 0.34 and the strongest score that is not a cut is 0.07 (a soft
// brightness change in a video with no cut at all).
const (
	CutThreshold = 0.10
	// minCutGapS merges scores that belong to one cut (a cut spreads over two or three frames at 30 fps).
	minCutGapS = 0.3
	// minShotS is the shortest shot: a cut within this of the start or the end is a flicker of the picture, not a cut.
	minShotS = 0.3
)

// scorePoint is one scene score.
type scorePoint struct {
	TimeS, Score float64
}

// parseScene reads ffmpeg's metadata print of the scene scores.
func parseScene(data []byte) ([]scorePoint, error) {
	var out []scorePoint
	t := -1.0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "frame:"); ok {
			if i := strings.Index(rest, "pts_time:"); i >= 0 {
				f := strings.Fields(rest[i+len("pts_time:"):])
				if len(f) == 0 {
					return nil, fmt.Errorf("styleprofile: bad frame line %q", line)
				}
				v, err := strconv.ParseFloat(f[0], 64)
				if err != nil {
					return nil, fmt.Errorf("styleprofile: bad frame line %q", line)
				}
				t = v
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "lavfi.scene_score="); ok && t >= 0 {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("styleprofile: bad scene score %q", line)
			}
			out = append(out, scorePoint{t, f})
		}
		if len(out) > media.MaxStyleSeconds*media.StyleSceneFPS+1000 {
			return nil, errors.New("styleprofile: too many scene scores")
		}
	}
	return out, sc.Err()
}

// DetectCuts returns the cuts: the times of the local maxima of the score at or above CutThreshold, at least minCutGapS apart (the
// stronger of two close ones wins), and not within minShotS of the start or the end of the video.
func DetectCuts(points []scorePoint, durationS float64) []float64 {
	var cands []scorePoint
	for _, p := range points {
		if p.Score >= CutThreshold && p.TimeS >= minShotS && (durationS <= 0 || p.TimeS <= durationS-minShotS) {
			cands = append(cands, p)
		}
	}
	var cuts []scorePoint
	for _, c := range cands {
		if n := len(cuts); n > 0 && c.TimeS-cuts[n-1].TimeS < minCutGapS {
			if c.Score > cuts[n-1].Score {
				cuts[n-1] = c
			}
			continue
		}
		cuts = append(cuts, c)
	}
	out := make([]float64, len(cuts))
	for i, c := range cuts {
		out[i] = round2(c.TimeS)
	}
	return out
}

// ShotLengths are the lengths of the shots a list of cuts makes of a video of durationS.
func ShotLengths(cuts []float64, durationS float64) []float64 {
	var out []float64
	prev := 0.0
	for _, c := range cuts {
		out = append(out, round2(c-prev))
		prev = c
	}
	return append(out, round2(max(durationS-prev, 0)))
}

// statFrame is one frame of the signalstats print.
type statFrame struct {
	YAvg, YHigh, SatAvg, SatHigh float64
}

// parseStats reads ffmpeg's metadata print of signalstats.
func parseStats(data []byte) ([]statFrame, error) {
	var out []statFrame
	var cur statFrame
	var have, inFrame bool
	flush := func() {
		if inFrame && have {
			out = append(out, cur)
		}
		cur, have = statFrame{}, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "frame:") {
			flush()
			inFrame = true
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(k, "lavfi.signalstats.") {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("styleprofile: bad statistic %q", line)
		}
		switch strings.TrimPrefix(k, "lavfi.signalstats.") {
		case "YAVG":
			cur.YAvg, have = f, true
		case "YHIGH":
			cur.YHigh = f
		case "SATAVG":
			cur.SatAvg = f
		case "SATHIGH":
			cur.SatHigh = f
		}
		if len(out) > media.MaxStyleSeconds*media.StyleStatsFPS+100 {
			return nil, errors.New("styleprofile: too many statistics frames")
		}
	}
	flush()
	return out, sc.Err()
}

// normLuma converts an 8-bit limited-range luma to 0..1, as the clip analysis does.
func normLuma(v float64) float64 { return math.Max(0, math.Min(1, (v-16)/219)) }

// satScale is the chroma radius that a saturation of 1 stands for (signalstats' SAT is the length of the (U, V) vector in 8-bit
// levels, at most about 181; 128 is the radius of a fully saturated primary on this scale for ordinary footage).
const satScale = 128.0

// lightOf summarises the statistics: the medians of the frames.
func lightOf(frames []statFrame) (lumaMean, lumaP90, satMean, satP90 float64) {
	col := func(f func(statFrame) float64) float64 {
		v := make([]float64, len(frames))
		for i, fr := range frames {
			v[i] = f(fr)
		}
		slices.Sort(v)
		return median(v)
	}
	if len(frames) == 0 {
		return
	}
	return round2(normLuma(col(func(f statFrame) float64 { return f.YAvg }))), round2(normLuma(col(func(f statFrame) float64 { return f.YHigh }))),
		round2(math.Min(1, col(func(f statFrame) float64 { return f.SatAvg })/satScale)), round2(math.Min(1, col(func(f statFrame) float64 { return f.SatHigh })/satScale))
}

// ---- the thumbnails: motion, zoom, flashes, vignette ----------------------------------------------------------------------

const (
	tsz = media.StyleThumbSize
	// zoomPunch is the scale change within zoomWindow steps that counts as a punch-in; pushNet the net scale change of a steady push.
	zoomPunch   = 0.058 // ln(1.06)
	zoomWindow  = 5
	pushMinStep = 15
	pushNet     = 0.03
	flashRise   = 35.0
)

// thumbs splits the raw file into frames.
func thumbs(raw []byte) [][]byte {
	n := len(raw) / media.StyleThumbPixels
	out := make([][]byte, n)
	for i := range out {
		out[i] = raw[i*media.StyleThumbPixels : (i+1)*media.StyleThumbPixels]
	}
	return out
}

func meanOf(f []byte) float64 {
	s := 0
	for _, v := range f {
		s += int(v)
	}
	return float64(s) / float64(len(f))
}

// scaleStep finds the zoom between two consecutive thumbnails: the scale s (ln s returned) that, applied to b about the centre, best
// matches a over the middle of the picture. 0 when no scale is clearly better than none.
func scaleStep(a, b []byte) float64 {
	const lo, hi = 8, tsz - 8 // the middle: edges leave the picture when it zooms
	sad := func(s float64) float64 {
		var sum float64
		cx, cy := float64(tsz-1)/2, float64(tsz-1)/2
		for y := lo; y < hi; y++ {
			for x := lo; x < hi; x++ {
				sx, sy := cx+(float64(x)-cx)/s, cy+(float64(y)-cy)/s
				if sx < 0 || sy < 0 || sx > tsz-1 || sy > tsz-1 {
					sum += 255
					continue
				}
				// Bilinear: a step of a percent moves a pixel by a fraction of a pixel on a thumbnail.
				ix, iy := min(int(sx), tsz-2), min(int(sy), tsz-2)
				fx, fy := sx-float64(ix), sy-float64(iy)
				v := float64(b[iy*tsz+ix])*(1-fx)*(1-fy) + float64(b[iy*tsz+ix+1])*fx*(1-fy) +
					float64(b[(iy+1)*tsz+ix])*(1-fx)*fy + float64(b[(iy+1)*tsz+ix+1])*fx*fy
				sum += math.Abs(float64(a[y*tsz+x]) - v)
			}
		}
		return sum
	}
	base := sad(1)
	if base < float64((hi-lo)*(hi-lo))*1.5 { // a picture that did not change
		return 0
	}
	best, bestSAD := 1.0, base
	for k := -12; k <= 12; k++ {
		if k == 0 {
			continue
		}
		s := 1 + 0.005*float64(k)
		if v := sad(s); v < bestSAD {
			best, bestSAD = s, v
		}
	}
	if bestSAD > 0.85*base {
		return 0
	}
	return math.Log(best)
}

// motionStats computes the picture-motion measures from the thumbnails: the median frame difference, the quick zooms, the share of the
// time in a slow steady zoom, the flashes and the vignette ratio.
func motionStats(frames [][]byte) (motion float64, zooms int, pushShare float64, flashes int, vignette float64) {
	n := len(frames)
	if n < 3 {
		return 0, 0, 0, 0, 1
	}
	diffs := make([]float64, 0, n-1)
	z := make([]float64, n)
	for i := 1; i < n; i++ {
		var d int
		for j := range frames[i] {
			x := int(frames[i][j]) - int(frames[i-1][j])
			if x < 0 {
				x = -x
			}
			d += x
		}
		diffs = append(diffs, float64(d)/float64(len(frames[i])))
		// A cut is not a zoom: skip the steps where the picture was replaced.
		if diffs[len(diffs)-1] < 40 {
			z[i] = scaleStep(frames[i-1], frames[i])
		}
	}
	slices.Sort(diffs)
	motion = round2(median(diffs))

	// Quick zooms: |sum of z over zoomWindow steps| >= zoomPunch, counted once per second.
	last := -100
	for i := 0; i+zoomWindow <= n; i++ {
		var s float64
		for k := 0; k < zoomWindow; k++ {
			s += z[i+k]
		}
		if math.Abs(s) >= zoomPunch && i-last > media.StyleThumbFPS {
			zooms++
			last = i
		}
	}
	// Slow steady zooms: runs of at least pushMinStep steps with one sign (zero steps allowed) and a net change of pushNet.
	inPush := 0
	for i := 1; i < n; {
		sign := 0.0
		j := i
		var net float64
		for j < n {
			if z[j] != 0 {
				if sign == 0 {
					sign = math.Copysign(1, z[j])
				} else if math.Copysign(1, z[j]) != sign {
					break
				}
			}
			net += z[j]
			j++
		}
		if j-i >= pushMinStep && math.Abs(net) >= pushNet && math.Abs(net) < zoomPunch*float64(j-i)/float64(zoomWindow) {
			inPush += j - i
		}
		i = max(j, i+1)
	}
	pushShare = round2(float64(inPush) / float64(n))

	// Flashes: the mean luma jumps up by flashRise over its neighbours and falls back within four steps.
	means := make([]float64, n)
	for i, f := range frames {
		means[i] = meanOf(f)
	}
	for i := 2; i+4 < n; i++ {
		before := (means[i-1] + means[i-2]) / 2
		after := (means[i+3] + means[i+4]) / 2
		if means[i]-before >= flashRise && means[i]-after >= flashRise {
			flashes++
			i += 4
		}
	}

	// The vignette: the corners over the centre.
	var ratios []float64
	for _, f := range frames {
		c, k := patch(f, tsz/2-8, tsz/2-8, 16), (patch(f, 0, 0, 8)+patch(f, tsz-8, 0, 8)+patch(f, 0, tsz-8, 8)+patch(f, tsz-8, tsz-8, 8))/4
		if c > 24 {
			ratios = append(ratios, k/c)
		}
	}
	vignette = 1
	if len(ratios) > 0 {
		slices.Sort(ratios)
		vignette = round2(math.Min(median(ratios), 1.5))
	}
	return motion, zooms, pushShare, flashes, vignette
}

func patch(f []byte, x0, y0, size int) float64 {
	var s int
	for y := y0; y < y0+size; y++ {
		for x := x0; x < x0+size; x++ {
			s += int(f[y*tsz+x])
		}
	}
	return float64(s) / float64(size*size)
}

// ---- running it ---------------------------------------------------------------------------------------------------------------

// MeasureVideo runs the style pass in the sandbox over the file and computes the picture measures: the cuts and the shot lengths, the
// motion proxies, the light and the colour. The file is untrusted: it is only ever an input of the sandbox job.
func MeasureVideo(ctx context.Context, runner media.Runner, path string) (Measures, error) {
	var m Measures
	abs, err := filepath.Abs(path)
	if err != nil {
		return m, err
	}
	md, err := media.Probe(ctx, runner, abs)
	if err != nil {
		return m, fmt.Errorf("styleprofile: probe: %w", err)
	}
	if !md.HasVideo {
		return m, errors.New("styleprofile: the file has no video")
	}
	m.Name = filepath.Base(path)
	m.DurationS = round2(float64(md.DurationMs) / 1000)
	m.FPS, m.Codec, m.Width, m.Height = md.FPS.Value, md.Codecs.Video, md.Width, md.Height
	if m.DurationS <= 0 || m.DurationS > media.MaxStyleSeconds {
		return m, fmt.Errorf("styleprofile: a reference video is 1 to %d seconds long, this one is %.1f", media.MaxStyleSeconds, m.DurationS)
	}
	dir, err := os.MkdirTemp("", "styleprofile-")
	if err != nil {
		return m, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	res, err := runner.Run(ctx, media.Invocation{
		Args: media.StyleArgs(media.InputPath(0)), Inputs: []media.Input{{Host: abs, Container: media.InputPath(0)}}, OutputDir: dir,
		Timeout: runTimeout(m.DurationS),
	})
	if err != nil {
		return m, fmt.Errorf("styleprofile: ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		return m, &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
	}
	scene, err := readRegular(filepath.Join(dir, media.StyleSceneFile), 32<<20)
	if err != nil {
		return m, err
	}
	stats, err := readRegular(filepath.Join(dir, media.StyleStatsFile), 64<<20)
	if err != nil {
		return m, err
	}
	raw, err := readRegular(filepath.Join(dir, media.StyleThumbsFile), media.MaxStyleThumbs*media.StyleThumbPixels)
	if err != nil {
		return m, err
	}
	points, err := parseScene(scene)
	if err != nil {
		return m, err
	}
	frames, err := parseStats(stats)
	if err != nil {
		return m, err
	}
	m.CutsS = DetectCuts(points, m.DurationS)
	m.ShotsS = ShotLengths(m.CutsS, m.DurationS)
	v := &m.Video
	v.LumaMean, v.LumaP90, v.SatMean, v.SatP90 = lightOf(frames)
	v.MotionMedian, v.ZoomEvents, v.PushInShare, v.Flashes, v.VignetteRatio = motionStats(thumbs(raw))
	if mins := m.DurationS / 60; mins > 0 {
		v.EffectsPerMinute = round1(float64(v.ZoomEvents+v.Flashes) / mins)
	}
	return m, nil
}
