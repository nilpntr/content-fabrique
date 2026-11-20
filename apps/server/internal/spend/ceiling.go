package spend

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
)

// The per-edit spend ceiling (the cost guard): the most one agent run (an edit is one editor run) may spend, whatever its plan's budget
// or its caller asks for. It sits on top of the run's budget: the run's dollar budget is the lesser of the two, so the runtime's budget
// enforcement (a reservation before every model call, the agent's graceful finish at the limit) stops the run at the ceiling, mid-run,
// with the newest version it made. The runtime also applies it when a run is claimed, so a ceiling lowered while a run waits or after
// a worker died holds for the rest of that run.
//
// It is set by three variables, the most specific winning:
//
//	EDIT_SPEND_CEILING_WORKSPACES  wks_...=micros,wks_...=micros   one workspace
//	EDIT_SPEND_CEILING_PLANS       creator=micros,pro=micros       every workspace on a plan
//	EDIT_SPEND_CEILING_MICROS      micros                          everything else (default DefaultEditCeilingMicros, $2.00)
//
// 0 in a list switches the ceiling off for that workspace or plan (the run keeps its own budget); EDIT_SPEND_CEILING_MICROS=0 switches
// the default off.

// DefaultEditCeilingMicros is $2.00: the founder's "about $2 for an edit is fine" (D-31), and the largest run budget any plan gives an
// edit (an extra careful first edit), so the default never cuts a plan's paid edit short; it does cut a development run, an eval run
// or an API run that asks for more (budget_usd allows up to $5).
const DefaultEditCeilingMicros int64 = 2_000_000

// MaxEditCeilingMicros is the largest ceiling the variables accept ($50): a typo of a few zeros is refused instead of meaning "no cap".
const MaxEditCeilingMicros int64 = 50_000_000

// Ceilings is the per-edit spend ceiling. The zero value has no ceiling at all (tests, and code that does not set one).
type Ceilings struct {
	// Default applies when neither the workspace nor its plan has its own; 0 means none.
	Default int64
	// Plans and Workspaces override Default; a 0 entry means no ceiling for that plan or workspace.
	Plans      map[plans.Plan]int64
	Workspaces map[uuid.UUID]int64
}

// Ceiling is the ceiling that applies to a run, and where it came from.
type Ceiling struct {
	// Micros is the ceiling; 0 means there is none.
	Micros int64
	// Source says which setting set it: "workspace", "plan <name>", "default" or "" when there is none.
	Source string
}

// For returns the ceiling of a workspace on a plan (plan may be empty: the workspace has none, or it is not known).
func (c Ceilings) For(ws ids.WorkspaceID, plan plans.Plan) Ceiling {
	if v, ok := c.Workspaces[ws.UUID]; ok {
		return Ceiling{Micros: v, Source: sourceIf(v, "workspace")}
	}
	if v, ok := c.Plans[plan]; ok && plan != "" {
		return Ceiling{Micros: v, Source: sourceIf(v, "plan "+string(plan))}
	}
	return Ceiling{Micros: c.Default, Source: sourceIf(c.Default, "default")}
}

// NeedsPlan says the answer of For can depend on the plan, so a caller that does not know the plan should read it.
func (c Ceilings) NeedsPlan() bool { return len(c.Plans) > 0 }

// Apply returns budget lowered to the ceiling (a ceiling of 0 leaves it as it is).
func (c Ceiling) Apply(budget int64) int64 {
	if c.Micros > 0 && budget > c.Micros {
		return c.Micros
	}
	return budget
}

func sourceIf(v int64, s string) string {
	if v <= 0 {
		return ""
	}
	return s
}

// LoadCeilings parses EDIT_SPEND_CEILING_MICROS, EDIT_SPEND_CEILING_PLANS and EDIT_SPEND_CEILING_WORKSPACES, returning every problem
// found. Messages name the variable and the entry, never a value that could be secret (there are none here).
func LoadCeilings(getenv func(string) string) (Ceilings, error) {
	var errs []error
	c := Ceilings{Default: DefaultEditCeilingMicros}
	if v := strings.TrimSpace(getenv("EDIT_SPEND_CEILING_MICROS")); v != "" {
		n, err := ceilingValue(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("EDIT_SPEND_CEILING_MICROS: %w", err))
		} else {
			c.Default = n
		}
	}
	if v := strings.TrimSpace(getenv("EDIT_SPEND_CEILING_PLANS")); v != "" {
		c.Plans = map[plans.Plan]int64{}
		err := eachPair(v, func(k string, n int64) error {
			p := plans.Plan(k)
			if !p.Grantable() {
				return fmt.Errorf("%q is not a plan (trial, studio, creator or pro)", k)
			}
			c.Plans[p] = n
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("EDIT_SPEND_CEILING_PLANS: %w", err))
		}
	}
	if v := strings.TrimSpace(getenv("EDIT_SPEND_CEILING_WORKSPACES")); v != "" {
		c.Workspaces = map[uuid.UUID]int64{}
		err := eachPair(v, func(k string, n int64) error {
			ws, err := ids.ParseWorkspaceID(k)
			if err != nil {
				return fmt.Errorf("%q is not a workspace id (wks_...)", k)
			}
			c.Workspaces[ws.UUID] = n
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("EDIT_SPEND_CEILING_WORKSPACES: %w", err))
		}
	}
	return c, errors.Join(errs...)
}

// eachPair calls fn for every key=micros entry of a comma-separated list.
func eachPair(list string, fn func(key string, micros int64) error) error {
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		k, v, ok := strings.Cut(entry, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return fmt.Errorf("entry %q is not key=micros", entry)
		}
		n, err := ceilingValue(v)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if err := fn(k, n); err != nil {
			return err
		}
	}
	return nil
}

func ceilingValue(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > MaxEditCeilingMicros {
		return 0, fmt.Errorf("must be micro-dollars from 0 (no ceiling) to %d", MaxEditCeilingMicros)
	}
	return n, nil
}

// USD formats micro-dollars as dollars with cents for messages to people (with four decimals under a cent, so a tiny amount is not
// shown as $0.00).
func USD(micros int64) string {
	if micros > 0 && micros < 10_000 {
		return fmt.Sprintf("$%.4f", float64(micros)/1e6)
	}
	return fmt.Sprintf("$%.2f", float64(micros)/1e6)
}
