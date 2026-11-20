package clipfeedback

import (
	"errors"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func shot(n int, asset string, in, out int64) timeline.Shot {
	return timeline.Shot{Number: n, ItemID: "c" + string(rune('0'+n)), AssetID: asset, Source: timeline.Source{InMs: in, OutMs: out}}
}

func TestTheRangeOfFeedbackIsTheShotsClipAndSeconds(t *testing.T) {
	s := shot(5, "ast_a", 12_000, 15_500)
	for _, c := range []struct {
		name          string
		kind          string
		offset        int64
		whole         bool
		clipMs        int64
		start, end    int64
		wantErrSubstr string
	}{
		{name: "never use the shot", kind: NeverUse, start: 12_000, end: 15_500},
		{name: "never use the whole clip", kind: NeverUse, whole: true, clipMs: 60_000, start: 0, end: 60_000},
		{name: "whole clip without a length", kind: NeverUse, whole: true, wantErrSubstr: "length"},
		{name: "never use with an offset", kind: NeverUse, offset: 2000, wantErrSubstr: "start_later only"},
		{name: "start 2 s later", kind: StartLater, offset: 2000, start: 12_000, end: 14_000},
		{name: "start later, cut at the clip's end", kind: StartLater, offset: 5000, clipMs: 13_000, start: 12_000, end: 13_000},
		{name: "start later by too little", kind: StartLater, offset: 100, wantErrSubstr: "offset_ms"},
		{name: "start later by too much", kind: StartLater, offset: 31_000, wantErrSubstr: "offset_ms"},
		{name: "start later, whole clip", kind: StartLater, offset: 2000, whole: true, wantErrSubstr: "never_use only"},
		{name: "unknown kind", kind: "maybe", wantErrSubstr: "kind"},
	} {
		start, end, err := Range(s, c.kind, c.offset, c.whole, c.clipMs)
		if c.wantErrSubstr != "" {
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.wantErrSubstr) {
				t.Errorf("%s: err %v, want one about %q", c.name, err, c.wantErrSubstr)
			}
			continue
		}
		if err != nil || start != c.start || end != c.end {
			t.Errorf("%s: %d-%d, %v; want %d-%d", c.name, start, end, err, c.start, c.end)
		}
	}
}

func TestViolationsHoldInEveryLaterEdit(t *testing.T) {
	rules := map[string][]Rule{
		"ast_a": {{AssetID: "ast_a", Kind: NeverUse, StartMs: 12_000, EndMs: 15_500, Shot: 5, Version: 3}},
		"ast_b": {{AssetID: "ast_b", Kind: StartLater, StartMs: 4_000, EndMs: 6_000, Shot: 6, Version: 3}},
	}
	// The next edit numbers its shots differently: the rules follow the clip and the seconds.
	shots := []timeline.Shot{
		shot(1, "ast_a", 11_000, 12_050), // touches the ruled-out range by 50 ms: tolerated
		shot(2, "ast_a", 14_000, 16_000), // plays 1.5 s of it
		shot(3, "ast_b", 4_500, 7_000),   // starts before the new start
		shot(4, "ast_b", 6_000, 8_000),   // starts at the new start: fine
		shot(5, "ast_c", 12_000, 15_500), // another clip, the same seconds: fine
	}
	vs := Violations(shots, rules)
	if len(vs) != 2 || vs[0].Shot.Number != 2 || vs[0].OverlapMs != 1500 || vs[1].Shot.Number != 3 {
		t.Fatalf("violations %+v", vs)
	}
	if d := vs[0].Detail(); !strings.Contains(d, "shot 2 (c2)") || !strings.Contains(d, "never to use again (12.0 s to 15.5 s)") {
		t.Errorf("never-use detail %q", d)
	}
	if d := vs[1].Detail(); !strings.Contains(d, "start at 6.0 s or later") || !strings.Contains(d, "in_ms to 6000") {
		t.Errorf("start-later detail %q", d)
	}
	if !rules["ast_a"][0].Touches(shots[0]) || rules["ast_a"][0].Touches(shots[4]) {
		t.Error("Touches: any overlap of the same clip, never another clip")
	}
	if p := rules["ast_b"][0].Plain(); p != "start that take at 6.0 s or later: do not play 4.0 s to 6.0 s" {
		t.Errorf("plain %q", p)
	}
}
