package envlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

var known = map[string]bool{"LISTED": true, "ALSO_LISTED": true}

func TestPassesWhenEveryNameIsListed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "apps/server/internal/a/a.go", `package a

import "os"

func f(getenv func(string) string) {
	_ = os.Getenv("LISTED")
	_, _ = os.LookupEnv("ALSO_LISTED")
	_ = getenv("LISTED")
	_ = os.Getenv(dynamic()) // not a literal: not seen
}

func dynamic() string { return "X" }
`)
	write(t, root, "apps/web/src/a.tsx", "const a = import.meta.env.LISTED\nconst b = process.env['ALSO_LISTED']\nconst c = import.meta.env.DEV\nif (process.env.CI) {}\n")
	got, err := Lint(root, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("unexpected findings: %v", got)
	}
}

func TestFailsOnUnlistedNames(t *testing.T) {
	root := t.TempDir()
	write(t, root, "apps/server/cmd/x/main.go", `package main

import "os"

type cfg struct{}

func (cfg) Getenv(string) string { return "" }

func main() {
	_ = os.Getenv("GO_UNLISTED")
	_, _ = os.LookupEnv("GO_LOOKUP_UNLISTED")
	var c cfg
	_ = c.Getenv("GO_METHOD_UNLISTED")
	_ = os.Getenv("LISTED")
}
`)
	// Test files, testdata and vendored code are not scanned.
	write(t, root, "apps/server/cmd/x/main_test.go", "package main\nimport \"os\"\nvar _ = os.Getenv(\"IN_TEST\")\n")
	write(t, root, "apps/server/testdata/t.go", "package t\nimport \"os\"\nvar _ = os.Getenv(\"IN_TESTDATA\")\n")
	write(t, root, "apps/web/src/routes/a.ts", "// import.meta.env.IN_COMMENT\nconst a = import.meta.env.VITE_WEB_UNLISTED\nconst b = process.env[\"WEB_INDEX_UNLISTED\"]\n")
	write(t, root, "apps/web/node_modules/x/index.ts", "const a = process.env.IN_NODE_MODULES\n")

	got, err := Lint(root, known)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	want := "GO_UNLISTED,GO_LOOKUP_UNLISTED,GO_METHOD_UNLISTED,VITE_WEB_UNLISTED,WEB_INDEX_UNLISTED"
	if strings.Join(names, ",") != want {
		t.Errorf("findings = %v, want %s", got, want)
	}
	if got[0].File != "apps/server/cmd/x/main.go" || got[0].Line != 10 {
		t.Errorf("first finding = %+v", got[0])
	}
	if !strings.Contains(got[0].String(), "GO_UNLISTED is read but not listed") {
		t.Errorf("message = %q", got[0].String())
	}
}

func TestMissingWebDirIsFine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "apps/server/a.go", "package a\n")
	if _, err := Lint(root, known); err != nil {
		t.Fatal(err)
	}
}

func TestParseErrorIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, "apps/server/bad.go", "package\n")
	if _, err := Lint(root, known); err == nil {
		t.Error("expected a parse error")
	}
}
