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

// renderFixtures returns the names of the render fixtures, from fixtures/render.txt.
func renderFixtures(t testing.TB) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(fixturesDir, "render.txt")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	var names []string
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
	if len(names) != 20 {
		t.Fatalf("want 20 render fixtures, have %d", len(names))
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
