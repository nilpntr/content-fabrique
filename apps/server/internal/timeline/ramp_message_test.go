package timeline

import (
	"strings"
	"testing"
)

func TestShotTooShortOnARampDoesNotSuggestSetSpeed(t *testing.T) {
	c := clipAt("c1", 0, 1196, 0) // 299 ms at 4x
	c.SpeedRamp = []SpeedPoint{{AtMs: 0, Speed: 4}, {AtMs: 600, Speed: 4}}
	res := validateSpec(t, withClips(baseSpec(), c, clipAt("c2", 0, 5000, 299)))
	wantCodes(t, res, CodeShotTooShort)
	msg := res.Issues[0].Message
	for _, want := range []string{"speed ramp", "ramp points", "replace the ramp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "at speed") {
		t.Errorf("message %q names a constant speed", msg)
	}
}
