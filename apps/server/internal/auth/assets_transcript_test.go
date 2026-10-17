package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// GET /v1/assets/{id}/transcript: 404 until the transcription job has saved one, then the words with timings and the speech
// structure; the asset says where the transcript stands (transcript_status) in the library list and on its own; another workspace
// sees neither.
func TestAssetTranscript(t *testing.T) {
	e := newAssetsEnv(t)
	a := e.uploadedAsset("ada", "talk.mp4")
	e.ingested("ada", a) // the ingest leaves an asset with 16 kHz audio pending transcription
	ctx := context.Background()
	id, err := ids.ParseAssetID(a.Id)
	if err != nil {
		t.Fatal(err)
	}

	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+a.Id, "ada", nil, http.StatusOK, &got)
	if got.TranscriptStatus != api.TranscriptStatus(store.TranscriptPending) {
		t.Fatalf("transcript_status after the ingest = %q", got.TranscriptStatus)
	}
	var errBody api.Error
	e.call(http.MethodGet, "/v1/assets/"+a.Id+"/transcript", "ada", nil, http.StatusNotFound, &errBody)

	words := `[{"text":"Hello,","start_ms":500,"end_ms":900,"confidence":0.91},{"text":"um","start_ms":1000,"end_ms":1300,"confidence":0.5},{"text":"world.","start_ms":1900,"end_ms":2400,"confidence":0.88}]`
	structure := `{"sentences":[{"first_word":0,"last_word":2,"start_ms":500,"end_ms":2400,"text":"Hello, um world."}],` +
		`"fillers":[{"first_word":1,"last_word":1,"start_ms":1000,"end_ms":1300,"text":"um","kind":"hesitation","strength":"strong"}],` +
		`"pauses":[{"after_word":1,"start_ms":1300,"end_ms":1900,"duration_ms":600,"sentence_break":false}]}`
	if _, err := e.st.SaveTranscript(ctx, e.ws["ada"], id, store.NewTranscript{
		Speech: true, Language: "en", Words: []byte(words), Structure: []byte(structure),
		Provider: "whispercpp", Model: "ggml-tiny.en", AudioMs: 3000, SpeechMs: 1800,
	}, nil); err != nil {
		t.Fatal(err)
	}

	var tr api.Transcript
	e.call(http.MethodGet, "/v1/assets/"+a.Id+"/transcript", "ada", nil, http.StatusOK, &tr)
	if tr.AssetId != a.Id || tr.Status != api.TranscriptStatus(store.TranscriptReady) || !tr.Speech || tr.Language != "en" || tr.Provider != "whispercpp" || tr.AudioMs != 3000 || tr.SpeechMs != 1800 {
		t.Fatalf("transcript: %+v", tr)
	}
	if len(tr.Words) != 3 || tr.Words[1].Text != "um" || tr.Words[1].StartMs != 1000 || tr.Words[2].EndMs != 2400 || tr.Words[0].Confidence != 0.91 {
		t.Fatalf("words: %+v", tr.Words)
	}
	s := tr.Structure
	if len(s.Sentences) != 1 || s.Sentences[0].LastWord != 2 || len(s.Fillers) != 1 || s.Fillers[0].Kind != api.SpeechFillerKindHesitation || s.Fillers[0].Strength != api.SpeechFillerStrengthStrong ||
		len(s.Pauses) != 1 || s.Pauses[0].DurationMs != 600 || s.Pauses[0].SentenceBreak {
		t.Fatalf("structure: %+v", s)
	}

	// The status is on the asset, on its own and in the list.
	e.call(http.MethodGet, "/v1/assets/"+a.Id, "ada", nil, http.StatusOK, &got)
	if got.TranscriptStatus != api.TranscriptStatus(store.TranscriptReady) {
		t.Fatalf("transcript_status = %q", got.TranscriptStatus)
	}
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &list)
	var listed bool
	for _, x := range list.Items {
		if x.Id == a.Id {
			listed = x.TranscriptStatus == api.TranscriptStatus(store.TranscriptReady)
		}
	}
	if !listed {
		t.Errorf("the list does not carry transcript_status ready for %s", a.Id)
	}

	// Another workspace: the asset and its transcript are unknown. A malformed id is a bad request.
	e.call(http.MethodGet, "/v1/assets/"+a.Id+"/transcript", "bob", nil, http.StatusNotFound, &errBody)
	e.call(http.MethodGet, "/v1/assets/not-an-id/transcript", "ada", nil, http.StatusBadRequest, &errBody)

	// A clip without speech has a transcript that says so, with empty arrays and not null.
	b := e.uploadedAsset("ada", "silent.mp4")
	e.ingested("ada", b)
	bid, _ := ids.ParseAssetID(b.Id)
	if _, err := e.st.SaveTranscript(ctx, e.ws["ada"], bid, store.NewTranscript{Speech: false, Provider: "whispercpp", Model: "m", AudioMs: 3000}, nil); err != nil {
		t.Fatal(err)
	}
	var none api.Transcript
	e.call(http.MethodGet, "/v1/assets/"+b.Id+"/transcript", "ada", nil, http.StatusOK, &none)
	if none.Status != api.TranscriptStatus(store.TranscriptNoSpeech) || none.Speech || none.Words == nil || len(none.Words) != 0 || none.Structure.Fillers == nil {
		t.Fatalf("no speech: %+v", none)
	}
}
