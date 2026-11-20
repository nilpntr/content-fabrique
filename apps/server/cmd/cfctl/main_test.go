package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The research commands that need no database: the allow-list, and the refusals that come before any connection.
func TestResearchCommands(t *testing.T) {
	lookup := func(k string) (string, bool) { return "", false }
	var out, errb bytes.Buffer
	if code := cmdResearch([]string{"sources"}, &out, &errb, lookup, defaultAgentDeps()); code != 0 {
		t.Fatalf("sources = %d, %s", code, errb.String())
	}
	for _, want := range []string{"oembed", "watch_sheet", "web", "ig_business_discovery", "youtube_api", "creator_oauth", "kept 30 days", "kept 365 days"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sources output lacks %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"ensembledata", "tikapi", "apify"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("sources output has %q", unwanted)
		}
	}
	// The retention printed is the one in the env file, the same source `sweep` reads.
	envFile := filepath.Join(t.TempDir(), "research.env")
	if err := os.WriteFile(envFile, []byte("RESEARCH_RETENTION_DAYS=90\nRESEARCH_YOUTUBE_RETENTION_DAYS=7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := cmdResearch([]string{"sources", "--env-file", envFile}, &out, &errb, lookup, defaultAgentDeps()); code != 0 ||
		!strings.Contains(out.String(), "kept 90 days") || !strings.Contains(out.String(), "kept 7 days") || strings.Contains(out.String(), "kept 365 days") {
		t.Fatalf("sources with an env file: %d\n%s%s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	bad := func(retention string) (string, bool) {
		if retention == "RESEARCH_RETENTION_DAYS" {
			return "0", true
		}
		return "", false
	}
	out.Reset()
	errb.Reset()
	if code := cmdResearch([]string{"sources"}, &out, &errb, bad, defaultAgentDeps()); code != 2 || !strings.Contains(errb.String(), "RESEARCH_RETENTION_DAYS") {
		t.Fatalf("a bad retention: %d %s", code, errb.String())
	}
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"nope"}, 2},
		{[]string{"forget"}, 2},
		{[]string{"forget", "has space"}, 2},
		{[]string{"forget", "gino", "--platform", "myspace"}, 2},
		{[]string{"forget", "gino", "extra"}, 2},
		{[]string{"sweep", "extra"}, 2},
		{[]string{"forget", "-lead"}, 2},
		{[]string{"sources", "--env-file", "/does/not/exist.env"}, 2},
	} {
		out.Reset()
		errb.Reset()
		if code := cmdResearch(c.args, &out, &errb, lookup, defaultAgentDeps()); code != c.code {
			t.Errorf("cmdResearch(%v) = %d, %s", c.args, code, errb.String())
		}
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "cfctl ", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{nil, 2, "", "Usage:"},
		{[]string{"nope"}, 2, "", `unknown command "nope"`},
	}
	for _, tc := range tests {
		var out, errb bytes.Buffer
		code := run(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String(), tc.stdout) || !strings.Contains(errb.String(), tc.stderr) {
			t.Errorf("run(%v) = %d, stdout %q, stderr %q", tc.args, code, out.String(), errb.String())
		}
	}
}
