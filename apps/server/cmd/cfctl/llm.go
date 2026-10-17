package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const llmUsage = `Usage:
  cfctl llm ping --stage <stage> [--profile <name>] [--live] [--workspace <wks_...>] [flags]

ping checks model access end to end for one routing stage. By default it only COUNTS TOKENS (free, generates nothing):
it resolves the stage in the routing profile, builds the client the way the app does (the same key, gateway and
rate-limit handling) and prints the model, effort, profile and who would be billed. With --live it also sends one tiny
message (a few cents on Opus, a fraction of a cent on Haiku), writes the usage_ledger row like any call, and shows it.

  --stage      the routing stage: editor, revision, ideation, tagging, judge, research.lead, research.workers
  --profile    a routing profile for this run, instead of ROUTING_PROFILE (dev-cheap, dev-mid, v1-start)
  --live       send one real message and record it in the ledger. Needs DATABASE_URL and --workspace
  --workspace  the workspace (wks_...) the ledger row belongs to; --live only
  --env-file   the env file; default .env at the repository root, and a relative name is taken from there too
  --yes        --live is refused unless CF_ENV is local or ci and the database is this machine, with CF_ENV and
               DATABASE_URL from the same place; --yes overrides that
  --allow-production
               --live is refused when CF_ENV is production without this, and with it still needs --yes

ping ignores LLM_MODE=record and replay: it always talks to the API. It never prints a key.
`

// llmDeps are the things cmdLLM needs from outside, replaced in tests.
type llmDeps struct {
	// httpClient replaces the SDK's transport. Nil uses the default.
	httpClient *http.Client
	// openStore opens the app pool for --live.
	openStore func(ctx context.Context, dsn string) (*store.Store, error)
}

func defaultLLMDeps() llmDeps {
	return llmDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "cfctl", MaxConns: 2})
	}}
}

func cmdLLM(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps llmDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, llmUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, llmUsage)
		return 0
	case "ping":
		return cmdLLMPing(args[1:], stdout, stderr, lookup, deps)
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl llm: unknown command %q\n\n%s", args[0], llmUsage)
		return 2
	}
}

// usd formats micro-dollars as dollars.
func usd(micros int64) string { return fmt.Sprintf("$%.6f", float64(micros)/1e6) }

// noLedger backs a count-only ping: nothing in it may write or read the ledger, the holds or the counters.
type noLedger struct{}

var errNoLedger = errors.New("llm ping without --live must not touch the ledger")

func (noLedger) ReserveSpend(context.Context, store.ReserveSpendParams) (store.SpendHold, []store.SpendAlert, error) {
	return store.SpendHold{}, nil, errNoLedger
}

func (noLedger) SettleSpend(context.Context, store.SettleSpendParams) (int, []store.SpendAlert, error) {
	return 0, nil, errNoLedger
}

func (noLedger) ReleaseSpend(context.Context, ids.WorkspaceID, string) error {
	return errNoLedger
}

func cmdLLMPing(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps llmDeps) int {
	flags := flag.NewFlagSet("llm ping", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, llmUsage) }
	stage := flags.String("stage", "", "routing stage")
	profileArg := flags.String("profile", "", "routing profile for this run")
	live := flags.Bool("live", false, "send one real message")
	workspaceArg := flags.String("workspace", "", "workspace id for the ledger row")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm --live outside plain local development")
	allowProd := flags.Bool("allow-production", false, "allow --live on production")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl llm ping: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if *stage == "" {
		_, _ = fmt.Fprintln(stderr, "cfctl llm ping: --stage is required (editor, revision, ideation, tagging, judge, research.lead, research.workers)")
		return 2
	}
	var ws ids.WorkspaceID
	if *live {
		id, err := ids.ParseWorkspaceID(strings.TrimSpace(*workspaceArg))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cfctl llm ping: --live needs --workspace <wks_...>: the workspace the ledger row belongs to")
			return 2
		}
		ws = id
	} else if *workspaceArg != "" {
		_, _ = fmt.Fprintln(stderr, "cfctl llm ping: --workspace belongs to --live")
		return 2
	}

	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	explicit := *envFileArg != ""
	path := *envFileArg
	if !explicit {
		path = filepath.Join(root, ".env")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file := map[string]string{}
	parsed, err := envfile.ParseFile(path)
	switch {
	case err == nil:
		file = parsed
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// process environment only
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl llm ping: cannot read the env file %s\n", displayPath(root, path))
		return 2
	}
	vars := doctor.MergeEnv(example, file, lookup)
	redactor := doctor.NewRedactor(example, vars)
	out, errOut := redactor.Writer(stdout), redactor.Writer(stderr)
	get := func(k string) string { return vars[k] }

	settings, err := llm.LoadSettings(get)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
		return 2
	}
	if settings.Mode != llm.ModeLive {
		_, _ = fmt.Fprintf(out, "note: LLM_MODE=%s is ignored: ping always talks to the API\n", settings.Mode)
		settings.Mode = llm.ModeLive
	}
	defaultProfile := cmp.Or(settings.RoutingProfile, *profileArg)
	routingFS := os.DirFS(filepath.Join(root, "apps", "server", "config", "routing"))
	router, err := llm.NewRouter(routingFS, defaultProfile)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: routing: %v\n", err)
		return 2
	}
	route, err := router.Resolve(*profileArg, *stage)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
		return 2
	}
	prices, err := llm.LoadPriceTable(os.DirFS(filepath.Join(root, "apps", "server", "config", "pricing")), llm.PriceFile)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
		return 2
	}

	ctx, stop := signalContext()
	defer stop()

	var ledger spend.Backend = noLedger{}
	var st *store.Store
	if *live {
		dsn := vars["DATABASE_URL"]
		if dsn == "" {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: --live needs DATABASE_URL in %s or the environment\n", displayPath(root, path))
			return 2
		}
		if err := config.ValidatePostgresURL(dsn); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: DATABASE_URL: %v\n", err)
			return 2
		}
		cfSource, urlSource := sourceOf("CF_ENV", file, lookup), sourceOf("DATABASE_URL", file, lookup)
		if msg := mutationRefusal("llm ping --live", "it spends money and writes a ledger row", "DATABASE_URL", vars["CF_ENV"], cfSource, urlSource, dsn, *yes, *allowProd); msg != "" {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %s (env file %s)\n", msg, displayPath(root, path))
			return 2
		}
		if st, err = deps.openStore(ctx, dsn); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
			return 1
		}
		defer st.Close()
		if _, err := st.GetWorkspace(ctx, ws); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: workspace %s: %v\n", ws, err)
			return 1
		}
		ledger = st
	} else {
		ws = ids.WorkspaceID{UUID: uuid.New()} // count_tokens needs a workspace for the client, not for a row
	}

	factory, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: get, Prices: prices, Ledger: ledger, HTTPClient: deps.httpClient})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
		return 2
	}
	client, billing, err := factory.For(ctx, ws, route.Stage)
	if err != nil {
		if errors.Is(err, llm.ErrNoKey) {
			_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v. Fill ANTHROPIC_API_KEY in %s\n", err, displayPath(root, path))
			return 1
		}
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: %v\n", err)
		return 1
	}

	path2 := "direct (api.anthropic.com)"
	if settings.BaseURL != "" {
		path2 = "through ANTHROPIC_BASE_URL (gateway)"
	}
	_, _ = fmt.Fprintf(out, "cfctl llm ping (CF_ENV=%s, env file %s)\n", settings.Env, displayPath(root, path))
	_, _ = fmt.Fprintf(out, "  stage      %s\n  profile    %s\n  model      %s\n  effort     %s\n  max_tokens %d\n  billed_to  %s\n  path       %s\n  prices     %s (%s)\n",
		route.Stage, route.Profile, route.Model, cmp.Or(route.Effort, "(model default)"), route.MaxTokens, billing, path2, prices.Version, pricedWord(prices, route.Model))

	req := route.Apply(llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock("Reply with the single word: pong")}}}})
	req.Run = llm.RunInfo{Profile: route.Profile}

	n, err := client.CountTokens(ctx, req)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: count_tokens failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "  count_tokens ok: %d input tokens (free)\n", n)
	if !*live {
		_, _ = fmt.Fprintln(out, "ok (count only; add --live to send one real message)")
		return 0
	}

	req.MaxTokens = 128
	run := uuid.New()
	req.Run.ID = run
	resp, err := client.Messages(ctx, req)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: the message failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "  reply      %q (stop_reason %s)\n  usage      input %d, cache write %d, cache read %d, output %d\n  request_id %s\n",
		resp.Text(), resp.StopReason, resp.Usage.InputTokens, resp.Usage.CacheWriteTokens, resp.Usage.CacheReadTokens, resp.Usage.OutputTokens, resp.RequestID)

	rows, err := st.UsageForRun(ctx, ws, run)
	if err != nil || len(rows) != 1 {
		_, _ = fmt.Fprintf(errOut, "cfctl llm ping: the call succeeded but its ledger row cannot be read back (%d rows, %v)\n", len(rows), err)
		return 1
	}
	r := rows[0]
	_, _ = fmt.Fprintf(out, "  ledger     %s  stage %s  model %s  tokens %d/%d/%d/%d  cost %s  price_table %s  billed_to %s\n",
		r.ID, r.Stage, r.Model, r.InputTokens, r.CacheWriteTokens, r.CacheReadTokens, r.OutputTokens, usd(r.ListCostMicros), r.PriceTableVersion, r.BilledTo)
	_, _ = fmt.Fprintln(out, "ok")
	return 0
}

func pricedWord(p *llm.PriceTable, model string) string {
	if p.Priced(model) {
		return "model priced"
	}
	return "model UNPRICED: live calls are refused unless LLM_ALLOW_UNPRICED=1"
}

func (noLedger) RetainSpend(context.Context, ids.WorkspaceID, string, time.Time) error {
	return errNoLedger
}

func (noLedger) ExtendSpend(context.Context, ids.WorkspaceID, string, time.Time) error {
	return errNoLedger
}
