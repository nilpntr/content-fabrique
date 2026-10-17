package media

import (
	"fmt"
	"strconv"
)

// The signals pass (P2-02/P2-03, D-10): one ffmpeg run over the proxy that writes, at a fixed sampling rate, everything the
// deterministic analysis (package analysis) needs. Three branches share one decode:
//
//   - thumbs.raw: a ThumbSize x ThumbSize grey thumbnail per sample (rawvideo, no header). Motion, exposure and the rep
//     segmentation are computed from these in Go.
//   - sharp.txt: the mean of an edge map per sample (edgedetect + signalstats), a sharpness proxy that drops when a frame is
//     blurred. blurdetect would also do, but edge energy is available in every ffmpeg the product has run on.
//   - scene.txt: scdet's scene-change score per sample.
//
// The file names are relative on purpose: the sandbox's writable mount is the working directory of the tool, and the metadata
// filter takes a plain file name (a path would need its colons and backslashes escaped).

// Analysis constants.
const (
	// AnalysisFPS is how many samples per second the pass takes; AnalysisStepMs is the spacing of the samples.
	AnalysisFPS    = 10
	AnalysisStepMs = 1000 / AnalysisFPS
	// ThumbSize is the side of a thumbnail in pixels: ThumbPixels bytes per sample.
	ThumbSize   = 32
	ThumbPixels = ThumbSize * ThumbSize
	// MaxAnalysisSamples bounds what a run may produce (30 minutes at AnalysisFPS plus slack); the caller checks the file sizes.
	MaxAnalysisSamples = 30*60*AnalysisFPS + 100
)

// The output files of AnalysisArgs, in the sandbox's working directory.
const (
	AnalysisThumbs = "thumbs.raw"
	AnalysisSharp  = "sharp.txt"
	AnalysisScene  = "scene.txt"
)

// AnalysisArgs writes the three signal files of the proxy at input into the working directory (see OutputPath).
func AnalysisArgs(input string) []string {
	graph := "[0:v]fps=" + strconv.Itoa(AnalysisFPS) + ",split=3[a][b][c];" +
		fmt.Sprintf("[a]scale=%d:%d:flags=area,format=gray[t];", ThumbSize, ThumbSize) +
		"[b]scale=360:-2:flags=area,format=yuv420p,edgedetect=low=0.08:high=0.2,signalstats," +
		"metadata=mode=print:key=lavfi.signalstats.YAVG:file=" + AnalysisSharp + "[s];" +
		"[c]scale=180:-2:flags=area,scdet=threshold=8:sc_pass=0," +
		"metadata=mode=print:key=lavfi.scd.score:file=" + AnalysisScene + "[d]"
	a := append(common(), inputOptions()...)
	a = append(a, "-i", input, "-filter_complex", graph,
		"-map", "[t]", "-an", "-sn", "-dn", "-f", "rawvideo", "-pix_fmt", "gray", OutputPath(AnalysisThumbs),
		"-map", "[s]", "-an", "-f", "null", "-",
		"-map", "[d]", "-an", "-f", "null", "-")
	return a
}
