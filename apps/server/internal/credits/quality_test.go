package credits_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/plans"
)

func loadOld(t *testing.T) *credits.PriceList {
	t.Helper()
	l, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The file the code runs on is the NEWEST one: a new price list is a new file, and a build that still prices new charges from an older
// file (because credits.ListFile was not moved) would quietly ignore it. Files sort by name, oldest first, so the last one wins.
func TestTheDefaultPriceListIsTheNewestFile(t *testing.T) {
	names, err := credits.ListFiles(configfiles.Plans())
	if err != nil || len(names) < 2 {
		t.Fatalf("price list files = %v, %v", names, err)
	}
	if newest := names[len(names)-1]; credits.ListFile != newest {
		t.Fatalf("credits.ListFile is %s but the newest file is %s: new charges would be priced from an older list", credits.ListFile, newest)
	}
	l, err := credits.LoadDefault()
	if err != nil || l.Version != "2026-10c" {
		t.Fatalf("default list = %v, %v", l, err)
	}
}

// Extra careful edits (quality max, D-31) are new actions on top of a list that is otherwise 2026-10 as it was: the standard credits, the
// prices, the allowances, the standard budgets, the fee, the rate and the 40 % share are unchanged, so nobody's videos per month change and
// the profit invariant is not loosened.
func TestStandardIsUnchangedFromTheOriginalList(t *testing.T) {
	cur, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	old := loadOld(t)
	for _, a := range []credits.Action{credits.EditFirst, credits.EditRevision, credits.ExportFinal, credits.IdeationRun} {
		if cur.Credits[a] != old.Credits[a] {
			t.Errorf("%s costs %d credits, was %d", a, cur.Credits[a], old.Credits[a])
		}
	}
	for _, p := range plans.Granted {
		cp, op := cur.Plans[p], old.Plans[p]
		if cp.PriceEURCents != op.PriceEURCents || cp.BillingMode != op.BillingMode || cp.AllowanceCredits != op.AllowanceCredits || cp.Refill != op.Refill {
			t.Errorf("%s: price, mode, allowance or refill changed: %+v, was %+v", p, cp, op)
		}
		for _, a := range []credits.Action{credits.EditFirst, credits.EditRevision, credits.ExportFinal, credits.IdeationRun} {
			if cp.COGSBudgetMicros[a] != op.COGSBudgetMicros[a] || cp.NonLLMCOGSBudgetMicros[a] != op.NonLLMCOGSBudgetMicros[a] {
				t.Errorf("%s %s: budgets changed (%d / %d, was %d / %d)", p, a, cp.COGSBudgetMicros[a], cp.NonLLMCOGSBudgetMicros[a], op.COGSBudgetMicros[a], op.NonLLMCOGSBudgetMicros[a])
			}
		}
		if cur.VideosPerPeriod(p) != old.VideosPerPeriod(p) {
			t.Errorf("%s: %d videos a period, was %d", p, cur.VideosPerPeriod(p), old.VideosPerPeriod(p))
		}
	}
	if cur.TrialCOGSAllowanceMicros != old.TrialCOGSAllowanceMicros || cur.MaxCOGSShare.Cmp(old.MaxCOGSShare) != 0 || cur.MoRFeePct.Cmp(old.MoRFeePct) != 0 ||
		cur.MoRFeeFixedEURCent != old.MoRFeeFixedEURCent || cur.USDPerEUR.Cmp(old.USDPerEUR) != 0 {
		t.Errorf("trial allowance, share, fee or exchange rate changed")
	}
}

// The extra careful edits hold the profit invariant for every plan that includes them, and with the fewest credits: one credit less would
// break it for some plan, so the customer is not charged more credits than the budgets need. No dollar figure is pinned here: the budgets
// are the list's to choose, the invariant's to bound.
func TestMaxEditsFitTheInvariantWithTheFewestCredits(t *testing.T) {
	cur, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	if vs := cur.Check(); len(vs) != 0 {
		t.Fatalf("the current list breaks the invariant: %v", vs)
	}
	if !cur.Offers(credits.Max) || cur.Credits[credits.EditFirstMax] <= cur.Credits[credits.EditFirst] || cur.Credits[credits.EditRevisionMax] <= cur.Credits[credits.EditRevision] {
		t.Fatalf("max edits cost %d and %d credits against %d and %d", cur.Credits[credits.EditFirstMax], cur.Credits[credits.EditRevisionMax], cur.Credits[credits.EditFirst], cur.Credits[credits.EditRevision])
	}
	for _, a := range []credits.Action{credits.EditFirstMax, credits.EditRevisionMax} {
		cheaper, _ := credits.LoadDefault()
		cheaper.Credits[a]--
		if len(cheaper.Check()) == 0 {
			t.Errorf("%s costs %d credits, but %d would hold the invariant too: the customer pays more credits than the budgets need", a, cur.Credits[a], cheaper.Credits[a])
		}
	}
	// A max edit gets a larger budget than a standard one wherever the plan includes it.
	for _, p := range []plans.Plan{plans.Creator, plans.Pro} {
		pl := cur.Plans[p]
		if !pl.QualityMax || pl.COGSBudgetMicros[credits.EditFirstMax] <= pl.COGSBudgetMicros[credits.EditFirst] || pl.COGSBudgetMicros[credits.EditRevisionMax] <= pl.COGSBudgetMicros[credits.EditRevision] {
			t.Errorf("%s: quality_max %v, max budgets %d / %d against standard %d / %d", p, pl.QualityMax, pl.COGSBudgetMicros[credits.EditFirstMax], pl.COGSBudgetMicros[credits.EditRevisionMax], pl.COGSBudgetMicros[credits.EditFirst], pl.COGSBudgetMicros[credits.EditRevision])
		}
	}
}

// The trial cannot use the extra careful edits; studio (on the creator's own key) and the managed plans can.
func TestWhoMayUseMaxQuality(t *testing.T) {
	cur, _ := credits.LoadDefault()
	for p, want := range map[plans.Plan]bool{plans.Trial: false, plans.Studio: true, plans.Creator: true, plans.Pro: true} {
		if got := cur.Plans[p].QualityMax; got != want {
			t.Errorf("%s: quality_max = %v, want %v", p, got, want)
		}
		_, _, ok := cur.EditCredits(p, credits.Max)
		if ok != want {
			t.Errorf("%s: EditCredits(max) ok = %v, want %v", p, ok, want)
		}
		if first, revision, ok := cur.EditCredits(p, credits.Standard); !ok || first != 6 || revision != 2 {
			t.Errorf("%s: standard costs %d and %d (ok %v), want 6 and 2", p, first, revision, ok)
		}
	}
	if first, revision, _ := cur.EditCredits(plans.Creator, credits.Max); first != 16 || revision != 7 {
		t.Errorf("a max edit costs %d and %d credits, want 16 and 7", first, revision)
	}
	if cur.Plans[plans.Trial].COGSBudgetMicros[credits.EditFirstMax] != 0 || cur.Plans[plans.Trial].COGSBudgetMicros[credits.EditRevisionMax] != 0 {
		t.Error("the trial has a budget for a max edit")
	}
	// The original list has none.
	old := loadOld(t)
	if old.Offers(credits.Max) {
		t.Error("2026-10 offers max edits")
	}
	if _, _, ok := old.EditCredits(plans.Creator, credits.Max); ok {
		t.Error("2026-10 prices a max edit")
	}
}

func TestActionForPicksTheActionOfTheEdit(t *testing.T) {
	for _, tc := range []struct {
		first bool
		q     credits.Quality
		want  credits.Action
	}{
		{true, credits.Standard, credits.EditFirst}, {false, credits.Standard, credits.EditRevision},
		{true, credits.Max, credits.EditFirstMax}, {false, credits.Max, credits.EditRevisionMax},
	} {
		if got := credits.ActionFor(tc.first, tc.q); got != tc.want || !got.IsEdit() {
			t.Errorf("ActionFor(%v, %s) = %s, want %s", tc.first, tc.q, got, tc.want)
		}
	}
	if credits.ExportFinal.IsEdit() || credits.IdeationRun.IsEdit() {
		t.Error("export or ideation is an edit")
	}
}

// A video is a first edit plus two revisions, so what it costs in credits comes from the list (the API and the CLI derive from it; no code
// says "1 video = 10 credits"). Standard videos: 10 credits and 2, 60, 15 and 40 a period, on the original list and on the current one.
func TestCreditsPerVideoFollowsTheList(t *testing.T) {
	cur, _ := credits.LoadDefault()
	old := loadOld(t)
	if cur.CreditsPerVideo() != 6+2*2 || old.CreditsPerVideo() != 6+2*2 {
		t.Fatalf("credits per video: current %d, 2026-10 %d", cur.CreditsPerVideo(), old.CreditsPerVideo())
	}
	for _, l := range []*credits.PriceList{old, cur} {
		for p, want := range map[plans.Plan]int{plans.Trial: 2, plans.Studio: 60, plans.Creator: 15, plans.Pro: 40} {
			if got := l.VideosPerPeriod(p); got != want {
				t.Errorf("%s: %s pays for %d videos, want %d", l.Version, p, got, want)
			}
		}
	}
	// A list that prices a standard edit differently changes the figure: nothing else holds the number.
	other, _ := credits.LoadDefault()
	other.Credits[credits.EditFirst] = 12
	if other.CreditsPerVideo() != 16 {
		t.Errorf("credits per video = %d with a 12-credit first edit, want 16", other.CreditsPerVideo())
	}
	if cur.VideosPerPeriod("nope") != 0 {
		t.Error("a plan the list does not have pays for videos")
	}
}

// An old price list is never edited, only left behind: it still loads, still passes the invariant, and still prices what it priced.
func TestTheOldPriceListIsKeptAsItWas(t *testing.T) {
	old := loadOld(t)
	if first, _ := old.Price(credits.EditFirst); first != 6 {
		t.Errorf("2026-10 prices a first edit at %d credits, want 6", first)
	}
	if b, _ := old.Budget(plans.Creator, credits.EditFirst); b != 960_000 {
		t.Errorf("2026-10 gives a creator's first edit %d, want 960,000", b)
	}
	if vs := old.Check(); len(vs) != 0 {
		t.Errorf("2026-10 fails its invariant: %v", vs)
	}
}

// The loader refuses a list that prices only one of the two extra careful edits, a plan that does not say whether it includes them, a plan
// that says it does not and still gives them a budget, and one that says it does and gives none.
func TestLoadRefusesBadMaxEditSections(t *testing.T) {
	good := string(mustReadFile(t, "2026-10c.yaml"))
	for name, tc := range map[string]struct{ content, want string }{
		"only the first":        {strings.Replace(good, "  edit.revision.max: 7\n", "", 1), "come together"},
		"no more than standard": {strings.Replace(good, "  edit.first.max: 16\n", "  edit.first.max: 6\n", 1), "must cost more"},
		"plan silent":           {strings.Replace(good, "    quality_max: true # on the creator's own key\n", "", 1), "quality_max must say"},
		"trial with a budget":   {strings.Replace(good, "      edit.first.max: 0\n", "      edit.first.max: 100\n", 1), "must be 0"},
		"max plan with none":    {strings.Replace(good, "      edit.first.max: 2000000\n      edit.revision.max: 2000000\n      export.final: 0\n      ideation.run: 500000", "      edit.first.max: 0\n      edit.revision.max: 2000000\n      export.final: 0\n      ideation.run: 500000", 1), "needs a budget"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := credits.LoadPriceList(fstest.MapFS{"2026-10c.yaml": {Data: []byte(tc.content)}}, "2026-10c.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one that mentions %q", err, tc.want)
			}
		})
	}
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(configfiles.Plans(), name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
