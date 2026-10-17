package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/quicklook"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const assetsQuickLookUsage = `Usage:
  cfctl assets quicklook --workspace <wks_... or org_...> [--all] [--asset <ast_...>] [--limit N] [flags]

Development only. Runs the quick look (P1-04b) in this process, without the job queue, for the ready videos of a workspace: it
cuts four sharp frames from each video's proxy (the media-tools sandbox, like the ingest), makes ONE Haiku 5.5 call per video
(stage "quicklook", at most $0.003 each), and stores the title, description and tags on the asset. A title a person edited is
kept. It spends real money when LLM_MODE is live or record, and nothing when it is replay. Prints one line per video: its
TypeID, the outcome, the title and tags, the cost from the usage_ledger and the time it took.

By default it takes the videos whose quick look has not succeeded: never run (assets from before the quick look existed),
failed, or still pending. The frames it cut before are reused.

  --workspace  the workspace: a wks_... id or a Clerk org_... id (required)
  --all        every ready video of the workspace, also those that have a quick look: it runs again and replaces the
               description and tags (and the title, unless a person edited it)
  --asset      only this asset (an ast_... id); it runs again even when it has a quick look
  --limit      at most this many videos (default 200)
  --env-file   the env file; default .env at the repository root, and a relative name is taken from there too
  --yes        needed unless CF_ENV is local or ci, the database is on this machine, and CF_ENV and DATABASE_URL come from
               the same place

It reads DATABASE_URL, the R2_* settings, MEDIA_SANDBOX_PROVIDER, MEDIA_TOOLS_IMAGE, MEDIA_TEMP_DIR, ANTHROPIC_API_KEY, LLM_MODE
and ROUTING_PROFILE from the env file. It is refused when CF_ENV is staging or production.

Example (the founder's workspace, live):  cd apps/server && go run ./cmd/cfctl assets quicklook --workspace wks_01... --all
`

// quickLookHTTPClient replaces the model SDK's transport; tests set it to a fake API. Nil uses the default.
var quickLookHTTPClient *http.Client

// perAssetTimeout bounds one video: the proxy download, the frames and the model call.
const perAssetTimeout = 5 * time.Minute

func cmdAssetsQuickLook(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	const name = "assets quicklook"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, assetsQuickLookUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	all := flags.Bool("all", false, "every ready video, again")
	assetArg := flags.String("asset", "", "one asset")
	limit := flags.Int("limit", 200, "most videos")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	var (
		wantWS  ids.WorkspaceID
		wantOrg string
	)
	switch w := strings.TrimSpace(*workspaceArg); {
	case w == "":
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --workspace is required (a wks_... id or a Clerk org_... id)\n", name)
		return 2
	case strings.HasPrefix(w, "org_"):
		wantOrg = w
	default:
		id, err := ids.ParseWorkspaceID(w)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: --workspace must be a workspace id like wks_01... or a Clerk organization id like org_...\n", name)
			return 2
		}
		wantWS = id
	}
	var wantAsset *ids.AssetID
	if a := strings.TrimSpace(*assetArg); a != "" {
		id, err := ids.ParseAssetID(a)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: --asset must be an asset id like ast_01...\n", name)
			return 2
		}
		wantAsset = &id
	}
	if *limit < 1 || *limit > 10000 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --limit must be between 1 and 10000\n", name)
		return 2
	}

	env, code := loadAgentEnv(name, *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	cfEnv := env.vars["CF_ENV"]
	if msg := importEnvRefusal(cfEnv); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
		return 2
	}
	dsn := env.vars["DATABASE_URL"]
	cfSource, urlSource := sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup)
	if msg := mutationRefusal(name, "it writes titles, descriptions and tags and may spend money", "DATABASE_URL", cfEnv, cfSource, urlSource, dsn, *yes, false); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
		return 2
	}

	settings, err := llm.LoadSettings(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	cfg, err := config.Load(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	if !cfg.ObjectStorageConfigured() {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: the R2_* settings (R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY) are not set in %s or the environment\n", name, displayPath(env.root, env.path))
		return 2
	}
	router, err := llm.NewRouter(configfiles.Routing(), cmp.Or(settings.RoutingProfile, "dev-cheap"))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: routing: %v\n", name, err)
		return 2
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	if cfg.MediaSandboxProvider == config.SandboxLocal {
		if err := sandbox.CheckLocalAllowed(cfg.Env); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
			return 2
		}
	}
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: MEDIA_TEMP_DIR: %v\n", name, err)
		return 1
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: MEDIA_TEMP_DIR: %v\n", name, err)
		return 1
	}
	provider, err := newMediaProvider(cfg.MediaSandboxProvider, "docker", root)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: MEDIA_SANDBOX_PROVIDER: %v\n", name, err)
		return 2
	}

	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "cfctl", MaxConns: 4})
	}})
	if code != 0 {
		return code
	}
	defer st.Close()
	objects, err := storage.New(cfg.Storage())
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: object storage: %v\n", name, err)
		return 1
	}
	var ws store.Workspace
	if wantOrg != "" {
		ws, err = st.GetWorkspaceByClerkOrgID(ctx, wantOrg)
	} else {
		ws, err = st.GetWorkspace(ctx, wantWS)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && ws.DeletedAt != nil) {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: no such workspace. Workspaces exist once someone has signed in to the app\n", name)
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: looking up the workspace failed: %v\n", name, err)
		return 1
	}

	factory, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: env.get, Prices: prices, Ledger: st, HTTPClient: quickLookHTTPClient})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	svc, err := quicklook.New(quicklook.Config{
		Store: st, Objects: objects,
		Runner:  &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: sandbox.Limits{CPU: 2, MemoryMB: 3072, Timeout: quicklook.FFmpegTimeout, PIDs: 512}},
		TempDir: filepath.Join(root, "quicklook"),
		Models:  factory, Router: router,
	})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}

	var targets []store.Asset
	if wantAsset != nil {
		a, err := st.GetAsset(ctx, ws.ID, *wantAsset)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: no such asset in this workspace\n", name)
			return 1
		}
		targets = []store.Asset{a}
	} else if targets, err = st.ListAssetsForQuickLook(ctx, ws.ID, *all, *limit); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: listing the videos failed: %v\n", name, err)
		return 1
	}

	_, _ = fmt.Fprintf(out, "cfctl %s (CF_ENV=%s, LLM_MODE=%s, profile %s)\n  workspace %q (%s)\n  %d video(s)\n",
		name, settings.Env, settings.Mode, router.DefaultProfile(), ws.Name, ws.ID, len(targets))
	force := *all || wantAsset != nil
	failed, totalMicros := 0, int64(0)
	for _, a := range targets {
		if ctx.Err() != nil {
			break
		}
		before, _ := st.UsageForRun(ctx, ws.ID, a.ID.UUID)
		start := time.Now()
		rctx, cancel := context.WithTimeout(ctx, perAssetTimeout)
		res, err := svc.Run(rctx, ws.ID, a.ID, quicklook.Options{Force: force})
		cancel()
		after, _ := st.UsageForRun(ctx, ws.ID, a.ID.UUID)
		var cost int64
		if len(after) > len(before) {
			for _, r := range after[len(before):] {
				cost += r.ListCostMicros
			}
		}
		totalMicros += cost
		switch {
		case err != nil:
			failed++
			_, _ = fmt.Fprintf(out, "%s  error   %v\n", a.ID, err)
		case res.Outcome == quicklook.OutcomeDone:
			now, _ := st.GetAsset(ctx, ws.ID, a.ID)
			_, _ = fmt.Fprintf(out, "%s  done    %q  [%s]  %s  %s\n", a.ID, now.Title, strings.Join(now.Tags, ", "), usd(cost), time.Since(start).Round(time.Millisecond))
		case res.Outcome == quicklook.OutcomeFailed:
			failed++
			_, _ = fmt.Fprintf(out, "%s  failed  %s  %s  %s\n", a.ID, res.Reason, usd(cost), time.Since(start).Round(time.Millisecond))
		default:
			_, _ = fmt.Fprintf(out, "%s  %s\n", a.ID, res.Outcome)
		}
	}
	_, _ = fmt.Fprintf(out, "total cost %s (list price, from the usage_ledger)\n", usd(totalMicros))
	if failed > 0 {
		return 1
	}
	return 0
}
