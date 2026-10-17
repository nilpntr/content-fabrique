package credits

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/nilpntr/content-fabrique/internal/plans"
)

// Violation is one budget that breaks the profit invariant.
type Violation struct {
	Plan   plans.Plan
	Action Action
	// Budget is the budget in the file; Limit is the most the invariant allows, both in micro-dollars (Limit rounded down).
	Budget int64
	Limit  int64
	// Rule says which form of the invariant failed.
	Rule string
}

func (v Violation) String() string {
	return fmt.Sprintf("plan %s, action %s: budget %d micro-dollars is over the limit of %d (%s)", v.Plan, v.Action, v.Budget, v.Limit, v.Rule)
}

// NetRevenuePerCreditMicros is what one credit of the plan earns us, in micro-dollars, after the merchant of record's fee:
//
//	(price x (1 - fee_pct) - fee_fixed) x usd_per_eur / allowance_credits
//
// exactly (a rational number). A free plan earns nothing.
func (l *PriceList) NetRevenuePerCreditMicros(p Plan) *big.Rat {
	if p.PriceEURCents == 0 || p.AllowanceCredits == 0 {
		return new(big.Rat)
	}
	net := new(big.Rat).SetInt64(p.PriceEURCents)
	net.Mul(net, new(big.Rat).Sub(big.NewRat(1, 1), l.MoRFeePct))
	net.Sub(net, new(big.Rat).SetInt64(l.MoRFeeFixedEURCent))
	net.Mul(net, l.USDPerEUR)
	net.Mul(net, big.NewRat(10_000, 1)) // euro cents to euros (/100), dollars to micro-dollars (x1,000,000)
	return net.Quo(net, new(big.Rat).SetInt64(int64(p.AllowanceCredits)))
}

// Check returns every budget of the list that could lose money, in a stable order. Empty means the list is safe.
//
// The invariant (spend control section 4), for every action of every plan:
//
//	cogs_budget(action, plan) <= max_cogs_share x net_revenue_per_credit(plan) x credits(action)
//
// with three cases that the design note names:
//   - a managed plan: the run budget is checked against what the plan's credits earn;
//   - the trial: it earns nothing, so the run budget is checked against its fixed COGS allowance, spread over the trial's credits;
//   - a BYOK plan: its tokens are billed to the creator's key (RequiredBilling; a run on any other key is refused), so its run budget is
//     not our cost; the non-LLM budget (compute, storage, transcription), which is our cost, is checked against what the plan's credits
//     earn. If the rule did not make the creator's key pay, the run budget would be our cost and is checked like a managed plan's.
func (l *PriceList) Check() []Violation { return l.CheckBilling(RequiredBilling) }

// CheckBilling is Check for a given rule of who pays for the model calls of each billing mode. A BYOK plan's run budget is left out of the
// invariant only when the rule makes the creator's own key pay for it; under any other rule that budget is our cost and is checked like a
// managed plan's, which a BYOK plan's $2 cannot pass. This is the assumption written down as code: the runtime refuses a BYOK run on our
// key (RequiredBilling), and a test fails if the rule and the list ever disagree.
func (l *PriceList) CheckBilling(payer func(BillingMode) string) []Violation {
	var out []Violation
	planNames := make([]string, 0, len(l.Plans))
	for p := range l.Plans {
		planNames = append(planNames, string(p))
	}
	sort.Strings(planNames)
	for _, name := range planNames {
		p := l.Plans[plans.Plan(name)]
		for _, a := range Actions {
			credits := int64(l.Credits[a])
			var (
				budget int64
				limit  *big.Rat
				rule   string
			)
			switch {
			case p.PriceEURCents == 0:
				// A trial: a fixed COGS allowance per trial, spread over its credits.
				budget = p.COGSBudgetMicros[a]
				limit = new(big.Rat).SetFrac64(l.TrialCOGSAllowanceMicros*credits, int64(p.AllowanceCredits))
				rule = "trial: allowance x credits / trial credits"
			case p.BillingMode == BYOK && payer(BYOK) == BilledToWorkspaceKey:
				budget = p.NonLLMCOGSBudgetMicros[a]
				limit = l.shareOfRevenue(p, credits)
				rule = "byok: non-LLM budget <= max share x net revenue x credits"
			default:
				budget = p.COGSBudgetMicros[a]
				limit = l.shareOfRevenue(p, credits)
				rule = "managed: budget <= max share x net revenue x credits"
			}
			if new(big.Rat).SetInt64(budget).Cmp(limit) > 0 {
				floor := new(big.Int).Quo(limit.Num(), limit.Denom())
				out = append(out, Violation{Plan: p.Name, Action: a, Budget: budget, Limit: floor.Int64(), Rule: rule})
			}
		}
	}
	return out
}

func (l *PriceList) shareOfRevenue(p Plan, credits int64) *big.Rat {
	r := l.NetRevenuePerCreditMicros(p)
	r.Mul(r, l.MaxCOGSShare)
	return r.Mul(r, new(big.Rat).SetInt64(credits))
}
