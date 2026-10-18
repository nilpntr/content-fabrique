package telemetry

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNG frames too
)

// ThumbLongSide is the long side of a thumbnail in a span, in pixels: enough to tell which frame of which clip the model saw.
const ThumbLongSide = 144

// Thumbnail returns a small JPEG of an image (JPEG or PNG) as a data: URL, at most maxBytes long, or an error. It is what an LLM span
// shows in place of a frame the model was sent, when content capture is on.
func Thumbnail(data []byte, longSide, maxBytes int) (string, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return "", errors.New("telemetry: empty image")
	}
	tw, th := w, h
	if w >= h && w > longSide {
		tw, th = longSide, max(1, h*longSide/w)
	} else if h > w && h > longSide {
		tw, th = max(1, w*longSide/h), longSide
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// A box filter: each target pixel is the mean of the source pixels it covers.
	for y := range th {
		y0, y1 := b.Min.Y+y*h/th, b.Min.Y+max((y+1)*h/th, y*h/th+1)
		for x := range tw {
			x0, x1 := b.Min.X+x*w/tw, b.Min.X+max((x+1)*w/tw, x*w/tw+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			// The mean of 16-bit channels, shifted to 8 bits, is at most 255.
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 0xff //nolint:gosec // at most 255, see above
		}
	}
	for _, q := range []int{60, 40, 25} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q}); err != nil {
			return "", err
		}
		url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		if len(url) <= maxBytes {
			return url, nil
		}
	}
	return "", errors.New("telemetry: the thumbnail does not fit its size cap")
}
