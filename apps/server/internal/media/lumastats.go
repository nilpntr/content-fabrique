package media

import "strconv"

// The luma statistics of a rendered video (check_edit's rendered-brightness checks): one ffmpeg run over the preview render that
// writes, at a fixed rate, the mean luma (YAVG) and the 90th percentile luma (YHIGH) of each sampled frame with the signalstats
// filter. The file name is relative on purpose, like the analysis files: the sandbox's writable mount is the working directory.

// Constants of the luma statistics pass.
const (
	// LumaStatsFPS is how many frames per second are sampled, and LumaStatsStepMs the spacing of the samples.
	LumaStatsFPS    = 5
	LumaStatsStepMs = 1000 / LumaStatsFPS
	// LumaStatsFile is the output file, in the sandbox's working directory.
	LumaStatsFile = "luma_stats.txt"
	// MaxLumaSamples bounds what a run may produce (30 minutes at LumaStatsFPS plus slack).
	MaxLumaSamples = 30*60*LumaStatsFPS + 100
)

// LumaStatsArgs writes the signalstats of the video at input into LumaStatsFile in the working directory. The picture is scaled
// down first (the statistics of a 270 px wide frame are those of the full one, and the pass is cheap).
func LumaStatsArgs(input string) []string {
	graph := "fps=" + strconv.Itoa(LumaStatsFPS) + ",scale=270:-2:flags=area,format=yuv420p,signalstats," +
		"metadata=mode=print:file=" + LumaStatsFile
	a := append(common(), inputOptions()...)
	return append(a, "-i", input, "-vf", graph, "-an", "-sn", "-dn", "-f", "null", "-")
}
