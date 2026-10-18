// Package quicklook is the quick look (P1-04b, docs/04): right after the ingest marks a video ready, four sharp frames spread
// over the clip go to Haiku 5.5 in one call, and what comes back, a title, a one-line description and up to eight tags, is
// stored on the asset and found by the library's search.
//
// The steps of Service.Run:
//
//  1. Frames. The editor's frame service (editor.FrameService.FramesSized) cuts twelve candidates from the 720 px proxy at
//     evenly spaced times (one ffmpeg process in the sandbox, 512 px on the long side) and caches them in object storage under
//     the asset's frames/ prefix; each is scored for sharpness, and the sharpest of each quarter of the clip wins (Pick). The
//     four chosen are listed in the asset's quick_look detail, so the editor's keyframe grid (P2-04) and a re-run reuse them.
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
	"image/jpeg"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
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
	FailNoSubjects    = "no_subjects"      // a subjects-only run whose answer had no usable boxes: the earlier ones stay
)

// ErrModelUnavailable is returned when the model client cannot be built because no key is configured: not a failure of the
// clip, so the job waits (the worker snoozes it) instead of marking the quick look failed.
var ErrModelUnavailable = jobs.ErrModelUnavailable

// ErrNotReady is returned when the asset is being ingested again: the job runs again later.
var ErrNotReady = jobs.ErrQuickLookNotReady

// Store is what the quick look needs from the database. *store.Store is the real one.
type Store interface {
	GetAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	ApplyQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, r store.QuickLookResult) (store.Asset, error)
	SkipQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	FailQuickLook(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, detail json.RawMessage) (store.Asset, error)
	MergeQuickLookDetail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, patch json.RawMessage) (store.Asset, error)
}

// Objects is the object storage the quick look reads its frames from. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// FrameSource cuts frames of a clip and caches them in object storage. *editor.FrameService is the real one (its FramesSized): the
// quick look and the editor use the same cache, so a frame is cut once however many look at it.
type FrameSource interface {
	FramesSized(ctx context.Context, ws ids.WorkspaceID, asset ids.AssetID, atMs []int64, longSide int) ([]editor.Frame, error)
}

// Router resolves the route of a stage. *llm.Router is the real one.
type Router interface {
	Resolve(profile, stage string) (llm.Route, error)
}

// Config builds a Service.
type Config struct {
	Store   Store
	Objects Objects
	Frames  FrameSource
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
	case cfg.Store == nil || cfg.Objects == nil || cfg.Frames == nil || cfg.Models == nil || cfg.Router == nil:
		return nil, errors.New("quicklook: a store, object storage, a frame service, a model factory and a router are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg}, nil
}

// Options tune one run.
type Options struct {
	// Force runs the quick look again for an asset whose quick look succeeded, and replaces its title (unless a person
	// edited it), description and tags.
	Force bool
	// KeepLabels stores only what is new (the subject boxes, the frames) and leaves the asset's title, description and tags as they are:
	// for assets whose quick look is older than the subject boxes.
	KeepLabels bool
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
	// Subjects is how many frames got a subject box.
	Subjects int `json:"subjects,omitempty"`
}

// frameInfo is a frame the model saw, as the asset's quick_look detail lists it: the time in the clip, the object key it is
// cached under (the editor's frames/ cache, 512 px) and how sharp it measured.
type frameInfo struct {
	AtMs      int64   `json:"at_ms"`
	Ref       string  `json:"ref"`
	Sharpness float64 `json:"sharpness"`
}

// detail is the asset's quick_look jsonb.
type detail struct {
	PromptVersion string      `json:"prompt_version"`
	Model         string      `json:"model,omitempty"`
	Profile       string      `json:"profile,omitempty"`
	Frames        []frameInfo `json:"frames,omitempty"`
	// Subjects is where the person is in the sampled frames (package subject), for the title placement; nil when the model saw nobody
	// or the quick look is older than the field.
	Subjects *subject.Track `json:"subjects,omitempty"`
	// SubjectsRead says the model was asked for the subject boxes (a v2 answer), whether or not anybody was in the frames: a clip with
	// nobody in it has no boxes and must not be asked again.
	SubjectsRead bool   `json:"subjects_read,omitempty"`
	Error        string `json:"error,omitempty"`
}

// SubjectsRead says the quick look detail records that the subject boxes were asked for (with an answer or none).
func SubjectsRead(detail json.RawMessage) bool {
	var d struct {
		Read bool `json:"subjects_read"`
	}
	return json.Unmarshal(detail, &d) == nil && d.Read
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
	case a.Status == store.AssetFailed:
		// The ingest failed after the quick look was owed: nothing is owed any more, and the library should not show it pending.
		if _, err := s.cfg.Store.SkipQuickLook(ctx, ws, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Result{}, fmt.Errorf("skip the quick look of a failed asset: %w", err)
		}
		return Result{Outcome: OutcomeGone}, nil
	case a.Kind != store.AssetKindVideo:
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
	// A run that only adds the subject boxes to a good quick look leaves it as it is when it fails: the labels are still good.
	keep := opts.KeepLabels && a.QuickLookStatus != nil && *a.QuickLookStatus == store.QuickLookSucceeded
	if fails != "" {
		if keep {
			return Result{Outcome: OutcomeFailed, Reason: fails}, nil
		}
		return s.fail(ctx, a, d, fails, log)
	}

	res, reason, err := s.ask(ctx, a, frames, &d, log)
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		res.Outcome, res.Reason = OutcomeFailed, reason
		if keep {
			return res, nil
		}
		return s.fail(ctx, a, d, reason, log, res)
	}
	if keep {
		// Only the new keys of the detail are written: what a person edited while the model call ran stays, and so do the labels.
		res.Title, res.Description, res.Tags = a.Title, a.Description, a.Tags
		if !d.SubjectsRead {
			// An answer with no usable boxes writes nothing: it must not clear boxes an earlier run found, or the marker that they were read.
			res.Outcome, res.Reason = OutcomeFailed, FailNoSubjects
			return res, nil
		}
		patch, err := subjectsPatch(d)
		if err != nil {
			return Result{}, err
		}
		if _, err := s.cfg.Store.MergeQuickLookDetail(ctx, ws, id, patch); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return Result{Outcome: OutcomeGone}, nil
			}
			return Result{}, fmt.Errorf("store the subject boxes: %w", err)
		}
		res.Outcome = OutcomeDone
		return res, nil
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
	req.Run = llm.RunInfo{ID: a.ID.UUID, Profile: route.Profile, Budget: llm.NewRunBudget(MaxMicros), MemberUserID: a.CreatedBy}

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
	case errors.As(err, &capErr) && (capErr.Kind == llm.CapPlatform || capErr.Kind == llm.CapPlatformBusy):
		// The platform breaker is open (D-31): nothing was sent, and it closes at the next UTC day. The job waits (snoozed, no attempt
		// used) like one without model access, rather than failing the clip's label for good.
		return Result{}, "", fmt.Errorf("%w: %w", ErrModelUnavailable, err)
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
	d.Subjects = subjectTrack(out.Subjects, fr)
	// The boxes count as asked for when the answer had usable ones, or said for the frames it spoke of that nobody is there; an answer that
	// had none, or only boxes that did not hold up, leaves the clip to be asked again.
	d.SubjectsRead = d.Subjects != nil || nobodyThere(out.Subjects)
	if d.Subjects != nil {
		res.Subjects = len(d.Subjects.Samples)
	}
	return res, "", nil
}

// subjectsPatch is what a subjects-only run writes into the detail of a good quick look: the frames it saw, the subject boxes (null when
// the answer said nobody is there, which replaces earlier ones) and the marker that they were asked for. It is built key by key so that nothing
// else is written: the prompt version, the model and the rest of the detail stay as the quick look that made the labels left them.
func subjectsPatch(d detail) (json.RawMessage, error) {
	patch := map[string]any{"subjects_read": d.SubjectsRead, "subjects": nil}
	if d.Frames != nil {
		patch["frames"] = d.Frames
	}
	if d.Subjects != nil {
		patch["subjects"] = d.Subjects
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("the subject patch: %w", err)
	}
	return b, nil
}

// nobodyThere says the answer had subject entries and none of them put a person in a frame.
func nobodyThere(reads []SubjectRead) bool {
	for _, r := range reads {
		if r.Present {
			return false
		}
	}
	return len(reads) > 0
}

// subjectTrack turns what the model said about each frame into the clip's subject track: the frame numbers become the times the frames
// were taken at, the aspect is that of the pictures the model saw, and every box is checked against the bounds (an entry that fails is
// dropped). nil when no usable box is left.
func subjectTrack(reads []SubjectRead, fr frameSet) *subject.Track {
	if len(fr.jpegs) == 0 {
		return nil
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(fr.jpegs[0]))
	if err != nil || cfg.Height == 0 {
		return nil
	}
	t := subject.Track{Aspect: float64(cfg.Width) / float64(cfg.Height)}
	seen := map[int]bool{}
	for _, r := range reads {
		if !r.Present || r.Frame < 1 || r.Frame > len(fr.at) || seen[r.Frame] {
			continue
		}
		seen[r.Frame] = true
		t.Samples = append(t.Samples, subject.Sample{AtMs: fr.at[r.Frame-1], X: r.X, Y: r.Y, W: r.W, H: r.H, HeadTopY: r.HeadTopY})
	}
	clean, ok := subject.Clean(t)
	if !ok {
		return nil
	}
	return &clean
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---- Frames ----------------------------------------------------------------------------------------------------------

// MaxFrameBytes is the largest picture accepted from storage: a 512 px JPEG is tens of kilobytes.
const MaxFrameBytes = 2 << 20

// frameSet is the pictures the model sees, in time order.
type frameSet struct {
	jpegs [][]byte
	at    []int64
	info  []frameInfo
}

// frames returns the pictures of the clip. The candidates are cut (or found cached) by the editor's frame service, which
// keeps them in object storage under the asset's frames/ prefix, so the editor's later looks and a re-run of the quick look
// reuse them. A non-empty reason means the clip cannot be shown to the model (no proxy, no frame could be cut).
func (s *Service) frames(ctx context.Context, a store.Asset, log *slog.Logger) (frameSet, string, error) {
	times := CandidateTimes(derefInt64(a.DurationMs))
	if len(times) == 0 {
		return frameSet{}, FailFrames, nil
	}
	got, err := s.cfg.Frames.FramesSized(ctx, a.WorkspaceID, a.ID, times, LongSide)
	var tool *media.ToolError
	switch {
	case errors.Is(err, editor.ErrNoVideo):
		return frameSet{}, FailNoProxy, nil
	case errors.As(err, &tool) && tool.ExitCode >= 129 && tool.ExitCode <= 143:
		return frameSet{}, "", fmt.Errorf("ffmpeg was killed: %w", err) // the machine's doing, not the clip's: the job runs again
	case errors.Is(err, editor.ErrNoFrames):
		log.Warn("quick look: no frame could be cut", "error", err)
		return frameSet{}, FailFrames, nil
	case err != nil:
		return frameSet{}, "", fmt.Errorf("frames: %w", err)
	}

	prefix := storage.FramePrefix(a.WorkspaceID, a.ID)
	var cands []Candidate
	seen := map[int64]bool{}
	for _, f := range got {
		if f.Err != "" || seen[f.AtMs] {
			continue // a moment that could not be decoded, or two requests that landed on one frame
		}
		if !strings.HasPrefix(f.Ref, prefix) {
			return frameSet{}, "", fmt.Errorf("the frame service returned %q, which is not a frame of the asset", f.Ref)
		}
		seen[f.AtMs] = true
		cands = append(cands, Candidate{AtMs: f.AtMs, Ref: f.Ref, JPEG: f.JPEG})
	}
	if len(cands) == 0 {
		return frameSet{}, FailFrames, nil
	}
	// The pictures the frame service has just cut come with their bytes; the cached ones are read from storage, a few at a time.
	if err := s.fill(ctx, cands); err != nil {
		return frameSet{}, "", err
	}
	for i := range cands {
		sharp, err := Sharpness(cands[i].JPEG)
		if err != nil {
			sharp = -1
		}
		cands[i].Sharpness = sharp
	}
	var fr frameSet
	for _, c := range Pick(cands) {
		fr.jpegs, fr.at = append(fr.jpegs, c.JPEG), append(fr.at, c.AtMs)
		fr.info = append(fr.info, frameInfo{AtMs: c.AtMs, Ref: c.Ref, Sharpness: roundSharp(c.Sharpness)})
	}
	return fr, "", nil
}

func roundSharp(v float64) float64 { return float64(int64(v*10)) / 10 }

// readParallel is how many cached pictures are read from storage at once.
const readParallel = 6

// fill reads the bytes of the candidates that have none, in parallel; the first failure ends the rest.
func (s *Service) fill(ctx context.Context, cands []Candidate) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
		sem      = make(chan struct{}, readParallel)
	)
	for i := range cands {
		if len(cands[i].JPEG) > 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			b, err := s.read(ctx, cands[i].Ref)
			if err != nil {
				once.Do(func() { firstErr = err; cancel() })
				return
			}
			cands[i].JPEG = b
		}()
	}
	wg.Wait()
	return firstErr
}

// read reads one cached picture: not too big, and a JPEG (what storage holds is ours, but it is cheap to check).
func (s *Service) read(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.cfg.Objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(obj.Body, MaxFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	if len(b) > MaxFrameBytes || !isJPEG(b) {
		return nil, fmt.Errorf("the cached frame %s is not a JPEG of a plausible size (%d bytes)", key, len(b))
	}
	return b, nil
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }
