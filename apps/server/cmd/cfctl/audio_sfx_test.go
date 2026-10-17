package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/audiocheck"
	"github.com/nilpntr/content-fabrique/internal/renderjobs"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The sheet of the sound marks the contact sheet's tiles at the times of their frames: the two offsets must stay equal (audiocheck
// cannot import renderjobs).
func TestAudioSheetTileOffsetMatchesTheContactSheet(t *testing.T) {
	if audiocheck.TileOffsetMs != renderjobs.SheetOffsetMs {
		t.Errorf("audiocheck.TileOffsetMs = %d, renderjobs.SheetOffsetMs = %d", audiocheck.TileOffsetMs, renderjobs.SheetOffsetMs)
	}
}

func needFFmpeg(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
	return p
}

func TestSFXGalleryRefusesToWriteIntoTheRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// The working directory of a test is the package directory, inside the repository.
	if code := run([]string{"sfx", "gallery", "-o", "gallery-test.html"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "inside the repository") {
		t.Errorf("code %d: %s", code, stderr.String())
	}
	if _, err := os.Stat("gallery-test.html"); err == nil {
		t.Error("a page was written into the repository")
	}
	if code := run([]string{"sfx", "gallery"}, &stdout, &stderr); code != 2 {
		t.Errorf("no -o: code %d", code)
	}
	if code := run([]string{"sfx", "gallery", "-o", filepath.Join(t.TempDir(), "x.html"), "--provider", "bogus"}, &stdout, &stderr); code != 2 {
		t.Errorf("a bogus provider: code %d", code)
	}
	if outsideRepo(t.TempDir(), galleryRepoRoot()) != true {
		t.Error("the temporary directory is not outside the repository")
	}
	if root := galleryRepoRoot(); root == "" || outsideRepo(filepath.Join(root, "docs", "x.html"), root) {
		t.Errorf("a path in the repository is outside it (root %q)", root)
	}
}

// The gallery renders every category and variant and three round-robin items per category, and writes a page that names them.
func TestSFXGalleryWritesAPage(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	page := filepath.Join(dir, "sounds", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o750); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sfx", "gallery", "-o", page}, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	variants := 0
	for _, c := range timeline.SFXCategories() {
		vs, _ := timeline.SFXVariants(c)
		variants += len(vs)
	}
	want := variants + 3*len(timeline.SFXCategories())
	files, err := filepath.Glob(filepath.Join(dir, "sounds", "index_files", "*.wav"))
	if err != nil || len(files) != want {
		t.Fatalf("%d wav files, want %d (%v)", len(files), want, err)
	}
	for _, ext := range []string{"wave.png", "spec.png"} {
		if got, _ := filepath.Glob(filepath.Join(dir, "sounds", "index_files", "*."+ext)); len(got) != want {
			t.Errorf("%d %s files, want %d", len(got), ext, want)
		}
	}
	// A waveform is a real picture.
	f, err := os.Open(filepath.Join(dir, "sounds", "index_files", "impact_punch_s1.wave.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if img, err := png.Decode(f); err != nil || img.Bounds().Dx() != 640 {
		t.Errorf("waveform: %v", err)
	}
	html, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range timeline.SFXCategories() {
		vs, _ := timeline.SFXVariants(c)
		for _, v := range vs {
			if !strings.Contains(string(html), c+"_"+v+"_s1.wav") {
				t.Errorf("the page has no player for %s/%s", c, v)
			}
		}
		if !strings.Contains(string(html), c+"_auto_rr3.wav") {
			t.Errorf("no round-robin player for %s", c)
		}
	}
	if strings.Count(string(html), "<audio") != want {
		t.Errorf("%d players, want %d", strings.Count(string(html), "<audio"), want)
	}
	// Nothing was written next to the page but its own files.
	entries, _ := os.ReadDir(filepath.Join(dir, "sounds"))
	if len(entries) != 2 {
		t.Errorf("the output directory holds %d entries, want the page and its files directory", len(entries))
	}
}

func TestAudioCheckFromAFileAndATimeMap(t *testing.T) {
	ff := needFFmpeg(t)
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "render.mp4")
	cmd := exec.CommandContext(context.Background(), ff, "-hide_banner", "-loglevel", "error", "-y", //nolint:gosec // test helper with fixed arguments
		"-f", "lavfi", "-i", "color=c=gray:s=160x96:r=30:d=4", "-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*440*t)|0.3*sin(2*PI*440*t)':s=48000:d=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-shortest", mp4)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	tm := `[{"out_start_ms":0,"out_end_ms":2000,"item_id":"c1","asset_id":"ast_x","source_in_ms":0,"speed":1},{"out_start_ms":2000,"out_end_ms":4000,"item_id":"c2","asset_id":"ast_x","source_in_ms":0,"speed":1},{"out_start_ms":0,"out_end_ms":1000,"item_id":"t1"}]`
	tmPath := filepath.Join(dir, "time_map.json")
	if err := os.WriteFile(tmPath, []byte(tm), 0o600); err != nil {
		t.Fatal(err)
	}
	sheet, report := filepath.Join(dir, "sheet.png"), filepath.Join(dir, "report.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"audio", "check", "--file", mp4, "--time-map", tmPath, "--sheet", sheet, "--json", report}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Loudness:", "PASS", "[target:", "wrote the sheet", "wrote the report"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output has no %q:\n%s", want, out)
		}
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil || parsed["summary"] == nil || parsed["checklist"] == nil {
		t.Errorf("the report is not the JSON of a Report: %v", err)
	}
	img, err := os.ReadFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(img)); err != nil {
		t.Errorf("the sheet is not a PNG: %v", err)
	}
	// Usage errors: both modes at once, neither, and a file without a time map.
	for _, args := range [][]string{
		{"audio", "check"}, {"audio", "check", "--file", mp4}, {"audio", "check", "--file", mp4, "--project", "prj_x"}, {"audio", "bogus"},
		{"audio", "check", "--project", "not-a-project", "--workspace", "wks_x"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Errorf("%v: code 0", args)
		}
	}
}
