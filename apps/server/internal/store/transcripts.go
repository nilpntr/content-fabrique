package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

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

// The codes of assets.transcript_failure_reason, safe to show.
const (
	// TranscriptSpendCap: the workspace's daily spend cap (DAILY_SPEND_CAP_MICROS) could not cover the transcription.
	TranscriptSpendCap = "spend_cap"
	// TranscriptMinutesCap: the workspace had its daily minutes of transcription already (TRANSCRIBE_DAILY_MINUTES).
	TranscriptMinutesCap = "minutes_cap"
	// TranscriptUnusableAudio: the audio cannot be transcribed by any provider.
	TranscriptUnusableAudio = "unusable_audio"
	// TranscriptGaveUp: every attempt of the job failed.
	TranscriptGaveUp = "gave_up"
)

// KindTranscription is the usage_ledger kind of a transcription call (D-27).
const KindTranscription = "transcription"

// IngestGeneration is the ingest generation of an asset: the time its ingest finished, in microseconds (0 when it has none). Jobs
// that must run once per ingest (analysis, transcription) carry it in their arguments, and are unique by it.
func IngestGeneration(a Asset) int64 {
	if a.IngestedAt == nil {
		return 0
	}
	return a.IngestedAt.UnixMicro()
}

// GenerationTime is the instant an ingest generation stands for (nil for 0): the value SaveTranscript and FailTranscript compare
// with assets.ingested_at.
func GenerationTime(generation int64) *time.Time {
	if generation == 0 {
		return nil
	}
	t := time.UnixMicro(generation).UTC()
	return &t
}

// ErrStaleTranscript is returned by SaveTranscript when the transcript was made from an ingest that is no longer the asset's (it
// was ingested again meanwhile) or the asset is not ready: nothing is written, and the job treats it as a clean no-op (the newer
// ingest has its own transcribe job).
var ErrStaleTranscript = errors.New("store: the transcript is out of date")

// SetTranscriptStatus moves the asset's transcript_status and clears its failure reason (use FailTranscript for `failed`).
// ErrNotFound when the asset is not a live asset of the workspace.
func (s *Store) SetTranscriptStatus(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, status string) error {
	switch status {
	case TranscriptNone, TranscriptPending, TranscriptNoSpeech, TranscriptReady, TranscriptFailed:
	default:
		return fmt.Errorf("%w: transcript status %q", ErrInvalid, status)
	}
	_, err := s.q.SetTranscriptStatus(ctx, db.SetTranscriptStatusParams{WorkspaceID: ws, ID: id, Status: status})
	return mapErr(err)
}

// FailTranscript marks the transcript failed with a reason code, for the ingest the job was made for (source is its
// assets.ingested_at) and only while the transcript is pending: a job that gives up late must not overwrite the ready transcript
// of a sibling job (`cfctl transcribe run --force`). ErrNotFound when the asset is gone, has been ingested again since, or is not
// pending, which is not an error for the caller.
func (s *Store) FailTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, source *time.Time) error {
	_, err := s.q.FailTranscript(ctx, db.FailTranscriptParams{WorkspaceID: ws, ID: id, Reason: reason, SourceIngestedAt: source})
	return mapErr(err)
}

// SetTranscribeHook registers fn to run, in the transaction that marks an asset with 16 kHz audio ready (FinishAssetIngest). fn
// enqueues the transcribe job in that transaction and reports whether it inserted one (the job is unique by asset and ingest
// generation, so a double enqueue for one ingest runs once and a re-ingest gets its own); only then is the asset marked `pending`.
// Without a hook (the api, the tests of other packages) the asset is marked pending anyway, and `cfctl transcribe run` can run it.
// Call it during setup; nil removes it.
func (s *Store) SetTranscribeHook(fn func(ctx context.Context, tx *Store, a Asset) (bool, error)) {
	s.hooks.transcribeDue = fn
}

// transcribeDue is called with the kinds of derivative the finished ingest kept. An asset that has no audio_16k any more (a
// re-ingest that lost its audio track) has nothing to transcribe and must not keep the words of its old audio: the transcript is
// deleted and the status goes back to none.
func (s *Store) transcribeDue(ctx context.Context, a Asset, kinds []string) (Asset, error) {
	if !slices.Contains(kinds, DerivativeAudio16k) {
		if err := mapErr(s.q.DeleteTranscript(ctx, db.DeleteTranscriptParams{WorkspaceID: a.WorkspaceID, AssetID: a.ID})); err != nil {
			return a, err
		}
		if err := s.SetTranscriptStatus(ctx, a.WorkspaceID, a.ID, TranscriptNone); err != nil {
			return a, err
		}
		a.TranscriptStatus, a.TranscriptFailureReason = TranscriptNone, nil
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
	a.TranscriptStatus, a.TranscriptFailureReason = TranscriptPending, nil
	return a, nil
}

// NewTranscript is what the transcribe job saves.
type NewTranscript struct {
	// SourceIngestedAt is the assets.ingested_at of the ingest whose audio was transcribed.
	SourceIngestedAt *time.Time
	Speech           bool
	Language         string
	Words            json.RawMessage // a JSON array
	Structure        json.RawMessage // a JSON object
	Provider         string
	Model            string
	AudioMs          int64
	SpeechMs         int64
}

// SaveTranscript writes the transcript and moves the asset to ready (speech) or no_speech, in one transaction that starts by
// locking the asset row, so two transcriptions of one asset save one after the other. ErrNotFound when the asset is not in this
// workspace (or is deleted); ErrStaleTranscript when it is not ready or its ingest is not the one the audio came from
// (t.SourceIngestedAt). Nothing is written then.
func (s *Store) SaveTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, t NewTranscript) (Transcript, error) {
	var out Transcript
	err := s.inTx(ctx, func(tx *Store) error {
		a, err := tx.q.GetAssetForTranscriptSave(ctx, db.GetAssetForTranscriptSaveParams{WorkspaceID: ws, ID: id})
		if err != nil {
			return mapErr(err)
		}
		if a.Status != AssetReady || !sameInstant(a.IngestedAt, t.SourceIngestedAt) {
			return ErrStaleTranscript
		}
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
			WorkspaceID: ws, AssetID: id, SourceIngestedAt: t.SourceIngestedAt, Speech: t.Speech, Language: t.Language, Words: words,
			Structure: structure, Provider: t.Provider, Model: t.Model, AudioMs: t.AudioMs, SpeechMs: t.SpeechMs,
		})
		if err != nil {
			return mapErr(err)
		}
		out = row
		return nil
	})
	return out, err
}

// GetTranscript returns the transcript of a live asset of the workspace, or ErrNotFound (no such asset, or not transcribed yet).
func (s *Store) GetTranscript(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID) (Transcript, error) {
	t, err := s.q.GetTranscript(ctx, db.GetTranscriptParams{WorkspaceID: ws, AssetID: id})
	return t, mapErr(err)
}

// TranscribedMs is the audio the workspace has had transcribed in [from, to), counted from the ledger.
func (s *Store) TranscribedMs(ctx context.Context, ws ids.WorkspaceID, from, to time.Time) (int64, error) {
	n, err := s.q.SumTranscribedMs(ctx, db.SumTranscribedMsParams{WorkspaceID: ws, FromTime: from, ToTime: to})
	return n, mapErr(err)
}
