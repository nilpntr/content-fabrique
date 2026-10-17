package assets

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

var errNoTranscript = &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "this asset has no transcript (see its transcript_status)"}

// Transcript returns the transcript of an asset of the caller's workspace: the words, their timings and the speech structure.
// An asset that does not exist, belongs to another workspace, or has not been transcribed (yet) is a 404.
func (s *Service) Transcript(ctx context.Context, who Actor, id ids.AssetID) (api.Transcript, *Error) {
	t, err := s.store.GetTranscript(ctx, who.Workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Transcript{}, errNoTranscript
	}
	if err != nil {
		return api.Transcript{}, s.internal(ctx, "get transcript", err)
	}
	out := api.Transcript{
		AssetId: id.String(), Status: api.TranscriptStatus(store.TranscriptReady), Speech: t.Speech, Language: t.Language,
		Provider: t.Provider, Model: t.Model, AudioMs: t.AudioMs, SpeechMs: t.SpeechMs, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Words: []api.TranscriptWord{},
		Structure: api.SpeechStructure{
			Sentences: []api.SpeechSentence{}, Fillers: []api.SpeechFiller{}, Pauses: []api.SpeechPause{},
		},
	}
	if !t.Speech {
		out.Status = api.TranscriptStatus(store.TranscriptNoSpeech)
		return out, nil
	}
	if err := json.Unmarshal(t.Words, &out.Words); err != nil {
		return api.Transcript{}, s.internal(ctx, "decode transcript words", err)
	}
	if len(t.Structure) > 0 {
		if err := json.Unmarshal(t.Structure, &out.Structure); err != nil {
			return api.Transcript{}, s.internal(ctx, "decode transcript structure", err)
		}
	}
	return out, nil
}
