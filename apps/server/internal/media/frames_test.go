package media

import (
	"strings"
	"testing"
)

func TestFramesArgsGolden(t *testing.T) {
	a, err := FramesArgs("/in/0", []FrameSpec{{AtMs: 0, Output: "/work/f0.jpg"}, {AtMs: 2500, Output: "/work/f1.jpg"}}, 384)
	if err != nil {
		t.Fatal(err)
	}
	vf := "scale=384:384:force_original_aspect_ratio=decrease:force_divisible_by=2:flags=lanczos"
	want := prefix + inOpts + " -ss 0.000 -i /in/0 " + inOpts + " -ss 2.500 -i /in/0 " +
		"-map 0:v:0 -an -sn -dn -map_metadata -1 -vf " + vf + " -frames:v 1 -update 1 -q:v 4 /work/f0.jpg " +
		"-map 1:v:0 -an -sn -dn -map_metadata -1 -vf " + vf + " -frames:v 1 -update 1 -q:v 4 /work/f1.jpg"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestFramesArgsRefusesBadInput(t *testing.T) {
	ok := []FrameSpec{{AtMs: 1, Output: "/work/a.jpg"}}
	tooMany := make([]FrameSpec, MaxFrameBatch+1)
	for i := range tooMany {
		tooMany[i] = FrameSpec{AtMs: int64(i), Output: "/work/x.jpg"}
	}
	for name, tc := range map[string]struct {
		frames []FrameSpec
		side   int
	}{
		"none":       {nil, 384},
		"too many":   {tooMany, 384},
		"negative":   {[]FrameSpec{{AtMs: -1, Output: "/work/a.jpg"}}, 384},
		"no output":  {[]FrameSpec{{AtMs: 1}}, 384},
		"tiny side":  {ok, 8},
		"huge side":  {ok, 9000},
		"zero side":  {ok, 0},
		"neg. frame": {[]FrameSpec{{AtMs: -5, Output: "/work/a.jpg"}}, 384},
	} {
		if _, err := FramesArgs("/in/0", tc.frames, tc.side); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if a, err := FramesArgs("/in/0", tooMany[:MaxFrameBatch], 384); err != nil || !strings.Contains(join(a), "-map 11:v:0") {
		t.Fatalf("a full batch must work: %v", err)
	}
}
