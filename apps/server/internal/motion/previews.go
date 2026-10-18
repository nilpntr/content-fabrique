package motion

import (
	"embed"
	"image"
	"image/color"
	"image/draw"
)

// previewFS holds a still of every template, rendered by the integration test (-update) from the frame where the template is fully on
// screen: what the editor sees when it lists the templates.
//
//go:embed previews/*.png
var previewFS embed.FS

// PreviewPNG returns the still preview of a template, a PNG.
func PreviewPNG(template string) ([]byte, bool) {
	data, err := previewFS.ReadFile("previews/" + template + ".png")
	return data, err == nil
}

// previewBackground is the dark grey a preview's transparent frame is laid over: neutral, so the template's own colours read.
var previewBackground = color.NRGBA{R: 0x2B, G: 0x2F, B: 0x36, A: 0xFF}

// Preview turns a captured overlay frame (transparent PNG, the overlay's clip) into a still for the editor: laid over a dark grey
// and halved (a 2x2 box filter), so a title is a few hundred pixels wide.
func Preview(frame image.Image) *image.NRGBA {
	b := frame.Bounds()
	full := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(full, full.Bounds(), &image.Uniform{C: previewBackground}, image.Point{}, draw.Src)
	draw.Draw(full, full.Bounds(), frame, b.Min, draw.Over)
	w, h := max(1, b.Dx()/2), max(1, b.Dy()/2)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum [4]int
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					o := full.PixOffset(min(2*x+dx, b.Dx()-1), min(2*y+dy, b.Dy()-1))
					for c := 0; c < 4; c++ {
						sum[c] += int(full.Pix[o+c])
					}
				}
			}
			o := out.PixOffset(x, y)
			for c := 0; c < 4; c++ {
				out.Pix[o+c] = uint8((sum[c] + 2) / 4) //nolint:gosec // the mean of four bytes is at most 255
			}
		}
	}
	return out
}
