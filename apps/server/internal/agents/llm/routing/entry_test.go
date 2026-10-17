package routing

import (
	"errors"
	"slices"
	"strings"
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
		{v1, "editor", Entry{Model: "claude-opus-5-5", Effort: "medium", MaxTokens: 24000}},
		{cheap, "editor", Entry{Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 24000}},
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

// A stage marked `enabled: false` (or under one) is not active: it resolves to a typed error, and its model is left out
// of Models(), which is the set `cfctl doctor` checks. The two views must stay the same set.
func TestDisabledStagesAndTheDoctorSet(t *testing.T) {
	v1, err := LoadProfile(repoConfig, "v1-start")
	if err != nil {
		t.Fatal(err)
	}
	_, err = v1.Entry("editor.subagents.inspector")
	var de *DisabledError
	if !errors.Is(err, ErrStageDisabled) || !errors.As(err, &de) || de.Stage != "editor.subagents.inspector" || de.Profile != "v1-start" {
		t.Fatalf("err = %#v", err)
	}
	if _, err := v1.Entry("editor.subagents"); err == nil || errors.Is(err, ErrStageDisabled) {
		t.Errorf("editor.subagents (a group with no model) = %v", err)
	}

	// Disabled at any depth, including a whole group.
	fsys := fstest.MapFS{"p.yaml": {Data: []byte(
		"profile: p\n" +
			"a: {model: m1}\n" +
			"group: {enabled: false, inner: {model: m2}, other: {model: m3}}\n" +
			"b: {model: m4, subagents: {x: {enabled: false, model: m5}, y: {model: m6}}}\n")}}
	p, err := LoadProfile(fsys, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Entry("group.inner"); !errors.Is(err, ErrStageDisabled) {
		t.Errorf("group.inner = %v", err)
	}
	if got := strings.Join(p.Stages(), ","); got != "a,b,b.subagents.y" {
		t.Errorf("stages = %s", got)
	}

	// Every checked-in profile: the models of its resolvable stages are exactly the models doctor checks.
	for _, name := range []string{"dev-cheap", "dev-mid", "v1-start"} {
		prof, err := LoadProfile(repoConfig, name)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, s := range prof.Stages() {
			e, err := prof.Entry(s)
			if err != nil {
				t.Errorf("%s: stage %s listed but does not resolve: %v", name, s, err)
				continue
			}
			set[e.Model] = true
		}
		var got []string
		for m := range set {
			got = append(got, m)
		}
		slices.Sort(got)
		if !slices.Equal(got, prof.Models()) {
			t.Errorf("%s: resolvable stages use %v but doctor checks %v", name, got, prof.Models())
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
