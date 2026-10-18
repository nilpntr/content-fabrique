package render

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fixturesDir is packages/schemas/timeline/fixtures, relative to this package (apps/server/internal/render).
const fixturesDir = "../../../../packages/schemas/timeline/fixtures"

// update rewrites the golden plans and the golden frames: `go test ./internal/render -update`. A reviewer reads the diff.
var update = flag.Bool("update", false, "rewrite the golden plans and golden frames")

// renderFixtures returns the names of the render fixtures: the 20 of fixtures/render.txt (P3-03) and the 5 text and caption
// fixtures of fixtures/render-text.txt (P3-04, a separate list so that the P3-01 fixture tests keep their count).
func renderFixtures(t testing.TB) []string {
	t.Helper()
	var names []string
	for _, list := range []string{"render.txt", "render-text.txt", "render-motion.txt", "render-looks.txt", "render-camera.txt"} {
		f, err := os.Open(filepath.Join(fixturesDir, list)) //nolint:gosec // test fixture
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			names = append(names, strings.TrimSuffix(line, ".json"))
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if len(names) != 35 {
		t.Fatalf("want 35 render fixtures (20 in render.txt, 6 text and caption fixtures in render-text.txt, 4 motion fixtures in render-motion.txt, 4 look and sound-effect fixtures in render-looks.txt, 1 living-camera fixture in render-camera.txt), have %d", len(names))
	}
	return names
}

// loadFixture reads a valid fixture, checks that it validates against the asset catalog and decodes it.
func loadFixture(t testing.TB, name string, cat timeline.MemoryCatalog) *timeline.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir, "valid", name+".json")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	if res := timeline.Validate(data, cat); len(res.Issues) != 0 {
		t.Fatalf("fixture %s is not valid: %+v", name, res.Issues)
	}
	spec, err := timeline.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
