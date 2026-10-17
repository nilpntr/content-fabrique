package media

import (
	"fmt"
	"strconv"
)

// Frames for the editor agent (P4-02a, docs/design/agent-runtime-v1.md section 4): still pictures cut from the proxy, small
// enough to send to a model.

// MaxFrameBatch is the most frames FramesArgs cuts in one run.
const MaxFrameBatch = 12

// FrameSpec is one still frame to cut: the time in the clip and the output file.
type FrameSpec struct {
	AtMs   int64
	Output string
}

// FramesArgs cuts one JPEG per spec from the proxy, each scaled to fit a longSide x longSide box (so longSide pixels on the
// long side, the aspect ratio kept, both sides even). The file is opened once per frame, each time at its own time with a
// fast seek (the proxy has a keyframe every second), and all frames come from one process. The pictures carry no metadata.
func FramesArgs(input string, frames []FrameSpec, longSide int) ([]string, error) {
	if len(frames) < 1 || len(frames) > MaxFrameBatch {
		return nil, fmt.Errorf("media: frames takes 1 to %d frames, got %d", MaxFrameBatch, len(frames))
	}
	if longSide < 16 || longSide > 4096 {
		return nil, fmt.Errorf("media: frame long side %d is outside 16..4096", longSide)
	}
	a := common()
	for _, f := range frames {
		if f.AtMs < 0 || f.Output == "" {
			return nil, fmt.Errorf("media: invalid frame %+v", f)
		}
		a = append(a, inputOptions()...)
		a = append(a, "-ss", seconds(f.AtMs), "-i", input)
	}
	side := strconv.Itoa(longSide)
	vf := "scale=" + side + ":" + side + ":force_original_aspect_ratio=decrease:force_divisible_by=2:flags=lanczos"
	for i, f := range frames {
		a = append(a, "-map", strconv.Itoa(i)+":v:0", "-an", "-sn", "-dn", "-map_metadata", "-1",
			"-vf", vf, "-frames:v", "1", "-update", "1", "-q:v", "4", f.Output)
	}
	return a, nil
}
