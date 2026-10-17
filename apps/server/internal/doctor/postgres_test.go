package doctor

import "testing"

func TestPostgresOlderThan18Fails(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	vars["DATABASE_URL"] = "postgres://cf:cf@" + fakePGVersion(t, "170005") + "/cf?sslmode=disable"
	rep := runDoctor(t, "ci", 0, vars, Endpoints{})
	if r := wantStatus(t, rep, "postgres DATABASE_URL", Invalid); r.Reason != "Postgres 18 or newer required" {
		t.Errorf("reason = %q", r.Reason)
	}
	wantStatus(t, rep, "postgres DATABASE_DIRECT_URL", OK)
}

// App servers never hold the schema owner's URL: staging and production check it only when it is set.
func TestMigrateURLIsNotRequiredOnAppServers(t *testing.T) {
	for _, env := range []string{"staging", "production"} {
		vars := goodVars(t)
		localInfra(t, vars)
		vars["DATABASE_MIGRATE_URL"] = ""
		rep := runDoctor(t, env, 0, vars, Endpoints{})
		if r := wantStatus(t, rep, "postgres DATABASE_MIGRATE_URL", Skipped); r.Reason != "not on app servers" {
			t.Errorf("%s: reason = %q", env, r.Reason)
		}
		if r := wantStatus(t, rep, "DATABASE_MIGRATE_URL", Skipped); r.Reason != "not on app servers" {
			t.Errorf("%s: variable reason = %q", env, r.Reason)
		}

		// Set (from a laptop), it is checked like any other.
		vars["DATABASE_MIGRATE_URL"] = "postgres://cf:cf@" + unreachableAddr(t) + "/cf?sslmode=disable"
		rep = runDoctor(t, env, 0, vars, Endpoints{})
		wantStatus(t, rep, "postgres DATABASE_MIGRATE_URL", Invalid)
	}
	// Everywhere else it stays required.
	for _, env := range []string{"local", "ci", "bench"} {
		vars := goodVars(t)
		localInfra(t, vars)
		vars["DATABASE_MIGRATE_URL"] = ""
		rep := runDoctor(t, env, 0, vars, Endpoints{})
		wantStatus(t, rep, "DATABASE_MIGRATE_URL", Missing)
		wantStatus(t, rep, "postgres DATABASE_MIGRATE_URL", Missing)
	}
}
