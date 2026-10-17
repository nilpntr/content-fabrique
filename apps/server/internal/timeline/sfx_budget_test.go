package timeline

import (
	"fmt"
	"strings"
	"testing"
)

// The effects of a spec have a budget: they are part of the render's filter graph, which is one process argument. A spec over it is
// refused with a message the editor can act on, and a healthy edit is far under it.
func TestSoundEffectBudget(t *testing.T) {
	spec := func(n int, name string) *Spec {
		var items []AudioItem
		for i := range n {
			items = append(items, sfxAt(fmt.Sprintf("s%d", i+1), name, int64(i)*10))
		}
		return addTrack(baseSpec(), audioTrack(items...))
	}
	// 12 effects of the largest kind (a riser, 6 kB): 72 kB, inside.
	wantCodes(t, validateSpec(t, spec(12, SFXRiser)))
	// 20 risers: 120 kB, over.
	issues := validateSpec(t, spec(20, SFXRiser))
	wantCodes(t, issues, CodeTooManyEffects)
	for _, i := range issues.Issues {
		if i.Code == CodeTooManyEffects && (!strings.Contains(i.Message, "20 sound effects") || !strings.Contains(i.Message, "remove_sound")) {
			t.Errorf("message: %s", i.Message)
		}
	}
	// Small ones go further: 80 clicks are 168 kB, 45 are 94 kB.
	wantCodes(t, validateSpec(t, spec(45, SFXClick)))
	wantCodes(t, validateSpec(t, spec(80, SFXClick)), CodeTooManyEffects)
	// Every category has a cost, and a name that is not one has none.
	for _, c := range SFXCategories() {
		if SFXGraphBytes(c) <= 0 {
			t.Errorf("%s has no graph cost", c)
		}
	}
	if SFXGraphBytes("boom") != 0 {
		t.Error("boom has a cost")
	}
}
