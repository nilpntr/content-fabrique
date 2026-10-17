package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// Transcript is a row of transcripts: the words of one asset and their speech structure (P2-05).
type Transcript = db.Transcript

// The values of assets.transcript_status.
const (
	TranscriptNone     = "none"
	TranscriptPending  = "pending"
	TranscriptNoSpeech = "no_speech"
	TranscriptReady    = "ready"
	TranscriptFailed   = "failed"
)

// KindTranscription is the usage_ledger kind of a transcription call (D-27).
const KindTranscription = "transcription"

// SetTranscriptStatus moves the asset's transcript_status. ErrNotFound when the asset is not a live asset of the workspace.
func (s *Store) SetTranscriptStatus(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, status string) error {
	switch status {
	case TranscriptNone, TranscriptPending, TranscriptNoSpeech, TranscriptReady, TranscriptFailed:
	default:
		return fmt.Errorf("%w: transcript status %q", ErrInvalid, status)
	}
	_, err := s.q.SetTranscriptStatus(ctx, db.SetTranscriptStatusParams{WorkspaceID: ws, ID: id, Status: status})
	return mapErr(err)
}

// SetTranscribeHook registers fn to run, in the transaction that marks an asset with 16 kHz audio ready (FinishAssetIngest). fn
// enqueues the transcribe job in that transaction and reports whether it inserted one (the job is unique by asset, so an asset
// that is ingested again does not transcribe again); only then is the asset marked `pending`. Without a hook (the api, the tests of
// other packages) the asset is marked pending anyway, and `cfctl transcribe run` can run it. Call it during setup; nil removes it.
func (s *Store) SetTranscribeHook(fn func(ctx context.Context, tx *Store, a Asset) (bool, error)) {
	s.hooks.transcribeDue = fn
}

// transcribeDue is called with the kinds of derivative the finished ingest kept. An asset without audio_16k has nothing to
// transcribe and keeps its status.
func (s *Store) transcribeDue(ctx context.Context, a Asset, kinds []string) (Asset, error) {
	if !slices.Contains(kinds, DerivativeAudio16k) {
		return a, nil
	}
	enqueue := true
	if s.hooks != nil && s.hooks.transcribeDue != nil {
		var err error
		if enqueue, err = s.hooks.transcribeDue(ctx, s, a); err != nil {
			return a, fmt.Errorf("transcribe hook: %w", err)
		}
	}
	if !enqueue {
		return a, nil
	}
	if err := s.SetTranscriptStatus(ctx, a.WorkspaceID, a.ID, TranscriptPending); err != nil {
		return a, err
	}
	a.TranscriptStatus = TranscriptPending
	return a, nil
}

// NewTranscript is what the transcribe job saves.
type NewTranscript struct {
	Speech    bool
	Language  string
	Words     json.RawMessage // a JSON array
	Structure json.RawMessage // a JSON object
	Provider  string
	Model     string
	AudioMs   int64
	SpeechMs  int64
}

// SaveTranscript writes the transcript, moves the asset to ready (speech) or no_speech, and, when usage is not nil, appends the
// ledger row of the call, all in one transaction: a transcript never exists without its metering, and a retried job does not
// meter twice. ErrNotFound when the asset is gone.
func (s *Store) SaveTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, t NewTranscript, usage *UsageEntry) (Transcript, error) {
	var out Transcript
	err := s.inTx(ctx, func(tx *Store) error {
		words, structure := t.Words, t.Structure
		if len(words) == 0 {
			words = json.RawMessage(`[]`)
		}
		if len(structure) == 0 {
			structure = json.RawMessage(`{}`)
		}
		status := TranscriptNoSpeech
		if t.Speech {
			status = TranscriptReady
		}
		if err := tx.SetTranscriptStatus(ctx, ws, id, status); err != nil {
			return err
		}
		row, err := tx.q.UpsertTranscript(ctx, db.UpsertTranscriptParams{
			WorkspaceID: ws, AssetID: id, Speech: t.Speech, Language: t.Language, Words: words, Structure: structure,
			Provider: t.Provider, Model: t.Model, AudioMs: t.AudioMs, SpeechMs: t.SpeechMs,
		})
		if err != nil {
			return mapErr(err)
		}
		out = row
		if usage != nil {
			e := *usage
			e.WorkspaceID, e.Kind = ws, KindTranscription
			return tx.InsertUsage(ctx, e)
		}
		return nil
	})
	return out, err
}

// GetTranscript returns the transcript of a live asset of the workspace, or ErrNotFound (no such asset, or not transcribed yet).
func (s *Store) GetTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Transcript, error) {
	t, err := s.q.GetTranscript(ctx, db.GetTranscriptParams{WorkspaceID: ws, AssetID: id})
	return t, mapErr(err)
}
