package routing

import (
	"strings"
	"testing"
	"testing/fstest"
)

// An `escalate` entry must go one effort step UP. A stage already at max has nothing above it, so it carries none (v1-start's editor);
// an escalation to the same or a lower effort, to a made-up effort, or without a base effort to compare against is a profile error, not
// a silently ignored line.
func TestEscalationMustGoAboveTheStagesEffort(t *testing.T) {
	fsys := fstest.MapFS{
		"p.yaml": {Data: []byte(`profile: p
ok:       {model: m, effort: medium, escalate: {when: hard_constraints_failed, effort: high, max_attempts: 1}}
top:      {model: m, effort: max}
atmax:    {model: m, effort: max, escalate: {when: hard_constraints_failed, effort: max, max_attempts: 1}}
belowmax: {model: m, effort: max, escalate: {when: hard_constraints_failed, effort: high, max_attempts: 1}}
same:     {model: m, effort: high, escalate: {when: hard_constraints_failed, effort: high}}
bogus:    {model: m, effort: medium, escalate: {effort: turbo}}
noeffort: {model: m, escalate: {effort: high}}
notmap:   {model: m, effort: medium, escalate: high}
`)},
	}
	p, err := LoadProfile(fsys, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ok", "top"} {
		if _, err := p.Entry(s); err != nil {
			t.Errorf("stage %s: %v", s, err)
		}
	}
	for _, s := range []string{"atmax", "belowmax", "same", "bogus", "noeffort", "notmap"} {
		if _, err := p.Entry(s); err == nil || !strings.Contains(err.Error(), "escalate") {
			t.Errorf("stage %s accepted or fails for another reason: %v", s, err)
		}
	}
}

// Every stage of the checked-in profiles resolves (so none has an escalation that goes nowhere); v1-start's standard editor and revision
// are unchanged (medium, and the editor may escalate to high), and its extra-careful entries think at max effort with no escalation.
func TestCheckedInProfilesResolveAndV1StartKeepsStandardAndHasMax(t *testing.T) {
	for _, name := range []string{"dev-cheap", "dev-mid", "v1-start"} {
		prof, err := LoadProfile(repoConfig, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range prof.Stages() {
			if _, err := prof.Entry(s); err != nil {
				t.Errorf("%s %s: %v", name, s, err)
			}
		}
		for _, s := range []string{"editor", "revision", "editor_max", "revision_max"} {
			if _, err := prof.Entry(s); err != nil {
				t.Errorf("%s has no usable %s entry: %v", name, s, err)
			}
		}
	}
	v1, err := LoadProfile(repoConfig, "v1-start")
	if err != nil {
		t.Fatal(err)
	}
	for stage, want := range map[string]struct {
		effort string
		tokens int
	}{"editor": {"medium", 24000}, "revision": {"medium", 12000}, "editor_max": {"max", 32000}, "revision_max": {"max", 16000}} {
		if e, err := v1.Entry(stage); err != nil || e.Effort != want.effort || e.MaxTokens != want.tokens {
			t.Fatalf("v1-start %s = %+v, %v; want effort %s and %d tokens", stage, e, err, want.effort, want.tokens)
		}
	}
}
