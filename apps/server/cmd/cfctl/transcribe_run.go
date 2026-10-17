package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

const transcribeRunUsage = `Usage:
  cfctl transcribe run --workspace <wks_... or org_...> --asset <ast_...> [--force] [--env-file FILE] [--yes]

run enqueues the transcribe job of one asset for its current ingest, and marks the asset's transcript pending: a worker does the
work (and its limits apply: the daily minutes and the daily spend cap). It is how a transcript that is no_speech or failed (a
daily limit that has reset, a provider that was off, a model that has improved) is done again.

The job is unique by asset and ingest, and River keeps finished jobs for a day, so asking again for an ingest that has a job already
is refused. --force enqueues it anyway, with a nonce in its arguments that makes it a different job: the old transcript stays
readable until the new one replaces it, and a job for an older ingest of the asset does nothing.

  --workspace  the workspace (a wks_... id or the Clerk org_... id)
  --asset      the asset
  --force      enqueue even when a job for this ingest exists
  --env-file   the env file (default .env at the repository root)
  --yes        confirm outside plain local development
`

func cmdTranscribeRun(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	const name = "transcribe run"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, transcribeRunUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	assetArg := flags.String("asset", "", "asset id")
	force := flags.Bool("force", false, "enqueue even when a job for this ingest exists")
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
	assetID, err := ids.ParseAssetID(strings.TrimSpace(*assetArg))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --asset is required and must be an asset id like ast_01...\n", name)
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
	if msg := mutationRefusal(name, "it enqueues a transcription", "DATABASE_URL", cfEnv, cfSource, urlSource, dsn, *yes, false); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
		return 2
	}
	if _, err := config.Load(env.get); err != nil {
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
	res, err := enqueueTranscription(ctx, st, ws.ID, assetID, *force)
	switch {
	case errors.Is(err, errNoAudio):
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	case errors.Is(err, store.ErrNotFound):
		_, _ = fmt.Fprintf(errOut, "cfctl %s: no such asset in this workspace\n", name)
		return 1
	case err != nil:
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	case !res.Enqueued:
		_, _ = fmt.Fprintf(errOut, "cfctl %s: a transcription job exists for this ingest of %s already (River keeps finished jobs for a day); --force enqueues another\n", name, assetID)
		return 1
	}
	_, _ = fmt.Fprintf(out, "cfctl %s (CF_ENV=%s)\n  workspace %q (%s)\n  %s: transcription enqueued for ingest generation %d, transcript_status pending (was %s)\n",
		name, cfEnv, ws.Name, ws.ID, assetID, res.Generation, res.Was)
	return 0
}

var errNoAudio = errors.New("the asset has no 16 kHz audio to transcribe (it is not a ready video or audio file with a sound track)")

// transcriptionEnqueued is what enqueueTranscription did.
type transcriptionEnqueued struct {
	Enqueued   bool
	Generation int64
	// Was is the transcript_status before.
	Was string
}

// enqueueTranscription inserts the transcribe job of the asset's current ingest and marks the transcript pending, in one
// transaction. Without force a job that exists for this ingest (even a finished one) is not duplicated and nothing changes; with
// force a nonce makes the job a different one. store.ErrNotFound when the asset is not in the workspace.
func enqueueTranscription(ctx context.Context, st *store.Store, ws ids.WorkspaceID, id ids.AssetID, force bool) (transcriptionEnqueued, error) {
	a, err := st.GetAsset(ctx, ws, id)
	if err != nil {
		return transcriptionEnqueued{}, err
	}
	if a.Status != store.AssetReady {
		return transcriptionEnqueued{}, fmt.Errorf("the asset is %s, not ready: its ingest has to finish first", a.Status)
	}
	derivs, err := st.ListAssetDerivatives(ctx, ws, []ids.AssetID{id})
	if err != nil {
		return transcriptionEnqueued{}, err
	}
	var hasAudio bool
	for _, d := range derivs {
		hasAudio = hasAudio || d.Kind == store.DerivativeAudio16k
	}
	if !hasAudio {
		return transcriptionEnqueued{}, errNoAudio
	}
	client, err := jobs.NewInsertClient(st.Pool(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return transcriptionEnqueued{}, err
	}
	enq := jobs.NewEnqueuer(client, nil)
	args := jobs.TranscribeArgs{WorkspaceID: ws, AssetID: id, Generation: transcribe.GenerationOf(a)}
	if force {
		args.Force = time.Now().UnixNano()
	}
	res := transcriptionEnqueued{Generation: args.Generation, Was: a.TranscriptStatus}
	err = st.InTx(ctx, func(tx *store.Store) error {
		ins, err := enq.InsertInStore(ctx, tx, args, nil)
		if err != nil {
			return err
		}
		if ins.UniqueSkippedAsDuplicate {
			return nil
		}
		res.Enqueued = true
		return tx.SetTranscriptStatus(ctx, ws, id, store.TranscriptPending)
	})
	return res, err
}
