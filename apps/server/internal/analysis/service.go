package analysis

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
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Service runs the analysis of an asset: it downloads the 720 px proxy, makes the ffmpeg signals pass in the sandbox, computes
// shots, reps, holds and dead time, and stores the result in one transaction. It holds no per-job state.
//
// Tenancy: the asset is looked up with the workspace of the job, the proxy's key is checked to be a derivative of that very
// asset, and what the sandbox writes is checked (regular files, size caps) before it is read.
type Service struct {
	cfg Config
}

// Store is what the analysis needs from the database. *store.Store is the real one.
type Store interface {
	GetAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error)
	SaveAnalysis(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.AnalysisResult) error
	GetAssetSignalsMeta(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.SignalsMeta, error)
}

// Objects is the object storage the proxy is read from. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// Config builds a Service.
type Config struct {
	Store   Store
	Objects Objects
	// Runner runs ffmpeg: in production a media.SandboxRunner over the media-tools image.
	Runner media.Runner
	// TempDir must be inside the sandbox provider's allowed roots, without symlinks in its path.
	TempDir string
	Params  Params
	Shots   ShotParams
	Logger  *slog.Logger
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Objects == nil || cfg.Runner == nil || cfg.TempDir == "" {
		return nil, errors.New("analysis: a store, object storage, a runner and a temp dir are required")
	}
	if cfg.Params == (Params{}) {
		cfg.Params = DefaultParams()
	}
	if cfg.Shots == (ShotParams{}) {
		cfg.Shots = DefaultShotParams()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg}, nil
}

// Options tune one run.
type Options struct {
	// Force analyses an asset that has an analysis of the current version already.
	Force bool
}

// Outcome says how a run ended.
type Outcome string

// The outcomes of a run that ended normally.
const (
	// OutcomeDone: the result is stored.
	OutcomeDone Outcome = "done"
	// OutcomeSkipped: it was analysed with this version already and Force was not set.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeGone: the asset is not a ready video of the workspace any more.
	OutcomeGone Outcome = "gone"
	// OutcomeStale: the asset was ingested again while this ran, so the result belongs to a proxy that is gone and was not stored; the
	// new ingest has its own analysis job.
	OutcomeStale Outcome = "stale"
	// OutcomeNoProxy: the asset has no proxy to analyse (nothing more to do; the ingest will make one).
	OutcomeNoProxy Outcome = "no_proxy"
)

// Result is the outcome of a run, recorded as the job's output.
type Result struct {
	Outcome Outcome  `json:"outcome"`
	Summary *Summary `json:"summary,omitempty"`
	Line    string   `json:"line,omitempty"`
}

// Errors the worker acts on.
var (
	// ErrNotReady: the asset is being ingested again; the job runs again later.
	ErrNotReady = jobs.ErrAnalysisNotReady
)

// Size limits of what the sandbox writes.
const (
	maxProxyBytes  = 2 << 30
	maxTextBytes   = 8 << 20
	maxThumbsBytes = media.MaxAnalysisSamples * media.ThumbPixels
)

// Timeouts: the pass decodes the proxy once at many times real time; a fixed part plus a share of the clip's length.
const (
	baseTimeout = 2 * time.Minute
)

func timeoutFor(durationMs int64) time.Duration {
	return baseTimeout + time.Duration(durationMs)*time.Millisecond/2
}

// Run analyses one asset.
func (s *Service) Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, opts Options) (Result, error) {
	a, err := s.cfg.Store.GetAsset(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{Outcome: OutcomeGone}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("get asset: %w", err)
	}
	log := s.cfg.Logger.With("workspace_id", ws.String(), "asset_id", id.String())
	switch {
	case a.Status == store.AssetFailed || a.Kind != store.AssetKindVideo:
		return Result{Outcome: OutcomeGone}, nil
	case a.Status != store.AssetReady:
		return Result{}, fmt.Errorf("%w (status %s)", ErrNotReady, a.Status)
	}
	if !opts.Force {
		// Up to date means: made by this version of the analysis from the proxy this ingest made. An asset that was ingested again
		// has a new ingested_at, so its analysis is made again.
		meta, err := s.cfg.Store.GetAssetSignalsMeta(ctx, ws, id)
		switch {
		case err == nil && meta.Version >= Version && sameTime(meta.SourceIngestedAt, a.IngestedAt):
			return Result{Outcome: OutcomeSkipped}, nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return Result{}, fmt.Errorf("look for an earlier analysis: %w", err)
		}
	}

	proxyKey, err := s.proxyKey(ctx, a)
	if errors.Is(err, errNoProxy) {
		return Result{Outcome: OutcomeNoProxy}, nil
	}
	if err != nil {
		return Result{}, err
	}
	var dur int64
	if a.DurationMs != nil {
		dur = *a.DurationMs
	}
	raw, err := s.signals(ctx, proxyKey, dur, log)
	if err != nil {
		return Result{}, err
	}

	an := Analyze(raw, s.cfg.Params, s.cfg.Shots)
	// The moments carry the asset's tags as labels when they are stored (the quick look may not have landed yet; it labels them again
	// when it does).
	res, err := toStore(an, a.IngestedAt)
	if err != nil {
		return Result{}, err
	}
	if err := s.cfg.Store.SaveAnalysis(ctx, ws, id, res); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{Outcome: OutcomeGone}, nil
		}
		if errors.Is(err, store.ErrStaleAnalysis) {
			log.InfoContext(ctx, "analysis is stale: the asset was ingested again while it ran; not stored")
			return Result{Outcome: OutcomeStale}, nil
		}
		return Result{}, fmt.Errorf("store the analysis: %w", err)
	}
	log.InfoContext(ctx, "analysis done", "shots", len(an.Shots), "moments", len(an.Moments), "reps", an.Summary.Reps, "dead_ms", an.Summary.DeadMs)
	return Result{Outcome: OutcomeDone, Summary: &an.Summary, Line: an.Summary.Line()}, nil
}

var _ jobs.Analyzer = (*Service)(nil)

// RunJob is Run for the analyze job (jobs.Analyzer).
func (s *Service) RunJob(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (any, bool, error) {
	res, err := s.Run(ctx, ws, id, Options{})
	if err != nil {
		return nil, false, err
	}
	return res, res.Outcome == OutcomeGone, nil
}

// toStore converts an analysis to the rows the store writes.
func toStore(an Analysis, ingested *time.Time) (store.AnalysisResult, error) {
	series, err := Pack(an.Series)
	if err != nil {
		return store.AnalysisResult{}, err
	}
	summary, err := json.Marshal(an.Summary)
	if err != nil {
		return store.AnalysisResult{}, err
	}
	out := store.AnalysisResult{
		Version: Version, SourceIngestedAt: ingested, StepMs: an.Series.StepMs, DurationMs: an.Series.DurationMs(), Series: series, Summary: summary,
	}
	for _, sh := range an.Shots {
		sig, err := json.Marshal(sh)
		if err != nil {
			return store.AnalysisResult{}, err
		}
		out.Shots = append(out.Shots, store.NewShot{Idx: sh.Idx, StartMs: sh.StartMs, EndMs: sh.EndMs, Signals: sig})
	}
	for _, m := range an.Moments {
		attrs, err := json.Marshal(m.Attrs)
		if err != nil {
			return store.AnalysisResult{}, err
		}
		out.Moments = append(out.Moments, store.NewMoment{
			Kind: m.Kind, StartMs: m.StartMs, EndMs: m.EndMs, Score: m.Score, Labels: m.Labels, Attrs: attrs, Description: m.Description,
		})
	}
	return out, nil
}

// sameTime reports whether two instants are the same (both unset counts); the database keeps microseconds.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

var errNoProxy = errors.New("analysis: the asset has no proxy")

// proxyKey finds the asset's proxy and checks that its key is the key of a derivative of this very asset.
func (s *Service) proxyKey(ctx context.Context, a store.Asset) (string, error) {
	derivs, err := s.cfg.Store.ListAssetDerivatives(ctx, a.WorkspaceID, []ids.AssetID{a.ID})
	if err != nil {
		return "", fmt.Errorf("list derivatives: %w", err)
	}
	for _, d := range derivs {
		if d.Kind != store.DerivativeProxy720 || d.AssetID != a.ID || d.WorkspaceID != a.WorkspaceID {
			continue
		}
		name, ok := strings.CutPrefix(d.R2Key, storage.AssetPrefix(a.WorkspaceID, a.ID))
		if !ok || !storage.ValidDerivativeName(name) {
			return "", fmt.Errorf("the proxy key of asset %s is not a derivative key of it", a.ID)
		}
		return d.R2Key, nil
	}
	return "", errNoProxy
}

// signals downloads the proxy, runs the ffmpeg pass in the sandbox and reads its output.
func (s *Service) signals(ctx context.Context, proxyKey string, durationMs int64, log *slog.Logger) (Raw, error) {
	dir, err := os.MkdirTemp(s.cfg.TempDir, "analysis-")
	if err != nil {
		return Raw{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return Raw{}, err
	}
	local := filepath.Join(dir, "in", "proxy")
	if err := s.download(ctx, proxyKey, local); err != nil {
		return Raw{}, err
	}
	return runPass(ctx, s.cfg.Runner, dir, local, durationMs, log)
}

// AnalyzeFile runs the analysis on a proxy file on this machine, without a database or object storage: cfctl's development
// command. The file is copied into a job directory under tempDir (which must be inside the sandbox provider's allowed roots),
// so the sandbox never sees anything else.
func AnalyzeFile(ctx context.Context, runner media.Runner, tempDir, path string, durationMs int64, p Params, sp ShotParams) (Analysis, error) {
	dir, err := os.MkdirTemp(tempDir, "analysis-")
	if err != nil {
		return Analysis{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return Analysis{}, err
	}
	src, err := os.Open(path) // #nosec G304 -- the operator named this file
	if err != nil {
		return Analysis{}, err
	}
	defer func() { _ = src.Close() }()
	local := filepath.Join(dir, "in", "proxy")
	dst, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- our job dir
	if err != nil {
		return Analysis{}, err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Analysis{}, err
	}
	raw, err := runPass(ctx, runner, dir, local, durationMs, slog.Default())
	if err != nil {
		return Analysis{}, err
	}
	return Analyze(raw, p, sp), nil
}

// runPass runs the ffmpeg signals pass on the proxy at local (inside dir) and reads what it wrote.
func runPass(ctx context.Context, runner media.Runner, dir, local string, durationMs int64, log *slog.Logger) (Raw, error) {
	out, err := os.MkdirTemp(dir, "out-")
	if err != nil {
		return Raw{}, err
	}
	res, err := runner.Run(ctx, media.Invocation{
		Args: media.AnalysisArgs(media.InputPath(0)), Inputs: []media.Input{{Host: local, Container: media.InputPath(0)}},
		OutputDir: out, Timeout: timeoutFor(durationMs),
	})
	if err != nil {
		return Raw{}, fmt.Errorf("ffmpeg: %w", err)
	}
	if res.ExitCode != 0 {
		te := &media.ToolError{Tool: "ffmpeg", ExitCode: res.ExitCode, Stderr: media.StderrSnippet(res.Stderr)}
		log.Warn("analysis: the signals pass failed", "exit_code", res.ExitCode, "stderr", te.Stderr)
		return Raw{}, te
	}
	thumbs, err := readOutput(filepath.Join(out, media.AnalysisThumbs), maxThumbsBytes)
	if err != nil {
		return Raw{}, err
	}
	sharp, err := readOutput(filepath.Join(out, media.AnalysisSharp), maxTextBytes)
	if err != nil {
		return Raw{}, err
	}
	scene, err := readOutput(filepath.Join(out, media.AnalysisScene), maxTextBytes)
	if err != nil {
		return Raw{}, err
	}
	return ParseRaw(thumbs, sharp, scene)
}

// readOutput reads a file the sandbox wrote: it must be a regular file no bigger than limit (a tool's output is untrusted).
func readOutput(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("analysis: ffmpeg wrote no %s", filepath.Base(path))
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, fmt.Errorf("analysis: %s is not a regular file of a plausible size (%d bytes)", filepath.Base(path), fi.Size())
	}
	return os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its own job dir
}

func (s *Service) download(ctx context.Context, key, path string) error {
	obj, err := s.cfg.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download the proxy: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > maxProxyBytes {
		return fmt.Errorf("the proxy is %d bytes, which is not plausible", obj.Size)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, maxProxyBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download the proxy: %w", err)
	}
	if n != obj.Size {
		return fmt.Errorf("the proxy download is %d bytes, storage reported %d", n, obj.Size)
	}
	return nil
}
