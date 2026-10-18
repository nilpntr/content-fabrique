package plans_test

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/plans"
)

func TestGrantable(t *testing.T) {
	for _, p := range []plans.Plan{plans.Trial, plans.Studio, plans.Creator, plans.Pro} {
		if !p.Grantable() || !p.Valid() {
			t.Errorf("%s should be grantable and valid", p)
		}
	}
	if plans.None.Grantable() || !plans.None.Valid() {
		t.Error("none is valid but cannot be granted")
	}
	for _, p := range []plans.Plan{"", "free", "Creator", "platinum"} {
		if p.Grantable() || p.Valid() {
			t.Errorf("%q should be neither grantable nor valid", p)
		}
	}
}

// The numbers are the product plan's (docs/01 section 10): a change here is a pricing decision.
func TestLimitsFollowTheProductPlan(t *testing.T) {
	for p, want := range map[plans.Plan]plans.Limits{
		plans.Trial:   {StorageGB: 2, AIBilling: plans.BilledByUs},
		plans.Studio:  {StorageGB: 100, AIBilling: plans.BilledByCreator},
		plans.Creator: {StorageGB: 50, AIBilling: plans.BilledByUs},
		plans.Pro:     {StorageGB: 200, AIBilling: plans.BilledByUs},
		plans.None:    {},
		"unknown":     {},
	} {
		if got := plans.LimitsOf(p); got != want {
			t.Errorf("LimitsOf(%q) = %+v, want %+v", p, got, want)
		}
	}
}
