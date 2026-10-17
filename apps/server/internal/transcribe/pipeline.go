package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ErrAssetGone is returned when there is nothing to transcribe any more: the asset was deleted or is not in the workspace.
var ErrAssetGone = errors.New("transcribe: the asset is deleted or not in this workspace")

// Objects is the object storage the pipeline reads. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// Store is what the pipeline needs from the database. *store.Store is the real one.
type Store interface {
	GetAsset(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (store.Asset, error)
	ListAssetDerivatives(ctx context.Context, ws ids.WorkspaceID, assetIDs []ids.AssetID) ([]store.Derivative, error)
	SetTranscriptStatus(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, status string) error
	FailTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, source *time.Time) error
	SaveTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, t store.NewTranscript) (store.Transcript, error)
	InsertUsage(ctx context.Context, e store.UsageEntry) error
	TranscribedMs(ctx context.Context, ws ids.WorkspaceID, from, to time.Time) (int64, error)
}

// SpendGuard is the workspace's daily spend cap. *llm.DailyGuard is the real one: the same cap, ledger and reservation algorithm
// the model client uses, so a day of transcription and a day of editing draw on one budget.
type SpendGuard interface {
	// Reserve sets the worst-case cost aside, or returns a *llm.CapError when the cap cannot cover it. release is called once the
	// call's ledger rows are written.
	Reserve(ctx context.Context, ws ids.WorkspaceID, est int64) (release func(), err error)
}

var _ SpendGuard = (*llm.DailyGuard)(nil)

// Config builds a Pipeline.
type Config struct {
	Store    Store
	Objects  Objects
	Provider Provider
	Detector Detector
	// Spend is the daily spend cap. Required whenever the provider has a price: a call that costs money is never made without
	// reserving its worst case first.
	Spend SpendGuard
	// DailyMinutes is the most audio a workspace may have transcribed per UTC day (TRANSCRIBE_DAILY_MINUTES), whoever transcribes
	// it; 0 turns the limit off (tests). A transcription that would pass it fails with minutes_cap.
	DailyMinutes int
	// Language is the language to ask for; empty detects it per clip.
	Language string
	// BilledTo is the ledger's billed_to: platform, or test in local, ci and bench.
	BilledTo string
	// TempDir holds one directory per running job. It must be inside the sandbox's allowed roots, without symlinks in its path.
	TempDir string
	Logger  *slog.Logger

	now func() time.Time
}

// Pipeline transcribes assets. It holds no per-job state beyond the audio in flight, so one Pipeline serves all the workers.
type Pipeline struct {
	cfg Config

	mu       sync.Mutex
	inflight map[ids.WorkspaceID]int64 // audio ms of the calls in flight, per workspace
}

// New returns a Pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil || cfg.Objects == nil || cfg.Provider == nil || cfg.Detector == nil:
		return nil, errors.New("transcribe: a store, object storage, a provider and a detector are required")
	case !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) != cfg.TempDir:
		return nil, fmt.Errorf("transcribe: the temp dir %q must be an absolute, clean path", cfg.TempDir)
	case cfg.DailyMinutes < 0:
		return nil, errors.New("transcribe: DailyMinutes must not be negative")
	case cfg.BilledTo == "":
		cfg.BilledTo = store.BilledToPlatform
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if err := os.MkdirAll(cfg.TempDir, 0o750); err != nil {
		return nil, fmt.Errorf("transcribe: temp dir: %w", err)
	}
	return &Pipeline{cfg: cfg, inflight: map[ids.WorkspaceID]int64{}}, nil
}

// Result is the outcome of a run that ended normally.
type Result struct {
	// Status is the asset's transcript_status afterwards: ready, no_speech or failed, or none for an asset without audio, or
	// stale for a run that found the asset ingested again (the newer ingest has its own job; nothing was written).
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Provider string `json:"provider,omitempty"`
	Words    int    `json:"words,omitempty"`
	AudioMs  int64  `json:"audio_ms,omitempty"`
	SpeechMs int64  `json:"speech_ms,omitempty"`
}

// StatusStale is Result.Status of a run that did nothing because its ingest is no longer the asset's.
const StatusStale = "stale"

// GenerationOf is the ingest generation of an asset: the time its ingest finished, in microseconds (0 when it has none). A
// transcribe job carries the generation it was enqueued for; it is part of the job's unique key, so an asset that is ingested
// again gets a new transcription instead of being deduplicated against the old job.
func GenerationOf(a store.Asset) int64 {
	if a.IngestedAt == nil {
		return 0
	}
	return a.IngestedAt.UnixMicro()
}

func instantOf(generation int64) *time.Time {
	if generation == 0 {
		return nil
	}
	t := time.UnixMicro(generation).UTC()
	return &t
}

// Fail marks the transcript failed (gave_up), for a job that has used its last attempt, but only if the asset is still at the
// ingest the job was for. A deleted or re-ingested asset is not an error.
func (p *Pipeline) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) error {
	err := p.cfg.Store.FailTranscript(ctx, ws, id, store.TranscriptGaveUp, instantOf(generation))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// Run transcribes the asset of the workspace for the ingest generation: it checks the 16 kHz audio for speech, transcribes it when
// there is some, places the words on the audio, works out the speech structure and saves it. Before a call to the provider the
// workspace's daily minutes and, for a provider that costs money, its daily spend cap are checked and the worst case is
// reserved; every billed call gets its own ledger row, written whatever happens next.
//
// Errors: ErrAssetGone (nothing to do), ErrNotConfigured (the provider is switched off; the job waits), and any other error is
// retried. Audio the provider cannot use, and a workspace over a daily limit, end the run with status failed and no error: a retry
// would not change either. An asset ingested again since the job was made ends the run with status stale, writing nothing.
func (p *Pipeline) Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, generation int64) (Result, error) {
	asset, err := p.cfg.Store.GetAsset(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrAssetGone
	}
	if err != nil {
		return Result{}, fmt.Errorf("get asset: %w", err)
	}
	log := p.cfg.Logger.With("workspace_id", ws.String(), "asset_id", id.String(), "provider", p.cfg.Provider.Name())
	if asset.Status != store.AssetReady || GenerationOf(asset) != generation {
		log.InfoContext(ctx, "transcription skipped: the asset was ingested again", "status", asset.Status)
		return Result{Status: StatusStale}, nil
	}
	source := asset.IngestedAt

	rows, err := p.cfg.Store.ListAssetDerivatives(ctx, ws, []ids.AssetID{id})
	if err != nil {
		return Result{}, fmt.Errorf("list derivatives: %w", err)
	}
	var audioKey string
	for _, d := range rows {
		if d.Kind == store.DerivativeAudio16k {
			audioKey = d.R2Key
		}
	}
	if audioKey == "" {
		// A silent clip or a picture: nothing was extracted, so there is nothing to transcribe.
		if err := p.setStatus(ctx, ws, id, store.TranscriptNone); err != nil {
			return Result{}, err
		}
		log.InfoContext(ctx, "transcription skipped: the asset has no audio", "kind", asset.Kind)
		return Result{Status: store.TranscriptNone}, nil
	}
	want, err := storage.DerivativeKey(ws, id, "audio_16k.wav")
	if err != nil || audioKey != want {
		return Result{}, fmt.Errorf("the audio key of the asset is not the key of its ids")
	}

	dir, err := os.MkdirTemp(p.cfg.TempDir, "transcribe-")
	if err != nil {
		return Result{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "audio.wav")
	if err := p.download(ctx, audioKey, path); err != nil {
		if errors.Is(err, ErrUnusableAudio) {
			return p.unusable(ctx, ws, id, source, err, log)
		}
		return Result{}, err
	}
	pcm, err := ReadWAV(path)
	if err != nil {
		return p.unusable(ctx, ws, id, source, err, log)
	}
	audio := Audio{Path: path, DurationMs: DurationMsOf(len(pcm))}

	start := time.Now()
	segs, err := p.cfg.Detector.DetectSpeech(ctx, audio)
	if err != nil {
		return Result{}, fmt.Errorf("speech check: %w", err)
	}
	speechMs := SpeechMs(segs)
	res := Result{Provider: p.cfg.Provider.Name(), AudioMs: audio.DurationMs, SpeechMs: speechMs}
	if speechMs < MinSpeechMs {
		log.InfoContext(ctx, "no speech", "audio_ms", audio.DurationMs, "speech_ms", speechMs)
		res.Status = store.TranscriptNoSpeech
		return p.save(ctx, ws, id, res, p.noSpeech(source, audio, speechMs))
	}

	// The limits come before anything that costs: minutes, then money.
	releaseMinutes, reason, err := p.reserveMinutes(ctx, ws, audio.DurationMs)
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		return p.blocked(ctx, ws, id, source, reason, res, log)
	}
	defer releaseMinutes()
	est, _ := p.cfg.Provider.CostMicros(audio.DurationMs)
	if est > 0 {
		if p.cfg.Spend == nil {
			return Result{}, errors.New("transcribe: the provider costs money and no spend guard is configured")
		}
		release, err := p.cfg.Spend.Reserve(ctx, ws, est)
		var ce *llm.CapError
		switch {
		case errors.As(err, &ce):
			return p.blocked(ctx, ws, id, source, store.TranscriptSpendCap, res, log)
		case err != nil:
			return Result{}, fmt.Errorf("spend cap: %w", err)
		}
		defer release() // after the ledger rows below: a concurrent call sees this one as a reservation or as rows, never neither
	}

	tr, terr := p.cfg.Provider.Transcribe(ctx, audio, Options{Language: p.cfg.Language})
	calls := tr.Calls
	if terr == nil && len(calls) == 0 {
		calls = []Call{{AudioMs: audio.DurationMs}}
	}
	if err := p.meter(ctx, ws, calls); err != nil {
		return Result{}, err // the rows are what the caps read: a call that cannot be recorded is retried, not forgotten
	}
	switch {
	case errors.Is(terr, ErrUnusableAudio):
		return p.unusable(ctx, ws, id, source, terr, log)
	case terr != nil:
		return Result{}, fmt.Errorf("transcribe: %w", terr)
	}

	words := WithinSpeech(Refine(tr.Words, pcm), segs)
	if len(words) == 0 {
		log.InfoContext(ctx, "no words in the speech", "audio_ms", audio.DurationMs, "speech_ms", speechMs)
		res.Status = store.TranscriptNoSpeech
		return p.save(ctx, ws, id, res, p.noSpeech(source, audio, speechMs))
	}

	structure := Analyse(words, tr.Language)
	wj, err := json.Marshal(words)
	if err != nil {
		return Result{}, err
	}
	sj, err := json.Marshal(structure)
	if err != nil {
		return Result{}, err
	}
	nt := store.NewTranscript{
		SourceIngestedAt: source, Speech: true, Language: tr.Language, Words: wj, Structure: sj, Provider: p.cfg.Provider.Name(),
		Model: p.cfg.Provider.Model(), AudioMs: audio.DurationMs, SpeechMs: speechMs,
	}
	res.Status, res.Words = store.TranscriptReady, len(words)
	res, err = p.save(ctx, ws, id, res, nt)
	if err != nil || res.Status != store.TranscriptReady {
		return res, err
	}
	log.InfoContext(ctx, "transcribed", "audio_ms", audio.DurationMs, "speech_ms", speechMs, "words", len(words), "language", tr.Language,
		"fillers", len(structure.Fillers), "pauses", len(structure.Pauses), "calls", len(calls), "wall", time.Since(start).Round(time.Millisecond))
	return res, nil
}

func (p *Pipeline) noSpeech(source *time.Time, audio Audio, speechMs int64) store.NewTranscript {
	return store.NewTranscript{
		SourceIngestedAt: source, Speech: false, Provider: p.cfg.Provider.Name(), Model: p.cfg.Provider.Model(), AudioMs: audio.DurationMs, SpeechMs: speechMs,
	}
}

// utcDay is the UTC day that contains t.
func utcDay(t time.Time) (from, to time.Time) {
	t = t.UTC()
	from = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return from, from.Add(24 * time.Hour)
}

// reserveMinutes checks the workspace's daily minutes of transcription: what the ledger holds for today, what other runs of this
// process have in flight, and this audio, against TRANSCRIBE_DAILY_MINUTES. It returns a reason (minutes_cap) when the limit is
// passed, or the function that gives the audio back once the run's ledger rows exist.
func (p *Pipeline) reserveMinutes(ctx context.Context, ws ids.WorkspaceID, ms int64) (release func(), reason string, err error) {
	if p.cfg.DailyMinutes == 0 {
		return func() {}, "", nil
	}
	from, to := utcDay(p.cfg.now())
	p.mu.Lock()
	before := p.inflight[ws]
	p.inflight[ws] = before + ms
	p.mu.Unlock()
	give := func() {
		p.mu.Lock()
		if left := p.inflight[ws] - ms; left > 0 {
			p.inflight[ws] = left
		} else {
			delete(p.inflight, ws)
		}
		p.mu.Unlock()
	}
	used, err := p.cfg.Store.TranscribedMs(ctx, ws, from, to)
	if err != nil {
		give()
		return nil, "", fmt.Errorf("reading today's transcription: %w", err) // fail closed
	}
	if used+before+ms > int64(p.cfg.DailyMinutes)*60_000 {
		give()
		return nil, store.TranscriptMinutesCap, nil
	}
	return give, "", nil
}

// meter writes the ledger row of every billed call, in the order they were made, with a fresh context: the calls are already paid
// for. A row failing is retried a few times and then reported.
func (p *Pipeline) meter(ctx context.Context, ws ids.WorkspaceID, calls []Call) error {
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, c := range calls {
		cost, version := p.cfg.Provider.CostMicros(c.AudioMs)
		e := store.UsageEntry{
			WorkspaceID: ws, Stage: "transcription", Kind: store.KindTranscription, Model: p.cfg.Provider.Model(), Provider: p.cfg.Provider.Name(),
			AudioMs: c.AudioMs, ListCostMicros: cost, PriceTableVersion: version, BilledTo: p.cfg.BilledTo, CreatedAt: p.cfg.now().UTC(),
		}
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			if err = p.cfg.Store.InsertUsage(mctx, e); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		if err != nil {
			p.cfg.Logger.Error("transcribe: usage ledger write failed", "error", err, "workspace_id", ws.String(), "provider", e.Provider,
				"audio_ms", e.AudioMs, "list_cost_micros", e.ListCostMicros)
			return fmt.Errorf("write usage ledger: %w", err)
		}
	}
	return nil
}

// save writes the transcript with a fresh context: the work is done and paid for. A transcript made from an ingest that is no
// longer the asset's is dropped (the run becomes stale).
func (p *Pipeline) save(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, res Result, t store.NewTranscript) (Result, error) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err := p.cfg.Store.SaveTranscript(sctx, ws, id, t)
	switch {
	case errors.Is(err, store.ErrStaleTranscript):
		p.cfg.Logger.InfoContext(ctx, "transcript dropped: the asset was ingested again meanwhile", "workspace_id", ws.String(), "asset_id", id.String())
		return Result{Status: StatusStale}, nil
	case errors.Is(err, store.ErrNotFound):
		return Result{}, ErrAssetGone
	case err != nil:
		return Result{}, fmt.Errorf("save transcript: %w", err)
	}
	return res, nil
}

func (p *Pipeline) setStatus(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, status string) error {
	if err := p.cfg.Store.SetTranscriptStatus(ctx, ws, id, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrAssetGone
		}
		return fmt.Errorf("set transcript status: %w", err)
	}
	return nil
}

// fail marks the transcript failed with a reason, for the ingest the run is for.
func (p *Pipeline) fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, source *time.Time, reason string) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := p.cfg.Store.FailTranscript(fctx, ws, id, reason, source)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("mark transcript failed: %w", err)
	}
	return nil
}

// blocked ends the run for a workspace that is over a daily limit: the transcript is failed with the reason (a retry the same day
// would meet the same limit; `cfctl transcribe run` does it again) and nothing was sent to the provider.
func (p *Pipeline) blocked(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, source *time.Time, reason string, res Result, log *slog.Logger) (Result, error) {
	log.WarnContext(ctx, "transcription blocked by a daily limit", "reason", reason, "audio_ms", res.AudioMs)
	if err := p.fail(ctx, ws, id, source, reason); err != nil {
		return Result{}, err
	}
	res.Status, res.Reason = store.TranscriptFailed, reason
	return res, nil
}

// unusable ends the run for audio no provider can work with: the transcript is failed and the job is not retried.
func (p *Pipeline) unusable(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, source *time.Time, cause error, log *slog.Logger) (Result, error) {
	log.WarnContext(ctx, "transcription failed: the audio cannot be used", "error", cause)
	if err := p.fail(ctx, ws, id, source, store.TranscriptUnusableAudio); err != nil {
		return Result{}, err
	}
	return Result{Status: store.TranscriptFailed, Reason: store.TranscriptUnusableAudio}, nil
}

// download writes the audio derivative to path, within MaxAudioBytes.
func (p *Pipeline) download(ctx context.Context, key, path string) error {
	obj, err := p.cfg.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download audio: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size > MaxAudioBytes {
		return fmt.Errorf("download audio: %d bytes is over the limit of %d: %w", obj.Size, MaxAudioBytes, ErrUnusableAudio)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, MaxAudioBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download audio: %w", err)
	}
	if n > MaxAudioBytes || n != obj.Size {
		return fmt.Errorf("download audio: got %d bytes, storage said %d", n, obj.Size)
	}
	return nil
}

// speechPadMs is how far outside a detected speech segment a word may start or end and still count as speech: the voice-activity
// model cuts soft word edges.
const speechPadMs = 200

// WithinSpeech drops the words that fall in no speech segment, which are the invented ones: speech models write words for silence,
// music and breathing. Words keep their order and times.
func WithinSpeech(words []Word, segs []Segment) []Word {
	out := words[:0:0]
	for _, w := range words {
		for _, s := range segs {
			if w.EndMs > s.StartMs-speechPadMs && w.StartMs < s.EndMs+speechPadMs {
				out = append(out, w)
				break
			}
		}
	}
	return out
}
