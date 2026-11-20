package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

func TestProofArguments(t *testing.T) {
	prj := ids.Encode(ids.PrefixProject, mustUUID(t))
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no project", []string{"shots", "--version", "1", "--workspace", "org_x"}, "give 1 id"},
		{"bad project", []string{"shots", "ast_x", "--version", "1", "--workspace", "org_x"}, "not a project id"},
		{"no version", []string{"shots", prj, "--workspace", "org_x"}, "--version is required"},
		{"no shot", []string{"never", prj, "--version", "2", "--workspace", "org_x"}, "--shot is required"},
		{"later without --by", []string{"later", prj, "--version", "2", "--shot", "3", "--workspace", "org_x"}, "--by is how many seconds"},
		{"later by too much", []string{"later", prj, "--version", "2", "--shot", "3", "--by", "45", "--workspace", "org_x"}, "0.5 to 30"},
		{"no workspace", []string{"list"}, "--workspace is required"},
		{"forget without id", []string{"forget", "--workspace", "org_x"}, "give 1 id"},
		{"unknown", []string{"delete"}, `unknown command "delete"`},
	} {
		var out, errb bytes.Buffer
		code := cmdProof(tc.args, &out, &errb, fakeProcessEnv(nil), agentDeps{})
		if code != 2 || !strings.Contains(errb.String(), tc.want) {
			t.Errorf("%s: exit %d, stderr %q, want 2 and %q", tc.name, code, errb.String(), tc.want)
		}
	}
	var out bytes.Buffer
	if code := cmdProof([]string{"help"}, &out, &out, fakeProcessEnv(nil), agentDeps{}); code != 0 || !strings.Contains(out.String(), "cfctl proof shots") {
		t.Errorf("help: %d %s", code, out.String())
	}
}

// cfctl render --proof is the preview with the shot numbers, and prints the shot list; a final proof is refused.
func TestRenderProofDryRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, renderAsset+".mp4"), []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runRender(renderFixture, "--assets", dir, "--dry-run", "--proof", "--ffmpeg", "/nonexistent/ffmpeg")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"Shot list", "  1  " + renderAsset, "ass=filename=subs.ass"} {
		if !strings.Contains(out, want) {
			t.Errorf("proof dry run lacks %q:\n%s", want, out)
		}
	}
	if code, _, errOut := runRender(renderFixture, "--assets", dir, "--dry-run", "--proof", "--profile", "final"); code != 2 || !strings.Contains(errOut, "never has them") {
		t.Errorf("a final proof: %d %s", code, errOut)
	}
}
