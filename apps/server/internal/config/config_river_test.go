package config

import (
	"strings"
	"testing"
)

func TestLoadDirectURLAndRiverUIPassword(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://u:p@h:5432/db"}
	c, err := Load(env(base))
	if err != nil || c.DatabaseDirectURL != "" || c.RiverUIPassword != "" {
		t.Fatalf("both are optional for the api: %+v, %v", c, err)
	}

	base["DATABASE_DIRECT_URL"] = " postgres://u:p@h:5433/db "
	base["RIVER_UI_PASSWORD"] = "s3cret-pw"
	c, err = Load(env(base))
	if err != nil || c.DatabaseDirectURL != "postgres://u:p@h:5433/db" || c.RiverUIPassword != "s3cret-pw" {
		t.Fatalf("loaded %+v, %v", c, err)
	}
	// Neither the URL nor the password reaches a log line.
	for _, s := range []string{c.String(), c.GoString()} {
		if strings.Contains(s, "s3cret-pw") || strings.Contains(s, "5433") {
			t.Errorf("config prints a secret: %s", s)
		}
	}

	base["DATABASE_DIRECT_URL"] = "mysql://u:very-secret@h/db"
	_, err = Load(env(base))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_DIRECT_URL") || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("a bad direct URL: err = %v", err)
	}
}
