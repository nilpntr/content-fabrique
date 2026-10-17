// Package credits is the customer-facing half of spend control (P7-01a, docs/design/spend-control-v1.md sections 3 to 5, decision
// D-31): the price list and the plans as versioned data, the profit invariant that is checked over them, and the service that decides
// what an editor run costs and sets the credits aside for it.
//
// The money itself lives in the store (credit_accounts, credit_ledger, credit_holds). This package never touches the database except
// through store; the price comes from the price list only, never from a client.
package credits

import (
	"fmt"
	"io/fs"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ListFile is the price list the code loads from config/plans. A change is a new file and a new version: ledger rows name the version
// that priced them.
const ListFile = "2026-10.yaml"

// Action is something the customer pays credits for.
type Action string

// The actions of the price list. A file that lacks one of them, or has another, does not load.
const (
	EditFirst    Action = "edit.first"
	EditRevision Action = "edit.revision"
	ExportFinal  Action = "export.final"
	IdeationRun  Action = "ideation.run"
)

// Actions lists every action, in the order of the price list.
var Actions = []Action{EditFirst, EditRevision, ExportFinal, IdeationRun}

// Valid reports whether a is one of the actions.
func (a Action) Valid() bool { return slices.Contains(Actions, a) }

// BillingMode says who pays for the model calls (D-18).
type BillingMode string

// The two ways to pay for the AI.
const (
	Managed BillingMode = "managed"
	BYOK    BillingMode = "byok"
)

// Plan is what one plan includes in a price list.
type Plan struct {
	Name          plans.Plan
	PriceEURCents int64
	BillingMode   BillingMode
	// AllowanceCredits is granted at the start of every period (Refill) or once (a trial).
	AllowanceCredits int
	Refill           bool
	StorageGB        int
	ConcurrentRuns   int
	// TranscribedMinutesPerDay caps hosted transcription per UTC day (P7-09b enforces it per plan; the env cap holds until then).
	TranscribedMinutesPerDay int
	// COGSBudgetMicros is the hard budget_micros of the run that performs an action. On a BYOK plan it protects the creator's own key.
	COGSBudgetMicros map[Action]int64
	// NonLLMCOGSBudgetMicros is, on a BYOK plan, what an action may cost US in compute, storage and transcription. The invariant
	// checks it; the compute is metered, not refused. Empty on a managed plan, whose run budget covers everything.
	NonLLMCOGSBudgetMicros map[Action]int64
}

// PriceList is one version of the price list and the plans.
type PriceList struct {
	Version string
	Source  string
	// Credits is the price of each action in credits.
	Credits map[Action]int
	Plans   map[plans.Plan]Plan
	// MoRFeePct is the merchant of record's percentage of each payment (0.05 is 5 %), MoRFeeFixedEURCents its fixed amount.
	MoRFeePct          *big.Rat
	MoRFeeFixedEURCent int64
	USDPerEUR          *big.Rat
	// MaxCOGSShare is the largest share of what a credit earns that its action's COGS may be (0.40).
	MaxCOGSShare      *big.Rat
	TopUpExpiryMonths int
	// TrialCOGSAllowanceMicros is the fixed COGS a trial may cost, spread over the trial's credits.
	TrialCOGSAllowanceMicros int64
}

// decimal is a number written with decimals in the YAML (0.05, 1.08), kept exact: a profit invariant must not depend on how a binary
// float rounds.
type decimal struct{ r *big.Rat }

func (d *decimal) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected a decimal number", n.Line)
	}
	r, ok := new(big.Rat).SetString(n.Value)
	if !ok || strings.ContainsAny(n.Value, "eE/") {
		return fmt.Errorf("line %d: %q is not a plain decimal number", n.Line, n.Value)
	}
	d.r = r
	return nil
}

type planFile struct {
	PriceEURCents          int64            `yaml:"price_eur_cents"`
	BillingMode            string           `yaml:"billing_mode"`
	AllowanceCredits       int              `yaml:"allowance_credits"`
	Refill                 bool             `yaml:"refill"`
	StorageGB              int              `yaml:"storage_gb"`
	ConcurrentRuns         int              `yaml:"concurrent_runs"`
	TranscribedMinutesDay  int              `yaml:"transcribed_minutes_per_day"`
	COGSBudgetMicros       map[string]int64 `yaml:"cogs_budget_micros"`
	NonLLMCOGSBudgetMicros map[string]int64 `yaml:"non_llm_cogs_budget_micros"`
}

type listFile struct {
	Version                  string              `yaml:"version"`
	Source                   string              `yaml:"source"`
	Notes                    []string            `yaml:"notes"`
	Actions                  map[string]int      `yaml:"actions"`
	MoRFeePct                decimal             `yaml:"mor_fee_pct"`
	MoRFeeFixedEURCents      int64               `yaml:"mor_fee_fixed_eur_cents"`
	USDPerEUR                decimal             `yaml:"usd_per_eur"`
	MaxCOGSShare             decimal             `yaml:"max_cogs_share"`
	TopUpExpiryMonths        int                 `yaml:"topup_expiry_months"`
	TrialCOGSAllowanceMicros int64               `yaml:"trial_cogs_allowance_micros"`
	Plans                    map[string]planFile `yaml:"plans"`
}

var versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,39}$`)

// LoadPriceList reads one price list (2026-10.yaml) from the plans directory and checks its shape: every action and every plan present,
// no unknown field, no negative number. It does not check the profit invariant: Check does, and a test runs it over every file.
func LoadPriceList(fsys fs.FS, name string) (*PriceList, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("credits: read price list %s: %w", name, err)
	}
	var f listFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // a misspelt budget must not silently become "no budget"
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("credits: parse price list %s: %w", name, err)
	}
	if !versionRE.MatchString(f.Version) {
		return nil, fmt.Errorf("credits: price list %s: version %q is missing or not a short plain string", name, f.Version)
	}
	if f.Version+".yaml" != name {
		return nil, fmt.Errorf("credits: price list %s declares version %q: the file name must be <version>.yaml", name, f.Version)
	}
	l := &PriceList{
		Version: f.Version, Source: f.Source, Credits: map[Action]int{}, Plans: map[plans.Plan]Plan{},
		MoRFeePct: f.MoRFeePct.r, MoRFeeFixedEURCent: f.MoRFeeFixedEURCents, USDPerEUR: f.USDPerEUR.r, MaxCOGSShare: f.MaxCOGSShare.r,
		TopUpExpiryMonths: f.TopUpExpiryMonths, TrialCOGSAllowanceMicros: f.TrialCOGSAllowanceMicros,
	}
	fail := func(format string, args ...any) (*PriceList, error) {
		return nil, fmt.Errorf("credits: price list %s: "+format, append([]any{name}, args...)...)
	}
	switch {
	case l.MoRFeePct == nil || l.MoRFeePct.Sign() < 0 || l.MoRFeePct.Cmp(big.NewRat(1, 1)) >= 0:
		return fail("mor_fee_pct must be at least 0 and below 1")
	case l.MoRFeeFixedEURCent < 0:
		return fail("mor_fee_fixed_eur_cents must not be negative")
	case l.USDPerEUR == nil || l.USDPerEUR.Sign() <= 0:
		return fail("usd_per_eur must be positive")
	case l.MaxCOGSShare == nil || l.MaxCOGSShare.Sign() <= 0 || l.MaxCOGSShare.Cmp(big.NewRat(1, 1)) > 0:
		return fail("max_cogs_share must be above 0 and at most 1")
	case l.TopUpExpiryMonths < 1 || l.TopUpExpiryMonths > 60:
		return fail("topup_expiry_months must be between 1 and 60")
	case l.TrialCOGSAllowanceMicros <= 0:
		return fail("trial_cogs_allowance_micros must be positive")
	}
	for k, c := range f.Actions {
		a := Action(k)
		if !a.Valid() {
			return fail("unknown action %q", k)
		}
		if c < 0 {
			return fail("action %s costs a negative number of credits", k)
		}
		l.Credits[a] = c
	}
	for _, a := range Actions {
		if _, ok := l.Credits[a]; !ok {
			return fail("action %s has no price (a free action is 0)", a)
		}
	}
	for k, pf := range f.Plans {
		p := plans.Plan(k)
		if !p.Grantable() {
			return fail("unknown plan %q", k)
		}
		pl := Plan{
			Name: p, PriceEURCents: pf.PriceEURCents, BillingMode: BillingMode(pf.BillingMode), AllowanceCredits: pf.AllowanceCredits,
			Refill: pf.Refill, StorageGB: pf.StorageGB, ConcurrentRuns: pf.ConcurrentRuns, TranscribedMinutesPerDay: pf.TranscribedMinutesDay,
			COGSBudgetMicros: map[Action]int64{}, NonLLMCOGSBudgetMicros: map[Action]int64{},
		}
		switch {
		case pl.BillingMode != Managed && pl.BillingMode != BYOK:
			return fail("plan %s: billing_mode must be managed or byok", k)
		case pl.PriceEURCents < 0 || pl.AllowanceCredits <= 0 || pl.StorageGB < 0 || pl.ConcurrentRuns < 1 || pl.TranscribedMinutesPerDay < 0:
			return fail("plan %s: a price, an allowance, a quota or a run limit is out of range", k)
		case pl.PriceEURCents == 0 && pl.Refill:
			return fail("plan %s: a free plan cannot refill its allowance", k)
		}
		for ak, v := range pf.COGSBudgetMicros {
			if !Action(ak).Valid() || v < 0 {
				return fail("plan %s: cogs_budget_micros %q is not a known action or is negative", k, ak)
			}
			pl.COGSBudgetMicros[Action(ak)] = v
		}
		for ak, v := range pf.NonLLMCOGSBudgetMicros {
			if !Action(ak).Valid() || v < 0 {
				return fail("plan %s: non_llm_cogs_budget_micros %q is not a known action or is negative", k, ak)
			}
			pl.NonLLMCOGSBudgetMicros[Action(ak)] = v
		}
		for _, a := range Actions {
			if _, ok := pl.COGSBudgetMicros[a]; !ok {
				return fail("plan %s: no cogs_budget_micros for %s (an action that costs nothing is 0)", k, a)
			}
			if pl.BillingMode == BYOK {
				if _, ok := pl.NonLLMCOGSBudgetMicros[a]; !ok {
					return fail("plan %s: a BYOK plan needs non_llm_cogs_budget_micros for %s", k, a)
				}
			}
		}
		if pl.BillingMode == Managed && len(pl.NonLLMCOGSBudgetMicros) > 0 {
			return fail("plan %s: non_llm_cogs_budget_micros belongs to a BYOK plan; a managed plan's run budget covers everything", k)
		}
		l.Plans[p] = pl
	}
	for _, p := range plans.Granted {
		if _, ok := l.Plans[p]; !ok {
			return fail("plan %s is missing", p)
		}
	}
	return l, nil
}

// ListFiles lists the price list files of the directory, oldest name first.
func ListFiles(fsys fs.FS) ([]string, error) {
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// Price returns the price of the action in credits. An unknown action is a programming error: ok is false.
func (l *PriceList) Price(a Action) (credits int, ok bool) {
	c, ok := l.Credits[a]
	return c, ok
}

// Plan returns what the plan includes. ok is false for none and for a plan the list does not have.
func (l *PriceList) Plan(p plans.Plan) (Plan, bool) {
	pl, ok := l.Plans[p]
	return pl, ok
}

// Budget returns the run budget, in micro-dollars, that the plan gives the action.
func (l *PriceList) Budget(p plans.Plan, a Action) (int64, bool) {
	pl, ok := l.Plans[p]
	if !ok {
		return 0, false
	}
	b, ok := pl.COGSBudgetMicros[a]
	return b, ok
}

// AllowanceSpecs is what the store needs to grant allowances: the credits each plan gets, whether it refills, and this list's version.
func (l *PriceList) AllowanceSpecs() map[plans.Plan]store.AllowanceSpec {
	out := make(map[plans.Plan]store.AllowanceSpec, len(l.Plans))
	for p, pl := range l.Plans {
		out[p] = store.AllowanceSpec{Plan: p, Credits: pl.AllowanceCredits, Refill: pl.Refill, ListVersion: l.Version}
	}
	return out
}
