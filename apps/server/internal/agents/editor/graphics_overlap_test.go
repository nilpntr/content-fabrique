package editor

import (
	"fmt"
	"strings"
	"testing"
)

// graphics_overlap says what motion.Layout does when two overlays meet: a counter on screen with a chapter card in the same band (the
// demo's "3. M12 REPS ROW") is stacked below it, a note; with no room to stack and no free time it is left out, a failure with the times
// that would fit; apart, nothing to say.
func TestGraphicsOverlap(t *testing.T) {
	card := `{"id":"g1","template":"chapter_card","at_ms":0,"duration_ms":%s,"anchor":"upper_third","params":{"text":"MACHINE ROW","number":3}},
		{"id":"g2","template":"counter","at_ms":%s,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}}`
	spec := graphicsSpec(t, fmt.Sprintf(card, "2900", "600"))
	c := runGraphicsChecks(t, CheckInput{Spec: spec})[CheckGraphicsOverlap]
	if c.Status != CheckPass || !strings.Contains(c.Detail, "g2 is stacked below g1") {
		t.Fatalf("stacked: %+v", c)
	}

	// The person's head high in the frame leaves room for one overlay above it, and the card fills the video.
	full := graphicsSpec(t, fmt.Sprintf(card, "8000", "600"))
	c = runGraphicsChecks(t, CheckInput{Spec: full, Subjects: subjects(0.24, 0.23)})[CheckGraphicsOverlap]
	if c.Status != CheckFail || !strings.Contains(c.Detail, `g2 (counter "12 REPS")`) || !strings.Contains(c.Detail, `g1 (chapter_card "3. MACHINE ROW")`) ||
		!strings.Contains(c.Detail, "leaves it out") || !strings.Contains(c.Detail, "fewer words or a lower scale") {
		t.Fatalf("left out: %+v", c)
	}

	apart := graphicsSpec(t, fmt.Sprintf(card, "2500", "2500"))
	if c := runGraphicsChecks(t, CheckInput{Spec: apart})[CheckGraphicsOverlap]; c.Status != CheckPass || !strings.Contains(c.Detail, "none over another") {
		t.Fatalf("apart: %+v", c)
	}
}
