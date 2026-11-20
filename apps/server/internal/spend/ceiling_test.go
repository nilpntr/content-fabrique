package spend

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
)

func TestLoadCeilingsDefaultsToTwoDollars(t *testing.T) {
	c, err := LoadCeilings(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	ws := ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())}
	if got := c.For(ws, plans.Creator); got.Micros != DefaultEditCeilingMicros || got.Source != "default" {
		t.Fatalf("default ceiling = %+v", got)
	}
	if c.NeedsPlan() {
		t.Error("no plan has its own ceiling, so the plan need not be read")
	}
	// The default never cuts a plan's own budget short: $2.00 is the largest any plan gives an edit.
	if c.For(ws, "").Apply(960_000) != 960_000 || c.For(ws, "").Apply(5_000_000) != 2_000_000 {
		t.Error("Apply")
	}
}

func TestLoadCeilingsMostSpecificWins(t *testing.T) {
	ws := ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())}
	other := ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())}
	vars := map[string]string{
		"EDIT_SPEND_CEILING_MICROS":     "1500000",
		"EDIT_SPEND_CEILING_PLANS":      " creator=800000 , pro=0",
		"EDIT_SPEND_CEILING_WORKSPACES": ids.Encode(ids.PrefixWorkspace, ws.UUID) + "=250000",
	}
	c, err := LoadCeilings(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ws     ids.WorkspaceID
		plan   plans.Plan
		micros int64
		source string
	}{
		{ws, plans.Creator, 250_000, "workspace"},
		{other, plans.Creator, 800_000, "plan creator"},
		{other, plans.Pro, 0, ""}, // switched off for pro
		{other, plans.Trial, 1_500_000, "default"},
		{other, "", 1_500_000, "default"},
	} {
		if got := c.For(tc.ws, tc.plan); got.Micros != tc.micros || got.Source != tc.source {
			t.Errorf("%s on %q: %+v, want %d from %q", tc.ws, tc.plan, got, tc.micros, tc.source)
		}
	}
	if !c.NeedsPlan() {
		t.Error("a plan has its own ceiling")
	}
	if (Ceiling{}).Apply(5_000_000) != 5_000_000 {
		t.Error("no ceiling must leave the budget alone")
	}
}

func TestLoadCeilingsRefusesNonsense(t *testing.T) {
	for k, v := range map[string]string{
		"EDIT_SPEND_CEILING_MICROS":     "-1",
		"EDIT_SPEND_CEILING_PLANS":      "gold=100",
		"EDIT_SPEND_CEILING_WORKSPACES": "not-an-id=100",
	} {
		_, err := LoadCeilings(func(name string) string {
			if name == k {
				return v
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	if _, err := LoadCeilings(func(k string) string {
		if k == "EDIT_SPEND_CEILING_MICROS" {
			return "999999999999"
		}
		return ""
	}); err == nil {
		t.Error("a ceiling above the maximum was accepted")
	}
	if _, err := LoadCeilings(func(k string) string {
		if k == "EDIT_SPEND_CEILING_PLANS" {
			return "creator"
		}
		return ""
	}); err == nil {
		t.Error("an entry without a value was accepted")
	}
}

func TestUSD(t *testing.T) {
	for in, want := range map[int64]string{0: "$0.00", 1: "$0.0000", 2500: "$0.0025", 2_000_000: "$2.00", 74_950_000: "$74.95"} {
		if got := USD(in); got != want {
			t.Errorf("USD(%d) = %s, want %s", in, got, want)
		}
	}
}
