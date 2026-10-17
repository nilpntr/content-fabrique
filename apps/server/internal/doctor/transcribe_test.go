package doctor

import "testing"

// The Workers AI price is checked by its own rule (D-27): a price of 0 or a malformed one is refused in staging and production,
// where Workers AI is the provider and an unpriced call would escape the daily spend cap.
func TestWorkersAIPriceMustBeVerifiedInHostedEnvironments(t *testing.T) {
	ep := Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}
	for _, tc := range []struct {
		env, price string
		want       Status
	}{
		{"staging", "6000", OK},
		{"production", "6000", OK},
		{"staging", "0", Invalid},
		{"production", "0", Invalid},
		{"staging", "free", Invalid},
		{"staging", "-5", Invalid},
		{"staging", "2000000", Invalid},
		{"local", "0", OK}, // development may run unpriced
		{"local", "abc", Invalid},
	} {
		vars := goodVars(t)
		vars["WORKERS_AI_PRICE_MICROS_PER_MINUTE"] = tc.price
		rep := runDoctor(t, tc.env, 0, vars, ep)
		r := find(t, rep, "WORKERS_AI_PRICE_MICROS_PER_MINUTE")
		if r.Status != tc.want {
			t.Errorf("%s with price %q: %s (%s), want %s", tc.env, tc.price, r.Status, r.Reason, tc.want)
		}
	}
	// Staging with no price at all is flagged as missing (it is a [YOU-staging] value).
	vars := goodVars(t)
	vars["WORKERS_AI_PRICE_MICROS_PER_MINUTE"] = ""
	if r := find(t, runDoctor(t, "staging", 0, vars, ep), "WORKERS_AI_PRICE_MICROS_PER_MINUTE"); r.Status != Missing {
		t.Errorf("staging without a price: %s (%s)", r.Status, r.Reason)
	}
}

func TestTranscribeDailyMinutesIsValidated(t *testing.T) {
	ep := Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}
	for val, want := range map[string]Status{"240": OK, "1": OK, "0": Invalid, "many": Invalid, "100001": Invalid} {
		vars := goodVars(t)
		vars["TRANSCRIBE_DAILY_MINUTES"] = val
		if r := find(t, runDoctor(t, "local", 0, vars, ep), "TRANSCRIBE_DAILY_MINUTES"); r.Status != want {
			t.Errorf("%q: %s (%s), want %s", val, r.Status, r.Reason, want)
		}
	}
}
