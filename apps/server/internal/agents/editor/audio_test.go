package editor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func item(id, status, value string) audiocheck.CheckItem {
	return audiocheck.CheckItem{ID: id, Title: "Title of " + id, Status: status, Value: value, Target: "target", Source: "estimate", Fix: "Fix for " + id + "."}
}

func soundReadyResult(items ...audiocheck.CheckItem) AudioResult {
	return AudioResult{
		Report:  audiocheck.Report{Profile: audiocheck.ProfileSoundReady, Checklist: items, Loudness: audiocheck.Loudness{IntegratedLUFS: -17}, Bed: &audiocheck.BedStats{LevelLUFS: -24}},
		Elapsed: 1500 * time.Millisecond, Source: "stem",
	}
}

func checkByID(cs []Check, id string) Check {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return Check{ID: "missing:" + id}
}

func TestAudioChecksFoldTheEarsIntoThreeChecks(t *testing.T) {
	cs := audioChecks(soundReadyResult(
		item("has_audio", audiocheck.StatusPass, "yes"), item("loudness", audiocheck.StatusPass, "-17.0 LUFS"), item("true_peak", audiocheck.StatusPass, "-2.5"),
		item("bed_present", audiocheck.StatusFail, "40 %"), item("bed_gaps", audiocheck.StatusWarn, "0.8 s"), item("bed_steady", audiocheck.StatusPass, "3 LU"),
		item("sfx_sync", audiocheck.StatusPass, "100 %"), item("sfx_cut_through", audiocheck.StatusWarn, "3 of 4"), item("trend_audible", audiocheck.StatusInfo, "-"),
	))
	if len(cs) != 3 {
		t.Fatalf("%d checks: %+v", len(cs), cs)
	}
	if c := checkByID(cs, CheckAudioLevel); c.Status != CheckPass || !strings.Contains(c.Detail, "-17.0 LUFS") {
		t.Errorf("level: %+v", c)
	}
	// A fail fails the group, names its one-line fix and mentions the borderline one.
	bed := checkByID(cs, CheckAudioBed)
	if bed.Status != CheckFail || !strings.Contains(bed.Detail, "Fix for bed_present.") || !strings.Contains(bed.Detail, "borderline") || !strings.Contains(bed.Detail, "Fix for bed_gaps.") {
		t.Errorf("bed: %+v", bed)
	}
	// A borderline estimate alone is not a reason for the one revision pass.
	if c := checkByID(cs, CheckAudioEffects); c.Status != CheckPass || !strings.Contains(c.Detail, "borderline") {
		t.Errorf("effects: %+v", c)
	}
}

// A beat marker at or past the end of the timeline gets no click, so it does not mean the preview has an audio stem.
func TestHasBeatGuide(t *testing.T) {
	spec := func(markers ...timeline.Marker) *timeline.Spec {
		return &timeline.Spec{Output: timeline.Output{FPS: 30}, Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{
			{ID: "c1", AssetID: "ast_x", Source: timeline.Source{InMs: 0, OutMs: 2000}, AtMs: 0},
		}}}, Markers: markers}
	}
	if hasBeatGuide(spec()) {
		t.Error("a guide without markers")
	}
	if hasBeatGuide(spec(timeline.Marker{Kind: timeline.MarkerBeat, AtMs: 2000}, timeline.Marker{Kind: timeline.MarkerDrop, AtMs: 5000})) {
		t.Error("markers at or past the end have a guide")
	}
	if !hasBeatGuide(spec(timeline.Marker{Kind: timeline.MarkerBeat, AtMs: 1999})) {
		t.Error("a beat inside the video has no guide")
	}
	if hasBeatGuide(spec(timeline.Marker{Kind: "section", AtMs: 500})) {
		t.Error("a marker that is not a beat has a guide")
	}
}

func TestAudioChecksInTheProgrammeProfileHaveNoBedCheck(t *testing.T) {
	r := soundReadyResult(item("loudness", audiocheck.StatusPass, "-14.0 LUFS"), item("sfx_sync", audiocheck.StatusPass, "ok"))
	r.Report.Profile = audiocheck.ProfileProgramme
	cs := audioChecks(r)
	if checkByID(cs, CheckAudioBed).ID != "missing:"+CheckAudioBed {
		t.Errorf("a programme report has a bed check: %+v", cs)
	}
	if len(cs) != 2 {
		t.Errorf("%+v", cs)
	}
}

// check_edit listens to the preview: a bed that is not there fails with its fix, and the time it took is in the metrics.
func TestCheckEditListensToThePreview(t *testing.T) {
	g := newGateEnv(t)
	g.ok(g.nextCall(g.rc), ToolEditTimeline, g.opsArgs(g.prj, addClipOp(g.clip, 6000, 9000), addClipOp(g.clip, 9000, 12000)))
	g.ok(g.nextCall(g.rc), ToolRenderPreview, `{}`)
	g.smp.series = renderedFlat([3]float64{3000, 24, 40}, [3]float64{3000, 24, 40})

	// No audio measured (the sampler has no audio preview): the audio checks do not apply, and say why.
	text := textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "n/a  audio_bed: there is no preview render of this version to listen to") {
		t.Errorf("no audio:\n%s", text)
	}

	g.smp.audio = &AudioResult{Report: soundReadyResult(item("bed_present", audiocheck.StatusFail, "20 %")).Report, Elapsed: 2300 * time.Millisecond, Source: "stem"}
	text = textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "FAIL audio_bed:") || !strings.Contains(text, "Fix for bed_present.") {
		t.Errorf("a missing bed:\n%s", text)
	}
	var rec struct {
		Data CheckRecord `json:"data"`
	}
	if err := json.Unmarshal((*g.artifacts)[len(*g.artifacts)-1], &rec); err != nil || rec.Data.Metrics.AudioMs != 2300 || rec.Data.Metrics.AudioProfile != audiocheck.ProfileSoundReady || rec.Data.Metrics.AudioSource != "stem" {
		t.Errorf("the metrics: %v %+v", err, rec.Data.Metrics)
	}

	// A preview from before audio stems, and a sampler that fails: the audio checks do not apply and the picture checks are unaffected.
	g.smp.audio, g.smp.audioErr = nil, ErrNoAudioStem
	text = textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "n/a  audio_level: the preview has no audio stem") || !strings.Contains(text, "PASS crushed") {
		t.Errorf("an old preview:\n%s", text)
	}
	g.smp.audioErr = errors.New("the sandbox is down")
	text = textOf(g.ok(g.nextCall(g.rc), ToolCheckEdit, `{}`))
	if !strings.Contains(text, "n/a  audio_effects: the preview's sound could not be measured") {
		t.Errorf("a failing sampler:\n%s", text)
	}
}

// The measurer over a generated preview and audio stem and the real ffmpeg: a preview with a beat guide is measured through its stem,
// one without a guide through the video, and a guided preview with no stem is ErrNoAudioStem.
func TestRenderMeasurerListensToTheStem(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	video, stem := filepath.Join(root, "render.mp4"), filepath.Join(root, "stem.m4a")
	// The video's sound is a loud 1 kHz tone (the guide's click, say); the stem's is a quiet 300 Hz bed.
	gen := func(out string, args ...string) {
		t.Helper()
		a := append([]string{"-nostdin", "-v", "error", "-y"}, args...)
		if o, err := exec.Command("ffmpeg", append(a, out)...).CombinedOutput(); err != nil {
			t.Skipf("this ffmpeg cannot generate the test file: %v: %s", err, o)
		}
	}
	gen(video, "-f", "lavfi", "-i", "color=c=0x303030:size=270x480:rate=30:duration=3", "-f", "lavfi", "-i", "sine=f=1000:d=3,volume=12dB",
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-c:a", "aac", "-shortest")
	gen(stem, "-f", "lavfi", "-i", "sine=f=300:d=3,volume=-30dB", "-c:a", "aac")
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ws := ids.WorkspaceID{UUID: newAssetID().UUID}
	prj := ids.ProjectID{UUID: newAssetID().UUID}
	rid := ids.RenderID{UUID: newAssetID().UUID}
	vkey, _ := storage.RenderKey(ws, prj, rid, storage.RenderMP4Name)
	skey, _ := storage.RenderKey(ws, prj, rid, storage.RenderAudioStemName)
	objs := newMemObjects()
	objs.set(vkey, read(video))
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	m := &RenderMeasurer{
		Store:   fakeRenders{renders: []store.Render{{ID: rid, WorkspaceID: ws, ProjectID: prj, Status: store.RenderReady, Mp4Key: &vkey}}},
		Objects: objs, TempDir: work, Logger: quiet,
		Runner: &media.SandboxRunner{Provider: sandbox.NewLocal(sandbox.Policy{AllowedRoots: []string{root}}), Image: "unused",
			Limits: sandbox.Limits{CPU: 1, MemoryMB: 512, Timeout: time.Minute, PIDs: 128}},
	}
	tracks := []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_x", Source: timeline.Source{OutMs: 3000}}}}}
	plain := &timeline.Spec{Output: timeline.Output{FPS: 30}, Tracks: tracks}
	guided := &timeline.Spec{Output: timeline.Output{FPS: 30}, Tracks: tracks, Markers: []timeline.Marker{{Kind: timeline.MarkerBeat, AtMs: 500}}}

	// No guide: the video's own sound, which is the loud tone.
	res, err := m.Audio(context.Background(), ws, prj, 1, plain)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "preview" || res.Report.Loudness.IntegratedLUFS < -15 || res.Elapsed <= 0 {
		t.Errorf("the video: source %s, %.1f LUFS, %v", res.Source, res.Report.Loudness.IntegratedLUFS, res.Elapsed)
	}
	// A guide and no stem: an old preview.
	if _, err := m.Audio(context.Background(), ws, prj, 1, guided); !errors.Is(err, ErrNoAudioStem) {
		t.Errorf("no stem: %v", err)
	}
	// A guide and a stem: the stem's own sound, 24 dB under the video's.
	objs.set(skey, read(stem))
	res2, err := m.Audio(context.Background(), ws, prj, 1, guided)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Source != "stem" || res2.Report.Loudness.IntegratedLUFS > res.Report.Loudness.IntegratedLUFS-15 {
		t.Errorf("the stem: source %s, %.1f LUFS against the video's %.1f", res2.Source, res2.Report.Loudness.IntegratedLUFS, res.Report.Loudness.IntegratedLUFS)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	none := &RenderMeasurer{Store: fakeRenders{}, Objects: objs, TempDir: work}
	if _, err := none.Audio(context.Background(), ws, prj, 1, plain); !errors.Is(err, ErrNoPreview) {
		t.Errorf("no preview: %v", err)
	}
}
