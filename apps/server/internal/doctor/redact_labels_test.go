package doctor

import (
	"bytes"
	"strings"
	"testing"
)

// Local defaults such as the "postgres" user must not mangle the report's labels.
func TestReportLabelsSurviveLocalDefaults(t *testing.T) {
	vars := goodVars(t)
	vars["DATABASE_URL"] = "postgres://postgres:s3cretpassw0rd@127.0.0.1:1/postgres?sslmode=disable"
	vars["DATABASE_DIRECT_URL"] = vars["DATABASE_URL"]
	example := loadExample(t)
	rep := runDoctor(t, "local", 0, vars, Endpoints{})

	var raw bytes.Buffer
	WriteText(&raw, rep, ".env")
	red := NewRedactor(example, vars)
	got := red.Redact(raw.String())
	if got != raw.String() {
		t.Errorf("redaction changed a clean report:\n%s", got)
	}
	for _, label := range []string{"postgres DATABASE_URL", "postgres DATABASE_DIRECT_URL", "object-storage", "R2_BUCKET", "CF_ENV"} {
		if !strings.Contains(got, label) {
			t.Errorf("label %q missing from the report", label)
		}
	}
	// The password itself is still caught if it ever leaks.
	if strings.Contains(red.Redact("pw s3cretpassw0rd"), "s3cretpassw0rd") {
		t.Error("the URL password survived")
	}
}

// The JSON encoder escapes & < > and quotes, so results are redacted before encoding. A DSN with & must not get out.
func TestJSONRedactsValuesWithSpecialCharacters(t *testing.T) {
	dsn := `postgres://user:pa"ss<word>@db.internal:5432/cf?sslmode=disable&application_name=SENTINEL_app&x=1`
	vars := goodVars(t)
	vars["DATABASE_URL"] = dsn
	red := NewRedactor(loadExample(t), vars)

	// A reason that leaked the whole value.
	rep := Report{Env: "local", Phase: "P0", Results: []Result{
		{Kind: "service", Name: "postgres DATABASE_URL", Status: Invalid, Reason: "failed to connect to " + dsn},
	}}
	var js bytes.Buffer
	if err := WriteJSON(red.Writer(&js), red.Report(rep), ".env"); err != nil {
		t.Fatal(err)
	}
	out := js.String()
	for _, frag := range []string{"SENTINEL_app", "application_name", "db.internal", `pa\"ss`, `pa"ss`, `\u0026`, "word>"} {
		if strings.Contains(out, frag) {
			t.Errorf("JSON output contains %q:\n%s", frag, out)
		}
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected a redaction marker:\n%s", out)
	}
	// The encoder no longer HTML-escapes ordinary output.
	js.Reset()
	if err := WriteJSON(&js, Report{Results: []Result{{Name: "a&b", Status: OK}}}, ".env"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), "a&b") {
		t.Errorf("HTML escaping is still on: %s", js.String())
	}
}
