package diff

import "testing"

func TestSummarizeSoundEffectsAndLooks(t *testing.T) {
	cases := []struct {
		name, patch string
		want        []string
	}{
		{"added effect", `[{"op":"add","path":"/tracks/2/items/1","value":{"id":"s2","kind":"sfx","sfx":"whoosh","at_ms":2800,"export":"licensed"}}]`,
			[]string{"added sound s2 (sfx whoosh at 2.8 s)"}},
		{"look with match", `[{"op":"add","path":"/tracks/0/items/1/look","value":{"preset":"gym_moody","match":true}}]`,
			[]string{"set the look of c2 to gym_moody, matching exposure"}},
		{"exposure hint", `[{"op":"add","path":"/tracks/0/items/1/exposure_ev","value":-1.5}]`,
			[]string{"set the exposure hint of c2 to -1.5 EV"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(t, tc.patch)
			if len(got) != len(tc.want) || got[0] != tc.want[0] {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
