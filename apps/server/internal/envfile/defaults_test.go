package envfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const defaultsExample = `# header
CF_ENV=local                                   # [AUTO] local | ci
DATABASE_MIGRATE_URL=postgres://cf:cf@localhost:5432/cf?sslmode=disable # [AUTO] local: the same database
SECRETS_MASTER_KEY_VERSION=1                   # [AUTO]
ANTHROPIC_BASE_URL=                            # [AUTO] empty = direct
SECRETS_MASTER_KEY=                            # [AUTO]
SANDBOX_GATEWAY_TOKEN=                         # [AUTO]
RIVER_UI_PASSWORD=                             # [AUTO]
SENTRY_DSN=https://example.invalid/1           # [optional]
CLOUDFLARE_ACCOUNT_ID=abc                      # [YOU-staging]
`

const secretsDone = "SECRETS_MASTER_KEY=a\nSANDBOX_GATEWAY_TOKEN=b\nRIVER_UI_PASSWORD=c\n"

func initDefaults(t *testing.T, envContent string) (res InitResult, got string, path string) {
	t.Helper()
	now = func() time.Time { return time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	dir := t.TempDir()
	example := filepath.Join(dir, ExampleFile)
	path = filepath.Join(dir, ".env")
	if err := os.WriteFile(example, []byte(defaultsExample), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(envContent), 0o644); err != nil { //nolint:gosec // testing a loose mode
		t.Fatal(err)
	}
	res, err := Init(example, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	return res, string(b), path
}

func TestInitAppendsAbsentAutoDefaults(t *testing.T) {
	// The founder's case: an old .env without DATABASE_MIGRATE_URL.
	old := "CF_ENV=local\nMINE=1 # keep\n" + secretsDone
	res, got, path := initDefaults(t, old)
	if strings.Join(res.Added, ",") != "DATABASE_MIGRATE_URL,SECRETS_MASTER_KEY_VERSION" || len(res.Filled) != 0 {
		t.Fatalf("result = %+v", res)
	}
	want := old + "\n# added by cfctl env init (2026-10-17)\n" +
		"DATABASE_MIGRATE_URL=postgres://cf:cf@localhost:5432/cf?sslmode=disable # [AUTO] local: the same database\n" +
		"SECRETS_MASTER_KEY_VERSION=1                   # [AUTO]\n"
	if got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	// Empty-default, non-[AUTO] and generated variables are not added.
	for _, n := range []string{"ANTHROPIC_BASE_URL", "SENTRY_DSN", "CLOUDFLARE_ACCOUNT_ID"} {
		if strings.Contains(got, n) {
			t.Errorf("%s was added", n)
		}
	}
}

func TestInitFillsEmptyAutoDefaultInPlace(t *testing.T) {
	old := "CF_ENV=\nDATABASE_MIGRATE_URL=  # mine\nSECRETS_MASTER_KEY_VERSION=\"\"\n" + secretsDone
	res, got, _ := initDefaults(t, old)
	if strings.Join(res.Filled, ",") != "CF_ENV,DATABASE_MIGRATE_URL,SECRETS_MASTER_KEY_VERSION" || len(res.Added) != 0 {
		t.Fatalf("result = %+v", res)
	}
	want := "CF_ENV=local\n" +
		"DATABASE_MIGRATE_URL=postgres://cf:cf@localhost:5432/cf?sslmode=disable # mine\n" +
		"SECRETS_MASTER_KEY_VERSION=1\n" + secretsDone
	if got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "added by") {
		t.Error("a header was written though nothing was appended")
	}
}

func TestInitLeavesNonEmptyAndNonAutoAlone(t *testing.T) {
	old := "CF_ENV=staging\nDATABASE_MIGRATE_URL=postgres://other\nSECRETS_MASTER_KEY_VERSION=7\nANTHROPIC_BASE_URL=\nSENTRY_DSN=\nCLOUDFLARE_ACCOUNT_ID=\n" + secretsDone
	res, got, path := initDefaults(t, old)
	if len(res.Added)+len(res.Filled) != 0 || got != old {
		t.Fatalf("file changed: %+v\n%s", res, got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Error("an unchanged file was rewritten")
	}
}

func TestInitAutoDefaultsKeepCRLF(t *testing.T) {
	crlfSecrets := strings.ReplaceAll(secretsDone, "\n", "\r\n")
	old := "CF_ENV=\r\nMINE=1\r\n" + crlfSecrets
	res, got, _ := initDefaults(t, old)
	if strings.Join(res.Added, ",") != "DATABASE_MIGRATE_URL,SECRETS_MASTER_KEY_VERSION" || strings.Join(res.Filled, ",") != "CF_ENV" {
		t.Fatalf("result = %+v", res)
	}
	if bare := strings.ReplaceAll(got, "\r\n", ""); strings.ContainsAny(bare, "\r\n") {
		t.Errorf("line endings mixed: %q", got)
	}
	want := "CF_ENV=local\r\nMINE=1\r\n" + crlfSecrets +
		"\r\n# added by cfctl env init (2026-10-17)\r\n" +
		"DATABASE_MIGRATE_URL=postgres://cf:cf@localhost:5432/cf?sslmode=disable # [AUTO] local: the same database\r\n" +
		"SECRETS_MASTER_KEY_VERSION=1                   # [AUTO]\r\n"
	if got != want {
		t.Errorf("file = %q\nwant %q", got, want)
	}
}

func TestInitAutoDefaultsNoTrailingNewlineAndIdempotent(t *testing.T) {
	res, got, path := initDefaults(t, "CF_ENV=local\n"+secretsDone+"LAST=1")
	if len(res.Added) != 2 || !strings.Contains(got, "LAST=1\n\n# added by cfctl env init (2026-10-17)\nDATABASE_MIGRATE_URL=") {
		t.Fatalf("result = %+v\n%s", res, got)
	}
	// Second run: nothing to do, byte-identical, not rewritten.
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // testing a loose mode
		t.Fatal(err)
	}
	res2, err := Init(filepath.Join(filepath.Dir(path), ExampleFile), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if len(res2.Added)+len(res2.Filled)+len(res2.Generated) != 0 || !bytes.Equal([]byte(got), after) {
		t.Errorf("second run changed something: %+v", res2)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Error("an unchanged file was rewritten")
	}
}
