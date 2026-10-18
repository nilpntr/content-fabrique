package media

import (
	"encoding/json"
	"testing"
)

// Slow motion is told from the frame rate. The founder's hammer-curl clip (2026-10-18) was shot at 120 fps and re-encoded by an app to
// play at 30: its average rate is 30.06, and only the base rate (r_frame_rate 120/1) still says how it was shot. An iPhone clip at 30 is
// not slow motion, though it carries com.apple.quicktime.full-frame-rate-playback-intent=0 like every iPhone clip; a time base of
// 90000/1 is not a frame rate.
func TestSlowMotionFromTheFrameRate(t *testing.T) {
	probe := func(r, avg string) []byte {
		return []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","width":1080,"height":1920,"r_frame_rate":"` + r +
			`","avg_frame_rate":"` + avg + `"}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"28.65","tags":{"com.apple.quicktime.full-frame-rate-playback-intent":"0"}}}`)
	}
	for _, c := range []struct {
		name, r, avg string
		slow         bool
	}{
		{"re-encoded 120 fps", "120/1", "21525/716", true},
		{"iPhone slow-mo original", "240/1", "240/1", true},
		{"iPhone 30 fps", "30/1", "1064100/35471", false},
		{"60 fps", "60/1", "60/1", false},
		{"a time base", "90000/1", "30/1", false},
	} {
		m, err := Normalise(probe(c.r, c.avg))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if m.SlowMotion() != c.slow {
			t.Errorf("%s: SlowMotion = %v (fps %v, base %+v)", c.name, m.SlowMotion(), m.FPS.Value, m.BaseFPS)
		}
		stored, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if SlowMotionProbe(stored) != c.slow {
			t.Errorf("%s: SlowMotionProbe of the stored probe = %v", c.name, !c.slow)
		}
	}
	if SlowMotionProbe(nil) || SlowMotionProbe([]byte(`{"fps":{"value":30}}`)) || !SlowMotionProbe([]byte(`{"fps":{"value":239.8}}`)) {
		t.Error("SlowMotionProbe of hand-written probes")
	}
}
