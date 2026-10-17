package doctor

import (
	"strings"
	"testing"
)

// The app's login must be unprivileged where the role split exists (staging, production). Locally the app and the
// migrate role are one login on purpose, so the finding is a note on an ok result.
func TestAppLoginPrivileges(t *testing.T) {
	url := func(addr string) string { return "postgres://app:pw@" + addr + "/cf?sslmode=disable" }
	for _, env := range []string{"staging", "production"} {
		vars := goodVars(t)
		localInfra(t, vars)
		vars["DATABASE_URL"] = url(fakePGPrivileged(t))
		rep := runDoctor(t, env, 0, vars, Endpoints{})
		r := wantStatus(t, rep, "postgres DATABASE_URL login", Invalid)
		for _, want := range []string{"app login is privileged", "is a superuser", "has CREATEROLE", "has CREATEDB", "owns schema public", "owns tables", "can CREATE in schema public"} {
			if !strings.Contains(r.Reason, want) {
				t.Errorf("%s: reason %q does not mention %q", env, r.Reason, want)
			}
		}
		if !rep.Failed() {
			t.Errorf("%s: a privileged app login must fail the run", env)
		}

		vars["DATABASE_URL"] = url(fakePG(t))
		rep = runDoctor(t, env, 0, vars, Endpoints{})
		if r := wantStatus(t, rep, "postgres DATABASE_URL login", OK); !strings.HasPrefix(r.Reason, "least privilege") {
			t.Errorf("%s: reason = %q", env, r.Reason)
		}
	}

	// Locally, in ci and in bench: ok, with the privileges named in the reason.
	for _, env := range []string{"local", "ci", "bench"} {
		vars := goodVars(t)
		localInfra(t, vars)
		vars["DATABASE_URL"] = url(fakePGPrivileged(t))
		rep := runDoctor(t, env, 0, vars, Endpoints{})
		r := wantStatus(t, rep, "postgres DATABASE_URL login", OK)
		if !strings.HasPrefix(r.Reason, "privileged: is a superuser") || !strings.Contains(r.Reason, "not on staging or production") {
			t.Errorf("%s: reason = %q", env, r.Reason)
		}
	}
}

// The direct URL is the same app role and is checked the same way.
func TestAppLoginChecksTheDirectURLToo(t *testing.T) {
	vars := goodVars(t)
	localInfra(t, vars)
	vars["DATABASE_DIRECT_URL"] = "postgres://app:pw@" + fakePGPrivileged(t) + "/cf?sslmode=disable"
	rep := runDoctor(t, "staging", 0, vars, Endpoints{})
	wantStatus(t, rep, "postgres DATABASE_DIRECT_URL login", Invalid)
	wantStatus(t, rep, "postgres DATABASE_URL login", OK)
}
