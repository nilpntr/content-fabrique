package media

import (
	"strings"
	"testing"
)

func TestStillsArgsGolden(t *testing.T) {
	a, err := StillsArgs("/in/0", []Still{{AtMs: 500, Output: "/work/c00.jpg"}, {AtMs: 2250, Output: "/work/c01.jpg"}}, 512)
	if err != nil {
		t.Fatal(err)
	}
	const wl = "-protocol_whitelist file -format_whitelist " + UntrustedFormats
	vf := "scale=512:512:force_original_aspect_ratio=decrease:force_divisible_by=2:flags=lanczos"
	want := "ffmpeg -nostdin -hide_banner -v error -y " +
		wl + " -ss 0.500 -i /in/0 " + wl + " -ss 2.250 -i /in/0 " +
		"-map 0:v:0 -an -sn -dn -map_metadata -1 -vf " + vf + " -frames:v 1 -update 1 -q:v 4 /work/c00.jpg " +
		"-map 1:v:0 -an -sn -dn -map_metadata -1 -vf " + vf + " -frames:v 1 -update 1 -q:v 4 /work/c01.jpg"
	if got := strings.Join(a, " "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestStillsArgsLimits(t *testing.T) {
	ok := []Still{{AtMs: 0, Output: "/work/a.jpg"}}
	if _, err := StillsArgs("/in/0", nil, 512); err == nil {
		t.Error("no stills accepted")
	}
	if _, err := StillsArgs("/in/0", make([]Still, MaxStills+1), 512); err == nil {
		t.Error("too many stills accepted")
	}
	for _, side := range []int{0, 15, 4097} {
		if _, err := StillsArgs("/in/0", ok, side); err == nil {
			t.Errorf("long side %d accepted", side)
		}
	}
	for _, bad := range []Still{{AtMs: -1, Output: "/work/a.jpg"}, {AtMs: 0, Output: ""}} {
		if _, err := StillsArgs("/in/0", []Still{bad}, 512); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
