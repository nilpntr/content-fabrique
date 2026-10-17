package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
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

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/renderjobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const agentUsage = `Usage:
  cfctl agent run  --agent <name> --workspace <wks_...> [--project <prj_...>] [--input <text or JSON>] [--profile <name>] [budget flags] [flags]
  cfctl agent show <run id> --workspace <wks_...> [flags]

DEVELOPMENT ONLY (refused on staging and production). run starts one agent run and works it in this process, without the
job queue: it spends real money when LLM_MODE is live or record, and nothing when it is replay. show prints a run: its
status, totals, every event and the usage_ledger rows, which is how a run's cost is measured.

  --agent      the agent to run (echo, editor)
  --workspace  the workspace (wks_...) that owns the run
  --project    the project (prj_...) of an editor run: it edits that project's timeline. Needs R2_* and the media sandbox settings
               (MEDIA_SANDBOX_PROVIDER, MEDIA_TOOLS_IMAGE, MEDIA_TEMP_DIR), because the editor looks at frames of the footage
  --input      the user's message; or a JSON object, which is the run's input as it is. For the editor it is the extra
               instructions of the run and may be left out
  --profile    a routing profile for this run, instead of ROUTING_PROFILE (dev-cheap, dev-mid, v1-start)
  --budget-usd, --max-tool-calls, --max-tokens, --wall-clock
               override the agent's budgets (dollars, count, tokens, duration such as 5m)
  --env-file   the env file; default .env at the repository root
  --yes        run is refused unless CF_ENV is local or ci and the database is this machine, with CF_ENV and DATABASE_URL
               from the same place; --yes overrides that
  --events     run: also print the events when the run ends
  --no-credits run an editor run without credits. By default it is charged like the API's: the workspace needs an active plan and
               enough credits (cfctl plan grant, cfctl credits grant), the run gets the plan's budget for a first edit or a revision,
               and it is charged when it succeeds, released when it fails. With --no-credits it costs nothing and keeps the budget flags,
               but it is still recorded as an edit of the project. Only with CF_ENV=local or ci (it is refused anywhere else)

When the agent calls ask_user, run prints the question and reads the answer from standard input.
`

// agentDeps are the things cmdAgent needs from outside, replaced in tests.
type agentDeps struct {
	httpClient *http.Client
	openStore  func(ctx context.Context, dsn string) (*store.Store, error)
	stdin      io.Reader
}

func defaultAgentDeps() agentDeps {
	return agentDeps{
		openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
			return store.Open(ctx, dsn, store.Options{AppName: "cfctl", MaxConns: 4})
		},
		stdin: os.Stdin,
	}
}

func cmdAgent(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, agentUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, agentUsage)
		return 0
	case "run":
		return cmdAgentRun(args[1:], stdout, stderr, lookup, deps)
	case "show":
		return cmdAgentShow(args[1:], stdout, stderr, lookup, deps)
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl agent: unknown command %q\n\n%s", args[0], agentUsage)
		return 2
	}
}

// agentEnv is what both subcommands read: the repository root, the merged variables and where the env file is.
type agentEnv struct {
	root, path string
	file       map[string]string
	vars       map[string]string
	out, err   io.Writer
}

func loadAgentEnv(name, envFileArg string, stdout, stderr io.Writer, lookup lookupEnv) (*agentEnv, int) {
	root, example, code := loadExample(stderr)
	if code != 0 {
		return nil, code
	}
	explicit := envFileArg != ""
	path := envFileArg
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
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl %s: cannot read the env file %s\n", name, displayPath(root, path))
		return nil, 2
	}
	vars := doctor.MergeEnv(example, file, lookup)
	redactor := doctor.NewRedactor(example, vars)
	return &agentEnv{root: root, path: path, file: file, vars: vars, out: redactor.Writer(stdout), err: redactor.Writer(stderr)}, 0
}

func (a *agentEnv) get(k string) string { return a.vars[k] }

// openDB checks DATABASE_URL and opens the store.
func (a *agentEnv) openDB(ctx context.Context, name string, deps agentDeps) (*store.Store, int) {
	dsn := a.vars["DATABASE_URL"]
	if dsn == "" {
		_, _ = fmt.Fprintf(a.err, "cfctl %s: DATABASE_URL is not set in %s or the environment\n", name, displayPath(a.root, a.path))
		return nil, 2
	}
	if err := config.ValidatePostgresURL(dsn); err != nil {
		_, _ = fmt.Fprintf(a.err, "cfctl %s: DATABASE_URL: %v\n", name, err)
		return nil, 2
	}
	st, err := deps.openStore(ctx, dsn)
	if err != nil {
		_, _ = fmt.Fprintf(a.err, "cfctl %s: %v\n", name, err)
		return nil, 1
	}
	return st, 0
}

func parseWorkspaceFlag(name, v string, stderr io.Writer) (ids.WorkspaceID, bool) {
	id, err := ids.ParseWorkspaceID(strings.TrimSpace(v))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --workspace <wks_...> is required\n", name)
		return ids.WorkspaceID{}, false
	}
	return id, true
}

func cmdAgentRun(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	flags := flag.NewFlagSet("agent run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, agentUsage) }
	agentName := flags.String("agent", "", "agent")
	workspaceArg := flags.String("workspace", "", "workspace id")
	input := flags.String("input", "", "message or JSON input")
	projectArg := flags.String("project", "", "project id")
	profile := flags.String("profile", "", "routing profile")
	budgetUSD := flags.Float64("budget-usd", 0, "dollar budget")
	maxCalls := flags.Int("max-tool-calls", 0, "tool-call budget")
	maxTokens := flags.Int64("max-tokens", 0, "token budget")
	wall := flags.Duration("wall-clock", 0, "wall-clock budget")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	showEvents := flags.Bool("events", false, "print the events at the end")
	noCredits := flags.Bool("no-credits", false, "run an editor run without credits (local and ci only)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl agent run: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	isEditor := *agentName == editor.Name
	if *agentName == "" || (strings.TrimSpace(*input) == "" && !isEditor) {
		_, _ = fmt.Fprintln(stderr, "cfctl agent run: --agent and --input are required (the editor needs no --input)")
		return 2
	}
	var projectID ids.ProjectID
	if isEditor || *projectArg != "" {
		var err error
		if projectID, err = ids.ParseProjectID(strings.TrimSpace(*projectArg)); err != nil {
			_, _ = fmt.Fprintln(stderr, "cfctl agent run: --project <prj_...> is required for the editor")
			return 2
		}
	}
	ws, ok := parseWorkspaceFlag("agent run", *workspaceArg, stderr)
	if !ok {
		return 2
	}
	runInput := json.RawMessage(*input)
	if !json.Valid(runInput) || !strings.HasPrefix(strings.TrimSpace(*input), "{") {
		if isEditor {
			var err error
			if runInput, err = editor.NewInput(*input); err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl agent run: %v\n", err)
				return 2
			}
		} else {
			runInput, _ = json.Marshal(runtime.Input{Message: *input})
		}
	}

	env, code := loadAgentEnv("agent run", *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	switch cfEnv := env.vars["CF_ENV"]; cfEnv {
	case config.EnvStaging, config.EnvProduction:
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: this command is for development only and is refused with CF_ENV=%s\n", cfEnv)
		return 2
	}
	if *noCredits {
		// Staging and production are refused above for the whole command; this keeps the flag local and ci only even if that ever changes,
		// and refuses an environment nobody named (a missing CF_ENV is not "local").
		if cfEnv := env.vars["CF_ENV"]; cfEnv != config.EnvLocal && cfEnv != config.EnvCI {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: --no-credits is for CF_ENV=local or ci only (CF_ENV is %q)\n", cfEnv)
			return 2
		}
	}
	settings, err := llm.LoadSettings(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v\n", err)
		return 2
	}
	dsn := env.vars["DATABASE_URL"]
	if msg := mutationRefusal("agent run", "it writes a run and ledger rows and may spend money", "DATABASE_URL", env.vars["CF_ENV"],
		sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), dsn, *yes, false); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: %s (env file %s)\n", msg, displayPath(env.root, env.path))
		return 2
	}

	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, "agent run", deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	if _, err := st.GetWorkspace(ctx, ws); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: workspace %s: %v\n", ws, err)
		return 1
	}
	if !projectID.IsZero() {
		if _, err := st.GetProject(ctx, ws, projectID); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: project %s in workspace %s: %v\n", projectID, ws, err)
			return 1
		}
	}

	opts := agentset.Options{
		Store: st, Getenv: env.get, DefaultProfile: *profile, Inline: true, HTTPClient: deps.httpClient,
	}
	storageCfg := storage.Config{
		Endpoint: env.get("R2_ENDPOINT"), Bucket: env.get("R2_BUCKET"), AccessKeyID: env.get("R2_ACCESS_KEY_ID"),
		SecretAccessKey: env.get("R2_SECRET_ACCESS_KEY"), Jurisdiction: strings.ToLower(env.get("R2_JURISDICTION")),
	}
	if storageCfg.Configured() {
		objects, err := storage.New(storageCfg)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: object storage: %v\n", err)
			return 1
		}
		opts.Objects = objects
		// render_preview queues a render job: a worker with the render pipeline must be running to work it.
		if opts.Preview, err = renderjobs.NewPreviewRendererFor(st, objects, nil); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: preview renderer: %v\n", err)
			return 1
		}
		provider := cmp.Or(env.get("MEDIA_SANDBOX_PROVIDER"), config.SandboxDocker)
		tempDir := cmp.Or(env.get("MEDIA_TEMP_DIR"), filepath.Join(os.TempDir(), "cf-media"))
		if opts.Frames, opts.TempDir, err = agentset.NewFrameRunner(provider, env.get("MEDIA_TOOLS_IMAGE"), tempDir, env.get("CF_ENV")); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: the sandbox for the editor's frames: %v\n", err)
			return 2
		}
	} else if isEditor {
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: the editor needs object storage: set R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY in %s\n", displayPath(env.root, env.path))
		return 2
	}
	rt, err := agentset.Open(opts)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v\n", err)
		return 2
	}
	svc, runner := rt.Service, rt.Runner
	run, err := svc.Start(ctx, runtime.StartParams{
		Agent: *agentName, WorkspaceID: ws, ProjectID: projectID.UUID, Input: runInput, Profile: *profile,
		Budgets:   runtime.Budgets{Micros: int64(*budgetUSD * 1e6), Tokens: *maxTokens, ToolCalls: *maxCalls, WallClock: *wall},
		NoCredits: *noCredits,
	})
	if err != nil {
		if errors.Is(err, store.ErrInsufficientCredits) {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: this workspace does not have enough credits for this edit (%v). Grant some with cfctl credits grant, or pass --no-credits (local and ci only)\n", err)
			return 1
		}
		if errors.Is(err, credits.ErrNoPlan) {
			_, _ = fmt.Fprintln(errOut, "cfctl agent run: this workspace has no active plan. Grant one with cfctl plan grant, or pass --no-credits (local and ci only)")
			return 1
		}
		if errors.Is(err, llm.ErrNoKey) {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v. Fill ANTHROPIC_API_KEY in %s, or use LLM_MODE=replay\n", err, displayPath(env.root, env.path))
			return 1
		}
		_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "cfctl agent run (CF_ENV=%s, LLM_MODE=%s)\n  run      %s\n  agent    %s\n  profile  %s  model %s  effort %s\n  billed   %s  budget %s\n",
		settings.Env, settings.Mode, ids.Encode(ids.PrefixRun, run.ID), run.Agent, run.RoutingProfile, run.Model, run.Effort, run.BilledTo, usd(run.BudgetMicros))

	stdin := bufio.NewReader(cmp.Or[io.Reader](deps.stdin, strings.NewReader("")))
	for {
		outcome, err := runner.Execute(ctx, ws, run.ID)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v\n", err)
			return 1
		}
		if outcome != runtime.OutcomeWaiting {
			break
		}
		question := lastQuestion(ctx, st, ws, run.ID)
		_, _ = fmt.Fprintf(out, "\nThe agent asks: %s\n> ", question)
		line, rerr := stdin.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			_, _ = fmt.Fprintln(out, "(no answer: cancelling the run)")
			if _, cerr := svc.Cancel(ctx, ws, run.ID); cerr != nil {
				_, _ = fmt.Fprintf(errOut, "cfctl agent run: %v\n", cerr)
			}
			break
		}
		if err := svc.Reply(ctx, ws, run.ID, line); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl agent run: reply: %v\n", err)
			return 1
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return 1
		}
	}
	code = printRun(ctx, out, errOut, st, ws, run.ID, *showEvents)
	if code != 0 {
		return code
	}
	if final, err := st.GetAgentRun(ctx, ws, run.ID); err == nil && final.Status != store.AgentSucceeded {
		return 1
	}
	return 0
}

// cacheHitText says how much of the prompts came from the prompt cache: read tokens over every prompt token (input, cache write
// and cache read). It is the number to watch when the system prompt or the tool list changes.
func cacheHitText(input, write, read int64) string {
	prompt := input + write + read
	if prompt <= 0 {
		return "no prompt tokens yet"
	}
	return fmt.Sprintf("%.0f%% of %d prompt tokens read from the cache (%d written)", 100*float64(read)/float64(prompt), prompt, write)
}

func lastQuestion(ctx context.Context, st *store.Store, ws ids.WorkspaceID, run uuid.UUID) string {
	e, err := st.LastAgentEvent(ctx, ws, run)
	if err != nil {
		return "(unknown)"
	}
	var p struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	return p.Question
}

func cmdAgentShow(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	flags := flag.NewFlagSet("agent show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, agentUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	envFileArg := flags.String("env-file", "", "env file")
	// Flags may come after the run id.
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(positional) != 1 {
		_, _ = fmt.Fprintln(stderr, "cfctl agent show: give exactly one run id (run_... or a UUID)")
		return 2
	}
	run, err := parseRunID(positional[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl agent show: %v\n", err)
		return 2
	}
	ws, ok := parseWorkspaceFlag("agent show", *workspaceArg, stderr)
	if !ok {
		return 2
	}
	env, code := loadAgentEnv("agent show", *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, "agent show", deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	return printRun(ctx, env.out, env.err, st, ws, run, true)
}

func parseRunID(s string) (uuid.UUID, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, ids.PrefixRun+"_") {
		u, err := ids.Decode(ids.PrefixRun, s)
		if err != nil {
			return uuid.Nil, fmt.Errorf("run id: %w", err)
		}
		return u, nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.New("the run id must be run_... or a UUID")
	}
	return u, nil
}

// printRun prints the run, its totals, the ledger rows and (with events) the event log.
func printRun(ctx context.Context, out, errOut io.Writer, st *store.Store, ws ids.WorkspaceID, id uuid.UUID, events bool) int {
	r, err := st.GetAgentRun(ctx, ws, id)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent: run %s: %v\n", ids.Encode(ids.PrefixRun, id), err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "\nrun %s  %s", ids.Encode(ids.PrefixRun, r.ID), r.Status)
	if r.FailureReason != nil {
		_, _ = fmt.Fprintf(out, " (%s)", *r.FailureReason)
	}
	_, _ = fmt.Fprintf(out, "\n  agent %s  stage %s  profile %s  model %s  effort %s  billed_to %s\n", r.Agent, r.Stage, r.RoutingProfile, r.Model, r.Effort, r.BilledTo)
	_, _ = fmt.Fprintf(out, "  cost %s of %s budget   tool calls %d of %d   running %s of %s\n",
		usd(r.CostMicros), usd(r.BudgetMicros), r.ToolCalls, r.BudgetToolCalls,
		(time.Duration(r.ActiveMs) * time.Millisecond).Round(time.Millisecond), (time.Duration(r.BudgetWallClockMs) * time.Millisecond).Round(time.Second))
	_, _ = fmt.Fprintf(out, "  tokens: input %d, cache write %d, cache read %d, output %d (budget %d)\n", r.InputTokens, r.CacheWriteTokens, r.CacheReadTokens, r.OutputTokens, r.BudgetTokens)
	_, _ = fmt.Fprintf(out, "  prompt cache: %s\n", cacheHitText(r.InputTokens, r.CacheWriteTokens, r.CacheReadTokens))
	if len(r.Output) > 0 {
		_, _ = fmt.Fprintf(out, "  output %s\n", clip(string(r.Output), 600))
	}

	rows, err := st.UsageForRun(ctx, ws, id)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent: ledger: %v\n", err)
		return 1
	}
	var total int64
	for _, row := range rows {
		total += row.ListCostMicros
	}
	_, _ = fmt.Fprintf(out, "\nusage_ledger: %d calls, %s at list price\n", len(rows), usd(total))
	for _, row := range rows {
		est := ""
		if row.Estimated {
			est = " (estimated: outcome unknown)"
		}
		_, _ = fmt.Fprintf(out, "  %s  %-18s in %-6d cache w/r %d/%d  out %-5d  %s%s\n", row.CreatedAt.Format("15:04:05"), row.Model, row.InputTokens,
			row.CacheWriteTokens, row.CacheReadTokens, row.OutputTokens, usd(row.ListCostMicros), est)
	}
	if !events {
		return 0
	}
	evs, err := st.AgentEvents(ctx, ws, id, 0, 0)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl agent: events: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "\nevents (%d):\n", len(evs))
	for _, e := range evs {
		_, _ = fmt.Fprintf(out, "  %3d  %-17s %s\n", e.Seq, e.Type, describeEvent(e))
	}
	return 0
}

// describeEvent is a one-line summary of an event's payload.
func describeEvent(e store.AgentEvent) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(e.Payload, &m) != nil {
		return clip(string(e.Payload), 140)
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	texts := func(k string) string {
		var blocks []struct {
			Type, Text, Name, Ref string
			Input                 json.RawMessage
		}
		_ = json.Unmarshal(m[k], &blocks)
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "tool_use":
				parts = append(parts, fmt.Sprintf("[%s %s]", b.Name, clip(string(b.Input), 80)))
			case "image":
				parts = append(parts, "[image "+b.Ref+"]")
			case "thinking":
				parts = append(parts, "[thinking]")
			}
		}
		return strings.Join(parts, " ")
	}
	switch e.Type {
	case store.EventStatus:
		s := str("state")
		for _, k := range []string{"reason", "limit", "question"} {
			if v := str(k); v != "" {
				s += " " + k + "=" + clip(v, 80)
			}
		}
		return s
	case store.EventAssistantMessage:
		var cost int64
		_ = json.Unmarshal(m["cost_micros"], &cost)
		var u struct {
			Input      int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_write_tokens"`
			CacheRead  int64 `json:"cache_read_tokens"`
			Output     int64 `json:"output_tokens"`
		}
		_ = json.Unmarshal(m["usage"], &u)
		return fmt.Sprintf("%s  stop=%s cost=%s in=%d cache w/r=%d/%d out=%d", clip(texts("content"), 100), str("stop_reason"), usd(cost), u.Input, u.CacheWrite, u.CacheRead, u.Output)
	case store.EventToolCall:
		return str("name") + " " + clip(string(m["input"]), 100)
	case store.EventToolResult:
		s := str("name") + ": " + clip(texts("content"), 100)
		if string(m["is_error"]) == "true" {
			s += "  (error)"
		}
		return s
	case store.EventError:
		return str("code") + ": " + clip(str("message"), 120)
	}
	return clip(string(e.Payload), 140)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
