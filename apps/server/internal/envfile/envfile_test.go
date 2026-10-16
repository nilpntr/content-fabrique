package envfile

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# header comment
#   SECRETS_MASTER_KEY: openssl rand -base64 32 (a comment, not an assignment)

ANTHROPIC_API_KEY=                             # [YOU-now]
VITE_CLERK_PUBLISHABLE_KEY=                    # [YOU-now] [public] pk_test_...
CF_ENV=local                                   # [AUTO] local | ci
SECRETS_MASTER_KEY=                            # [AUTO]
SANDBOX_GATEWAY_TOKEN=keepme                   # [AUTO] already set
RIVER_UI_PASSWORD=                             # [AUTO]
DATABASE_URL=postgres://cf:cf@localhost:5432/cf?sslmode=disable          # [AUTO] pooled
CLOUDFLARE_ACCOUNT_ID=                         # [YOU-staging]
`

func TestParseExample(t *testing.T) {
	vars, err := ParseExample(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 8 {
		t.Fatalf("got %d vars, want 8", len(vars))
	}
	byName := map[string]Var{}
	for _, v := range vars {
		byName[v.Name] = v
	}
	if v := byName["VITE_CLERK_PUBLISHABLE_KEY"]; !v.Has(TagNow) || !v.Has(TagPublic) || v.Has(TagAuto) || v.Comment != "pk_test_..." {
		t.Errorf("VITE_CLERK_PUBLISHABLE_KEY = %+v", v)
	}
	if v := byName["DATABASE_URL"]; v.Default != "postgres://cf:cf@localhost:5432/cf?sslmode=disable" || !v.Has(TagAuto) {
		t.Errorf("DATABASE_URL = %+v", v)
	}
	if v := byName["ANTHROPIC_API_KEY"]; v.Default != "" || !v.Has(TagNow) {
		t.Errorf("ANTHROPIC_API_KEY = %+v", v)
	}
	if !byName["CLOUDFLARE_ACCOUNT_ID"].Has(TagStaging) {
		t.Error("CLOUDFLARE_ACCOUNT_ID should be YOU-staging")
	}
}

func TestParseExampleErrors(t *testing.T) {
	if _, err := ParseExample(strings.NewReader("A=1 # [AUTO]\nA=2 # [AUTO]\n")); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := ParseExample(strings.NewReader("A=1\n")); err == nil {
		t.Error("untagged variable accepted")
	}
}

func TestParse(t *testing.T) {
	in := `export A=1
B="two words" # trailing
C='x # y'
D=has#hash
E=
F=  # only a comment
G=old
G=new
# H=ignored
lower=ignored
`
	m, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "x # y", "D": "has#hash", "E": "", "F": "", "G": "new"}
	if len(m) != len(want) {
		t.Errorf("got %v", m)
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf("%s = %q (present %v), want %q", k, got, ok, v)
		}
	}
}

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ExampleFile), []byte("A=1 # [AUTO]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "apps", "server")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := FindRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if g, _ := filepath.EvalSymlinks(got); g != want {
		t.Errorf("FindRoot = %q, want %q", got, root)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	example := filepath.Join(dir, ExampleFile)
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(example, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Init(example, envPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || strings.Join(res.Generated, ",") != "SECRETS_MASTER_KEY,RIVER_UI_PASSWORD" ||
		strings.Join(res.AlreadySet, ",") != "SANDBOX_GATEWAY_TOKEN" {
		t.Errorf("result = %+v", res)
	}
	st, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}

	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := base64.StdEncoding.DecodeString(m["SECRETS_MASTER_KEY"]); err != nil || len(key) != 32 {
		t.Errorf("SECRETS_MASTER_KEY is not base64 of 32 bytes")
	}
	if b, err := hex.DecodeString(m["RIVER_UI_PASSWORD"]); err != nil || len(b) != 16 {
		t.Errorf("RIVER_UI_PASSWORD is not hex of 16 bytes")
	}
	if m["SANDBOX_GATEWAY_TOKEN"] != "keepme" {
		t.Error("an existing value was overwritten")
	}
	// Every other line is untouched, including comments and order.
	exLines := strings.Split(sample, "\n")
	gotLines := strings.Split(string(got), "\n")
	if len(exLines) != len(gotLines) {
		t.Fatalf("line count changed: %d -> %d", len(exLines), len(gotLines))
	}
	for i := range exLines {
		changed := strings.HasPrefix(exLines[i], "SECRETS_MASTER_KEY=") || strings.HasPrefix(exLines[i], "RIVER_UI_PASSWORD=")
		if !changed && exLines[i] != gotLines[i] {
			t.Errorf("line %d changed: %q -> %q", i, exLines[i], gotLines[i])
		}
		if changed && !strings.HasSuffix(gotLines[i], "# [AUTO]") {
			t.Errorf("tag comment lost on line %d", i)
		}
	}

	// A second run changes nothing.
	if err := os.Chmod(envPath, 0o644); err != nil {
		t.Fatal(err)
	}
	res2, err := Init(example, envPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(envPath)
	if res2.Created || len(res2.Generated) != 0 || len(res2.AlreadySet) != 3 || !bytes.Equal(got, after) {
		t.Errorf("second run changed something: %+v", res2)
	}
	if st, _ := os.Stat(envPath); st.Mode().Perm() != 0o644 {
		t.Error("an unchanged file was rewritten")
	}
}

func TestInitFillsExistingAndAppendsMissing(t *testing.T) {
	dir := t.TempDir()
	example := filepath.Join(dir, ExampleFile)
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(example, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	// Quoted-empty value, a missing secret, no trailing newline and a 0644 file that gets rewritten.
	if err := os.WriteFile(envPath, []byte("A=1\nSECRETS_MASTER_KEY=\"\"\nSANDBOX_GATEWAY_TOKEN=x"), 0o644); err != nil { //nolint:gosec // testing a loose mode
		t.Fatal(err)
	}
	res, err := Init(example, envPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || strings.Join(res.Generated, ",") != "SECRETS_MASTER_KEY,RIVER_UI_PASSWORD" {
		t.Errorf("result = %+v", res)
	}
	m, err := ParseFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["SANDBOX_GATEWAY_TOKEN"] != "x" || m["SECRETS_MASTER_KEY"] == "" || m["RIVER_UI_PASSWORD"] == "" {
		t.Errorf("unexpected content: %d keys", len(m))
	}
	if st, _ := os.Stat(envPath); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestInitMissingExample(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(filepath.Join(dir, "nope"), filepath.Join(dir, ".env"), nil); err == nil {
		t.Error("expected an error when .env.example is missing")
	}
}
