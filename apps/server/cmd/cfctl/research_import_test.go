package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The watch-sheet commands without a database and without the network: the template, the dry run over the recorded sheet, and the
// refusals that come before any connection or call.
func TestResearchWatchSheetCommands(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := cmdResearch(args, &out, &errb, lookup, defaultAgentDeps())
		return code, out.String(), errb.String()
	}
	if code, out, _ := run("template"); code != 0 || !strings.Contains(out, "url,handle,observer,watched_at,posted_at") || !strings.Contains(out, "own words") {
		t.Fatalf("template = %d\n%s", code, out)
	}
	sheet := "../../internal/research/testdata/r2/watch-sheet.csv"
	code, out, errOut := run("import", sheet, "--dry-run")
	if code != 0 || !strings.Contains(out, "30 row(s) checked: 30 post(s) by 5 creator(s)") || !strings.Contains(out, "10 of 10 shown date(s) agree") {
		t.Fatalf("dry run = %d\n%s%s", code, out, errOut)
	}
	// Flags before and after the file are both read.
	if code, out, errOut := run("import", "--dry-run", sheet, "--observer", "ana"); code != 0 || !strings.Contains(out, "30 row(s) checked") {
		t.Fatalf("flags around the file = %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = run("add-post", "https://www.tiktok.com/@synthetic_lifter_a/video/7657610837863983488", "--observer", "sam",
		"--saves", "1.2K", "--hook-text", "<TOPIC> 101", "--format", "one_take", "--posted", "2026-07-01", "--dry-run")
	if code != 0 || !strings.Contains(out, "1 row(s) checked") || !strings.Contains(out, "1 of 1 shown date(s) agree") {
		t.Fatalf("add-post dry run = %d\n%s%s", code, out, errOut)
	}

	bad := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(bad, []byte("url,observer,hook_text\nhttps://www.tiktok.com/@a_b/video/7657610837863983488,sam,never skip leg day\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No DATABASE_URL: the import stops before a connection, and --replay keeps it off the network in any case.
	noDB := filepath.Join(t.TempDir(), "research.env")
	if err := os.WriteFile(noDB, []byte("CF_ENV=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"import"}, "name the sheet"},
		{[]string{"import", "/does/not/exist.csv"}, "cannot read"},
		{[]string{"import", bad}, "capitals"},
		{[]string{"import", sheet, "extra"}, "exactly one"},
		{[]string{"import", sheet, "--replay", "a", "--record", "b"}, "exclude each other"},
		{[]string{"import", sheet, "--no-resolve", "--record", "b"}, "no fixture directory"},
		{[]string{"import", sheet, "--replay", "../../internal/research/testdata/r2/oembed", "--env-file", noDB}, "DATABASE_URL"},
		{[]string{"add-post"}, "name the post link"},
		{[]string{"add-post", "https://www.tiktok.com/@a_b", "--observer", "sam", "--dry-run"}, "not the link"},
		{[]string{"add-post", "https://www.tiktok.com/@a_b/video/7657610837863983488", "--observer", "sam", "--notes", "https://x.test/a.mp4", "--dry-run"}, "media"},
		{[]string{"template", "x"}, "no arguments"},
	} {
		code, _, errOut := run(c.args...)
		if code != 2 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v = %d, want 2 and %q:\n%s", c.args, code, c.want, errOut)
		}
	}
}
