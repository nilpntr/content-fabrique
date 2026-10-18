package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) {
	code := storetest.Run(m)
	storagetest.Stop() // the MinIO of the assets import tests, if one was started
	os.Exit(code)
}

// migrateCLI runs `cfctl migrate args...` with an env file that holds url and returns the exit code and output.
func migrateCLI(t *testing.T, url string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "migrate.env")
	if err := os.WriteFile(envFile, []byte("DATABASE_MIGRATE_URL="+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code = cmdMigrate(append([]string{"--env-file", envFile}, args...), &out, &errb, fakeProcessEnv(nil))
	return code, out.String(), errb.String()
}

func TestMigrateCLI(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	url := srv.DSN(database, "postgres", "postgres")

	code, out, errOut := migrateCLI(t, url, "status")
	if code != 0 || !strings.Contains(out, "00001_app_role.sql") || !strings.Contains(out, "pending") {
		t.Fatalf("status before up = %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := migrateCLI(t, url, "version"); code != 0 || lastLine(out) != "0" {
		t.Fatalf("version before up = %d %q", code, out)
	}

	if code, out, errOut := migrateCLI(t, url, "up"); code != 0 || !strings.Contains(out, "00004_webhook_event_status.sql") {
		t.Fatalf("up = %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := migrateCLI(t, url, "up"); code != 0 || !strings.Contains(out, "nothing to do") {
		t.Fatalf("second up = %d %s", code, out)
	}
	if n := countTables(t, url); n != 32 {
		t.Fatalf("tables after up = %d", n)
	}
	if n := countRiverTables(t, url); n == 0 {
		t.Fatalf("River tables after up = %d, want them migrated too", n)
	}
	// status lists both lines, all applied.
	if code, out, errOut := migrateCLI(t, url, "status"); code != 0 || !strings.Contains(out, "river/001_") || strings.Contains(out, " pending\n") {
		t.Fatalf("status after up = %d\n%s\n%s", code, out, errOut)
	}

	// Flags after the command work too, and down --to 0 rolls everything back.
	if code, out, errOut := migrateCLI(t, url, "down", "--to", "0", "--yes"); code != 0 || !strings.Contains(out, "down") {
		t.Fatalf("down --to 0 = %d\n%s\n%s", code, out, errOut)
	}
	if n := countTables(t, url) + countRiverTables(t, url); n != 0 {
		t.Fatalf("tables after down --to 0 = %d (River's go with the rest)", n)
	}
	if code, _, errOut := migrateCLI(t, url, "up"); code != 0 {
		t.Fatalf("up after down = %d %s", code, errOut)
	}
	if code, out, errOut := migrateCLI(t, url, "redo", "--yes"); code != 0 || !strings.Contains(out, "down") || !strings.Contains(out, "up") {
		t.Fatalf("redo = %d\n%s\n%s", code, out, errOut)
	}
	// up --to N leaves River alone and says so.
	if code, _, _ := migrateCLI(t, url, "down", "--to", "0", "--yes"); code != 0 {
		t.Fatal("down --to 0 before the --to test")
	}
	if code, out, _ := migrateCLI(t, url, "up", "--to", "2"); code != 0 || !strings.Contains(out, "WARNING: River's tables were NOT migrated") || countRiverTables(t, url) != 0 {
		t.Fatalf("up --to 2 = %d\n%s", code, out)
	}
	if code, _, _ := migrateCLI(t, url, "up"); code != 0 || countRiverTables(t, url) == 0 {
		t.Fatal("up after up --to 2 must migrate River")
	}
	// A down that does not reach 0 keeps River; the one that does removes it after the app schema.
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || countRiverTables(t, url) == 0 || strings.Contains(out, "river/") || !strings.Contains(out, "00026_facet_backfill.go") {
		t.Fatalf("down one removed River too: %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00025_asset_facets.sql") {
		t.Fatalf("down one (00025) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00024_style_references.sql") {
		t.Fatalf("down one (00024) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00023_credit_actions_max.sql") {
		t.Fatalf("down one (00023) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00022_asset_music.sql") {
		t.Fatalf("down one (00022) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00021_creator_style.sql") {
		t.Fatalf("down one (00021) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00020_credit_hold_frozen.sql") {
		t.Fatalf("down one (00020) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00019_agent_run_min_output_tokens.sql") {
		t.Fatalf("down one (00019) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00018_credits.sql") {
		t.Fatalf("down one (00018) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00017_spend_control.sql") {
		t.Fatalf("down one (00017) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00016_transcripts.sql") {
		t.Fatalf("down one (00016) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00015_clip_analysis.sql") {
		t.Fatalf("down one (00015) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00014_annotations.sql") {
		t.Fatalf("down one (00014) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00013_quick_look.sql") {
		t.Fatalf("down one (00013) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00012_renders.sql") {
		t.Fatalf("down one (00012) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00011_timeline_version_idempotency.sql") {
		t.Fatalf("down one (00011) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00010_agent_runs.sql") {
		t.Fatalf("down one (00010) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00009_projects.sql") {
		t.Fatalf("down one (00009) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00008_usage_ledger.sql") {
		t.Fatalf("down one (00008) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00007_asset_ingest.sql") {
		t.Fatalf("down one (00007) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00006_assets.sql") {
		t.Fatalf("down one (00006) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00005_workspace_plans.sql") {
		t.Fatalf("down one (00005) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00004_webhook_event_status.sql") {
		t.Fatalf("down one (00004) = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00003_clerk_sync.sql") {
		t.Fatalf("down one = %d %s", code, out)
	}
	if code, out, _ := migrateCLI(t, url, "down", "--to", "0", "--yes"); code != 0 || strings.Index(out, "00001_app_role.sql") > strings.Index(out, "river/001_") {
		t.Fatalf("down --to 0 must drop River after the app migrations: %d\n%s", code, out)
	}
	if code, _, _ := migrateCLI(t, url, "up"); code != 0 {
		t.Fatal("up")
	}
	if code, out, _ := migrateCLI(t, url, "down", "--yes"); code != 0 || !strings.Contains(out, "00026_facet_backfill.go") {
		t.Fatalf("down one = %d %s", code, out)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// countTables counts the app's tables (River's are counted by countRiverTables).
func countTables(t *testing.T, url string) int {
	return countTablesWhere(t, url, `tablename <> 'goose_db_version' AND tablename NOT LIKE 'river\_%'`)
}

func countRiverTables(t *testing.T, url string) int {
	return countTablesWhere(t, url, `tablename LIKE 'river\_%'`)
}

func countTablesWhere(t *testing.T, url, cond string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND `+cond).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The password must not appear in anything the command prints, on success or failure.
func TestMigrateCLIPrintsNoPassword(t *testing.T) {
	const password = "very-secret-pw-7731"
	// Port 1 refuses connections: the failure path prints the driver's error.
	url := "postgres://someone:" + password + "@127.0.0.1:1/cf?sslmode=disable&connect_timeout=2"
	for _, args := range [][]string{{"up"}, {"status"}, {"down", "--to", "0", "--yes"}, {"version"}} {
		code, out, errOut := migrateCLI(t, url, args...)
		if code != 1 {
			t.Errorf("%v: exit %d, want 1 (database unreachable)", args, code)
		}
		if strings.Contains(out+errOut, password) {
			t.Errorf("%v printed the password:\n%s%s", args, out, errOut)
		}
	}
}

func TestMigrateCLIUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no command", nil, nil, "Usage"},
		{"unknown command", []string{"sideways"}, nil, "unknown command"},
		{"bad version", []string{"up", "--to", "x"}, nil, "--to must be"},
		{"to with status", []string{"status", "--to", "1"}, nil, "up and down only"},
		{"extra argument", []string{"up", "now"}, nil, "unexpected argument"},
		{"missing url", []string{"up"}, nil, "DATABASE_MIGRATE_URL is not set"},
		{"not a postgres url", []string{"up"}, map[string]string{"DATABASE_MIGRATE_URL": "mysql://u:p@h/db"}, "must be a postgres"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			// An explicit empty env file keeps the developer's real .env out of the test.
			envFile := filepath.Join(t.TempDir(), "empty.env")
			if err := os.WriteFile(envFile, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--env-file", envFile}, tc.args...)
			if code := cmdMigrate(args, &out, &errb, fakeProcessEnv(tc.env)); code != 2 {
				t.Fatalf("exit %d, want 2\n%s%s", code, out.String(), errb.String())
			}
			if !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("stderr %q does not contain %q", errb.String(), tc.want)
			}
		})
	}

	var out, errb bytes.Buffer
	if code := cmdMigrate([]string{"--env-file", filepath.Join(t.TempDir(), "absent.env"), "up"}, &out, &errb, fakeProcessEnv(nil)); code != 2 {
		t.Errorf("an explicit env file that does not exist: exit %d, want 2", code)
	}
}
