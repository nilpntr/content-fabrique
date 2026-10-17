package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guardPassword = "pw-for-guard-test"

func guardURL(host string) string {
	return "postgres://u:" + guardPassword + "@" + host + ":1/cf?sslmode=disable&connect_timeout=2"
}

// guardCase says where CF_ENV and DATABASE_MIGRATE_URL are set. An empty string leaves the setting out of that place.
type guardCase struct {
	name     string
	args     []string
	cfFile   string
	cfProc   string
	urlHost  string // the database host
	urlInEnv bool   // true: the URL is in the process environment, false: in the env file
	wantCode int    // 2: refused by the guard; 1: past the guard (the closed port refuses the connection)
	wantErr  string
}

func (tc guardCase) run(t *testing.T) (code int, out, errOut string) {
	t.Helper()
	proc := map[string]string{}
	var body strings.Builder
	if tc.cfFile != "" {
		body.WriteString("CF_ENV=" + tc.cfFile + "\n")
	}
	if tc.cfProc != "" {
		proc["CF_ENV"] = tc.cfProc
	}
	if tc.urlInEnv {
		proc["DATABASE_MIGRATE_URL"] = guardURL(tc.urlHost)
	} else {
		body.WriteString("DATABASE_MIGRATE_URL=" + guardURL(tc.urlHost) + "\n")
	}
	envFile := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(envFile, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	code = cmdMigrate(append([]string{"--env-file", envFile}, tc.args...), &o, &e, fakeProcessEnv(proc))
	return code, o.String(), e.String()
}

// down and redo drop tables, so they fail closed: no --yes unless CF_ENV is explicitly local or ci, the host is this
// machine, and CF_ENV and the URL come from one place. Production needs --allow-production on top.
func TestMigrateDestructiveGuard(t *testing.T) {
	down, redo, downTo0 := []string{"down"}, []string{"redo"}, []string{"down", "--to", "0"}
	tests := []guardCase{
		// Allowed without --yes: explicit local or ci, loopback or the compose service, one source.
		{name: "local localhost", args: down, cfFile: "local", urlHost: "localhost", wantCode: 1},
		{name: "local 127.0.0.1 redo", args: redo, cfFile: "local", urlHost: "127.0.0.1", wantCode: 1},
		{name: "local IPv6 loopback", args: downTo0, cfFile: "local", urlHost: "[::1]", wantCode: 1},
		{name: "local compose service", args: down, cfFile: "local", urlHost: "postgres", wantCode: 1},
		{name: "ci loopback", args: downTo0, cfFile: "ci", urlHost: "127.0.0.1", wantCode: 1},
		{name: "both from the environment", args: down, cfProc: "local", urlHost: "localhost", urlInEnv: true, wantCode: 1},

		// Refused without --yes.
		{name: "CF_ENV missing", args: down, urlHost: "localhost", wantCode: 2, wantErr: "CF_ENV is not set"},
		{name: "CF_ENV missing redo", args: redo, urlHost: "127.0.0.1", wantCode: 2, wantErr: "CF_ENV is not set"},
		{name: "bench is not local", args: down, cfFile: "bench", urlHost: "localhost", wantCode: 2, wantErr: "CF_ENV=bench is not local or ci"},
		{name: "staging", args: down, cfFile: "staging", urlHost: "localhost", wantCode: 2, wantErr: "CF_ENV=staging is not local or ci"},
		{name: "staging to 0", args: downTo0, cfFile: "staging", urlHost: "db.invalid", wantCode: 2, wantErr: "CF_ENV=staging"},
		{name: "local but a remote host", args: down, cfFile: "local", urlHost: "db.invalid", wantCode: 2, wantErr: "database host is not this machine"},
		{name: "ci but a remote host", args: redo, cfFile: "ci", urlHost: "10.0.0.5", wantCode: 2, wantErr: "database host is not this machine"},
		{name: "host that only starts with localhost", args: down, cfFile: "local", urlHost: "localhost.evil.invalid", wantCode: 2, wantErr: "database host is not this machine"},
		{name: "CF_ENV from the file, URL from the environment", args: down, cfFile: "local", urlHost: "localhost", urlInEnv: true, wantCode: 2, wantErr: "CF_ENV comes from the env file but DATABASE_MIGRATE_URL from the process environment"},
		{name: "CF_ENV from the environment, URL from the file", args: down, cfProc: "local", urlHost: "localhost", wantCode: 2, wantErr: "CF_ENV comes from the process environment but DATABASE_MIGRATE_URL from the env file"},
		{name: "several reasons are all named", args: down, cfFile: "staging", urlHost: "db.invalid", urlInEnv: true, wantCode: 2, wantErr: "database host is not this machine"},

		// --yes lifts everything except production.
		{name: "staging with --yes", args: []string{"down", "--yes"}, cfFile: "staging", urlHost: "db.invalid", wantCode: 1},
		{name: "missing CF_ENV with --yes", args: []string{"redo", "--yes"}, urlHost: "localhost", wantCode: 1},
		{name: "mixed sources with --yes", args: []string{"down", "--yes"}, cfFile: "local", urlHost: "localhost", urlInEnv: true, wantCode: 1},

		// Production.
		{name: "production", args: down, cfFile: "production", urlHost: "db.invalid", wantCode: 2, wantErr: "--allow-production"},
		{name: "production --yes only", args: []string{"down", "--yes"}, cfFile: "production", urlHost: "localhost", wantCode: 2, wantErr: "--allow-production"},
		{name: "production --allow-production only", args: []string{"down", "--allow-production"}, cfFile: "production", urlHost: "db.invalid", wantCode: 2, wantErr: "--yes"},
		{name: "production redo with both", args: []string{"redo", "--yes", "--allow-production"}, cfFile: "production", urlHost: "db.invalid", wantCode: 1},

		// up, status and version are never guarded.
		{name: "up on staging", args: []string{"up"}, cfFile: "staging", urlHost: "db.invalid", wantCode: 1},
		{name: "status without CF_ENV", args: []string{"status"}, urlHost: "db.invalid", wantCode: 1},
		{name: "up on production", args: []string{"up"}, cfFile: "production", urlHost: "db.invalid", wantCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// "Past the guard" cases fail at the connection: a closed local port, or a name under .invalid, which never
			// resolves, so no test reaches the network.
			code, out, errOut := tc.run(t)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s%s", code, tc.wantCode, out, errOut)
			}
			if tc.wantErr != "" && !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr %q does not contain %q", errOut, tc.wantErr)
			}
			if strings.Contains(out+errOut, guardPassword) {
				t.Errorf("the password was printed:\n%s%s", out, errOut)
			}
			if tc.wantCode == 1 && !strings.Contains(out, "env file") {
				t.Errorf("header does not name the target:\n%s", out)
			}
		})
	}
}

// A run that gets past the checks says what it is about to touch, including where CF_ENV and the URL came from.
func TestMigrateHeaderNamesTheSources(t *testing.T) {
	tc := guardCase{args: []string{"status"}, cfProc: "staging", urlHost: "127.0.0.1", wantCode: 1}
	code, out, errOut := tc.run(t)
	if code != 1 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"127.0.0.1:1/cf", "CF_ENV=staging from the process environment", "DATABASE_MIGRATE_URL from the env file", "x.env"} {
		if !strings.Contains(out, want) {
			t.Errorf("header %q does not contain %q", out, want)
		}
	}
	tc = guardCase{args: []string{"status"}, urlHost: "127.0.0.1", wantCode: 1}
	if _, out, _ := tc.run(t); !strings.Contains(out, "CF_ENV=(unset) from nowhere") {
		t.Errorf("missing CF_ENV is not shown as unset: %q", out)
	}
}
