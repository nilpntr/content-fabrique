package credits_test

import (
	"io/fs"
	"math/big"
	"strings"
	"testing"
	"testing/fstest"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/plans"
)

// Every price list file in config/plans must hold the profit invariant (spend control v1 section 4):
//
//	cogs_budget(action, plan) <= max_cogs_share x net_revenue_per_credit(plan) x credits(action)
//
// A new file is picked up here by itself; a plan or an action that could lose money fails the build.
func TestEveryPriceListFileHoldsTheProfitInvariant(t *testing.T) {
	names, err := credits.ListFiles(configfiles.Plans())
	if err != nil || len(names) == 0 {
		t.Fatalf("price list files = %v, %v", names, err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			l, err := credits.LoadPriceList(configfiles.Plans(), name)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range l.Check() {
				t.Errorf("%s: %s", name, v)
			}
		})
	}
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

// The file the code runs on is one of them.
func TestTheDefaultPriceListLoads(t *testing.T) {
	l, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	if l.Version+".yaml" != credits.ListFile {
		t.Fatalf("version %q for file %s", l.Version, credits.ListFile)
	}
}

// The values of the design note (spend control v1 sections 3 and 4) are in the file.
func TestThePriceListHasTheDesignNotesValues(t *testing.T) {
	l, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	for a, want := range map[credits.Action]int{credits.EditFirst: 16, credits.EditRevision: 7, credits.ExportFinal: 0, credits.IdeationRun: 1} {
		if got, ok := l.Price(a); !ok || got != want {
			t.Errorf("%s costs %d, want %d", a, got, want)
		}
	}
	type row struct {
		mode                            credits.BillingMode
		allowance                       int
		refill                          bool
		storage, concurrent, minutesDay int
		price                           int64
	}
	for p, want := range map[plans.Plan]row{
		plans.Trial:   {credits.Managed, 30, false, 2, 1, 30, 0},
		plans.Studio:  {credits.BYOK, 1800, true, 100, 3, 240, 2500},
		plans.Creator: {credits.Managed, 150, true, 50, 2, 120, 5900},
		plans.Pro:     {credits.Managed, 400, true, 200, 4, 360, 12900},
	} {
		got, ok := l.Plan(p)
		if !ok {
			t.Fatalf("no plan %s", p)
		}
		have := row{got.BillingMode, got.AllowanceCredits, got.Refill, got.StorageGB, got.ConcurrentRuns, got.TranscribedMinutesPerDay, got.PriceEURCents}
		if have != want {
			t.Errorf("plan %s = %+v, want %+v", p, have, want)
		}
		for _, a := range credits.Actions {
			if _, ok := got.COGSBudgetMicros[a]; !ok {
				t.Errorf("plan %s has no budget for %s", p, a)
			}
		}
		if got.UnchargedRunsPerDay != 5 {
			t.Errorf("plan %s allows %d runs per day that end without a charge, want 5", p, got.UnchargedRunsPerDay)
		}
	}
	if l.TopUpExpiryMonths != 12 || l.MaxCOGSShare.Cmp(big.NewRat(40, 100)) != 0 || l.MoRFeePct.Cmp(big.NewRat(5, 100)) != 0 {
		t.Errorf("top-up expiry %d, share %v, fee %v", l.TopUpExpiryMonths, l.MaxCOGSShare, l.MoRFeePct)
	}
	// export.final is free, so no plan may budget anything for it.
	for p, pl := range l.Plans {
		if b := pl.COGSBudgetMicros[credits.ExportFinal]; b != 0 {
			t.Errorf("plan %s budgets %d for a free action", p, b)
		}
	}
}

// The worked example of the design note: the creator plan earns about $0.40 per credit, so 16 credits allow a first edit of up to $2.56 and
// 7 credits a revision of up to $1.12; the list gives the editor's max-effort runs $2.00 and $0.80 (D-31).
func TestWorkedExampleCreator(t *testing.T) {
	l, _ := credits.LoadDefault()
	creator, _ := l.Plan(plans.Creator)
	net, _ := l.NetRevenuePerCreditMicros(creator).Float64()
	if net < 400_100 || net > 400_300 {
		t.Fatalf("net revenue per credit = %.1f micro-dollars, want about 400,176", net)
	}
	first, _ := l.Budget(plans.Creator, credits.EditFirst)
	revision, _ := l.Budget(plans.Creator, credits.EditRevision)
	if first != 2_000_000 || revision != 800_000 {
		t.Fatalf("budgets %d and %d, want 2,000,000 and 800,000", first, revision)
	}
	// What the rule allows is above them (16 x 0.4 x 400,176 = 2.56 million), so the list is not on the edge for creator.
	limit := func(credits int64) int64 {
		r := l.NetRevenuePerCreditMicros(creator)
		r.Mul(r, l.MaxCOGSShare)
		f, _ := r.Mul(r, new(big.Rat).SetInt64(credits)).Float64()
		return int64(f)
	}
	if got := limit(16); got < 2_560_000 || got > 2_562_000 {
		t.Fatalf("the rule allows %d for 16 credits, want about 2,561,000", got)
	}
}

// D-31 (pending founder confirmation): the editor thinks at max effort, so a first edit and a revision get larger hard run budgets than
// 2026-10 gave them. The profit invariant is NOT loosened to get there: the prices, the merchant fee, the exchange rate and the 40 %
// share are those of 2026-10, and so are the allowances of the managed plans that pay (the credits per action went up instead). The list
// passes the invariant, and it passes it with the fewest credits: one credit less on an edit would break it for some plan, so the
// customer is not charged more credits than the budgets need. No dollar figure is pinned here: the budgets are the list's to choose, the
// invariant's to bound.
func TestMaxEffortBudgetsFitTheInvariantWithTheFewestCredits(t *testing.T) {
	cur, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if vs := cur.Check(); len(vs) != 0 {
		t.Fatalf("the current list breaks the invariant: %v", vs)
	}
	for _, p := range plans.Granted {
		cp, op := cur.Plans[p], old.Plans[p]
		if cp.PriceEURCents != op.PriceEURCents || cp.BillingMode != op.BillingMode {
			t.Errorf("%s: price or billing mode changed (%d %s, was %d %s)", p, cp.PriceEURCents, cp.BillingMode, op.PriceEURCents, op.BillingMode)
		}
		if p == plans.Creator || p == plans.Pro {
			if cp.AllowanceCredits != op.AllowanceCredits {
				t.Errorf("%s: allowance changed to %d (was %d)", p, cp.AllowanceCredits, op.AllowanceCredits)
			}
		}
		// A max-effort edit needs more than the old budgets gave it, on every managed plan (the point of the new list).
		if p != plans.Studio {
			for _, a := range []credits.Action{credits.EditFirst, credits.EditRevision} {
				if cur.Plans[p].COGSBudgetMicros[a] <= old.Plans[p].COGSBudgetMicros[a] {
					t.Errorf("%s %s: budget %d is not above the old %d", p, a, cur.Plans[p].COGSBudgetMicros[a], old.Plans[p].COGSBudgetMicros[a])
				}
			}
		}
	}
	if cur.MaxCOGSShare.Cmp(old.MaxCOGSShare) != 0 || cur.MoRFeePct.Cmp(old.MoRFeePct) != 0 || cur.MoRFeeFixedEURCent != old.MoRFeeFixedEURCent || cur.USDPerEUR.Cmp(old.USDPerEUR) != 0 {
		t.Errorf("share, fee or exchange rate changed: %v %v %d %v", cur.MaxCOGSShare, cur.MoRFeePct, cur.MoRFeeFixedEURCent, cur.USDPerEUR)
	}
	// The fewest credits: take one off an edit and the invariant must fail for at least one plan.
	for _, a := range []credits.Action{credits.EditFirst, credits.EditRevision} {
		cheaper, _ := credits.LoadDefault()
		cheaper.Credits[a]--
		if len(cheaper.Check()) == 0 {
			t.Errorf("%s costs %d credits, but %d would hold the invariant too: the customer pays more credits than the budgets need", a, cur.Credits[a], cheaper.Credits[a])
		}
	}
	// The one trial: a first edit and two revisions fit its credits, and its fixed COGS allowance is what the budgets of one video need
	// (the invariant above is what ties the allowance to the budgets).
	trial := cur.Plans[plans.Trial]
	if trial.AllowanceCredits < cur.CreditsPerVideo() {
		t.Errorf("trial: %d credits for a %d-credit video", trial.AllowanceCredits, cur.CreditsPerVideo())
	}
}

// A video is a first edit plus two revisions, so what it costs in credits comes from the list (the API and the CLI derive from it; no
// code says "1 video = 10 credits"). The old list still says 10, and its plans paid for 2, 60, 15 and 40 videos; the current one says 30.
func TestCreditsPerVideoFollowsTheList(t *testing.T) {
	cur, _ := credits.LoadDefault()
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cur.CreditsPerVideo() != 16+2*7 || old.CreditsPerVideo() != 6+2*2 {
		t.Fatalf("credits per video: current %d, 2026-10 %d", cur.CreditsPerVideo(), old.CreditsPerVideo())
	}
	for _, tc := range []struct {
		list *credits.PriceList
		want map[plans.Plan]int
	}{
		{old, map[plans.Plan]int{plans.Trial: 2, plans.Studio: 60, plans.Creator: 15, plans.Pro: 40}},
		{cur, map[plans.Plan]int{plans.Trial: 1, plans.Studio: 60, plans.Creator: 5, plans.Pro: 13}},
	} {
		for p, want := range tc.want {
			if got := tc.list.VideosPerPeriod(p); got != want {
				t.Errorf("%s: %s pays for %d videos, want %d", tc.list.Version, p, got, want)
			}
		}
	}
	if cur.VideosPerPeriod("nope") != 0 {
		t.Error("a plan the list does not have pays for videos")
	}
}

// An old price list is never edited, only left behind: it still loads, still passes the invariant, and still prices what it priced.
func TestTheOldPriceListIsKeptAsItWas(t *testing.T) {
	old, err := credits.LoadPriceList(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
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

// The test is only worth something if it fails when a plan could lose money. The boundary is exact: the largest budget the rule allows
// passes and one micro-dollar more fails, for every managed plan and every action that has a price.
func TestInvariantBoundaryIsExact(t *testing.T) {
	l, _ := credits.LoadDefault()
	for _, p := range []plans.Plan{plans.Trial, plans.Creator, plans.Pro} {
		for _, a := range credits.Actions {
			if l.Credits[a] == 0 {
				continue
			}
			pl := l.Plans[p]
			// Find the limit by setting an absurd budget and reading what the check says.
			pl.COGSBudgetMicros = clone(pl.COGSBudgetMicros)
			pl.COGSBudgetMicros[a] = 1 << 40
			l.Plans[p] = pl
			var limit int64 = -1
			for _, v := range l.Check() {
				if v.Plan == p && v.Action == a {
					limit = v.Limit
				}
			}
			if limit <= 0 {
				t.Fatalf("%s %s: no limit reported", p, a)
			}
			pl.COGSBudgetMicros[a] = limit
			if vs := l.Check(); len(vs) != 0 {
				t.Errorf("%s %s: a budget of exactly the limit (%d) was refused: %v", p, a, limit, vs)
			}
			pl.COGSBudgetMicros[a] = limit + 1
			if vs := l.Check(); len(vs) != 1 || vs[0].Plan != p || vs[0].Action != a {
				t.Errorf("%s %s: a budget one over the limit (%d) was not caught: %v", p, a, limit+1, vs)
			}
			pl.COGSBudgetMicros[a] = limit // back to something that passes
		}
	}
}

func clone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// A bad fixture: a price list whose creator plan lets a first edit cost $1.50 (limit about $0.96), whose trial allows $3 against a $2.50
// allowance and whose BYOK plan allows $0.50 of our own compute per edit. Each must be named.
const badList = `
version: "2099-01"
source: a deliberately bad fixture for the profit-invariant test
actions: {edit.first: 6, edit.revision: 2, export.final: 0, ideation.run: 1}
mor_fee_pct: 0.05
mor_fee_fixed_eur_cents: 47
usd_per_eur: 1.08
max_cogs_share: 0.40
topup_expiry_months: 12
trial_cogs_allowance_micros: 2500000
plans:
  trial:
    price_eur_cents: 0
    billing_mode: managed
    allowance_credits: 20
    refill: false
    storage_gb: 2
    concurrent_runs: 1
    uncharged_runs_per_day: 5
    transcribed_minutes_per_day: 30
    cogs_budget_micros: {edit.first: 1000000, edit.revision: 250000, export.final: 0, ideation.run: 125000}
  studio:
    price_eur_cents: 2500
    billing_mode: byok
    allowance_credits: 600
    refill: true
    storage_gb: 100
    concurrent_runs: 3
    uncharged_runs_per_day: 5
    transcribed_minutes_per_day: 240
    cogs_budget_micros: {edit.first: 2000000, edit.revision: 2000000, export.final: 0, ideation.run: 500000}
    non_llm_cogs_budget_micros: {edit.first: 500000, edit.revision: 33000, export.final: 0, ideation.run: 16000}
  creator:
    price_eur_cents: 5900
    billing_mode: managed
    allowance_credits: 150
    refill: true
    storage_gb: 50
    concurrent_runs: 2
    uncharged_runs_per_day: 5
    transcribed_minutes_per_day: 120
    cogs_budget_micros: {edit.first: 1500000, edit.revision: 320000, export.final: 0, ideation.run: 160000}
  pro:
    price_eur_cents: 12900
    billing_mode: managed
    allowance_credits: 400
    refill: true
    storage_gb: 200
    concurrent_runs: 4
    uncharged_runs_per_day: 5
    transcribed_minutes_per_day: 360
    cogs_budget_micros: {edit.first: 790000, edit.revision: 260000, export.final: 0, ideation.run: 130000}
`

func TestInvariantFailsOnABadFixture(t *testing.T) {
	l, err := credits.LoadPriceList(fstest.MapFS{"2099-01.yaml": {Data: []byte(badList)}}, "2099-01.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, v := range l.Check() {
		got[string(v.Plan)+" "+string(v.Action)] = true
		if v.Budget <= v.Limit {
			t.Errorf("a violation within its limit: %s", v)
		}
	}
	for _, want := range []string{"creator edit.first", "trial edit.first", "studio edit.first"} {
		if !got[want] {
			t.Errorf("the bad fixture's %s was not caught (caught %v)", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("caught %v, want exactly the three bad budgets", got)
	}
}

// A fee that eats the price makes every credit lose money, whatever the budgets.
func TestInvariantFailsWhenTheFeeTakesTheRevenue(t *testing.T) {
	l, _ := credits.LoadDefault()
	l.MoRFeeFixedEURCent = 5900 // the whole price of a creator payment
	var creator int
	for _, v := range l.Check() {
		if v.Plan == plans.Creator {
			creator++
		}
	}
	if creator == 0 {
		t.Fatal("a fee that takes the whole price of the creator plan was not caught")
	}
}

// A price list that is not well formed does not load, so it cannot be half-trusted.
func TestLoadRefusesBadFiles(t *testing.T) {
	good := string(mustRead(t))
	for name, tc := range map[string]struct {
		file, content string
		want          string
	}{
		"wrong file name":        {"2026-11.yaml", good, "file name must be"},
		"missing action":         {"2026-10.yaml", strings.Replace(good, "  ideation.run: 1\n", "", 1), "ideation.run has no price"},
		"unknown action":         {"2026-10.yaml", strings.Replace(good, "  ideation.run: 1\n", "  ideation.run: 1\n  edit.third: 9\n", 1), "unknown action"},
		"unknown field":          {"2026-10.yaml", strings.Replace(good, "topup_expiry_months: 12", "topup_expiry_months: 12\nbonus_credits: 5", 1), "bonus_credits"},
		"exponent in a decimal":  {"2026-10.yaml", strings.Replace(good, "usd_per_eur: 1.08", "usd_per_eur: 1.08e0", 1), "plain decimal"},
		"negative budget":        {"2026-10.yaml", strings.Replace(good, "edit.first: 960000", "edit.first: -1", 1), "negative"},
		"missing plan":           {"2026-10.yaml", cutPlan(good, "  pro:"), "plan pro is missing"},
		"byok without non-llm":   {"2026-10.yaml", strings.Replace(good, "    non_llm_cogs_budget_micros: # 0.40 x $0.0419 net per credit = $0.0168 per credit\n      edit.first: 100000\n", "    non_llm_cogs_budget_micros:\n", 1), "non_llm_cogs_budget_micros"},
		"free plan that refills": {"2026-10.yaml", strings.Replace(good, "refill: false", "refill: true", 1), "free plan cannot refill"},
		"no allowance":           {"2026-10.yaml", strings.Replace(good, "allowance_credits: 20", "allowance_credits: 0", 1), "out of range"},
		"no uncharged cap":       {"2026-10.yaml", strings.Replace(good, "uncharged_runs_per_day: 5", "uncharged_runs_per_day: 0", 1), "out of range"},
		"share above one":        {"2026-10.yaml", strings.Replace(good, "max_cogs_share: 0.40", "max_cogs_share: 1.40", 1), "max_cogs_share"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := credits.LoadPriceList(fstest.MapFS{tc.file: {Data: []byte(tc.content)}}, tc.file)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one that mentions %q", err, tc.want)
			}
		})
	}
}

func mustRead(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(configfiles.Plans(), "2026-10.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cutPlan removes the plan that starts at the marker line, up to the next plan.
func cutPlan(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return s
	}
	return s[:i]
}

// A price list is a new file, never an edit: an old file keeps loading, whatever the newest one says.
func TestPriceListFilesAreNamedByVersion(t *testing.T) {
	names, _ := credits.ListFiles(configfiles.Plans())
	for _, n := range names {
		l, err := credits.LoadPriceList(configfiles.Plans(), n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if l.Version+".yaml" != n {
			t.Errorf("%s declares version %s", n, l.Version)
		}
	}
}

// The invariant leaves a BYOK plan's run budget out only because the creator's own key pays for it, and the runtime refuses a BYOK run on
// any other key. If that rule ever stopped holding, the budget would be our cost: the check says so (the assumption is code, not a skip).
func TestInvariantAssumesByokRunsArePaidByTheCreatorsKey(t *testing.T) {
	if credits.RequiredBilling(credits.BYOK) != credits.BilledToWorkspaceKey || credits.RequiredBilling(credits.Managed) != "" {
		t.Fatalf("required billing: byok %q, managed %q", credits.RequiredBilling(credits.BYOK), credits.RequiredBilling(credits.Managed))
	}
	l, _ := credits.LoadDefault()
	if vs := l.Check(); len(vs) != 0 {
		t.Fatalf("the list fails its own invariant: %v", vs)
	}
	// A rule under which our key pays for a BYOK run: the studio plan's $2 per edit is far over what its credits earn.
	ours := func(credits.BillingMode) string { return "platform" }
	got := map[credits.Action]bool{}
	for _, v := range l.CheckBilling(ours) {
		if v.Plan != plans.Studio {
			t.Errorf("unexpected violation %s", v)
		}
		got[v.Action] = true
	}
	for _, a := range []credits.Action{credits.EditFirst, credits.EditRevision, credits.IdeationRun} {
		if !got[a] {
			t.Errorf("a BYOK run billed to our key was not caught for %s", a)
		}
	}
}
