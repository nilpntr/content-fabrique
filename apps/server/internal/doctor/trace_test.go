package doctor

import "testing"

// OTEL_TRACE_CONTENT (D-36): empty is valid everywhere (metadata-only spans); 1 is for the local stack and its own Phoenix only.
func TestTraceContentIsLocalOnly(t *testing.T) {
	ep := Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}
	for _, tc := range []struct {
		env, val string
		want     Status
	}{
		{"local", "", OK}, {"local", "1", OK}, {"local", "0", OK}, {"local", "yes", Invalid},
		{"ci", "1", Invalid}, {"staging", "1", Invalid}, {"production", "1", Invalid},
		{"staging", "", OK}, {"production", "", OK},
	} {
		vars := goodVars(t)
		vars["OTEL_TRACE_CONTENT"] = tc.val
		if r := find(t, runDoctor(t, tc.env, 0, vars, ep), "OTEL_TRACE_CONTENT"); r.Status != tc.want {
			t.Errorf("%s with %q: %s (%s), want %s", tc.env, tc.val, r.Status, r.Reason, tc.want)
		}
	}
}
