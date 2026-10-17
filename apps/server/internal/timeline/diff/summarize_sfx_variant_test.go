package diff

import "testing"

func TestSummarizeSoundEffectVariant(t *testing.T) {
	got := summarize(t, `[{"op":"add","path":"/tracks/2/items/1","value":{"id":"s2","kind":"sfx","sfx":"impact","variant":"deep","at_ms":2800,"export":"licensed"}}]`)
	if len(got) != 1 || got[0] != "added sound s2 (sfx impact deep at 2.8 s)" {
		t.Errorf("got %q", got)
	}
}
