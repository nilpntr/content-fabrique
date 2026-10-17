package media

import "strconv"

// The style pass (P6-05, internal/styleprofile): one ffmpeg run over a finished video that a creator posted, which writes three things
// at fixed rates from one decode:
//
//   - style_scene.txt: the scene-change score of every frame at StyleSceneFPS (the cut detector reads it);
//   - style_stats.txt: signalstats (luma and saturation statistics) of a frame every StyleStatsStepMs;
//   - style_thumbs.raw: a StyleThumbSize x StyleThumbSize grey thumbnail every StyleThumbStepMs (rawvideo, no header), from which
//     the camera motion, the zoom and the vignette proxies are computed in Go.
//
// The file names are relative on purpose, as in the analysis pass: the sandbox's writable mount is the working directory.

// Constants of the style pass.
const (
	StyleSceneFPS    = 30
	StyleStatsFPS    = 4
	StyleStatsStepMs = 1000 / StyleStatsFPS
	StyleThumbFPS    = 10
	StyleThumbStepMs = 1000 / StyleThumbFPS
	StyleThumbSize   = 48
	StyleThumbPixels = StyleThumbSize * StyleThumbSize
	// MaxStyleSeconds bounds a reference video: a posted short is a few minutes at the most.
	MaxStyleSeconds = 600
	// MaxStyleThumbs bounds the thumbnail file.
	MaxStyleThumbs = MaxStyleSeconds*StyleThumbFPS + 100
)

// The output files of StyleArgs, in the sandbox's working directory.
const (
	StyleSceneFile  = "style_scene.txt"
	StyleStatsFile  = "style_stats.txt"
	StyleThumbsFile = "style_thumbs.raw"
)

// StyleAudioFile is the mono 8 kHz float dump of the sound that the spectral measures read (StyleAudioArgs).
const StyleAudioFile = "style_audio.f32"

// StyleAudioArgs dumps the sound of the video at input as mono 8 kHz little-endian float32 into StyleAudioFile in the working
// directory. A video with no audio stream makes ffmpeg exit with an error, which the caller treats as "no sound".
func StyleAudioArgs(input string) []string {
	a := append(common(), inputOptions()...)
	return append(a, "-t", strconv.Itoa(MaxStyleSeconds), "-i", input, "-vn", "-sn", "-dn", "-ac", "1", "-ar", "8000", "-f", "f32le", OutputPath(StyleAudioFile))
}

// StyleArgs is the style pass over the video at input. It reads only the first MaxStyleSeconds seconds.
func StyleArgs(input string) []string {
	graph := "[0:v]split=3[a][b][c];" +
		"[a]fps=" + strconv.Itoa(StyleSceneFPS) + ",scale=270:-2:flags=area,select=gte(scene\\,0),metadata=mode=print:key=lavfi.scene_score:file=" + StyleSceneFile + "[s];" +
		"[b]fps=" + strconv.Itoa(StyleStatsFPS) + ",scale=270:-2:flags=area,format=yuv420p,signalstats,metadata=mode=print:file=" + StyleStatsFile + "[t];" +
		"[c]fps=" + strconv.Itoa(StyleThumbFPS) + ",scale=" + strconv.Itoa(StyleThumbSize) + ":" + strconv.Itoa(StyleThumbSize) + ":flags=area,format=gray[u]"
	a := append(common(), inputOptions()...)
	return append(a, "-t", strconv.Itoa(MaxStyleSeconds), "-i", input, "-filter_complex", graph,
		"-map", "[s]", "-an", "-f", "null", "-",
		"-map", "[t]", "-an", "-f", "null", "-",
		"-map", "[u]", "-an", "-f", "rawvideo", "-pix_fmt", "gray", OutputPath(StyleThumbsFile))
}
