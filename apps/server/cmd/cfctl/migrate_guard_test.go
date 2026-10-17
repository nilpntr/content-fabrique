package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runMigrateWithEnvFile runs `cfctl migrate` with an env file holding body. The database URL points at a closed
// port, so a run that gets past the checks fails with exit 1 (cannot connect) and one that is refused exits 2.
func runMigrateWithEnvFile(t *testing.T, body string, args ...string) (code int, out, errOut string) {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	code = cmdMigrate(append([]string{"--env-file", envFile}, args...), &o, &e, fakeProcessEnv(nil))
	return code, o.String(), e.String()
}

const deadURL = "DATABASE_MIGRATE_URL=postgres://u:pw-for-guard-test@127.0.0.1:1/cf?sslmode=disable&connect_timeout=2\n"

// down and redo drop tables, so staging wants --yes and production is refused unless --allow-production is given.
func TestMigrateDestructiveGuard(t *testing.T) {
	tests := []struct {
		name     string
		cfEnv    string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"local down", "local", []string{"down"}, 1, ""},
		{"local redo", "local", []string{"redo"}, 1, ""},
		{"ci down to 0", "ci", []string{"down", "--to", "0"}, 1, ""},
		{"staging down refused", "staging", []string{"down"}, 2, "without --yes"},
		{"staging redo refused", "staging", []string{"redo"}, 2, "without --yes"},
		{"staging down to 0 refused", "staging", []string{"down", "--to", "0"}, 2, "without --yes"},
		{"staging down with --yes", "staging", []string{"down", "--yes"}, 1, ""},
		{"staging up needs nothing", "staging", []string{"up"}, 1, ""},
		{"staging status needs nothing", "staging", []string{"status"}, 1, ""},
		{"production down refused", "production", []string{"down"}, 2, "--allow-production"},
		{"production down with --yes still refused", "production", []string{"down", "--yes"}, 2, "--allow-production"},
		{"production down with --allow-production still needs --yes", "production", []string{"down", "--allow-production"}, 2, "without --yes"},
		{"production redo with both", "production", []string{"redo", "--yes", "--allow-production"}, 1, ""},
		{"production up needs nothing", "production", []string{"up"}, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runMigrateWithEnvFile(t, "CF_ENV="+tc.cfEnv+"\n"+deadURL, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s%s", code, tc.wantCode, out, errOut)
			}
			if tc.wantErr != "" && !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr %q does not contain %q", errOut, tc.wantErr)
			}
			if tc.wantCode == 2 && !strings.Contains(errOut, "CF_ENV="+tc.cfEnv) {
				t.Errorf("a refusal must say which environment it is about: %q", errOut)
			}
			if strings.Contains(out+errOut, "pw-for-guard-test") {
				t.Errorf("the password was printed:\n%s%s", out, errOut)
			}
			// A run that passes the guard says what it is about to touch: the environment and the env file.
			if tc.wantCode == 1 && (!strings.Contains(out, "CF_ENV="+tc.cfEnv) || !strings.Contains(out, "x.env") || !strings.Contains(out, "127.0.0.1:1/cf")) {
				t.Errorf("header does not name the target:\n%s", out)
			}
		})
	}
}

// CF_ENV comes from the process environment too, when the env file leaves it out.
func TestMigrateGuardReadsTheProcessEnvironment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(envFile, []byte(deadURL), 0o600); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	if code := cmdMigrate([]string{"--env-file", envFile, "down"}, &o, &e, fakeProcessEnv(map[string]string{"CF_ENV": "staging"})); code != 2 {
		t.Fatalf("exit %d, want 2\n%s%s", code, o.String(), e.String())
	}
}
