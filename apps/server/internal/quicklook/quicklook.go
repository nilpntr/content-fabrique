// Package quicklook is the quick look (P1-04b, docs/04): right after the ingest marks a video ready, four sharp frames spread
// over the clip go to Haiku 5.5 in one call, and what comes back, a title, a one-line description and up to eight tags, is
// stored on the asset and found by the library's search.
//
// The steps of Service.Run:
//
//  1. Frames. The 720 px proxy is cut into twelve candidates at evenly spaced times (one ffmpeg process in the sandbox,
//     512 px on the long side), each is scored for sharpness, and the sharpest of each quarter of the clip wins (Pick). The
//     four pictures are cached in object storage under the asset's prefix (quicklook_0.jpg ...) and listed in the asset's
//     quick_look detail, so the editor's keyframe grid (P2-04) can reuse them and a re-run does not cut them again.
//  2. One model call through the llm layer: stage "quicklook" (config/routing, claude-haiku-5-5 in every profile), a strict
//     tool whose arguments are the answer, and a per-call budget of $0.003 reserved before sending (llm.Budget). The call is
//     metered in usage_ledger like any other, with the asset's id as the run_id, so a clip's cost is one query.
//  3. The answer is untrusted: Validate cleans it like an asset title and rejects what does not fit. Only then is it stored,
//     and the title only while a person has not edited it (assets.title_source).
//
// A failure of ours that a retry can fix (the sandbox, object storage, a busy API) is returned as an error and the job runs
// again. A failure that no retry fixes (the answer did not validate, the model refused, a spend cap) marks the quick look
// `failed` with a code and ends the job. The asset is usable either way: it only lacks its description and tags.
//
// Tenancy: the asset is looked up with the workspace of the job, and every object key is built from the ids of that row.
package quicklook

import (
	"bytes"
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

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The codes a failed quick look records in assets.quick_look (the "error" field). Safe to show.
const (
	FailNoProxy       = "no_proxy"         // the asset has no proxy to cut frames from
	FailFrames        = "frames_failed"    // the tool ran and could not cut frames
	FailInvalidOutput = "invalid_output"   // the answer did not validate
	FailNoAnswer      = "no_answer"        // the model did not call the tool
	FailRefused       = "model_refused"    // stop reason refusal
	FailTruncated     = "answer_truncated" // stop reason max_tokens
	FailSpendCap      = "spend_cap"        // a spend cap or the $0.003 budget refused the call
	FailModelRejected = "model_rejected"   // the API refused the request (a 4xx that is ours to fix)
	FailReplayMiss    = "replay_miss"      // LLM_MODE=replay and no cassette holds the request
	FailInternal      = "internal"         // the last attempt of the job failed
)

// ErrModelUnavailable is returned when the model client cannot be built because no key is configured: not a failure of the
// clip, so the job waits (the worker snoozes it) instead of marking the quick look failed.
var ErrModelUnavailable = jobs.ErrModelUnavailable

// ErrNotReady is returned when the asset is being ingested again: the job runs again later.
var ErrNotReady = jobs.ErrQuickLookNotReady

// Store is what the quick look needs from the database. *store.Store is the real one.
type Store interface {
	GetAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error)
	ApplyQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.QuickLookResult) (store.Asset, error)
	FailQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, detail json.RawMessage) (store.Asset, error)
}

// Objects is the object storage the quick look needs. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
}

// Router resolves the route of a stage. *llm.Router is the real one.
type Router interface {
	Resolve(profile, stage string) (llm.Route, error)
}

// Config builds a Service.
type Config struct {
	Store   Store
	Objects Objects
	// Runner runs ffmpeg. A *media.SandboxRunner gets the job's labels.
	Runner media.Runner
	// TempDir holds one directory per running job. It must be inside the sandbox provider's allowed roots and its path must
	// have no symlinks in it.
	TempDir string
	// Models hands out the model client for a workspace and stage.
	Models llm.ClientFactory
	Router Router
	Logger *slog.Logger
}

// Service runs quick looks. It holds no per-job state.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil || cfg.Objects == nil || cfg.Runner == nil || cfg.Models == nil || cfg.Router == nil:
		return nil, errors.New("quicklook: a store, object storage, a runner, a model factory and a router are required")
	case !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) != cfg.TempDir:
		return nil, fmt.Errorf("quicklook: the temp dir %q must be an absolute, clean path", cfg.TempDir)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.TempDir, 0o750); err != nil {
		return nil, fmt.Errorf("quicklook: temp dir: %w", err)
	}
	return &Service{cfg: cfg}, nil
}

// Options tune one run.
type Options struct {
	// Force runs the quick look again for an asset whose quick look succeeded, and replaces its title (unless a person
	// edited it), description and tags.
	Force bool
}

// Outcome says how a run ended.
type Outcome string

// The outcomes of a run that ended normally.
const (
	// OutcomeDone: the result is stored.
	OutcomeDone Outcome = "done"
	// OutcomeFailed: the quick look is marked failed (Result.Reason says why).
	OutcomeFailed Outcome = "failed"
	// OutcomeSkipped: it had succeeded already and Force was not set.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeGone: the asset is not a ready video of the workspace (deleted, failed, not a video, never existed).
	OutcomeGone Outcome = "gone"
)

// Result is the outcome of a run.
type Result struct {
	Outcome Outcome `json:"outcome"`
	// Reason is the failure code when Outcome is failed.
	Reason string `json:"reason,omitempty"`
	// Title, Description and Tags are what was stored (the title may differ on the asset when a person edited it).
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Usage is what the model call consumed; zero when no call was made.
	Usage llm.Usage `json:"usage"`
	// Model is the model that answered.
	Model string `json:"model,omitempty"`
}

// frameInfo is a cached frame as the asset's quick_look detail lists it.
type frameInfo struct {
	AtMs      int64   `json:"at_ms"`
	Name      string  `json:"name"`
	Sharpness float64 `json:"sharpness"`
}

// detail is the asset's quick_look jsonb.
type detail struct {
	PromptVersion string      `json:"prompt_version"`
	Model         string      `json:"model,omitempty"`
	Profile       string      `json:"profile,omitempty"`
	Frames        []frameInfo `json:"frames,omitempty"`
	Error         string      `json:"error,omitempty"`
}

func (d detail) json() json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Run does the quick look of one asset. See the package comment for what its results and errors mean.
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
	case a.Kind != store.AssetKindVideo, a.Status == store.AssetFailed:
		return Result{Outcome: OutcomeGone}, nil
	case a.Status != store.AssetReady:
		return Result{}, fmt.Errorf("%w (status %s)", ErrNotReady, a.Status)
	case a.QuickLookStatus != nil && *a.QuickLookStatus == store.QuickLookSucceeded && !opts.Force:
		return Result{Outcome: OutcomeSkipped}, nil
	}

	d := detail{PromptVersion: PromptVersion}
	frames, fails, err := s.frames(ctx, a, log)
	d.Frames = frames.info
	if err != nil {
		return Result{}, err
	}
	if fails != "" {
		return s.fail(ctx, a, d, fails, log)
	}

	res, reason, err := s.ask(ctx, a, frames, &d, log)
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		res.Outcome, res.Reason = OutcomeFailed, reason
		return s.fail(ctx, a, d, reason, log, res)
	}
	out := store.QuickLookResult{Title: res.Title, Description: res.Description, Tags: res.Tags, Detail: d.json()}
	if _, err := s.cfg.Store.ApplyQuickLook(ctx, ws, id, out); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{Outcome: OutcomeGone}, nil
		}
		return Result{}, fmt.Errorf("store the quick look: %w", err)
	}
	res.Outcome = OutcomeDone
	log.InfoContext(ctx, "quick look done", "tags", len(res.Tags), "input_tokens", res.Usage.InputTokens, "output_tokens", res.Usage.OutputTokens)
	return res, nil
}

var _ jobs.QuickLooker = (*Service)(nil)

// RunJob is Run for the quick_look job (jobs.QuickLooker): a plain run, whose result River records as the job's output.
func (s *Service) RunJob(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (any, bool, error) {
	res, err := s.Run(ctx, ws, id, Options{})
	if err != nil {
		return nil, false, err
	}
	return res, res.Outcome == OutcomeGone, nil
}

// GiveUp marks the quick look failed (FailInternal), for a job that has used its last attempt.
func (s *Service) GiveUp(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) error {
	return s.Fail(ctx, ws, id, FailInternal)
}

// Fail marks the quick look of an asset failed with a code: for a job that has used its last attempt. A deleted asset is not
// an error.
func (s *Service) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string) error {
	d := detail{PromptVersion: PromptVersion, Error: reason}
	if _, err := s.cfg.Store.FailQuickLook(ctx, ws, id, d.json()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// fail records the failure and returns the result. res, when given, carries what the model call used.
func (s *Service) fail(ctx context.Context, a store.Asset, d detail, reason string, log *slog.Logger, res ...Result) (Result, error) {
	d.Error = reason
	if _, err := s.cfg.Store.FailQuickLook(ctx, a.WorkspaceID, a.ID, d.json()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{Outcome: OutcomeGone}, nil
		}
		return Result{}, fmt.Errorf("record the failure: %w", err)
	}
	log.WarnContext(ctx, "quick look failed", "reason", reason)
	r := Result{Outcome: OutcomeFailed, Reason: reason}
	if len(res) > 0 {
		r = res[0]
		r.Outcome, r.Reason = OutcomeFailed, reason
	}
	return r, nil
}

// ask makes the one model call and validates the answer. reason is the failure code when the clip cannot be labelled; err is
// a failure a retry may fix.
func (s *Service) ask(ctx context.Context, a store.Asset, fr frameSet, d *detail, log *slog.Logger) (Result, string, error) {
	route, err := s.cfg.Router.Resolve("", Stage)
	if err != nil {
		return Result{}, "", fmt.Errorf("route: %w", err) // a missing stage is our configuration, not the clip
	}
	client, _, err := s.cfg.Models.For(ctx, a.WorkspaceID, Stage)
	if err != nil {
		if errors.Is(err, llm.ErrNoKey) {
			return Result{}, "", fmt.Errorf("%w: %w", ErrModelUnavailable, err)
		}
		return Result{}, "", fmt.Errorf("model client: %w", err)
	}
	d.Model, d.Profile = route.Model, route.Profile

	positions := make([]int, len(fr.at))
	for i, at := range fr.at {
		if dur := derefInt64(a.DurationMs); dur > 0 {
			positions[i] = int(min(max(at*100/dur, 0), 100))
		}
	}
	req := route.Apply(buildRequest(fr.jpegs, positions))
	// The asset's id is the ledger's run_id: one query gives a clip's cost. The budget is the acceptance criterion.
	req.Run = llm.RunInfo{ID: a.ID.UUID, Profile: route.Profile, Budget: llm.NewRunBudget(MaxMicros)}

	resp, err := client.Messages(ctx, req)
	res := Result{Usage: resp.Usage, Model: resp.Model}
	if res.Model == "" {
		res.Model = route.Model
	}
	var capErr *llm.CapError
	var apiErr *llm.APIError
	switch {
	case err == nil:
	case errors.Is(err, llm.ErrLedger) && len(resp.Content) > 0:
		// The call is paid and answered but its ledger row could not be written (logged for reconciliation). The label is
		// still worth keeping.
		log.Error("quick look: the usage ledger row was not written", "error", err)
	case errors.As(err, &capErr):
		return res, FailSpendCap, nil
	case errors.Is(err, llm.ErrReplayMiss):
		return res, FailReplayMiss, nil
	case errors.Is(err, llm.ErrUnpriced):
		return Result{}, "", fmt.Errorf("%w: %w", ErrModelUnavailable, err)
	case errors.As(err, &apiErr) && !apiErr.Retryable():
		log.Warn("quick look: the API refused the request", "status", apiErr.Status, "type", apiErr.Type)
		return res, FailModelRejected, nil
	default:
		// Busy or down (the llm layer already retried), a request whose outcome is unknown (it may have been billed: the
		// ledger holds an estimated row at the worst case) or a cancelled context: the job runs again. A call costs a tenth of
		// a cent and every attempt has its own $0.003 budget, so a few repeats stay well inside the acceptance bound.
		return Result{}, "", fmt.Errorf("model call: %w", err)
	}

	switch resp.StopReason {
	case "refusal":
		return res, FailRefused, nil
	case "max_tokens":
		return res, FailTruncated, nil
	}
	var call *llm.Block
	for _, u := range resp.ToolUses() {
		if u.Name == ToolName {
			call = &u
			break
		}
	}
	if call == nil {
		return res, FailNoAnswer, nil
	}
	out, err := Validate(call.Input)
	if err != nil {
		log.Warn("quick look: the model's answer was rejected", "error", err)
		return res, FailInvalidOutput, nil
	}
	res.Title, res.Description, res.Tags = out.Title, out.Description, out.Tags
	return res, "", nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- Frames ----------------------------------------------------------------------------------------------------------

// MaxFrameBytes is the largest picture accepted from the sandbox or from storage: a 512 px JPEG is tens of kilobytes.
const MaxFrameBytes = 2 << 20

// maxProxyBytes is the largest proxy downloaded to cut frames from.
const maxProxyBytes = 4 << 30

// FFmpegTimeout is how long cutting the candidates may take: it is a handful of short seeks in a 720 px file.
const FFmpegTimeout = 2 * time.Minute

// frameSet is the pictures the model sees, in time order.
type frameSet struct {
	jpegs [][]byte
	at    []int64
	info  []frameInfo
}

// FrameName is the object name of the i-th cached frame: quicklook_0.jpg ...
func FrameName(i int) string { return fmt.Sprintf("quicklook_%d.jpg", i) }

// frames returns the pictures of the clip: the cached ones when the asset's detail lists them and they are all there, else
// freshly cut. A non-empty reason means the clip cannot be shown to the model (no proxy, the tool failed).
func (s *Service) frames(ctx context.Context, a store.Asset, log *slog.Logger) (frameSet, string, error) {
	if fr, ok := s.cached(ctx, a, log); ok {
		return fr, "", nil
	}
	proxy, ok, err := s.proxyKey(ctx, a)
	if err != nil {
		return frameSet{}, "", err
	}
	if !ok {
		return frameSet{}, FailNoProxy, nil
	}
	cands, reason, err := s.cut(ctx, a, proxy, log)
	if err != nil || reason != "" {
		return frameSet{}, reason, err
	}
	picked := Pick(cands)
	var fr frameSet
	for i, c := range picked {
		key, err := storage.DerivativeKey(a.WorkspaceID, a.ID, FrameName(i))
		if err != nil {
			return frameSet{}, "", err
		}
		if err := s.cfg.Objects.Put(ctx, key, bytes.NewReader(c.JPEG), int64(len(c.JPEG)), "image/jpeg"); err != nil {
			return frameSet{}, "", fmt.Errorf("store frame %d: %w", i, err)
		}
		fr.jpegs = append(fr.jpegs, c.JPEG)
		fr.at = append(fr.at, c.AtMs)
		fr.info = append(fr.info, frameInfo{AtMs: c.AtMs, Name: FrameName(i), Sharpness: roundSharp(c.Sharpness)})
	}
	return fr, "", nil
}

func roundSharp(v float64) float64 { return float64(int64(v*10)) / 10 }

// cached reads the frames the asset's quick_look detail lists. Any problem (no detail, a name that is not ours, a missing or
// oversized object) means "not cached": the frames are cut again.
func (s *Service) cached(ctx context.Context, a store.Asset, log *slog.Logger) (frameSet, bool) {
	if len(a.QuickLook) == 0 {
		return frameSet{}, false
	}
	var d detail
	if err := json.Unmarshal(a.QuickLook, &d); err != nil || len(d.Frames) == 0 {
		return frameSet{}, false
	}
	var fr frameSet
	for i, f := range d.Frames {
		if f.Name != FrameName(i) { // only the names we write: the detail is data, the key is built from the ids
			return frameSet{}, false
		}
		key, err := storage.DerivativeKey(a.WorkspaceID, a.ID, f.Name)
		if err != nil {
			return frameSet{}, false
		}
		obj, err := s.cfg.Objects.Get(ctx, key)
		if err != nil {
			if !errors.Is(err, storage.ErrNotFound) {
				log.Warn("quick look: a cached frame could not be read; cutting the frames again", "name", f.Name, "error", err)
			}
			return frameSet{}, false
		}
		b, err := readCapped(obj.Body, MaxFrameBytes)
		_ = obj.Body.Close()
		if err != nil || !isJPEG(b) {
			return frameSet{}, false
		}
		fr.jpegs, fr.at, fr.info = append(fr.jpegs, b), append(fr.at, f.AtMs), append(fr.info, f)
	}
	return fr, true
}

func readCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("more than %d bytes", limit)
	}
	return b, nil
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }

// proxyKey is the object key of the asset's proxy, checked to be one of the asset's own objects.
func (s *Service) proxyKey(ctx context.Context, a store.Asset) (string, bool, error) {
	rows, err := s.cfg.Store.ListAssetDerivatives(ctx, a.WorkspaceID, []ids.AssetID{a.ID})
	if err != nil {
		return "", false, fmt.Errorf("list derivatives: %w", err)
	}
	for _, r := range rows {
		if r.Kind != store.DerivativeProxy720 {
			continue
		}
		name, ok := strings.CutPrefix(r.R2Key, storage.AssetPrefix(a.WorkspaceID, a.ID))
		if !ok || !storage.ValidDerivativeName(name) || r.SizeBytes > maxProxyBytes {
			return "", false, nil
		}
		return r.R2Key, true, nil
	}
	return "", false, nil
}

// cut downloads the proxy and cuts the candidates in the sandbox.
func (s *Service) cut(ctx context.Context, a store.Asset, proxyKey string, log *slog.Logger) ([]Candidate, string, error) {
	times := CandidateTimes(derefInt64(a.DurationMs))
	if len(times) == 0 {
		return nil, FailFrames, nil
	}
	dir, err := os.MkdirTemp(s.cfg.TempDir, "quicklook-")
	if err != nil {
		return nil, "", fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	for _, d := range []string{in, out} {
		if err := os.Mkdir(d, 0o755); err != nil { // #nosec G301 -- the container user may be another uid
			return nil, "", err
		}
	}
	proxy := filepath.Join(in, "proxy.mp4")
	if err := s.download(ctx, proxyKey, proxy); err != nil {
		return nil, "", err
	}

	stills := make([]media.Still, len(times))
	for i, at := range times {
		stills[i] = media.Still{AtMs: at, Output: media.OutputPath(fmt.Sprintf("c%02d.jpg", i))}
	}
	args, err := media.StillsArgs(media.InputPath(0), stills, LongSide)
	if err != nil {
		return nil, "", err
	}
	runner := s.cfg.Runner
	if lr, ok := runner.(interface {
		WithLabels(map[string]string) media.Runner
	}); ok {
		runner = lr.WithLabels(map[string]string{"cf.job": "quick_look", "cf.workspace": a.WorkspaceID.String(), "cf.asset": a.ID.String()})
	}
	res, err := runner.Run(ctx, media.Invocation{Args: args, Inputs: []media.Input{{Host: proxy, Container: media.InputPath(0)}}, OutputDir: out, Timeout: FFmpegTimeout})
	if err != nil {
		return nil, "", fmt.Errorf("ffmpeg: %w", err)
	}
	if res.ExitCode >= 129 && res.ExitCode <= 143 {
		return nil, "", fmt.Errorf("ffmpeg was killed (exit code %d): %s", res.ExitCode, media.StderrSnippet(res.Stderr))
	}

	var cands []Candidate
	for i, at := range times {
		b, ok := readOutput(filepath.Join(out, fmt.Sprintf("c%02d.jpg", i)))
		if !ok {
			continue // a time past the last frame of a clip with an odd tail: the other candidates carry on
		}
		sharp, err := Sharpness(b)
		if err != nil {
			sharp = -1
		}
		cands = append(cands, Candidate{AtMs: at, JPEG: b, Sharpness: sharp})
	}
	if len(cands) == 0 {
		log.Warn("quick look: ffmpeg cut no frames", "exit_code", res.ExitCode, "stderr", media.StderrSnippet(res.Stderr))
		return nil, FailFrames, nil
	}
	if res.ExitCode != 0 {
		// A time at the very end of a clip can have no frame, which ffmpeg reports with a non-zero exit although the other
		// pictures were cut: use what there is.
		log.Info("quick look: ffmpeg could not cut every candidate", "exit_code", res.ExitCode, "cut", len(cands), "asked", len(times))
	}
	return cands, "", nil
}

// readOutput reads one picture the sandbox wrote: a regular file, not too big, a JPEG. Its output is untrusted.
func readOutput(path string) ([]byte, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 4 || fi.Size() > MaxFrameBytes {
		return nil, false
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a fixed name in the job's own output directory
	if err != nil || !isJPEG(b) {
		return nil, false
	}
	return b, true
}

// download copies the proxy to path.
func (s *Service) download(ctx context.Context, key, path string) error {
	obj, err := s.cfg.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download proxy: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size > maxProxyBytes {
		return fmt.Errorf("download proxy: %d bytes is over the limit", obj.Size)
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
		return fmt.Errorf("download proxy: %w", err)
	}
	if obj.Size >= 0 && n != obj.Size {
		return fmt.Errorf("download proxy: got %d bytes, storage said %d", n, obj.Size)
	}
	return nil
}
