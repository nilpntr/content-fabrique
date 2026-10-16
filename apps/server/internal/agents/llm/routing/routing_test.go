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
		"dev-mid":   {"claude-sonnet-5-5"},
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
