// Package plans is the catalogue of plans (product plan section 10): their names and what each includes. It knows
// nothing about who has which plan; that is the workspace_plans table (store.GetEntitlements).
//
// The numbers are the launch hypotheses of the product plan, to be validated in the beta. Billing itself arrives with
// P7-01; until then plans are granted by hand with `cfctl plan grant`.
package plans

// Plan is the name of a plan, as stored in workspace_plans.plan and sent to the web app.
type Plan string

// The plans. None is "no plan": the paywall.
const (
	None    Plan = "none"
	Trial   Plan = "trial"
	Studio  Plan = "studio"
	Creator Plan = "creator"
	Pro     Plan = "pro"
)

// Granted lists the plans that can be granted (every plan but None), cheapest first.
var Granted = []Plan{Trial, Studio, Creator, Pro}

// Valid reports whether p is one of the plans, None included.
func (p Plan) Valid() bool { return p == None || p.Grantable() }

// Grantable reports whether p is a plan that can be granted (it is not None).
func (p Plan) Grantable() bool {
	for _, g := range Granted {
		if p == g {
			return true
		}
	}
	return false
}

// AIBilling says who pays for the model calls (D-18).
type AIBilling string

// The two ways to pay for the AI.
const (
	BilledByUs      AIBilling = "managed"
	BilledByCreator AIBilling = "byok"
)

// Limits is what a plan includes. A zero number means "not included".
type Limits struct {
	// Exports is the number of exported videos per month (the trial's two are in total, not per month).
	Exports   int       `json:"exports"`
	StorageGB int       `json:"storage_gb"`
	AIBilling AIBilling `json:"ai_billing"`
}

// LimitsOf returns what the plan includes. A workspace with no plan (None, or a name this build does not know) gets
// the zero Limits: nothing.
func LimitsOf(p Plan) Limits {
	switch p {
	case Trial:
		return Limits{Exports: 2, StorageGB: 2, AIBilling: BilledByUs}
	case Studio:
		return Limits{Exports: 60, StorageGB: 100, AIBilling: BilledByCreator}
	case Creator:
		return Limits{Exports: 15, StorageGB: 50, AIBilling: BilledByUs}
	case Pro:
		return Limits{Exports: 40, StorageGB: 200, AIBilling: BilledByUs}
	default:
		return Limits{}
	}
}
