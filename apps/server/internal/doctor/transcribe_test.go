package doctor

import "testing"

// The Workers AI price lives in config/pricing (D-31); WORKERS_AI_PRICE_MICROS_PER_MINUTE only overrides it. The variable is optional
// everywhere and, when set, must be a price. That an unpriced provider never runs is tested where it is built (transcribe/setup).
func TestWorkersAIPriceOverrideIsOptionalAndChecked(t *testing.T) {
	ep := Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}
	for _, tc := range []struct {
		env, price string
		want       Status
	}{
		{"staging", "6000", OK},
		{"production", "6000", OK},
		{"staging", "", Skipped},
		{"production", "", Skipped},
		{"staging", "0", Invalid},
		{"staging", "free", Invalid},
		{"staging", "-5", Invalid},
		{"staging", "2000000", Invalid},
		{"local", "", Skipped},
		{"local", "abc", Invalid},
	} {
		vars := goodVars(t)
		vars["TRANSCRIBE_PROVIDER"] = "workersai"
		vars["WORKERS_AI_PRICE_MICROS_PER_MINUTE"] = tc.price
		rep := runDoctor(t, tc.env, 0, vars, ep)
		r := find(t, rep, "WORKERS_AI_PRICE_MICROS_PER_MINUTE")
		if r.Status != tc.want {
			t.Errorf("%s with price %q: %s (%s), want %s", tc.env, tc.price, r.Status, r.Reason, tc.want)
		}
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

// D-31: the platform breaker must be set on purpose in staging and production; elsewhere it is not needed. The caps are positive
// whole numbers, and an unpriced paid provider is never allowed outside development and ci.
func TestSpendControlRules(t *testing.T) {
	ep := Endpoints{Anthropic: "http://127.0.0.1:1", Clerk: "http://127.0.0.1:1"}
	check := func(env, name, val string) Result {
		t.Helper()
		vars := goodVars(t)
		vars[name] = val
		return find(t, runDoctor(t, env, 0, vars, ep), name)
	}
	for _, env := range []string{"staging", "production"} {
		if r := check(env, "PLATFORM_DAILY_SPEND_CAP_MICROS", ""); r.Status != Missing {
			t.Errorf("%s without the breaker: %s (%s), want missing", env, r.Status, r.Reason)
		}
		if r := check(env, "PLATFORM_DAILY_SPEND_CAP_MICROS", "150000000"); r.Status != OK {
			t.Errorf("%s with the breaker: %s (%s)", env, r.Status, r.Reason)
		}
		if r := check(env, "LLM_ALLOW_UNPRICED", "1"); r.Status != Invalid {
			t.Errorf("%s with LLM_ALLOW_UNPRICED=1: %s (%s), want invalid", env, r.Status, r.Reason)
		}
	}
	for _, env := range []string{"local", "ci"} {
		if r := check(env, "LLM_ALLOW_UNPRICED", "1"); r.Status != OK {
			t.Errorf("%s with LLM_ALLOW_UNPRICED=1: %s (%s)", env, r.Status, r.Reason)
		}
	}
	if r := check("local", "PLATFORM_DAILY_SPEND_CAP_MICROS", ""); r.Status != Skipped {
		t.Errorf("local without the breaker: %s (%s)", r.Status, r.Reason)
	}
	for _, val := range []string{"0", "-1", "lots", "1.5"} {
		for _, name := range []string{"PLATFORM_DAILY_SPEND_CAP_MICROS", "PLATFORM_SPEND_SMALL_CALL_MICROS", "DAILY_SPEND_CAP_MICROS", "LLM_MAX_REQUEST_MICROS"} {
			if r := check("staging", name, val); r.Status != Invalid {
				t.Errorf("%s=%q: %s (%s), want invalid", name, val, r.Status, r.Reason)
			}
		}
	}
}
