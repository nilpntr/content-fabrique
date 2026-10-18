package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

func (r *projectRig) runStyle(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "style-test", MaxConns: 2})
	}}
	code = cmdStyle(append(args, "--env-file", r.envFile), &out, &errb, fakeProcessEnv(map[string]string{}), deps)
	return code, out.String(), errb.String()
}

func referenceMeasures(t *testing.T) []styleprofile.Measures {
	t.Helper()
	raw, err := os.ReadFile("../../internal/styleprofile/testdata/reference_measures.json")
	if err != nil {
		t.Fatal(err)
	}
	var ms []styleprofile.Measures
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestStyleShowAndClear(t *testing.T) {
	r := newProjectRig(t)
	ws := r.ws.ID.String()

	code, out, errOut := r.runStyle("show", "--workspace", ws)
	if code != 0 || !strings.Contains(out, "no creator style profile") {
		t.Fatalf("show with none: %d %s%s", code, out, errOut)
	}
	if _, err := styleprofile.Save(context.Background(), r.st, r.ws.ID, referenceMeasures(t)); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = r.runStyle("show", "--workspace", "org_project")
	if code != 0 {
		t.Fatalf("show: %d %s%s", code, out, errOut)
	}
	// The sound, in plain language.
	for _, want := range []string{"Creator style, from 5 reference videos", "Pacing: long takes", "Effects: none", "Sound: Quiet", "intro, then the music lifts", "not beat-locked", "energy builds to the end", "no fade-out", "music only at about -", "no sound effects", "Evidence:"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = r.runStyle("show", "--workspace", ws, "--json")
	var p styleprofile.Profile
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.References != 5 || p.Version != styleprofile.ProfileVersion {
		t.Fatalf("show --json: %d %s", code, out)
	}
	// Clearing deletes it.
	if code, out, errOut = r.runStyle("clear", "--workspace", ws); code != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("clear: %d %s%s", code, out, errOut)
	}
	if code, out, _ = r.runStyle("show", "--workspace", ws); code != 0 || !strings.Contains(out, "no creator style profile") {
		t.Fatalf("show after clear: %d %s", code, out)
	}
	// A workspace that is not there, and a bad id.
	if code, _, errOut = r.runStyle("show", "--workspace", "org_nobody"); code == 0 || errOut == "" {
		t.Errorf("an unknown workspace: %d %q", code, errOut)
	}
	if code, _, _ = r.runStyle("show", "--workspace", "nonsense"); code != 2 {
		t.Errorf("a bad workspace id: %d", code)
	}
	if code, _, errOut = r.runStyle("frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command: %d %q", code, errOut)
	}
}

func TestStyleAnalyzeRefusesWhatItCannotUse(t *testing.T) {
	r := newProjectRig(t)
	if code, _, errOut := r.runStyle("analyze", "--workspace", r.ws.ID.String()); code != 2 || !strings.Contains(errOut, "--files") {
		t.Errorf("no files: %d %q", code, errOut)
	}
	if code, _, errOut := r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--files", "/nonexistent/a.mp4"); code != 2 || !strings.Contains(errOut, "not a file") {
		t.Errorf("a file that is not there: %d %q", code, errOut)
	}
	many := make([]string, styleprofile.MaxReferences+1)
	for i := range many {
		many[i] = "a.mp4"
	}
	if code, _, errOut := r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--files", strings.Join(many, ",")); code != 2 || !strings.Contains(errOut, "at most") {
		t.Errorf("too many: %d %q", code, errOut)
	}
	if code, _, errOut := r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--provider", "vm", "--files", "x"); code == 0 || errOut == "" {
		t.Errorf("a provider that does not exist: %d %q", code, errOut)
	}
}

// The whole path on a made-up video, through the local sandbox: analyse, store, show. It skips without ffmpeg.
func TestStyleAnalyzeAStoredProfile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	r := newProjectRig(t)
	dir := t.TempDir()
	clip := filepath.Join(dir, "ref.mp4")
	gen := exec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=270x480:rate=30:duration=3", "-f", "lavfi", "-i", "color=c=0x2060c0:s=270x480:r=30:d=3",
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=44100:duration=6",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0,format=yuv420p[v]", "-map", "[v]", "-map", "2:a", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v: %s", err, out)
	}
	// --no-store: measures and prints, writes nothing, needs no workspace.
	code, out, errOut := r.runStyle("analyze", "--no-store", "--provider", "local", "--files", clip)
	if code != 0 || !strings.Contains(out, "analysed ref.mp4") || !strings.Contains(out, "1 cuts") || !strings.Contains(out, "Creator style, from 1 reference") {
		t.Fatalf("analyze --no-store: %d\n%s%s", code, out, errOut)
	}
	if _, err := r.st.GetCreatorStyle(context.Background(), r.ws.ID); err == nil {
		t.Fatal("--no-store stored a profile")
	}
	// Stored, without the vision read.
	code, out, errOut = r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--provider", "local", "--no-vision", "--files", clip)
	if code != 0 || !strings.Contains(out, "Stored as the workspace's creator style profile") {
		t.Fatalf("analyze: %d\n%s%s", code, out, errOut)
	}
	if row, err := r.st.GetCreatorStyle(context.Background(), r.ws.ID); err != nil || row.ReferencesCount != 1 {
		t.Fatalf("stored: %+v %v", row, err)
	}
	// --append of the same file replaces its measures instead of counting it twice.
	code, out, errOut = r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--provider", "local", "--no-vision", "--append", "--files", clip)
	if code != 0 {
		t.Fatalf("analyze --append: %d\n%s%s", code, out, errOut)
	}
	if row, err := r.st.GetCreatorStyle(context.Background(), r.ws.ID); err != nil || row.ReferencesCount != 1 {
		t.Fatalf("after append: %+v %v", row, err)
	}
	// A file that is not a video is an error and changes nothing.
	junk := filepath.Join(dir, "junk.mp4")
	if err := os.WriteFile(junk, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut = r.runStyle("analyze", "--workspace", r.ws.ID.String(), "--provider", "local", "--no-vision", "--files", junk); code != 1 || !strings.Contains(errOut, "junk.mp4") {
		t.Errorf("junk: %d %q", code, errOut)
	}
	if row, err := r.st.GetCreatorStyle(context.Background(), r.ws.ID); err != nil || row.ReferencesCount != 1 {
		t.Fatalf("a failed analysis changed the profile: %+v %v", row, err)
	}
}

// cfctl style set changes only the settings it is given (a live run of set --chapter-titles on reset every earlier choice); a setting given
// as "suggested", or named in --clear, goes back to the layers below.
func TestStyleSetMergesIntoTheCreatorsSettings(t *testing.T) {
	r := newProjectRig(t)
	ws := r.ws.ID.String()
	settings := func() styleprofile.Settings {
		t.Helper()
		row, err := r.st.GetCreatorStyleSettings(context.Background(), r.ws.ID)
		if err != nil {
			t.Fatal(err)
		}
		s, err := styleprofile.ParseSettings(row.Settings)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if code, out, errOut := r.runStyle("set", "--workspace", ws, "--vibe", "hype", "--transitions", "lots", "--text-animation", "on", "--camera-motion", "lots", "--title-case", "upper"); code != 0 {
		t.Fatalf("set: %d %s%s", code, out, errOut)
	}
	if code, out, errOut := r.runStyle("set", "--workspace", ws, "--chapter-titles", "on"); code != 0 || !strings.Contains(out, "the others stay") {
		t.Fatalf("set chapter titles: %d %s%s", code, out, errOut)
	}
	want := styleprofile.Settings{Vibe: "hype", Transitions: "lots", TextAnimation: "on", CameraMotion: "lots", TitleCase: "upper", ChapterTitles: "on"}
	if got := settings(); got != want {
		t.Fatalf("the earlier settings did not stay: %+v", got)
	}
	if code, out, errOut := r.runStyle("set", "--workspace", ws, "--transitions", "suggested", "--clear", "title_case,camera-motion"); code != 0 {
		t.Fatalf("clear: %d %s%s", code, out, errOut)
	}
	want.Transitions, want.TitleCase, want.CameraMotion = "", "", ""
	if got := settings(); got != want {
		t.Fatalf("after clearing: %+v", got)
	}
	if code, _, errOut := r.runStyle("set", "--workspace", ws, "--clear", "sky_colour"); code != 2 || !strings.Contains(errOut, "not a setting") {
		t.Errorf("an unknown setting to clear: %d %s", code, errOut)
	}
	if got := settings(); got != want {
		t.Errorf("a refused set changed the settings: %+v", got)
	}
}
