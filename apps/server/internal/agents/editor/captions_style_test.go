package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// The creator's letter colour and case carry over to the captions (D-36: the creator's settings win), and a creator who set neither gets no
// line about captions.
func TestCaptionsFollowTheCreatorsSettings(t *testing.T) {
	r := styleprofile.Resolved{TitleColour: "#F5F0DC", TitleCase: styleprofile.CaseLower}
	r.Sources.TitleColour, r.Sources.TitleCase = styleprofile.SourceYou, styleprofile.SourceMeasured
	got := captionsText(r, r.Sources)
	for _, want := range []string{"set_captions", "fill #F5F0DC (the creator's choice)", "uppercase false (measured from their videos)", "Never send corrections"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %s", want, got)
		}
	}
	r = styleprofile.Resolved{TitleCase: styleprofile.CaseUpper}
	if got := captionsText(r, r.Sources); !strings.Contains(got, "uppercase true") || strings.Contains(got, "fill") {
		t.Errorf("case only: %s", got)
	}
	if got := captionsText(styleprofile.Resolved{}, styleprofile.Settings{}); got != "" {
		t.Errorf("no settings: %q", got)
	}
}
