package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/envfile"
)

// `cfctl env init` on an old .env adds the [AUTO] defaults it lacks and prints names only. --quiet prints nothing
// when there is nothing to change.
func TestEnvInitAddsDefaultsAndPrintsNamesOnly(t *testing.T) {
	f, err := os.Open(filepath.Join(repoRoot(t), envfile.ExampleFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	vars, err := envfile.ParseExample(f)
	if err != nil {
		t.Fatal(err)
	}
	var old strings.Builder
	var migrateURL string
	for _, v := range vars {
		switch v.Name {
		case "DATABASE_MIGRATE_URL":
			migrateURL = v.Default // absent from the old file
		case "R2_BUCKET":
			old.WriteString("R2_BUCKET=\n") // present but empty
		default:
			old.WriteString(v.Name + "=zzz-sentinel\n")
		}
	}
	if migrateURL == "" {
		t.Fatal("DATABASE_MIGRATE_URL has no default in .env.example")
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(old.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"env", "init", "--env-file", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "added: DATABASE_MIGRATE_URL") || !strings.Contains(out, "filled: R2_BUCKET") {
		t.Errorf("output = %q", out)
	}
	for _, value := range []string{migrateURL, "cf-media-dev", "zzz-sentinel"} {
		if strings.Contains(out, value) {
			t.Errorf("output contains a value: %q", value)
		}
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "DATABASE_MIGRATE_URL="+migrateURL) || !strings.Contains(string(got), "R2_BUCKET=cf-media-dev") {
		t.Error("defaults were not written")
	}

	for _, quiet := range []bool{false, true} {
		args := []string{"env", "init", "--env-file", path}
		if quiet {
			args = []string{"env", "init", "--quiet", "--env-file", path}
		}
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("second run exit %d: %s", code, stderr.String())
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(got, after) {
			t.Error("second run changed the file")
		}
		if quiet && stdout.Len() != 0 {
			t.Errorf("--quiet printed %q", stdout.String())
		}
	}
}
