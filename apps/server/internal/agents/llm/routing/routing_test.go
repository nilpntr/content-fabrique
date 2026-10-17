package routing

import (
	"os"
	"slices"
	"testing"
	"testing/fstest"
)

// The checked-in routing directory.
var repoConfig = os.DirFS("../../../../config/routing")

func TestRepoProfiles(t *testing.T) {
	cat, err := LoadCatalog(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat["claude-opus-5-5"].Cloudflare; got != "anthropic/claude-opus-5.5" {
		t.Errorf("opus cloudflare name = %q", got)
	}
	tests := map[string][]string{
		"dev-cheap": {"claude-haiku-5-5"},
		"dev-mid":   {"claude-haiku-5-5", "claude-sonnet-5-5"}, // haiku for the quick look (P1-04b)
		"v1-start":  {"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5"},
	}
	for name, want := range tests {
		p, err := LoadProfile(repoConfig, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(p.Models(), want) {
			t.Errorf("%s models = %v, want %v", name, p.Models(), want)
		}
		for _, m := range p.Models() {
			if _, ok := cat[m]; !ok {
				t.Errorf("%s uses %s, which models.yaml does not list", name, m)
			}
		}
	}
}

func TestLoadProfileErrors(t *testing.T) {
	fsys := fstest.MapFS{
		"wrong.yaml": {Data: []byte("profile: other\neditor: {model: a}\n")},
		"empty.yaml": {Data: []byte("profile: empty\neditor: {effort: low}\n")},
		"list.yaml":  {Data: []byte("- a\n")},
		"skip.yaml":  {Data: []byte("profile: skip\nx: {model: a}\ny: {enabled: false, model: b}\n")},
	}
	for _, name := range []string{"missing", "wrong", "empty", "list", "../x", "Bad"} {
		if _, err := LoadProfile(fsys, name); err == nil {
			t.Errorf("LoadProfile(%q) succeeded, want an error", name)
		}
	}
	p, err := LoadProfile(fsys, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Models(), []string{"a"}) {
		t.Errorf("disabled block not skipped: %v", p.Models())
	}
}

func TestLoadCatalogErrors(t *testing.T) {
	if _, err := LoadCatalog(fstest.MapFS{}); err == nil {
		t.Error("missing models.yaml accepted")
	}
	if _, err := LoadCatalog(fstest.MapFS{"models.yaml": {Data: []byte("models:\n  a: {}\n")}}); err == nil {
		t.Error("entry without a cloudflare name accepted")
	}
}

func TestEntryMinOutputTokens(t *testing.T) {
	fsys := fstest.MapFS{"p.yaml": {Data: []byte("profile: p\neditor: { model: claude-opus-5-5, max_tokens: 24000, min_output_tokens: 4096 }\nrevision: { model: claude-opus-5-5, max_tokens: 12000 }\nbad: { model: claude-opus-5-5, max_tokens: 1000, min_output_tokens: 2000 }\nzero: { model: claude-opus-5-5, min_output_tokens: 0 }\n")}}
	p, err := LoadProfile(fsys, "p")
	if err != nil {
		t.Fatal(err)
	}
	if e, err := p.Entry("editor"); err != nil || e.MinOutputTokens != 4096 || e.MaxTokens != 24000 {
		t.Fatalf("editor = %+v, %v", e, err)
	}
	if e, err := p.Entry("revision"); err != nil || e.MinOutputTokens != 0 {
		t.Fatalf("revision = %+v, %v", e, err)
	}
	for _, stage := range []string{"bad", "zero"} {
		if _, err := p.Entry(stage); err == nil {
			t.Errorf("stage %s: an invalid min_output_tokens was accepted", stage)
		}
	}
}
