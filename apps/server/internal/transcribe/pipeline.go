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
	"time"

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
	SaveTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, t store.NewTranscript, usage *store.UsageEntry) (store.Transcript, error)
}

// Config builds a Pipeline.
type Config struct {
	Store    Store
	Objects  Objects
	Provider Provider
	Detector Detector
	// Language is the language to ask for; empty detects it per clip.
	Language string
	// BilledTo is the ledger's billed_to: platform, or test in local, ci and bench.
	BilledTo string
	// TempDir holds one directory per running job. It must be inside the sandbox's allowed roots, without symlinks in its path.
	TempDir string
	Logger  *slog.Logger
}

// Pipeline transcribes assets. It holds no per-job state, so one Pipeline serves all the workers.
type Pipeline struct {
	cfg Config
}

// New returns a Pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil || cfg.Objects == nil || cfg.Provider == nil || cfg.Detector == nil:
		return nil, errors.New("transcribe: a store, object storage, a provider and a detector are required")
	case !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) != cfg.TempDir:
		return nil, fmt.Errorf("transcribe: the temp dir %q must be an absolute, clean path", cfg.TempDir)
	case cfg.BilledTo == "":
		cfg.BilledTo = store.BilledToPlatform
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.TempDir, 0o750); err != nil {
		return nil, fmt.Errorf("transcribe: temp dir: %w", err)
	}
	return &Pipeline{cfg: cfg}, nil
}

// Result is the outcome of a run that ended normally.
type Result struct {
	// Status is the asset's transcript_status afterwards: ready, no_speech or failed, or none for an asset without audio.
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Provider string `json:"provider,omitempty"`
	Words    int    `json:"words,omitempty"`
	AudioMs  int64  `json:"audio_ms,omitempty"`
	SpeechMs int64  `json:"speech_ms,omitempty"`
}

// Fail marks the transcript failed, for a job that has used its last attempt. A deleted asset is not an error.
func (p *Pipeline) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) error {
	err := p.cfg.Store.SetTranscriptStatus(ctx, ws, id, store.TranscriptFailed)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// Run transcribes the asset of the workspace: it checks the 16 kHz audio for speech, transcribes it when there is some, places the
// words on the audio, works out the speech structure and saves it all with the ledger row of the call.
//
// Errors: ErrAssetGone (nothing to do), ErrNotConfigured (the provider is switched off; the job waits), and any other error is
// retried. Audio the provider cannot use ends the run with status failed and no error.
func (p *Pipeline) Run(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Result, error) {
	asset, err := p.cfg.Store.GetAsset(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrAssetGone
	}
	if err != nil {
		return Result{}, fmt.Errorf("get asset: %w", err)
	}
	log := p.cfg.Logger.With("workspace_id", ws.String(), "asset_id", id.String(), "provider", p.cfg.Provider.Name())

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
		return Result{}, err
	}
	pcm, err := ReadWAV(path)
	if err != nil {
		return p.unusable(ctx, ws, id, err, log)
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
		return res, p.save(ctx, ws, id, p.noSpeech(audio, speechMs), nil)
	}

	tr, err := p.cfg.Provider.Transcribe(ctx, audio, Options{Language: p.cfg.Language})
	switch {
	case errors.Is(err, ErrUnusableAudio):
		return p.unusable(ctx, ws, id, err, log)
	case err != nil:
		return Result{}, fmt.Errorf("transcribe: %w", err)
	}
	words := Refine(tr.Words, pcm)
	words = WithinSpeech(words, segs)
	if len(words) == 0 {
		log.InfoContext(ctx, "no words in the speech", "audio_ms", audio.DurationMs, "speech_ms", speechMs)
		res.Status = store.TranscriptNoSpeech
		// The provider was called: the call is metered even though nothing came of it.
		return res, p.save(ctx, ws, id, p.noSpeech(audio, speechMs), p.usage(audio))
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
		Speech: true, Language: tr.Language, Words: wj, Structure: sj, Provider: p.cfg.Provider.Name(), Model: p.cfg.Provider.Model(),
		AudioMs: audio.DurationMs, SpeechMs: speechMs,
	}
	if err := p.save(ctx, ws, id, nt, p.usage(audio)); err != nil {
		return Result{}, err
	}
	res.Status, res.Words = store.TranscriptReady, len(words)
	log.InfoContext(ctx, "transcribed", "audio_ms", audio.DurationMs, "speech_ms", speechMs, "words", len(words), "language", tr.Language,
		"fillers", len(structure.Fillers), "pauses", len(structure.Pauses), "wall", time.Since(start).Round(time.Millisecond))
	return res, nil
}

func (p *Pipeline) noSpeech(audio Audio, speechMs int64) store.NewTranscript {
	return store.NewTranscript{
		Speech: false, Provider: p.cfg.Provider.Name(), Model: p.cfg.Provider.Model(), AudioMs: audio.DurationMs, SpeechMs: speechMs,
	}
}

// usage is the ledger row of one provider call over the audio.
func (p *Pipeline) usage(audio Audio) *store.UsageEntry {
	cost, version := p.cfg.Provider.CostMicros(audio.DurationMs)
	return &store.UsageEntry{
		Stage: "transcription", Model: p.cfg.Provider.Model(), Provider: p.cfg.Provider.Name(), AudioMs: audio.DurationMs,
		ListCostMicros: cost, PriceTableVersion: version, BilledTo: p.cfg.BilledTo, CreatedAt: time.Now().UTC(),
	}
}

// save writes the transcript with a fresh context: the work is done and paid for.
func (p *Pipeline) save(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, t store.NewTranscript, usage *store.UsageEntry) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := p.cfg.Store.SaveTranscript(sctx, ws, id, t, usage); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrAssetGone
		}
		return fmt.Errorf("save transcript: %w", err)
	}
	return nil
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

// unusable ends the run for audio no provider can work with: the transcript is failed and the job is not retried.
func (p *Pipeline) unusable(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, cause error, log *slog.Logger) (Result, error) {
	log.WarnContext(ctx, "transcription failed: the audio cannot be used", "error", cause)
	if err := p.setStatus(ctx, ws, id, store.TranscriptFailed); err != nil {
		return Result{}, err
	}
	return Result{Status: store.TranscriptFailed, Reason: "unusable_audio"}, nil
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
