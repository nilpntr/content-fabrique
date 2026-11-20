package media

import "testing"

func TestFullDecodeArgsGolden(t *testing.T) {
	a, err := FullDecodeArgs("/in/0", iphonePortrait())
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + inOpts + " -i /in/0 -map 0:0 -an -sn -dn -nostats -stats_period 30 -progress pipe:1 -f null -"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := FullDecodeArgs("/in/0", Metadata{}); err == nil {
		t.Fatal("a file without video must be refused")
	}
}

func TestDecodedMs(t *testing.T) {
	for _, c := range []struct {
		in   string
		ms   int64
		want bool
	}{
		{"frame=0\nout_time_us=N/A\nprogress=continue\n", 0, false},
		{"out_time_us=1000000\nprogress=continue\nframe=90\nout_time_us=2966667\nout_time_ms=2966667\nprogress=end\n", 2966, true},
		{"", 0, false},
		{"out_time_us=-5\n", 0, false},
	} {
		ms, ok := DecodedMs([]byte(c.in))
		if ms != c.ms || ok != c.want {
			t.Errorf("DecodedMs(%q) = %d, %v; want %d, %v", c.in, ms, ok, c.ms, c.want)
		}
	}
}

func TestVariableFrameRate(t *testing.T) {
	fps := func(v float64) FPS { return FPS{Value: v} }
	for _, c := range []struct {
		name string
		m    Metadata
		want bool
	}{
		{"constant 30", Metadata{HasVideo: true, FPS: fps(30)}, false},
		{"constant 29.97", Metadata{HasVideo: true, FPS: fps(29.97)}, false},
		{"phone average 29.13", Metadata{HasVideo: true, FPS: fps(29.13)}, true},
		{"base rate far from the average", Metadata{HasVideo: true, FPS: fps(20), BaseFPS: &FPS{Value: 30}}, true},
		{"slow motion conformed to 30", Metadata{HasVideo: true, FPS: fps(30), BaseFPS: &FPS{Value: 240}}, false},
		{"no video", Metadata{FPS: fps(29.13)}, false},
		{"a time base, not a base rate", Metadata{HasVideo: true, FPS: fps(30), BaseFPS: &FPS{Value: 90000}}, false},
		{"constant 15", Metadata{HasVideo: true, FPS: fps(15)}, false},
		{"unknown rate", Metadata{HasVideo: true}, false},
	} {
		if got := c.m.VariableFrameRate(); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
