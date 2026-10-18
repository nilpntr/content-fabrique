package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
	"github.com/nilpntr/content-fabrique/internal/version"
)

const traceUsage = `Usage:
  cfctl trace export --run <run_...> --workspace <wks_... or org_...> [--endpoint URL] [--content=false] [--thumbnails N] [flags]

DEVELOPMENT ONLY (refused unless CF_ENV is local, ci or bench). export replays a stored agent run as spans, from its events, its
usage_ledger rows and its artifacts, and sends them to an OTLP/HTTP endpoint: by default the local Phoenix of ` + "`task dev:obs`" + `
(http://localhost:6006). Nothing is run again and no model is called: it costs nothing. The spans are the tree a live run makes (the
run, the run context, every model call with its messages and usage, every tool call with its arguments, result and artifacts, the
checks of check_edit, the budget events), at the times the events were written. Exporting a run twice sends the same span ids again.

  --run         the run (run_... or its UUID) (required)
  --workspace   the run's workspace: a wks_... id or a Clerk org_... id (required)
  --endpoint    the OTLP/HTTP base URL; default OTEL_EXPORTER_OTLP_ENDPOINT, else http://localhost:6006
  --content     put the messages, tool arguments and results and thumbnails on the spans. Default: true when the endpoint is on
                this machine, false otherwise
  --thumbnails  how many model calls get thumbnails of the frames they were sent (read from object storage, R2_*); default 40, 0 = none
  --env-file    the env file; default .env at the repository root
`

func cmdTrace(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, traceUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, traceUsage)
		return 0
	case "export":
		return cmdTraceExport(args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl trace: unknown command %q\n\n%s", args[0], traceUsage)
	return 2
}

// defaultPhoenix is the local Phoenix of `task dev:obs` (compose.yaml): its UI and its OTLP/HTTP endpoint on one port.
const defaultPhoenix = "http://localhost:6006"

func cmdTraceExport(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "trace export"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, traceUsage) }
	runArg := flags.String("run", "", "run id")
	workspaceArg := flags.String("workspace", "", "workspace id")
	endpointArg := flags.String("endpoint", "", "OTLP/HTTP base URL")
	content := flags.Bool("content", false, "put content on the spans")
	thumbs := flags.Int("thumbnails", 40, "model calls with thumbnails")
	envFileArg := flags.String("env-file", "", "env file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	contentSet := false
	flags.Visit(func(f *flag.Flag) { contentSet = contentSet || f.Name == "content" })
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	runID, err := parseRunID(*runArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --run: %v\n", name, err)
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	env, code := guardDevCommand(name, *envFileArg, false, false, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	endpoint := strings.TrimSpace(*endpointArg)
	if endpoint == "" {
		endpoint = env.get("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = defaultPhoenix
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: --endpoint must be an absolute http(s) URL\n", name)
		return 2
	}
	local := isLoopback(u.Hostname())
	withContent := local
	if contentSet {
		withContent = *content
	}

	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, errOut)
	if code != 0 {
		return code
	}
	run, err := st.GetAgentRun(ctx, ws.ID, runID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: run %s in workspace %s: %v\n", name, ids.Encode(ids.PrefixRun, runID), ws.ID, err)
		return 1
	}
	events, err := st.AgentEvents(ctx, ws.ID, runID, 0, 0)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: events: %v\n", name, err)
		return 1
	}
	ledger, err := st.UsageForRun(ctx, ws.ID, runID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: ledger: %v\n", name, err)
		return 1
	}

	in := runtime.ExportInput{Workspace: ws.ID, Run: run, Events: events, Ledger: ledger, SystemVersion: editor.PromptVersion, MaxThumbnailCalls: *thumbs}
	// The agent definitions give the run's first message and the system prompt (built as the api builds them: definitions only).
	if rt, err := agentset.Open(agentset.Options{Store: st, Getenv: env.get, DefinitionOnly: true}); err == nil {
		if a, ok := rt.Agents.Get(run.Agent); ok {
			in.Agent = a
		}
	}
	if run.Agent != editor.Name {
		in.SystemVersion = ""
	}
	if withContent && *thumbs > 0 {
		cfg := storageConfigOf(env)
		if cfg.Configured() {
			if objects, err := storage.New(cfg); err == nil {
				in.Images = editor.ImageResolver{Objects: objects}
			}
		}
	}

	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(telemetry.TracesURL(endpoint)), otlptracehttp.WithHeaders(telemetry.ParseHeaders(env.get("OTEL_EXPORTER_OTLP_HEADERS"))))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: the OTLP exporter: %v\n", name, err)
		return 1
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithIDGenerator(telemetry.IDGenerator{}), sdktrace.WithResource(cfctlResource()),
		sdktrace.WithRawSpanLimits(telemetry.SpanLimits()), sdktrace.WithBatcher(exp, sdktrace.WithMaxExportBatchSize(16)))
	telemetry.SetContent(withContent)
	res, err := runtime.ExportTrace(ctx, tp, in)
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	if serr := tp.Shutdown(flushCtx); serr != nil && err == nil {
		err = fmt.Errorf("sending the spans to %s: %w", endpoint, serr)
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "exported run %s: %d spans, %d events, %d ledger rows, content %v\n  trace %s\n",
		ids.Encode(ids.PrefixRun, runID), res.Spans, len(events), len(ledger), withContent, res.TraceID)
	if link := phoenixTraceURL(ctx, endpoint, res.TraceID); link != "" {
		_, _ = fmt.Fprintf(out, "  open   %s\n", link)
	}
	return 0
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func storageConfigOf(env *agentEnv) storage.Config {
	return storage.Config{
		Endpoint: env.get("R2_ENDPOINT"), Bucket: env.get("R2_BUCKET"), AccessKeyID: env.get("R2_ACCESS_KEY_ID"),
		SecretAccessKey: env.get("R2_SECRET_ACCESS_KEY"), Jurisdiction: strings.ToLower(env.get("R2_JURISDICTION")),
	}
}

func cfctlResource() *resource.Resource {
	return resource.NewWithAttributes("",
		attribute.String("service.name", telemetry.ServiceCfctl), attribute.String("service.version", version.Get()),
		attribute.String("openinference.project.name", telemetry.PhoenixProject))
}

// setupCfctlTracing exports the spans of a cfctl command that runs agents (agent run, eval) when OTEL_EXPORTER_OTLP_ENDPOINT is set in
// the env file or the environment, with content when OTEL_TRACE_CONTENT=1 and CF_ENV=local. The returned function flushes the spans; it
// is a no-op when nothing is exported.
func setupCfctlTracing(env *agentEnv) func() {
	return setupCfctlTracingTo(env, env.get("OTEL_EXPORTER_OTLP_ENDPOINT"))
}

// setupCfctlTracingTo is setupCfctlTracing to a given endpoint (eval suite sends its spans to the Phoenix it logs the experiment to).
func setupCfctlTracingTo(env *agentEnv, endpoint string) func() {
	if endpoint == "" {
		return func() {}
	}
	cfEnv := env.get("CF_ENV")
	shutdown, err := telemetry.SetupTracing(context.Background(), telemetry.Options{
		Service: telemetry.ServiceCfctl, Version: version.Get(), Env: cfEnv, OTLPEndpoint: endpoint, ExplicitEndpoint: true,
		Headers: telemetry.ParseHeaders(env.get("OTEL_EXPORTER_OTLP_HEADERS")), TraceContent: telemetry.ContentAllowed(cfEnv, env.get("OTEL_TRACE_CONTENT")),
	}, nil)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl: trace export is off: %v\n", err)
		return func() {}
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl: sending the spans: %v\n", err)
		}
	}
}

// phoenixTraceURL is the Phoenix page of a trace, when endpoint is a Phoenix that knows the project; "" otherwise.
func phoenixTraceURL(ctx context.Context, endpoint string, tid trace.TraceID) string {
	// Phoenix makes the project when the first span of it arrives, a moment after the export: ask a few times.
	var id string
	for range 10 {
		if id = phoenixProjectID(ctx, endpoint, telemetry.PhoenixProject); id != "" || ctx.Err() != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if id == "" {
		return ""
	}
	return strings.TrimRight(endpoint, "/") + "/projects/" + id + "/traces/" + tid.String()
}

// phoenixProjectID looks a Phoenix project's id up by name (GET /v1/projects); "" when the endpoint is not a Phoenix or has none.
func phoenixProjectID(ctx context.Context, endpoint, project string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/projects", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return ""
	}
	for _, p := range body.Data {
		if p.Name == project {
			return p.ID
		}
	}
	return ""
}
