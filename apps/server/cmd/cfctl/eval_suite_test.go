package main

import (
	"bytes"
	"strings"
	"testing"
)

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
