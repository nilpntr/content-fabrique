package media

import (
	"fmt"
	"strconv"
)

// Stills: several JPEG pictures cut from one video in one ffmpeg process, for the quick look (P1-04b) and anything else that
// shows a model a few frames of a clip. The command builder is a pure function like the rest of the package and is golden
// tested; the caller runs it through a Runner.

// MaxStills is the most pictures StillsArgs cuts in one run.
const MaxStills = 16

// Still is one picture to cut: the time in the clip and the output file.
type Still struct {
	AtMs   int64
	Output string
}

// StillsArgs cuts one JPEG per still from the input, each scaled to fit a longSide x longSide box (so longSide pixels on the
// long side, the aspect ratio kept, both sides even; a smaller picture is scaled up as well, for a uniform cost per image).
// The input is opened once per picture, each time at its own time with an accurate seek, and all pictures come from one
// process. The pictures carry no metadata. The input is expected to be a derivative we made (the proxy), but the whitelist of
// untrusted inputs applies all the same.
func StillsArgs(input string, stills []Still, longSide int) ([]string, error) {
	if len(stills) < 1 || len(stills) > MaxStills {
		return nil, fmt.Errorf("media: stills takes 1 to %d pictures, got %d", MaxStills, len(stills))
	}
	if longSide < 16 || longSide > 4096 {
		return nil, fmt.Errorf("media: still long side %d is outside 16..4096", longSide)
	}
	a := common()
	for _, s := range stills {
		if s.AtMs < 0 || s.Output == "" {
			return nil, fmt.Errorf("media: invalid still %+v", s)
		}
		a = append(a, inputOptions()...)
		a = append(a, "-ss", seconds(s.AtMs), "-i", input)
	}
	side := strconv.Itoa(longSide)
	vf := "scale=" + side + ":" + side + ":force_original_aspect_ratio=decrease:force_divisible_by=2:flags=lanczos"
	for i, s := range stills {
		a = append(a, "-map", strconv.Itoa(i)+":v:0", "-an", "-sn", "-dn", "-map_metadata", "-1",
			"-vf", vf, "-frames:v", "1", "-update", "1", "-q:v", "4", s.Output)
	}
	return a, nil
}
