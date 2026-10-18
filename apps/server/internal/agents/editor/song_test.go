package editor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/music"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// saveMusic stores a music analysis for an asset, as the analyze job does for every asset with sound.
func (e *env) saveMusic(ws store.Workspace, a ids.AssetID, an music.Analysis) {
	e.t.Helper()
	doc, err := json.Marshal(an)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.SaveAssetMusic(context.Background(), ws.ID, a, music.Version, nil, doc); err != nil {
		e.t.Fatal(err)
	}
}

// The gym plays music, so the analysis hears music in a footage clip's own sound too. Only an audio file the creator gave is the song:
// the footage list has no song line for a gym clip, get_music refuses it, and the opening message lists only the song.
func TestOnlyTheCreatorsAudioIsTheSong(t *testing.T) {
	e := newEnv(t)
	gym := e.asset(e.wsA, clip{title: "Seated row"})
	song := e.asset(e.wsA, clip{title: "song", kind: store.AssetKindAudio, durMs: 23000})
	an := songAnalysis()
	e.saveMusic(e.wsA, gym, an)
	e.saveMusic(e.wsA, song, an)
	prj := e.project(e.wsA, "9:16", "", gym, song)
	rc := e.runCtx(e.wsA, prj, "")

	list := textOf(e.ok(rc, ToolListFootage, `{}`))
	lines := strings.Split(list, "\n- ")
	for _, l := range lines {
		switch {
		case strings.Contains(l, gym.String()) && strings.Contains(l, "song:"):
			t.Errorf("the gym clip's sound is listed as a song:\n%s", l)
		case strings.Contains(l, song.String()) && !strings.Contains(l, "song:"):
			t.Errorf("the creator's song has no song line:\n%s", l)
		}
	}
	if msg := e.toolErr(e.nextCall(rc), ToolGetMusic, `{"asset_id":"`+gym.String()+`"}`); !strings.Contains(msg, "is footage") || !strings.Contains(msg, "not a song") {
		t.Errorf("get_music on a gym clip: %q", msg)
	}
	if got := textOf(e.ok(e.nextCall(rc), ToolGetMusic, `{"asset_id":"`+song.String()+`"}`)); !strings.Contains(got, "beats_ms") {
		t.Errorf("get_music on the song: %s", got)
	}
	blocks, err := e.deps.runContext(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, b := range blocks {
		all.WriteString(b.Text)
	}
	if n := strings.Count(all.String(), "  song: "); n != 1 {
		t.Errorf("the opening message has %d song lines, want 1 (the creator's audio):\n%s", n, all.String())
	}
}

// A video's sound is the song only when the creator chose it for the edit: a creator_audio item on the timeline names it.
func TestAChosenVideoSoundIsASong(t *testing.T) {
	spec := &timeline.Spec{Tracks: []timeline.Track{&timeline.AudioTrack{ID: "sound", Items: []timeline.AudioItem{
		&timeline.CreatorAudio{AudioCommon: timeline.AudioCommon{ID: "song"}, AssetID: "ast_video"},
	}}}}
	chosen := chosenSounds(spec)
	yes := true
	video := store.ProjectFootageRow{Kind: store.AssetKindVideo, HasAudio: &yes}
	if isSong(video, chosen) {
		t.Error("a video that is not the chosen one is a song")
	}
	if !chosen["ast_video"] || len(chosenSounds(nil)) != 0 {
		t.Errorf("chosen %v", chosen)
	}
	audio := store.ProjectFootageRow{Kind: store.AssetKindAudio, HasAudio: &yes}
	if !isSong(audio, nil) {
		t.Error("the creator's audio file is not a song")
	}
	no := false
	if isSong(store.ProjectFootageRow{Kind: store.AssetKindAudio, HasAudio: &no}, nil) {
		t.Error("an audio file with no sound is a song")
	}
}
