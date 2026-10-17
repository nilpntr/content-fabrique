package routing

import (
	"testing"
	"testing/fstest"
)

func TestEntry(t *testing.T) {
	v1, err := LoadProfile(repoConfig, "v1-start")
	if err != nil {
		t.Fatal(err)
	}
	cheap, err := LoadProfile(repoConfig, "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		p     Profile
		stage string
		want  Entry
	}{
		{v1, "editor", Entry{Model: "claude-opus-5-5", Effort: "medium"}},
		{cheap, "editor", Entry{Model: "claude-haiku-5-5", Effort: "medium"}},
		{v1, "research.lead", Entry{Model: "claude-opus-5-5", Effort: "medium"}},
		{v1, "research.workers", Entry{Model: "claude-haiku-5-5"}},
		{v1, "tagging", Entry{Model: "claude-haiku-5-5", Batch: true}},
		{v1, "judge", Entry{Model: "claude-opus-5-5", Effort: "high", Batch: true}},
	}
	for _, c := range cases {
		got, err := c.p.Entry(c.stage)
		if err != nil || got != c.want {
			t.Errorf("%s %s = %+v, %v; want %+v", c.p.Name, c.stage, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "nope", "editor.nope", "research", "editor.model", "a..b"} {
		if _, err := v1.Entry(bad); err == nil {
			t.Errorf("Entry(%q) succeeded", bad)
		}
	}
}

func TestEntryValidatesEffortAndMaxTokens(t *testing.T) {
	fsys := fstest.MapFS{
		"p.yaml": {Data: []byte("profile: p\na: {model: m, effort: max, max_tokens: 4096}\nb: {model: m, effort: turbo}\nc: {model: m, max_tokens: 0}\nd: {model: m, max_tokens: lots}\n")},
	}
	p, err := LoadProfile(fsys, "p")
	if err != nil {
		t.Fatal(err)
	}
	if e, err := p.Entry("a"); err != nil || e.MaxTokens != 4096 || e.Effort != "max" {
		t.Fatalf("a = %+v, %v", e, err)
	}
	for _, s := range []string{"b", "c", "d"} {
		if _, err := p.Entry(s); err == nil {
			t.Errorf("stage %s accepted", s)
		}
	}
	if _, err := (Profile{}).Entry("a"); err == nil {
		t.Error("zero Profile resolved a stage")
	}
}
