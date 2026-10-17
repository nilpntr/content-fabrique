package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fakeResolver resolves every asset to a fixed, readable path per variant, so the golden plans do not depend on a
// machine. Every asset has audio.
func fakeResolver(cat timeline.MemoryCatalog) AssetResolver {
	return ResolverFunc(func(assetID string, v Variant) (ResolvedAsset, error) {
		a, ok := cat[assetID]
		if !ok {
			return ResolvedAsset{}, fmt.Errorf("unknown asset %s", assetID)
		}
		ext := ".mp4"
		if a.Kind == timeline.AssetAudio {
			ext = ".m4a"
		}
		return ResolvedAsset{Location: "/assets/" + string(v) + "/" + assetID + ext, HasAudio: true}, nil
	})
}

// snapshot renders a plan as text for a golden file: the arguments one per line (the filter_complex is printed once,
// below, a statement to a line), then the cut list, the time map and the rest.
func snapshot(name string, p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fixture: %s\nprofile: %s\n", name, p.Profile.Name)
	fmt.Fprintf(&b, "output: %dx%d at %d fps, %d frames, %d ms\n", p.OutputSize.Width, p.OutputSize.Height, p.FPS, p.TotalFrames, p.DurationMs)
	b.WriteString("\nargs:\n")
	for i, a := range p.Args {
		if a == p.FilterComplex && i > 0 && p.Args[i-1] == "-filter_complex" {
			a = "<filter_complex>"
		}
		b.WriteString("  " + a + "\n")
	}
	b.WriteString("\nfilter_complex:\n")
	for _, l := range strings.Split(PrettyFilter(p.FilterComplex), "\n") {
		b.WriteString("  " + l + "\n")
	}
	if p.Loudnorm != nil {
		b.WriteString("\nloudnorm:\n")
		fmt.Fprintf(&b, "  filter: %s\n  two_pass: %v\n", p.Loudnorm.Filter, p.Loudnorm.TwoPass)
		if len(p.Loudnorm.Pass1Args) > 0 {
			b.WriteString("  pass1_args:\n")
			for i, a := range p.Loudnorm.Pass1Args {
				if i > 0 && p.Loudnorm.Pass1Args[i-1] == "-filter_complex" {
					b.WriteString("    <filter_complex of the measuring pass>\n")
					for _, l := range strings.Split(PrettyFilter(a), "\n") {
						b.WriteString("      " + l + "\n")
					}
					continue
				}
				b.WriteString("    " + a + "\n")
			}
		}
	}
	section := func(title string, v any) {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(&b, "\n%s:\n%s\n", title, data)
	}
	section("inputs", p.Inputs)
	section("cut_list", p.CutList)
	section("time_map", p.TimeMap)
	section("sound_cues", p.SoundCues)
	section("pending_text", p.PendingText)
	fmt.Fprintf(&b, "\npending_captions: %v\npending_watermark: %v\n", p.PendingCaptions, p.PendingWatermark)
	return b.String()
}

func TestGoldenPlans(t *testing.T) {
	cat := testAssets(t)
	dir := filepath.Join("testdata", "golden", "plans")
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for _, name := range renderFixtures(t) {
		spec := loadFixture(t, name, cat)
		for _, prof := range Profiles() {
			file := fmt.Sprintf("%s.%s.txt", name, prof.Name)
			want = append(want, file)
			t.Run(file, func(t *testing.T) {
				plan, err := Compile(spec, prof, fakeResolver(cat))
				if err != nil {
					t.Fatal(err)
				}
				got := snapshot(name, plan)
				path := filepath.Join(dir, file)
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				exp, err := os.ReadFile(path) //nolint:gosec // golden file
				if err != nil {
					t.Fatalf("%v (run `go test ./internal/render -update` to create it)", err)
				}
				if string(exp) != got {
					t.Errorf("plan differs from %s; run `go test ./internal/render -update` and read the diff\n%s", path, firstDiff(string(exp), got))
				}
			})
		}
	}
	// A golden plan for a fixture that is no longer a render fixture is stale.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !slices.Contains(want, e.Name()) {
			t.Errorf("stale golden plan %s", filepath.Join(dir, e.Name()))
		}
	}
}

// firstDiff shows the first line that differs, so a failure is readable without a diff tool.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want: %s\n  got:  %s", i+1, wl, gl)
		}
	}
	return ""
}
