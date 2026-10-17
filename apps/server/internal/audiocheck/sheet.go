package audiocheck

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The sheet: a picture of the sound for a vision model (and for a person). The x axis is the output time in ms, the same axis as
// the contact sheet (docs/design/timeline-v1.md section 6), whose tiles are the frames SheetOffsetMs after each cut: the cyan
// numbers on the axis are the tile indexes, at the time of their frame, so that a tile of the contact sheet and a stretch of sound
// can be put side by side. Top to bottom: the waveform (red where it is at full scale), the momentary loudness with the integrated
// loudness and a band of 3 LU around it, an event lane (cuts white, effects magenta, onsets yellow) and the time axis. Silences are
// shaded red across all of them. Drawn with image/png and the font of font.go: no dependency.
const (
	sheetW       = 1200
	marginL      = 34
	marginR      = 10
	legendH      = 22
	waveH        = 130
	loudH        = 100
	laneH        = 40
	axisH        = 32
	panelGap     = 6
	loudTopLUFS  = -5.0
	loudLowLUFS  = -50.0
	tileOffsetMs = 100 // renderjobs.SheetOffsetMs: the contact sheet's frame is this long after each cut
)

var (
	colBG      = color.RGBA{18, 18, 24, 255}
	colPanel   = color.RGBA{26, 26, 34, 255}
	colGrid    = color.RGBA{56, 56, 70, 255}
	colWave    = color.RGBA{120, 170, 255, 255}
	colClip    = color.RGBA{255, 60, 60, 255}
	colLoud    = color.RGBA{255, 170, 60, 255}
	colInt     = color.RGBA{80, 220, 120, 255}
	colBand    = color.NRGBA{80, 220, 120, 40}
	colCut     = color.RGBA{245, 245, 245, 255}
	colSFX     = color.RGBA{255, 70, 200, 255}
	colOnset   = color.RGBA{255, 230, 60, 255}
	colSilence = color.NRGBA{200, 40, 40, 70}
	colTile    = color.RGBA{60, 200, 255, 255}
	colText    = color.RGBA{220, 220, 230, 255}
	colTextDim = color.RGBA{140, 140, 155, 255}
)

// SheetSize is the size of every sheet in pixels.
func SheetSize() (w, h int) {
	return sheetW, legendH + waveH + panelGap + loudH + panelGap + laneH + axisH
}

// drawSheet draws the sheet and returns it as a PNG.
func drawSheet(r Report, m measured, cuts []int64, cfg config) []byte {
	w, h := SheetSize()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillRect(img, img.Bounds(), colBG)
	plotW := w - marginL - marginR
	dur := math.Max(r.DurationS, 0.001)
	x := func(ms float64) int { return marginL + int(math.Round(ms/1000/dur*float64(plotW))) }

	// Legend.
	lx := marginL
	for _, e := range []struct {
		c    color.Color
		text string
	}{{colWave, "SOUND"}, {colLoud, "LOUDNESS"}, {colInt, "PROGRAMME"}, {colCut, "CUT"}, {colSFX, "SFX"}, {colOnset, "ONSET"}, {colSilence, "SILENCE"}, {colTile, "TILE"}} {
		fillRect(img, image.Rect(lx, 6, lx+10, 16), e.c)
		drawText(img, lx+14, 5, 1, colText, e.text)
		lx += 14 + textWidth(e.text, 1) + 14
	}
	if !r.HasAudio {
		drawText(img, marginL, legendH+20, 2, colClip, "NO SOUND TRACK")
		return encode(img)
	}
	title := fmt.Sprintf("%.1f LUFS  %.1f DBTP  %.1f S", r.Loudness.IntegratedLUFS, r.Loudness.TruePeakDBTP, r.DurationS)
	drawText(img, w-marginR-textWidth(title, 1), 5, 1, colTextDim, title)

	waveTop := legendH
	loudTop := waveTop + waveH + panelGap
	laneTop := loudTop + loudH + panelGap
	axisTop := laneTop + laneH
	for _, p := range []image.Rectangle{
		image.Rect(marginL, waveTop, marginL+plotW, waveTop+waveH),
		image.Rect(marginL, loudTop, marginL+plotW, loudTop+loudH),
		image.Rect(marginL, laneTop, marginL+plotW, laneTop+laneH),
	} {
		fillRect(img, p, colPanel)
	}

	// Silences first, under the rest.
	for _, g := range r.Silence.Gaps {
		a, b := x(g.StartS*1000), max(x(g.EndS*1000), x(g.StartS*1000)+1)
		for _, p := range []image.Rectangle{
			image.Rect(a, waveTop, b, waveTop+waveH), image.Rect(a, loudTop, b, loudTop+loudH), image.Rect(a, laneTop, b, laneTop+laneH),
		} {
			fillRect(img, p, colSilence)
		}
	}

	// Waveform.
	mid := waveTop + waveH/2
	fillRect(img, image.Rect(marginL, mid, marginL+plotW, mid+1), colGrid)
	lo, hi := waveform(m.pcm, plotW)
	scale := float64(waveH/2 - 2)
	for px := 0; px < plotW; px++ {
		top, bot := mid-int(math.Round(float64(clamp1(hi[px]))*scale)), mid-int(math.Round(float64(clamp1(lo[px]))*scale))
		c := colWave
		if hi[px] >= 0.99 || lo[px] <= -0.99 {
			c = colClip
		}
		fillRect(img, image.Rect(marginL+px, top, marginL+px+1, max(bot, top+1)+1), c)
	}
	drawText(img, 4, waveTop+2, 1, colTextDim, "WAVE")

	// Loudness: a band around the programme level, its line, and the momentary curve.
	ly := func(lufs float64) int {
		t := (math.Min(math.Max(lufs, loudLowLUFS), loudTopLUFS) - loudLowLUFS) / (loudTopLUFS - loudLowLUFS)
		return loudTop + loudH - 1 - int(math.Round(t*float64(loudH-2)))
	}
	for _, db := range []float64{-40, -30, -20, -10} {
		fillRect(img, image.Rect(marginL, ly(db), marginL+plotW, ly(db)+1), colGrid)
		drawText(img, 2, ly(db)-3, 1, colTextDim, fmt.Sprintf("%.0f", db))
	}
	if r.Loudness.IntegratedLUFS > -69 {
		fillRect(img, image.Rect(marginL, ly(r.Loudness.IntegratedLUFS+bodyWithinLU), marginL+plotW, ly(r.Loudness.IntegratedLUFS-bodyWithinLU)), colBand)
		for px := marginL; px < marginL+plotW; px += 6 {
			fillRect(img, image.Rect(px, ly(r.Loudness.IntegratedLUFS), px+3, ly(r.Loudness.IntegratedLUFS)+1), colInt)
		}
	}
	prevX, prevY := -1, 0
	for _, p := range r.Loudness.Momentary {
		px, py := x(p.TimeS*1000), ly(p.LUFS)
		if prevX >= 0 {
			line(img, prevX, prevY, px, py, colLoud)
		}
		prevX, prevY = px, py
	}
	drawText(img, 4, loudTop+2, 1, colTextDim, "LUFS")

	// Onsets, in the lane, then the cuts and the effects through all the panels.
	for _, o := range r.Onsets.List {
		hgt := 8
		if o.RiseDB >= salientDB {
			hgt = 16
		}
		fillRect(img, image.Rect(x(float64(o.TimeMs)), laneTop+laneH-hgt, x(float64(o.TimeMs))+1, laneTop+laneH), colOnset)
	}
	for _, c := range cuts {
		fillRect(img, image.Rect(x(float64(c)), waveTop, x(float64(c))+1, laneTop+laneH), colCut)
	}
	for _, s := range cfg.sfx {
		if float64(s.AtMs)/1000 >= dur {
			continue
		}
		px := x(float64(s.AtMs))
		fillRect(img, image.Rect(px, waveTop, px+1, laneTop+laneH), colSFX)
		for k := 0; k < 5; k++ { // a flag at the top of the lane
			fillRect(img, image.Rect(px-k, laneTop+k, px+k+1, laneTop+k+1), colSFX)
		}
	}

	// The axis: seconds, and the contact sheet's tiles.
	step := axisStep(dur)
	for t := 0.0; t <= dur+1e-9; t += step {
		px := x(t * 1000)
		fillRect(img, image.Rect(px, axisTop, px+1, axisTop+4), colText)
		label := fmt.Sprintf("%g", t)
		drawText(img, min(px+2, w-marginR-textWidth(label, 1)), axisTop+6, 1, colText, label)
	}
	drawText(img, 2, axisTop+6, 1, colTextDim, "S")
	tile := 0
	starts := append([]int64{0}, cuts...)
	for _, c := range starts {
		px := x(float64(c + tileOffsetMs))
		for k := 0; k < 4; k++ {
			fillRect(img, image.Rect(px-k, axisTop+axisH-1-k, px+k+1, axisTop+axisH-k), colTile)
		}
		label := fmt.Sprintf("%d", tile)
		drawText(img, px+4, axisTop+axisH-10, 1, colTile, label)
		tile++
	}
	return encode(img)
}

func clamp1(v float32) float32 {
	return float32(math.Max(-1, math.Min(1, float64(v))))
}

// axisStep is the spacing of the time labels in seconds: about ten across the width.
func axisStep(dur float64) float64 {
	for _, s := range []float64{0.5, 1, 2, 5, 10, 15, 30, 60, 120} {
		if dur/s <= 12 {
			return s
		}
	}
	return 300
}

// line draws a one pixel line (Bresenham).
func line(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx, dy := absInt(x1-x0), -absInt(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	e := dx + dy
	for {
		fillRect(img, image.Rect(x0, y0, x0+1, y0+1), c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
