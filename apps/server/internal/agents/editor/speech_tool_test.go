package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

// The get_transcript tool over the database: tenancy, windows and the answers for a clip without speech or without a transcript.

func (e *env) saveSpeech(ws store.Workspace, a ids.AssetID, speech bool, words []transcribe.Word) {
	e.t.Helper()
	raw, _ := json.Marshal(words)
	lang := "en"
	if !speech {
		raw, lang = []byte("[]"), ""
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO transcripts (workspace_id, asset_id, speech, language, words, provider, model, audio_ms)
		VALUES ($1, $2, $3, $4, $5, 'whispercpp', 'm', 10000)`, ws.ID, a, speech, lang, raw); err != nil {
		e.t.Fatal(err)
	}
}

func TestGetTranscriptTool(t *testing.T) {
	e := newEnv(t)
	talker := e.asset(e.wsA, clip{title: "Talker"})
	silent := e.asset(e.wsA, clip{title: "Silent"})
	fresh := e.asset(e.wsA, clip{title: "Not transcribed"})
	library := e.asset(e.wsA, clip{title: "Library only"})
	foreign := e.asset(e.wsB, clip{title: "Someone else's"})
	prj := e.project(e.wsA, "9:16", "", talker, silent, fresh)
	e.saveSpeech(e.wsA, talker, true, talkingClip().Words)
	e.saveSpeech(e.wsA, silent, false, nil)
	e.saveSpeech(e.wsA, library, true, talkingClip().Words)
	e.saveSpeech(e.wsB, foreign, true, talk("Secret:0", "plans.:40"))
	rc := e.runCtx(e.wsA, prj, "")

	text := textOf(e.ok(rc, ToolGetTranscript, `{"asset_id":"`+talker.String()+`"}`))
	for _, want := range []string{"Sentences (3)", "Proposed keep ranges", `<untrusted_data kind="transcript">`, "Here is the truth."} {
		if !strings.Contains(text, want) {
			t.Errorf("answer lacks %q:\n%s", want, text)
		}
	}
	if win := textOf(e.ok(rc, ToolGetTranscript, `{"asset_id":"`+talker.String()+`","in_ms":0,"out_ms":1600}`)); !strings.Contains(win, "Sentences (1)") {
		t.Errorf("window:\n%s", win)
	}
	if got := textOf(e.ok(rc, ToolGetTranscript, `{"asset_id":"`+silent.String()+`"}`)); !strings.Contains(got, "no speech") {
		t.Errorf("silent clip: %s", got)
	}
	if got := textOf(e.ok(rc, ToolGetTranscript, `{"asset_id":"`+fresh.String()+`"}`)); !strings.Contains(got, "no transcript") {
		t.Errorf("clip without a transcript: %s", got)
	}
	// Only this project's clips, and the refusal is the same for another workspace's clip as for one that does not exist.
	for _, id := range []string{library.String(), foreign.String(), "ast_nonsense"} {
		msg := e.toolErr(rc, ToolGetTranscript, `{"asset_id":"`+id+`"}`)
		if !strings.Contains(msg, "not one of this project's clips") && !strings.Contains(msg, "not an asset id") {
			t.Errorf("asset %q: %q", id, msg)
		}
		if strings.Contains(msg, "Secret") {
			t.Errorf("the refusal leaks: %q", msg)
		}
	}
	if msg := e.toolErr(rc, ToolGetTranscript, `{"asset_id":"`+talker.String()+`","in_ms":5000,"out_ms":1000}`); !strings.Contains(msg, "in_ms before out_ms") {
		t.Errorf("window order: %q", msg)
	}
	if msg := e.toolErr(e.runCtx(e.wsA, ids.ProjectID{}, ""), ToolGetTranscript, `{"asset_id":"`+talker.String()+`"}`); !strings.Contains(msg, "no project") {
		t.Errorf("no project: %q", msg)
	}
}
