// Package ingest is the asset_ingest job's pipeline (P1-04, architecture section 5): it takes an uploaded original from
// object storage, probes and validates it inside the sandbox, makes the derivatives the UI and the analysis need (a seek
// friendly 720 px proxy, poster, sprite sheet with its WebVTT track, waveform peaks, 16 kHz audio and a hover preview),
// uploads them next to the original and marks the asset ready.
//
// Everything that parses the file runs through a media.Runner, which in production is a sandbox provider: the worker
// itself never opens the original. What comes back out of the sandbox is treated as untrusted too (regular files only,
// size caps, the WAV header is checked before its peaks are read).
//
// Tenancy: the pipeline acts with the workspace and asset of the job, looks the asset up with both, and builds every
// object key from the ids of that row. The object store it writes through is a scopedObjects, which refuses any key
// outside the asset's own prefix.
//
// Failures are of two kinds. A fileError is the file's fault (not a video, undecodable, too long): the asset is marked
// failed with a reason code that is safe to show and Run returns normally, so the job ends and is not retried. Anything
// else (the sandbox is down, storage did not answer, the worker is shutting down) is returned as an error and the job
// runs again; its last attempt gives up through Fail.
//
// A re-run is safe: derivative names are fixed, so the objects and rows are replaced, and rows of kinds the new run does
// not produce are removed.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ErrAssetGone is returned when the asset cannot be ingested any more: it does not exist in the workspace, was deleted,
// or has failed. Retrying cannot help.
var ErrAssetGone = errors.New("ingest: the asset is deleted, failed or not in this workspace")

// Objects is the object storage the pipeline needs. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
	// Delete removes one object; one that is not there is not an error.
	Delete(ctx context.Context, key string) error
}

// Store is what the pipeline needs from the database. *store.Store is the real one.
type Store interface {
	ClaimAssetForIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	SetIngestProgress(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, progress int, stage string) error
	FinishAssetIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.IngestResult) (store.Asset, error)
	FailAssetIngest(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) (store.Asset, error)
	ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error)
	GetAssetForObjectDeletion(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
}

// Config builds a Pipeline.
type Config struct {
	Store   Store
	Objects Objects
	// Runner runs the media tools. A *media.SandboxRunner gets the job's labels.
	Runner media.Runner
	// TempDir holds one directory per running job. It must be inside the sandbox provider's allowed roots and its path must
	// have no symlinks in it (resolve it with filepath.EvalSymlinks first).
	TempDir string
	// MaxDuration is the longest accepted clip; zero means DefaultMaxDuration. New refuses a value whose worst case
	// (WorstCase) does not fit in JobTimeout, so the tool timeouts, the job timeout and the accepted length agree.
	MaxDuration time.Duration
	Logger      *slog.Logger
}

// Pipeline ingests assets. It holds no per-job state, so one Pipeline serves all the workers.
type Pipeline struct {
	cfg Config
}

// New returns a Pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil || cfg.Objects == nil || cfg.Runner == nil:
		return nil, errors.New("ingest: a store, object storage and a runner are required")
	case !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) != cfg.TempDir:
		return nil, fmt.Errorf("ingest: the temp dir %q must be an absolute, clean path", cfg.TempDir)
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = DefaultMaxDuration
	}
	if worst := WorstCase(cfg.MaxDuration); worst > JobTimeout {
		return nil, fmt.Errorf("ingest: a %v clip needs up to %v, more than the job timeout of %v; accept shorter clips", cfg.MaxDuration, worst, JobTimeout)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.TempDir, 0o750); err != nil {
		return nil, fmt.Errorf("ingest: temp dir: %w", err)
	}
	p := &Pipeline{cfg: cfg}
	p.sweepStale(time.Now())
	return p, nil
}

// sweepStale removes the job directories (ingest-*) of runs that died without cleaning up (a crashed or killed worker).
// A directory counts as stale when nothing in it changed for longer than JobTimeout, which is longer than any run lives, so
// a directory of a run on another worker that shares the temp dir is never touched.
func (p *Pipeline) sweepStale(now time.Time) {
	entries, err := os.ReadDir(p.cfg.TempDir)
	if err != nil {
		p.cfg.Logger.Warn("could not list the ingest temp dir", "dir", p.cfg.TempDir, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "ingest-") {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) <= JobTimeout {
			continue
		}
		if err := os.RemoveAll(filepath.Join(p.cfg.TempDir, e.Name())); err != nil {
			p.cfg.Logger.Warn("could not remove a stale ingest directory", "dir", e.Name(), "error", err)
			continue
		}
		p.cfg.Logger.Info("removed a stale ingest directory", "dir", e.Name())
	}
}

// Result is the outcome of a run that ended normally.
type Result struct {
	// Status is store.AssetReady, or store.AssetFailed when the file could not be ingested (see Reason).
	Status string `json:"status"`
	// Reason is the failure code when Status is failed.
	Reason string `json:"reason,omitempty"`
	// Derivatives lists the kinds that were written.
	Derivatives []string `json:"derivatives,omitempty"`
}

// Fail marks an asset failed, for a file that cannot be processed or a job that has given up on its last attempt. A failed
// asset has no derivatives, whatever it had before (a re-run that fails does not leave the old ones behind): the rows go in
// the same transaction and the objects are removed afterwards (the original stays). A deleted or already finished asset is
// not an error.
func (p *Pipeline) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) error {
	old, err := p.cfg.Store.ListAssetDerivatives(ctx, ws, []ids.AssetID{id})
	if err != nil {
		return fmt.Errorf("list derivatives: %w", err)
	}
	if _, err := p.cfg.Store.FailAssetIngest(ctx, ws, id, reason); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	keys := make([]string, len(old))
	for i, d := range old {
		keys[i] = d.R2Key
	}
	p.deleteDerivatives(ctx, ws, id, keys)
	return nil
}

// deleteDerivatives removes derivative objects of the asset, best effort: a key that is not a derivative key of this asset
// is skipped (the original is never removed here), and a failure is logged; the asset's deletion job removes the whole
// prefix in the end.
func (p *Pipeline) deleteDerivatives(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, keys []string) {
	prefix := storage.AssetPrefix(ws, id)
	for _, key := range keys {
		name, ok := strings.CutPrefix(key, prefix)
		if !ok || !storage.ValidDerivativeName(name) {
			p.cfg.Logger.Warn("not removing an object that is not a derivative of the asset", "asset_id", id.String())
			continue
		}
		if err := p.cfg.Objects.Delete(ctx, key); err != nil {
			p.cfg.Logger.Warn("could not remove a derivative object", "asset_id", id.String(), "name", name, "error", err)
		}
	}
}

// Run ingests the asset of the workspace. See the package comment for what its results and errors mean.
func (p *Pipeline) Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Result, error) {
	asset, err := p.cfg.Store.ClaimAssetForIngest(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrAssetGone
	}
	if err != nil {
		return Result{}, fmt.Errorf("claim asset: %w", err)
	}
	log := p.cfg.Logger.With("workspace_id", ws.String(), "asset_id", id.String(), "kind", asset.Kind)

	objs, err := newScopedObjects(p.cfg.Objects, ws, id, asset.R2Key)
	if err != nil {
		return Result{}, p.giveUp(ctx, ws, id, store.FailureProcessing, err, log)
	}
	dir, err := os.MkdirTemp(p.cfg.TempDir, "ingest-")
	if err != nil {
		return Result{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	runner := p.cfg.Runner
	if lr, ok := runner.(interface {
		WithLabels(map[string]string) media.Runner
	}); ok {
		runner = lr.WithLabels(map[string]string{"cf.job": "asset_ingest", "cf.workspace": ws.String(), "cf.asset": id.String()})
	}
	r := &run{p: p, ws: ws, id: id, asset: asset, dir: dir, runner: runner, objs: objs, log: log}

	start := time.Now()
	res, err := r.execute(ctx)
	var fe *fileError
	switch {
	case errors.As(err, &fe):
		log.WarnContext(ctx, "asset ingest failed: the file cannot be processed", "reason", fe.reason, "error", fe.err)
		r.removeUploaded(ctx) // the failure also removes the rows of an earlier run; whatever this run wrote goes too
		return Result{Status: store.AssetFailed, Reason: fe.reason}, p.markFailed(ctx, ws, id, fe.reason)
	case errors.Is(err, ErrAssetGone):
		r.removeUploaded(ctx) // deleted while the job worked: nobody will use what it wrote
		return Result{}, err
	case err != nil:
		return Result{}, err
	}
	log.InfoContext(ctx, "asset ingested", "duration", time.Since(start).Round(time.Millisecond), "derivatives", res.Derivatives)
	return res, nil
}

// giveUp marks the asset failed for a problem of ours that no retry will fix, and returns the error to report.
func (p *Pipeline) giveUp(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, cause error, log *slog.Logger) error {
	log.ErrorContext(ctx, "asset ingest cannot continue", "reason", reason, "error", cause)
	if err := p.markFailed(ctx, ws, id, reason); err != nil {
		return err
	}
	return ErrAssetGone
}

// markFailed records the failure even if ctx is cancelled meanwhile. A row that is not processing any more is fine.
func (p *Pipeline) markFailed(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.Fail(cctx, ws, id, reason); err != nil {
		return fmt.Errorf("mark asset failed: %w", err)
	}
	return nil
}

// run is one execution of the pipeline.
type run struct {
	p      *Pipeline
	ws     ids.WorkspaceID
	id     ids.AssetID
	asset  store.Asset
	dir    string
	runner media.Runner
	objs   *scopedObjects
	log    *slog.Logger

	derived []*derivative
	// durMs is the probed duration, which scales the tool timeouts (0 until the probe has run).
	durMs int64
	// uploaded are the derivative names this run has written to object storage so far.
	uploaded []string
}

// derivative is a file the run made and will upload.
type derivative struct {
	kind        string
	name        string
	contentType string
	path        string
	size        int64
	meta        map[string]any
}

// Where the progress bar stands when a stage starts. The proxy is the long part, so it gets the widest range.
const (
	pctDownload    = 2
	pctProbe       = 8
	pctProxy       = 12
	pctDerivatives = 62
	pctUpload      = 85
)

func (r *run) progress(ctx context.Context, pct int, stage string) error {
	err := r.p.cfg.Store.SetIngestProgress(ctx, r.ws, r.id, pct, stage)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrAssetGone
	case err != nil:
		r.log.WarnContext(ctx, "could not record ingest progress", "error", err) // cosmetic: the job goes on
	}
	return nil
}

func (r *run) execute(ctx context.Context) (Result, error) {
	if err := r.progress(ctx, pctDownload, store.StageDownloading); err != nil {
		return Result{}, err
	}
	for _, sub := range []string{"in", "derived"} {
		if err := os.Mkdir(filepath.Join(r.dir, sub), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
			return Result{}, err
		}
	}
	original := filepath.Join(r.dir, "in", "original")
	if err := r.download(ctx, original); err != nil {
		return Result{}, err
	}

	if err := r.progress(ctx, pctProbe, store.StageProbing); err != nil {
		return Result{}, err
	}
	meta, err := r.probe(ctx, original)
	if err != nil {
		return Result{}, err
	}
	if reason, verr := Validate(r.asset.Kind, meta, r.p.cfg.MaxDuration); reason != "" {
		return Result{}, fileErr(reason, verr)
	}
	r.durMs = meta.DurationMs

	var out ingested
	switch r.asset.Kind {
	case store.AssetKindVideo:
		out, err = r.video(ctx, original, meta)
	case store.AssetKindAudio:
		out, err = r.audio(ctx, original, meta)
	case store.AssetKindImage:
		out, err = r.image(ctx, original, meta)
	}
	if err != nil {
		return Result{}, err
	}

	if err := r.progress(ctx, pctUpload, store.StageUploading); err != nil {
		return Result{}, err
	}
	rows, err := r.upload(ctx)
	if err != nil {
		return Result{}, err
	}
	probe, err := json.Marshal(meta)
	if err != nil {
		return Result{}, err
	}
	out.result.Probe = probe
	out.result.Derivatives = rows
	// What an earlier run made, to remove the kinds this one no longer makes (a clip that lost its audio track).
	before, err := r.p.cfg.Store.ListAssetDerivatives(ctx, r.ws, []ids.AssetID{r.id})
	if err != nil {
		return Result{}, fmt.Errorf("list derivatives: %w", err)
	}
	if _, err := r.p.cfg.Store.FinishAssetIngest(ctx, r.ws, r.id, out.result); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, ErrAssetGone
		}
		return Result{}, fmt.Errorf("finish asset: %w", err)
	}
	r.removeStale(ctx, before, rows)
	kinds := make([]string, len(r.derived))
	for i, d := range r.derived {
		kinds[i] = d.kind
	}
	return Result{Status: store.AssetReady, Derivatives: kinds}, nil
}

// ingested is what a kind-specific step reports for the asset row.
type ingested struct {
	result store.IngestResult
}

// removeUploaded deletes the derivatives this run wrote, for a run that ended without making the asset ready (it was deleted
// while the job worked, or the file turned out bad late). It removes only what this run uploaded, by name: the original is
// the deletion job's business. It uses a fresh context, as the run's may be the thing that ended.
func (r *run) removeUploaded(ctx context.Context) {
	if len(r.uploaded) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, name := range r.uploaded {
		if err := r.objs.delete(cctx, name); err != nil {
			r.log.WarnContext(ctx, "could not remove a derivative of an ingest that did not finish", "name", name, "error", err)
		}
	}
}

// removeStale deletes the objects of derivative kinds that an earlier run made and this one did not.
func (r *run) removeStale(ctx context.Context, before []store.Derivative, now []store.NewDerivative) {
	made := make(map[string]bool, len(now))
	for _, d := range now {
		made[d.Kind] = true
	}
	var keys []string
	for _, d := range before {
		if !made[d.Kind] {
			keys = append(keys, d.R2Key)
		}
	}
	if len(keys) > 0 {
		r.p.deleteDerivatives(ctx, r.ws, r.id, keys)
	}
}

// download streams the original to path, checking its size against the row.
func (r *run) download(ctx context.Context, path string) error {
	obj, err := r.objs.getOriginal(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return fileErr(store.FailureProcessing, errors.New("the uploaded object is missing"))
	}
	if err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size != r.asset.SizeBytes {
		return fmt.Errorf("download original: storage reports %d bytes, the asset has %d", obj.Size, r.asset.SizeBytes)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, r.asset.SizeBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	if n != r.asset.SizeBytes {
		return fmt.Errorf("download original: got %d bytes, want %d", n, r.asset.SizeBytes)
	}
	return nil
}

// probe runs ffprobe in the sandbox. A file ffprobe refuses is the file's fault; a sandbox that could not run is ours.
func (r *run) probe(ctx context.Context, path string) (media.Metadata, error) {
	probe := media.Probe
	if r.asset.Kind == store.AssetKindImage {
		probe = media.ProbeImage
	}
	m, err := probe(ctx, r.runner, path)
	var te *media.ToolError
	switch {
	case errors.As(err, &te) && killedExit(te.ExitCode):
		return m, fmt.Errorf("probe: %w (exit code %d)", errKilled, te.ExitCode)
	case errors.As(err, &te):
		return m, fileErr(classifyToolError(te, store.FailureUnsupportedFormat), err)
	case errors.Is(err, media.ErrUnparsable):
		return m, fileErr(store.FailureCorruptMedia, err) // ffprobe ran but its output made no sense
	case err != nil:
		return m, fmt.Errorf("probe: %w", err) // the sandbox, the disk or the context: not the file's fault
	}
	return m, nil
}
