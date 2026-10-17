package renderjobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// scriptRecorder wraps the runner and keeps the subtitle scripts (.ass) it finds in the work directory of every run: the captions
// the render drew.
type scriptRecorder struct {
	media.Runner
	mu      sync.Mutex
	scripts []string
}

func (r *scriptRecorder) Run(ctx context.Context, inv media.Invocation) (sandbox.Result, error) {
	res, err := r.Runner.Run(ctx, inv)
	if inv.OutputDir != "" {
		matches, _ := filepath.Glob(filepath.Join(inv.OutputDir, "*.ass"))
		for _, m := range matches {
			if b, rerr := os.ReadFile(m); rerr == nil { //nolint:gosec // a file of this test's own work directory
				r.mu.Lock()
				r.scripts = append(r.scripts, string(b))
				r.mu.Unlock()
			}
		}
	}
	return res, err
}

func (r *scriptRecorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.scripts, "\n")
}

// A render whose timeline has a captions track gets the transcript of each asset it plays from the store (P2-05): the words appear
// in the subtitle script. Without a transcript the track draws nothing, and a timeline without captions never reads one.
func TestCaptionsRenderGetsTheTranscriptOfTheAsset(t *testing.T) {
	e := newEnv(t)
	clips := testClips(t)
	w := e.newWorkspace(t, "org_render_captions")
	a, b := e.readyAsset(t, w, "a", clips[0]), e.readyAsset(t, w, "b", clips[1])
	p := e.newProject(t, w, a, b)

	spec := shortSpec(a, b)
	spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "captions", From: "transcript", Style: "word_highlight", Enabled: true})
	version := e.addVersion(t, w, p, spec)

	words := `[{"text":"Zebra","start_ms":100,"end_ms":500,"confidence":0.9},{"text":"quokka","start_ms":600,"end_ms":1000,"confidence":0.9}]`
	if _, err := e.st.SaveTranscript(context.Background(), w.ID, a.ID, store.NewTranscript{
		Speech: true, Language: "en", Words: []byte(words), Provider: "whispercpp", Model: "m", AudioMs: 10000, SpeechMs: 800,
	}, nil); err != nil {
		t.Fatal(err)
	}

	rec := &scriptRecorder{Runner: e.runner}
	pipe := e.newPipeline(t, e.objs, rec)
	r, _ := e.request(t, w, p, version, "preview")
	if out, err := pipe.Run(context.Background(), w.ID, r.ID); err != nil || out.Status != store.RenderReady {
		t.Fatalf("render: %+v %v", out, err)
	}
	script := strings.ToLower(rec.joined())
	if !strings.Contains(script, "zebra") || !strings.Contains(script, "quokka") {
		t.Fatalf("the captions lack the transcript's words:\n%s", rec.joined())
	}

	// The same timeline, the asset has no speech: no captions are drawn, and the render still succeeds.
	if _, err := e.st.SaveTranscript(context.Background(), w.ID, a.ID, store.NewTranscript{Speech: false, Provider: "whispercpp", Model: "m", AudioMs: 10000}, nil); err != nil {
		t.Fatal(err)
	}
	rec2 := &scriptRecorder{Runner: e.runner}
	r2, _ := e.request(t, w, p, version, "final")
	if out, err := e.newPipeline(t, e.objs, rec2).Run(context.Background(), w.ID, r2.ID); err != nil || out.Status != store.RenderReady {
		t.Fatalf("render without words: %+v %v", out, err)
	}
	if s := strings.ToLower(rec2.joined()); strings.Contains(s, "zebra") {
		t.Errorf("a clip without speech got captions:\n%s", s)
	}
}

func TestHasCaptionsNeedsAnEnabledTrack(t *testing.T) {
	spec := &timeline.Spec{Tracks: []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID}}}
	if hasCaptions(spec) {
		t.Error("no captions track")
	}
	spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "c", Enabled: false})
	if hasCaptions(spec) {
		t.Error("a disabled captions track reads no transcript")
	}
	spec.Tracks = append(spec.Tracks, &timeline.CaptionsTrack{ID: "c2", Enabled: true})
	if !hasCaptions(spec) {
		t.Error("an enabled captions track needs transcripts")
	}
}
