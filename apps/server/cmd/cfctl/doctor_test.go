package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/envfile"
)

// repoRoot is the real repository root, found from the test's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := envfile.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func exampleNames(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), envfile.ExampleFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	vars, err := envfile.ParseExample(f)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = v.Name
	}
	return names
}

func fakeProcessEnv(m map[string]string) lookupEnv {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// Every variable is set to a sentinel that fails its format check, so doctor makes no network call. Nothing the
// CLI prints may contain a value.
func TestDoctorCLIPrintsNoValues(t *testing.T) {
	env := map[string]string{}
	var sentinels []string
	for _, n := range exampleNames(t) {
		env[n] = "SENTINEL_" + n + "_9f2c"
		sentinels = append(sentinels, env[n])
	}
	for _, args := range [][]string{
		{"--env", "production", "--phase", "P7"},
		{"--env", "ci", "--json"},
		{"--env", "local", "--env-file", filepath.Join(t.TempDir(), "absent.env")}, // explicit and missing: usage error
	} {
		var out, errb bytes.Buffer
		code := cmdDoctor(args, &out, &errb, fakeProcessEnv(env))
		wantCode := 1
		if strings.Contains(strings.Join(args, " "), "absent") {
			wantCode = 2
		}
		if code != wantCode {
			t.Errorf("doctor %v exit = %d, want %d\n%s%s", args, code, wantCode, out.String(), errb.String())
		}
		for _, s := range sentinels {
			if strings.Contains(out.String(), s) || strings.Contains(errb.String(), s) {
				t.Errorf("doctor %v printed a loaded value", args)
			}
		}
		if strings.Contains(out.String()+errb.String(), "SENTINEL") {
			t.Errorf("doctor %v printed a sentinel fragment", args)
		}
	}
}

func TestDoctorCLIUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--env", "prod"},
		{"--env", "local", "--phase", "P9"},
		{"--env", "local", "extra"},
		{"--bogus"},
	} {
		var out, errb bytes.Buffer
		if code := cmdDoctor(args, &out, &errb, fakeProcessEnv(nil)); code != 2 {
			t.Errorf("doctor %v exit = %d, want 2", args, code)
		}
	}
}

// A non-empty value in the file wins over the process environment; the process fills names the file lacks or leaves
// empty. (No Anthropic or Clerk key is involved, so the run makes no network call.)
func TestDoctorCLIEnvFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(file, []byte("CF_ENV=local\r\nLLM_MODE=\r\nROUTING_PROFILE=dev-mid\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process := fakeProcessEnv(map[string]string{"CF_ENV": "ci", "LLM_MODE": "live", "ROUTING_PROFILE": "dev-cheap", "APP_BASE_URL": "http://localhost:3000"})
	var out, errb bytes.Buffer
	cmdDoctor([]string{"--env", "local", "--env-file", file}, &out, &errb, process)
	text := out.String()
	// CF_ENV=local from the file matches --env local; had the process value (ci) won, it would be a mismatch.
	if !strings.Contains(text, "ok       CF_ENV") {
		t.Errorf("the file's value should win:\n%s", text)
	}
	// LLM_MODE is empty in the file, so the process value fills it, and APP_BASE_URL is absent from the file.
	for _, name := range []string{"LLM_MODE", "APP_BASE_URL"} {
		if !strings.Contains(text, "ok       "+name) {
			t.Errorf("the process environment should fill %s:\n%s", name, text)
		}
	}
}

func TestEnvLintRealRepoPasses(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdEnvLint(nil, &out, &errb); code != 0 {
		t.Fatalf("env lint exit = %d\n%s%s", code, out.String(), errb.String())
	}
}

// A fixture repository: an unlisted read fails, a listed one passes.
func TestEnvLintFixture(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".env.example", "LISTED=1   # [AUTO]\n")
	write("apps/server/a.go", "package a\nimport \"os\"\nvar _ = os.Getenv(\"LISTED\")\n")
	t.Chdir(root)

	var out, errb bytes.Buffer
	if code := cmdEnvLint(nil, &out, &errb); code != 0 {
		t.Fatalf("listed read: exit %d\n%s", code, errb.String())
	}

	write("apps/server/b.go", "package a\nimport \"os\"\nvar _ = os.Getenv(\"NOT_IN_EXAMPLE\")\n")
	out.Reset()
	errb.Reset()
	if code := cmdEnvLint(nil, &out, &errb); code != 1 {
		t.Fatalf("unlisted read: exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "apps/server/b.go:3: NOT_IN_EXAMPLE is read but not listed") {
		t.Errorf("stderr = %s", errb.String())
	}
}

func TestEnvInitCLI(t *testing.T) {
	root := t.TempDir()
	example, err := os.ReadFile(filepath.Join(repoRoot(t), envfile.ExampleFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, envfile.ExampleFile), example, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var out, errb bytes.Buffer
	if code := cmdEnv([]string{"init"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "generated: SECRETS_MASTER_KEY, SANDBOX_GATEWAY_TOKEN, RIVER_UI_PASSWORD") {
		t.Errorf("stdout = %s", out.String())
	}
	env, _ := envfile.ParseFile(filepath.Join(root, ".env"))
	for _, secret := range []string{env["SECRETS_MASTER_KEY"], env["SANDBOX_GATEWAY_TOKEN"], env["RIVER_UI_PASSWORD"]} {
		if secret == "" || strings.Contains(out.String(), secret) {
			t.Error("a secret is empty or was printed")
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, ".env"))
	out.Reset()
	if code := cmdEnv([]string{"init"}, &out, &errb); code != 0 {
		t.Fatal("second run failed")
	}
	after, _ := os.ReadFile(filepath.Join(root, ".env"))
	if !bytes.Equal(before, after) || !strings.Contains(out.String(), "nothing changed") {
		t.Errorf("the second run changed something: %s", out.String())
	}
}
