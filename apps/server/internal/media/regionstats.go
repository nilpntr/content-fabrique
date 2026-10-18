package media

import (
	"fmt"
	"strconv"
	"strings"
)

// The luma statistics of regions of single frames of a rendered video: check_edit's overlay legibility check measures the picture
// behind a motion-graphics overlay (D-35) on a frame where the overlay is not drawn. One ffmpeg run over the preview, one branch per
// region: the frame is selected by its number, cropped to the region, and signalstats writes its 10th and 90th percentile and mean luma
// into RegionStatsFile(i) in the working directory.

// MaxRegions bounds the regions of one run.
const MaxRegions = 32

// RegionCrop is a region of one frame: the frame number and the rectangle as fractions of the frame (0 to 1).
type RegionCrop struct {
	Frame      int64
	X, Y, W, H float64
}

// RegionStatsFile is the output file of region i, in the sandbox's working directory.
func RegionStatsFile(i int) string { return "region_" + strconv.Itoa(i) + ".txt" }

// RegionStatsArgs measures the regions of the video at input. Every number in the graph is formatted here; nothing else reaches it.
func RegionStatsArgs(input string, regions []RegionCrop) ([]string, error) {
	if len(regions) == 0 || len(regions) > MaxRegions {
		return nil, fmt.Errorf("media: %d regions, want 1 to %d", len(regions), MaxRegions)
	}
	frac := func(v float64) string { return strconv.FormatFloat(min(1, max(0, v)), 'f', 4, 64) }
	var g strings.Builder
	g.WriteString("[0:v]split=" + strconv.Itoa(len(regions)))
	for i := range regions {
		g.WriteString("[s" + strconv.Itoa(i) + "]")
	}
	for i, r := range regions {
		if r.Frame < 0 || r.W <= 0 || r.H <= 0 {
			return nil, fmt.Errorf("media: region %d is empty or before the first frame", i)
		}
		fmt.Fprintf(&g, ";[s%d]select='eq(n\\,%d)',crop=w='iw*%s':h='ih*%s':x='iw*%s':y='ih*%s',format=yuv420p,signalstats,metadata=mode=print:file=%s[o%d]",
			i, r.Frame, frac(r.W), frac(r.H), frac(r.X), frac(r.Y), RegionStatsFile(i), i)
	}
	a := append(common(), inputOptions()...)
	a = append(a, "-i", input, "-filter_complex", g.String())
	for i := range regions {
		a = append(a, "-map", "[o"+strconv.Itoa(i)+"]", "-f", "null", "-")
	}
	return a, nil
}
