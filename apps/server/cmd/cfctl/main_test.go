package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "cfctl ", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{nil, 2, "", "Usage:"},
		{[]string{"nope"}, 2, "", `unknown command "nope"`},
	}
	for _, tc := range tests {
		var out, errb bytes.Buffer
		code := run(tc.args, &out, &errb)
		if code != tc.code || !strings.Contains(out.String(), tc.stdout) || !strings.Contains(errb.String(), tc.stderr) {
			t.Errorf("run(%v) = %d, stdout %q, stderr %q", tc.args, code, out.String(), errb.String())
		}
	}
}
