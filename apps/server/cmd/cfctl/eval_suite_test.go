package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvalSuiteRepeatAndCostGuard(t *testing.T) {
	var out, errb bytes.Buffer
	if got := run([]string{"eval", "suite", "--repeat", "11"}, &out, &errb); got != 2 || !strings.Contains(errb.String(), "--repeat") {
		t.Errorf("--repeat 11: %d %q", got, errb.String())
	}
	if (suiteFlags{live: true}).repeats() != DefaultLiveRepeat || (suiteFlags{}).repeats() != 1 || (suiteFlags{repeat: 3}).repeats() != 3 {
		t.Error("repeat defaults")
	}
	out.Reset()
	errb.Reset()
	if guardCost(&out, &errb, "eval suite", 2_500_000, 2, []string{"a: 2 × ..."}) || !strings.Contains(errb.String(), "over --max-usd") ||
		!strings.Contains(out.String(), "estimated cost $2.50 of $2.00") {
		t.Errorf("an estimate over the budget was let through: %q %q", out.String(), errb.String())
	}
	if !guardCost(&out, &errb, "eval suite", 1_000_000, 2, nil) {
		t.Error("an estimate under the budget was refused")
	}
}

func TestEvalSuiteArguments(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"eval", "suite", "--help"}, 0, "Usage"},
		{[]string{"eval", "suite", "--live"}, 2, "--max-usd"},
		{[]string{"eval", "suite", "--max-usd", "-1"}, 2, "--max-usd"},
		{[]string{"eval", "suite", "extra"}, 2, "unexpected argument"},
	} {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code || !strings.Contains(out.String()+errb.String(), c.msg) {
			t.Errorf("%v: code %d, output %q", c.args, got, out.String()+errb.String())
		}
	}
}
