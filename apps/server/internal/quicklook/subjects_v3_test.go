package quicklook

import (
	"encoding/json"
	"testing"
)

// Boxes read before quicklook.v3 have no head of their own: cfctl assets quicklook --subjects reads them again.
func TestSubjectsCurrent(t *testing.T) {
	for detail, want := range map[string]bool{
		`{"prompt_version":"quicklook.v2","subjects_read":true,"subjects":{"aspect":0.56,"samples":[]}}`:   false,
		`{"prompt_version":"quicklook.v2","subjects_read":true,"subjects_prompt":"` + PromptVersion + `"}`: true,
		`{"prompt_version":"` + PromptVersion + `","subjects_read":true}`:                                  true,
		`{"prompt_version":"` + PromptVersion + `"}`:                                                       false,
		`not json`: false,
	} {
		if got := SubjectsCurrent(json.RawMessage(detail)); got != want {
			t.Errorf("%s: %v", detail, got)
		}
	}
}
