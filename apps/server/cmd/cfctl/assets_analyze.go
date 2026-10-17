package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const assetsAnalyzeUsage = `Usage:
  cfctl assets analyze --workspace <wks_... or org_...> [--all] [--asset <ast_...>] [--limit N] [flags]

Development only. Runs the clip analysis (P2-02, P2-03, P2-09) in this process, without the job queue, for the ready videos of a
workspace: one ffmpeg pass over each video's proxy in the media-tools sandbox gives motion, exposure, sharpness and scene scores;
from them it stores the shots, the reps, still holds and dead time (the moments the editor's find_moments reads) and a summary.
No model is called and nothing is spent. Prints one line per video: its TypeID, the outcome and the summary line.

By default it takes the videos with no analysis of the current version (the ones from before the analysis existed, and after a
change of its numbers).

  --workspace  the workspace: a wks_... id or a Clerk org_... id (required)
  --all        every ready video of the workspace, also those analysed already: it runs again and replaces the result
  --asset      only this asset (an ast_... id); it runs again even when it is analysed
  --limit      at most this many videos (default 200)
  --env-file   the env file; default .env at the repository root, and a relative name is taken from there too
  --yes        needed unless CF_ENV is local or ci, the database is on this machine, and CF_ENV and DATABASE_URL come from
               the same place

It reads DATABASE_URL, the R2_* settings, MEDIA_SANDBOX_PROVIDER, MEDIA_TOOLS_IMAGE and MEDIA_TEMP_DIR from the env file. It is
refused when CF_ENV is staging or production.

Example:  cd apps/server && go run ./cmd/cfctl assets analyze --workspace wks_01... --all
`

// analyzePerAssetTimeout bounds one video: the proxy download and the ffmpeg pass.
const analyzePerAssetTimeout = 30 * time.Minute

func cmdAssetsAnalyze(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	const name = "assets analyze"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, assetsAnalyzeUsage) }
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
	if msg := mutationRefusal(name, "it writes shots and moments", "DATABASE_URL", cfEnv, cfSource, urlSource, dsn, *yes, false); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
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
	runner, dir, err := analysisRunner(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
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
	svc, err := analysis.New(analysis.Config{Store: st, Objects: objects, Runner: runner, TempDir: dir})
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
	} else if targets, err = st.ListAssetsForAnalysis(ctx, ws.ID, analysis.Version, *all, *limit); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: listing the videos failed: %v\n", name, err)
		return 1
	}

	_, _ = fmt.Fprintf(out, "cfctl %s (CF_ENV=%s, analysis version %d)\n  workspace %q (%s)\n  %d video(s)\n", name, cfg.Env, analysis.Version, ws.Name, ws.ID, len(targets))
	force := *all || wantAsset != nil
	failed := 0
	for _, a := range targets {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		rctx, cancel := context.WithTimeout(ctx, analyzePerAssetTimeout)
		res, err := svc.Run(rctx, ws.ID, a.ID, analysis.Options{Force: force})
		cancel()
		took := time.Since(start).Round(time.Millisecond)
		switch {
		case err != nil:
			failed++
			_, _ = fmt.Fprintf(out, "%s  error   %v\n", a.ID, err)
		case res.Outcome == analysis.OutcomeDone:
			_, _ = fmt.Fprintf(out, "%s  done    %s  (%s)\n", a.ID, res.Line, took)
		default:
			_, _ = fmt.Fprintf(out, "%s  %s\n", a.ID, res.Outcome)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// analysisRunner builds the sandbox runner and job directory of the analysis from the settings, like the worker does.
func analysisRunner(cfg config.Config) (media.Runner, string, error) {
	if err := os.MkdirAll(cfg.MediaTempDir, 0o750); err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	root, err := filepath.EvalSymlinks(cfg.MediaTempDir)
	if err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	provider, err := newMediaProvider(cfg.MediaSandboxProvider, "docker", root)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "analysis"), 0o750); err != nil {
		return nil, "", fmt.Errorf("MEDIA_TEMP_DIR: %w", err)
	}
	limits := sandbox.Limits{CPU: 2, MemoryMB: 2048, Timeout: 5 * time.Minute, PIDs: 256}
	return &media.SandboxRunner{Provider: provider, Image: cfg.MediaToolsImage, Limits: limits, Labels: map[string]string{"cf.job": "analyze"}}, filepath.Join(root, "analysis"), nil
}
