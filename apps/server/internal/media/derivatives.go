package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The command builders of the ingest derivatives (P1-04, architecture section 5). Like the others in this package they are
// pure functions of their arguments, golden-tested; the ingest package runs them through a Runner (the sandbox).
//
// The proxy is made from the original; every other picture derivative (poster, sprite sheet, hover preview) is made from
// the proxy, which is already upright, SDR and 720 px, so those steps decode a small, seek-friendly file and never repeat
// the rotation and tone mapping logic.

// Sizes and limits of the derivatives.
const (
	// ProxyShortSide is the proxy's short side in pixels. A smaller source is not scaled up.
	ProxyShortSide = 720
	// HoverShortSide is the hover preview's short side.
	HoverShortSide = 360
	// SpriteTileWidth is the width of one sprite tile.
	SpriteTileWidth = 160
	// SpriteMaxTiles is the most tiles one sprite sheet holds (a 10 x 10 grid).
	SpriteMaxTiles = 100
	spriteMaxCols  = 10
	spriteMaxTileH = 480

	// HoverTotalMs is the length of the hover preview, HoverSegmentMs the length of one of its segments. A clip shorter
	// than HoverTotalMs is used whole.
	HoverTotalMs   = 8000
	HoverSegmentMs = 2000
	hoverSegments  = HoverTotalMs / HoverSegmentMs
	// hoverMaxRate caps the hover preview's bit rate: HoverTotalMs of it is at most 600 kbit/s * 8 s = 600 kB, well under 1 MB.
	hoverMaxRate = "600k"

	// WaveformPoints is the number of peaks in waveform.json.
	WaveformPoints = 1000
	// AudioSampleRate is the sample rate of audio_16k.wav (mono, 16-bit PCM): what speech recognition wants.
	AudioSampleRate = 16000
)

// ImageFormats are the demuxers accepted for an uploaded picture (the asset kind `image`). They are separate from
// UntrustedFormats because only image assets are probed with them.
const ImageFormats = "image2,png_pipe,jpeg_pipe"

// ProbeImageArgs is ProbeArgs for a picture.
func ProbeImageArgs(input string) []string {
	a := ProbeArgs(input)
	for i, s := range a {
		if s == UntrustedFormats {
			a[i] = ImageFormats
		}
	}
	return a
}

// ProbeImage is Probe for a picture.
func ProbeImage(ctx context.Context, r Runner, path string) (Metadata, error) {
	return probeWith(ctx, r, path, ProbeImageArgs)
}

// ---- Sizes -------------------------------------------------------------------------------------------------------------

// ScaledSize scales a w x h picture so its short side is short pixels, keeping the aspect ratio, both sides even (H.264
// with 4:2:0 needs it). A picture whose short side is already at most short keeps its size (rounded down to even).
func ScaledSize(w, h, short int) (int, int) {
	if w <= 0 || h <= 0 || short <= 0 {
		return 0, 0
	}
	if min(w, h) <= short {
		return max(w&^1, 2), max(h&^1, 2)
	}
	if w <= h {
		return short &^ 1, max(even(float64(h)*float64(short)/float64(w)), 2)
	}
	return max(even(float64(w)*float64(short)/float64(h)), 2), short &^ 1
}

// even rounds to the nearest even integer.
func even(f float64) int { return int(math.Round(f/2)) * 2 }

// GOP is the proxy's frame rate as a whole number, and with it the keyframe interval in frames: one second. It rounds up
// (a phone's variable-rate "29.13 fps" clip is a 30 fps clip, and 29.97 is 30) so that the constant frame rate the proxy is
// encoded at is never below the source's, and it is at least 1 and at most 240. An unknown rate counts as 30.
func GOP(fps float64) int {
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		fps = 30
	}
	return min(max(int(math.Ceil(fps-0.05)), 1), 240)
}

// ---- Decodability check ------------------------------------------------------------------------------------------------

// DecodeCheckArgs decodes the first frames of the video stream and throws them away. A video ffmpeg cannot decode (an
// unsupported codec, a file that is cut off before its first frame) fails here, quickly, before the expensive steps.
func DecodeCheckArgs(input string, m Metadata) ([]string, error) {
	if !m.HasVideo {
		return nil, errors.New("media: decode check needs a video stream")
	}
	a := append(common(), "-xerror")
	a = append(a, inputOptions()...)
	return append(a, "-i", input, "-map", "0:"+strconv.Itoa(m.VideoIndex), "-frames:v", "3", "-an", "-f", "null", "-"), nil
}

// ---- Proxy -------------------------------------------------------------------------------------------------------------

// colourTags are put on every picture derivative: the proxy is always SDR BT.709, limited range.
var colourTags = []string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"}

// zscale's names for the colour descriptions ffprobe reports. Only values that really differ from BT.709 are listed.
var (
	zMatrix    = map[string]string{"bt470bg": "470bg", "smpte170m": "170m", "bt2020nc": "2020_ncl", "bt2020c": "2020_cl"}
	zPrimaries = map[string]string{"bt470bg": "470bg", "smpte170m": "170m", "smpte240m": "240m", "bt2020": "2020"}
	zTransfer  = map[string]string{"smpte170m": "601", "bt2020-10": "2020_10", "bt2020-12": "2020_12"}
)

// NeedsColourConversion reports whether an SDR YUV source is tagged with a colour space other than BT.709 (BT.601 from a
// camcorder or a DVD rip, BT.2020 SDR) and so needs converting. Untagged or "unspecified" sources count as BT.709, which is
// what players assume, and RGB sources (screenshots, stills) are left alone.
func NeedsColourConversion(m Metadata) bool {
	if !strings.HasPrefix(m.PixFmt, "yuv") {
		return false
	}
	_, a := zMatrix[m.ColorSpace]
	_, b := zPrimaries[m.ColorPrimaries]
	_, c := zTransfer[m.ColorTransfer]
	return a || b || c
}

func orDefault(table map[string]string, key string) string {
	if v, ok := table[key]; ok {
		return v
	}
	return "709"
}

// ColourFilters is the part of the video filter graph that gets the picture to BT.709 SDR. HDR (PQ or HLG) is tone mapped
// with Hable in linear light through zimg (zscale), the same route ffmpeg's own documentation recommends; the input colour
// description is passed explicitly, so a file whose tags are incomplete still converts. It returns nil for a picture that
// needs no conversion. The result ends in format=yuv420p.
func ColourFilters(m Metadata) []string {
	switch {
	case m.HDR:
		in := "tin=smpte2084"
		if m.HDRFormat == "hlg" {
			in = "tin=arib-std-b67"
		}
		return []string{
			"zscale=" + in + ":min=2020_ncl:pin=2020:rin=tv:t=linear:npl=100",
			"format=gbrpf32le",
			"zscale=p=709",
			"tonemap=tonemap=hable:desat=0",
			"zscale=t=709:m=709:r=tv",
			"format=yuv420p",
		}
	case NeedsColourConversion(m):
		return []string{
			"zscale=min=" + orDefault(zMatrix, m.ColorSpace) + ":pin=" + orDefault(zPrimaries, m.ColorPrimaries) + ":tin=" + orDefault(zTransfer, m.ColorTransfer) +
				":t=709:m=709:p=709:r=tv",
			"format=yuv420p",
		}
	}
	return nil
}

// sdrTags marks the frames BT.709 limited range, so the encoder writes it into the stream whatever the source said (a
// file with no colour tags gets them).
const sdrTags = "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv"

// ProxyArgs makes proxy_720.mp4 from the original: H.264 (High, 8-bit 4:2:0), 720 px on the short side of the upright
// picture (rotation applied by ffmpeg's autorotate and the display matrix removed, so a portrait clip is 720x1280 with no
// rotation flag), a constant frame rate (GOP(fps), a whole number) with a keyframe every GOP frames, which is exactly
// every second, so a variable-rate phone clip seeks as predictably as any other, the index up front for instant playback, SDR BT.709, no metadata (a phone's location tags stay behind), and the audio stream m
// selected, as AAC. A file with no decodable audio gets no audio. The scaling comes before the tone mapping, so the
// expensive float conversion runs on the 720 px picture and not on a 4K one.
func ProxyArgs(input, output string, m Metadata) ([]string, error) {
	if !m.HasVideo || m.Width <= 0 || m.Height <= 0 {
		return nil, errors.New("media: proxy needs a video stream with a known size")
	}
	w, h := ScaledSize(m.Width, m.Height, ProxyShortSide)
	gop := strconv.Itoa(GOP(m.FPS.Value))
	vf := []string{"scale=" + strconv.Itoa(w) + ":" + strconv.Itoa(h) + ":flags=lanczos", "setsar=1"}
	if cf := ColourFilters(m); cf != nil {
		vf = append(vf, cf...) // ends in format=yuv420p
	} else {
		vf = append(vf, "format=yuv420p")
	}
	vf = append(vf, sdrTags)

	a := append(common(), inputOptions()...)
	a = append(a, "-i", input, "-map", "0:"+strconv.Itoa(m.VideoIndex))
	if idx := m.SelectedAudio; idx != nil {
		a = append(a, "-map", "0:"+strconv.Itoa(*idx))
	}
	a = append(a, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		"-vf", strings.Join(vf, ","),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-maxrate", "8M", "-bufsize", "16M",
		"-fps_mode", "cfr", "-r", gop, "-g", gop, "-keyint_min", gop, "-sc_threshold", "0")
	a = append(a, colourTags...)
	if m.SelectedAudio != nil {
		a = append(a, "-c:a", "aac", "-b:a", "128k")
		if s, ok := m.SelectedAudioStream(); ok && s.Channels > 2 {
			a = append(a, "-ac", "2")
		}
	}
	return append(a, "-movflags", "+faststart", output), nil
}

// ---- Poster ------------------------------------------------------------------------------------------------------------

// PosterAtMs is where the poster is taken: a tenth into the clip, and never in the last half second.
func PosterAtMs(durationMs int64) int64 {
	at := durationMs / 10
	if limit := durationMs - 500; at > limit {
		at = limit
	}
	return max(at, 0)
}

// PosterArgs makes poster.jpg from the proxy: a representative frame (ffmpeg's `thumbnail` picks the frame closest to the
// average of the next 24, which skips fades and blur) at atMs.
func PosterArgs(input, output string, atMs int64) []string {
	a := append(common(), inputOptions()...)
	return append(a, "-ss", seconds(atMs), "-i", input, "-map", "0:v:0", "-an",
		"-vf", "thumbnail=24", "-frames:v", "1", "-update", "1", "-q:v", "3", output)
}

// ImagePosterArgs makes poster.jpg from an uploaded picture: the upright picture, at most 720 px on the short side.
func ImagePosterArgs(input, output string, m Metadata) ([]string, error) {
	if !m.HasVideo || m.Width <= 0 || m.Height <= 0 {
		return nil, errors.New("media: poster needs a picture with a known size")
	}
	w, h := ScaledSize(m.Width, m.Height, ProxyShortSide)
	vf := []string{"scale=" + strconv.Itoa(w) + ":" + strconv.Itoa(h) + ":flags=lanczos", "setsar=1"}
	a := append(common(), "-protocol_whitelist", "file", "-format_whitelist", ImageFormats)
	return append(a, "-i", input, "-map", "0:"+strconv.Itoa(m.VideoIndex), "-an", "-map_metadata", "-1",
		"-vf", strings.Join(vf, ","), "-frames:v", "1", "-update", "1", "-q:v", "3", output), nil
}

// ---- Sprite sheet ------------------------------------------------------------------------------------------------------

// SpriteGrid is the layout of a sprite sheet: one tile every IntervalSec seconds, Cols x Rows tiles of TileW x TileH.
type SpriteGrid struct {
	IntervalSec int
	Count       int // tiles that hold a picture
	Cols, Rows  int
	TileW       int
	TileH       int
}

// NewSpriteGrid lays out the sprite sheet of a clip of durationMs whose upright picture is dispW x dispH. One tile per
// second up to SpriteMaxTiles seconds; a longer clip gets a longer interval, so the sheet never has more than 100 tiles.
func NewSpriteGrid(durationMs int64, dispW, dispH int) (SpriteGrid, error) {
	if durationMs <= 0 || dispW <= 0 || dispH <= 0 {
		return SpriteGrid{}, errors.New("media: sprite grid needs a duration and a picture size")
	}
	sec := (durationMs + 999) / 1000
	interval := int(max(1, (sec+SpriteMaxTiles-1)/SpriteMaxTiles))
	count := int(min(max((durationMs+int64(interval)*1000-1)/(int64(interval)*1000), 1), SpriteMaxTiles))
	tw := SpriteTileWidth
	th := max(even(float64(tw)*float64(dispH)/float64(dispW)), 2)
	if th > spriteMaxTileH {
		th = spriteMaxTileH
		tw = max(even(float64(th)*float64(dispW)/float64(dispH)), 2)
	}
	cols := min(count, spriteMaxCols)
	return SpriteGrid{IntervalSec: interval, Count: count, Cols: cols, Rows: (count + cols - 1) / cols, TileW: tw, TileH: th}, nil
}

// Width and Height are the size of the whole sheet.
func (g SpriteGrid) Width() int  { return g.Cols * g.TileW }
func (g SpriteGrid) Height() int { return g.Rows * g.TileH }

// SpriteArgs makes sprite.jpg from the proxy: one frame every g.IntervalSec seconds, scaled to a tile, laid out as a grid.
func SpriteArgs(input, output string, g SpriteGrid) ([]string, error) {
	if g.IntervalSec < 1 || g.Count < 1 || g.Cols < 1 || g.Rows < 1 || g.TileW < 2 || g.TileH < 2 {
		return nil, fmt.Errorf("media: invalid sprite grid %+v", g)
	}
	vf := "fps=1/" + strconv.Itoa(g.IntervalSec) + ",scale=" + strconv.Itoa(g.TileW) + ":" + strconv.Itoa(g.TileH) +
		":flags=bicubic,tile=" + strconv.Itoa(g.Cols) + "x" + strconv.Itoa(g.Rows)
	a := append(common(), inputOptions()...)
	return append(a, "-i", input, "-map", "0:v:0", "-an", "-vf", vf, "-frames:v", "1", "-update", "1", "-q:v", "5", output), nil
}

// SpriteVTT is the WebVTT thumbnails track for a sheet stored under the name sprite: one cue per tile with the tile's
// rectangle as a media fragment (`#xywh=x,y,w,h`). The name is relative, so the player resolves it against wherever it
// fetched the track from, or rewrites it to a presigned URL.
func SpriteVTT(g SpriteGrid, durationMs int64, sprite string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for i := 0; i < g.Count; i++ {
		start := int64(i) * int64(g.IntervalSec) * 1000
		end := min(start+int64(g.IntervalSec)*1000, durationMs)
		if end <= start {
			end = start + 1
		}
		x, y := (i%g.Cols)*g.TileW, (i/g.Cols)*g.TileH
		fmt.Fprintf(&b, "\n%s --> %s\n%s#xywh=%d,%d,%d,%d\n", vttTime(start), vttTime(end), sprite, x, y, g.TileW, g.TileH)
	}
	return b.String()
}

func vttTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// ---- Hover preview -----------------------------------------------------------------------------------------------------

// Segment is a part of the clip, in milliseconds.
type Segment struct {
	StartMs int64
	LenMs   int64
}

// HoverSegments picks the parts of the clip the hover preview shows: four segments of two seconds, each centred in one
// quarter of the clip, so they are spread over it and never overlap. A clip of eight seconds or less is used whole.
func HoverSegments(durationMs int64) []Segment {
	if durationMs <= HoverTotalMs {
		return []Segment{{StartMs: 0, LenMs: max(durationMs, 1)}}
	}
	out := make([]Segment, hoverSegments)
	for i := range out {
		centre := durationMs * int64(2*i+1) / (2 * hoverSegments)
		start := min(max(centre-HoverSegmentMs/2, 0), durationMs-HoverSegmentMs)
		out[i] = Segment{StartMs: start, LenMs: HoverSegmentMs}
	}
	return out
}

// HoverArgs makes hover_preview.mp4 from the proxy: the segments joined, muted, 360 px on the short side, H.264 with a
// bit rate cap that keeps eight seconds under 600 kB, a keyframe every second, the index up front. proxyW and proxyH are the
// proxy's size and fps its frame rate.
func HoverArgs(input, output string, segs []Segment, proxyW, proxyH int, fps float64) ([]string, error) {
	if len(segs) < 1 || len(segs) > hoverSegments {
		return nil, fmt.Errorf("media: hover preview takes 1 to %d segments, got %d", hoverSegments, len(segs))
	}
	w, h := ScaledSize(proxyW, proxyH, HoverShortSide)
	if w == 0 {
		return nil, errors.New("media: hover preview needs the proxy's size")
	}
	gop := strconv.Itoa(GOP(fps))
	scale := "scale=" + strconv.Itoa(w) + ":" + strconv.Itoa(h) + ":flags=lanczos,setsar=1,format=yuv420p"

	a := common()
	a = append(a, inputOptions()...)
	for _, s := range segs {
		if s.StartMs < 0 || s.LenMs < 1 {
			return nil, fmt.Errorf("media: invalid hover segment %+v", s)
		}
		a = append(a, "-ss", seconds(s.StartMs), "-t", seconds(s.LenMs), "-i", input)
	}
	if len(segs) == 1 {
		a = append(a, "-map", "0:v:0", "-vf", scale)
	} else {
		var g strings.Builder
		for i := range segs {
			fmt.Fprintf(&g, "[%d:v:0]setpts=PTS-STARTPTS[v%d];", i, i)
		}
		for i := range segs {
			fmt.Fprintf(&g, "[v%d]", i)
		}
		fmt.Fprintf(&g, "concat=n=%d:v=1:a=0,%s[v]", len(segs), scale)
		a = append(a, "-filter_complex", g.String(), "-map", "[v]")
	}
	a = append(a, "-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		"-c:v", "libx264", "-preset", "medium", "-profile:v", "main", "-pix_fmt", "yuv420p", "-crf", "30",
		"-maxrate", hoverMaxRate, "-bufsize", "1200k",
		"-g", gop, "-keyint_min", gop, "-sc_threshold", "0")
	a = append(a, colourTags...)
	return append(a, "-movflags", "+faststart", output), nil
}

// ---- Audio -------------------------------------------------------------------------------------------------------------

// Audio16kArgs makes audio_16k.wav from the selected audio stream of the original: mono, 16 kHz, 16-bit PCM, for speech
// recognition and for the waveform. It needs a selected stream; the caller skips it for a file without audio.
func Audio16kArgs(input, output string, m Metadata) ([]string, error) {
	if m.SelectedAudio == nil {
		return nil, errors.New("media: no decodable audio stream to extract")
	}
	a := append(common(), inputOptions()...)
	return append(a, "-i", input, "-map", "0:"+strconv.Itoa(*m.SelectedAudio), "-vn", "-sn", "-dn",
		"-map_metadata", "-1", "-map_chapters", "-1",
		"-ac", "1", "-ar", strconv.Itoa(AudioSampleRate), "-c:a", "pcm_s16le", "-f", "wav", output), nil
}

// seconds formats milliseconds as ffmpeg seconds.
func seconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }
