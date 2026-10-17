package render

import (
	"strings"
	"testing"
)

// A filter_complex is split into statements at the semicolons outside single quotes (the effects' asendcmd command lists are quoted
// and hold semicolons), and joining the pieces with ";" gives the graph back.
func TestSplitStatementsRespectsQuotes(t *testing.T) {
	g := "anullsrc=r=48000:d=1[a];anoisesrc=d=1,asendcmd=c='0 bandpass@x frequency 400;0.5 bandpass@x frequency 800',bandpass@x=f=400[b];[a][b]amix=inputs=2[o]"
	got := splitStatements(g)
	if len(got) != 3 || !strings.Contains(got[1], "frequency 400;0.5 bandpass") {
		t.Fatalf("pieces: %q", got)
	}
	if strings.Join(got, ";") != g {
		t.Errorf("joined: %q", strings.Join(got, ";"))
	}
	if pretty := PrettyFilter(g); strings.Count(pretty, "\n") != 2 || strings.ReplaceAll(pretty, "\n", "") != g {
		t.Errorf("pretty: %q", pretty)
	}
	if got := splitStatements("single"); len(got) != 1 || got[0] != "single" {
		t.Errorf("one statement: %q", got)
	}
	if got := splitStatements(""); len(got) != 1 || got[0] != "" {
		t.Errorf("empty: %q", got)
	}
}
