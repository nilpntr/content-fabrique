package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const spendUsage = `Usage:
  cfctl spend summary [--cap-usd N] [--days N] [--json] [--env-file F]

The project's test spend (the cost guard): every cost the usage ledger holds with billed_to = test (development, ci and bench, D-21),
across every workspace of the database, against the founder's test cap. It prints the total, what calls in flight have reserved, and
the spend by day (UTC), by run type (the ledger's stage: editor, judge_edit, improve_edit, quicklook, ...; a cost that is not a model
call has its kind in front, transcription:transcribe) and by model. Read-only: it reads DATABASE_URL and never prints a secret.

The eval commands (cfctl eval suite, cfctl eval improve) check the same cap before they start: they print what the run may cost at
most and refuse it when that is more than the cap has left, unless --over-test-cap is given.

  --cap-usd   the test cap in dollars; default TEST_SPEND_CAP_MICROS (default $75)
  --days      how many days to list, newest first (default 14; 0 lists every day)
  --json      print the summary as JSON
  --env-file  the env file; default .env at the repository root
`

// DefaultTestSpendCapMicros is the founder's test cap: $75 (raised from $50 on 2026-10-18).
const DefaultTestSpendCapMicros int64 = 75_000_000

func cmdSpend(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	return cmdSpendAt(args, stdout, stderr, lookup, deps, time.Now)
}

func cmdSpendAt(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps, now func() time.Time) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, spendUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, spendUsage)
		return 0
	case "summary":
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl spend: unknown command %q\n\n%s", args[0], spendUsage)
		return 2
	}
	const name = "spend summary"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, spendUsage) }
	capUSD := fs.Float64("cap-usd", 0, "test cap in dollars")
	days := fs.Int("days", 14, "days to list")
	asJSON := fs.Bool("json", false, "JSON")
	envFile := fs.String("env-file", "", "env file")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, fs.Arg(0))
		return 2
	}
	if *capUSD < 0 || *days < 0 || *days > 3660 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --cap-usd must not be negative and --days is 0 to 3660\n", name)
		return 2
	}
	env, code := loadAgentEnv(name, *envFile, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	capMicros, err := testCapMicros(env.vars)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 2
	}
	if *capUSD > 0 {
		capMicros = int64(*capUSD * 1e6)
	}
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	sum, err := st.TestSpend(ctx, now(), *days)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: reading the ledger: %v\n", name, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(env.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(spendJSON(sum, capMicros))
		return 0
	}
	printSpendSummary(env.out, sum, capMicros, cmp.Or(env.vars["CF_ENV"], "(unset)"))
	return 0
}

// testCapMicros is TEST_SPEND_CAP_MICROS, or the default.
func testCapMicros(vars map[string]string) (int64, error) {
	v := strings.TrimSpace(vars["TEST_SPEND_CAP_MICROS"])
	if v == "" {
		return DefaultTestSpendCapMicros, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("TEST_SPEND_CAP_MICROS: must be micro-dollars, 0 or more (75000000 is $75)")
	}
	return n, nil
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

func printSpendSummary(out io.Writer, sum store.TestSpendSummary, capMicros int64, cfEnv string) {
	spent := sum.TotalMicros + sum.HeldMicros
	_, _ = fmt.Fprintf(out, "cfctl spend summary (CF_ENV=%s): test spend, billed_to = test, every workspace\n\n", cfEnv)
	_, _ = fmt.Fprintf(out, "total     %s of the %s test cap (%.0f %%), %s left\n", spend.USD(sum.TotalMicros), spend.USD(capMicros),
		pct(spent, capMicros), spend.USD(max(capMicros-spent, 0)))
	if sum.HeldMicros > 0 {
		_, _ = fmt.Fprintf(out, "in flight %s reserved by calls that have not settled (counted in what is left)\n", spend.USD(sum.HeldMicros))
	}
	if spent > capMicros {
		_, _ = fmt.Fprintf(out, "OVER THE CAP by %s: the eval commands refuse live runs until the cap is raised (TEST_SPEND_CAP_MICROS) or --over-test-cap is given\n",
			spend.USD(spent-capMicros))
	}
	if sum.Calls == 0 {
		_, _ = fmt.Fprintln(out, "rows      none: nothing has been spent on test calls yet")
		return
	}
	_, _ = fmt.Fprintf(out, "rows      %d from %s to %s", sum.Calls, sum.First.UTC().Format("2006-01-02"), sum.Last.UTC().Format("2006-01-02"))
	if sum.Estimated > 0 {
		_, _ = fmt.Fprintf(out, " (%d estimated at their reserved worst case: the call's usage never came back)", sum.Estimated)
	}
	_, _ = fmt.Fprintln(out)
	section := func(title string, rows []store.SpendSlice) {
		_, _ = fmt.Fprintf(out, "\n%s\n", title)
		tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
		for _, r := range rows {
			unit := "rows"
			if r.Calls == 1 {
				unit = "row"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%.0f %%\t%d %s\n", r.Key, spend.USD(r.Micros), pct(r.Micros, sum.TotalMicros), r.Calls, unit)
		}
		_ = tw.Flush()
	}
	section("by day (UTC, newest first)", sum.ByDay)
	section("by run type", sum.ByRunType)
	section("by model", sum.ByModel)
}

func spendJSON(sum store.TestSpendSummary, capMicros int64) map[string]any {
	list := func(rows []store.SpendSlice) []map[string]any {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{"key": r.Key, "micros": r.Micros, "rows": r.Calls})
		}
		return out
	}
	return map[string]any{
		"total_micros": sum.TotalMicros, "held_micros": sum.HeldMicros, "cap_micros": capMicros,
		"left_micros": max(capMicros-sum.TotalMicros-sum.HeldMicros, 0), "rows": sum.Calls, "estimated_rows": sum.Estimated,
		"by_day": list(sum.ByDay), "by_run_type": list(sum.ByRunType), "by_model": list(sum.ByModel),
	}
}

// guardTestCap is the cost guard of a command that may spend on test calls: it prints what the run may cost at most (upTo) next to
// what is left of the test cap, and refuses it (false) when upTo is more than that, unless override is set. A run billed to anyone
// but test (staging, production) is not the test cap's business, and a run that may cost nothing passes.
func guardTestCap(ctx context.Context, st *store.Store, env *agentEnv, name string, upTo int64, override bool) bool {
	billing, err := llm.BillingFor(env.vars["CF_ENV"])
	if err != nil || billing != llm.BilledToTest || upTo <= 0 {
		return true
	}
	capMicros, err := testCapMicros(env.vars)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return false
	}
	spent, err := st.TestSpendMicros(ctx, time.Now())
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: reading the test spend: %v\n", name, err)
		return false
	}
	left := max(capMicros-spent, 0)
	_, _ = fmt.Fprintf(env.out, "this run may cost up to %s; test spend so far %s of the %s test cap, %s left (cfctl spend summary)\n",
		spend.USD(upTo), spend.USD(spent), spend.USD(capMicros), spend.USD(left))
	if upTo <= left {
		return true
	}
	if override {
		_, _ = fmt.Fprintf(env.out, "--over-test-cap: running although it may pass the test cap by %s\n", spend.USD(upTo-left))
		return true
	}
	_, _ = fmt.Fprintf(env.err, "cfctl %s: refused: this run may cost up to %s and the test cap has %s left. Lower --max-usd, --repeat or --cases, "+
		"raise TEST_SPEND_CAP_MICROS with the founder's go-ahead, or pass --over-test-cap\n", name, spend.USD(upTo), spend.USD(left))
	return false
}
