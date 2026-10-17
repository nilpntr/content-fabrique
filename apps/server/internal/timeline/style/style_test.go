package style

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fontsDir is packages/media-tools/fonts, relative to this package.
const fontsDir = "../../../../../packages/media-tools/fonts"

func readFont(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fontsDir, file)) //nolint:gosec // vendored test input
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The generated tables are what the vendored font files say: regenerating them gives the committed ones, so the layout cannot
// drift from the fonts (go generate ./internal/timeline/style rewrites metrics_gen.go).
func TestTablesMatchTheFontFiles(t *testing.T) {
	for _, file := range FontFiles() {
		ff, err := ParseFont(readFont(t, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		got, ok := generatedTables[file]
		if !ok {
			t.Fatalf("no generated table for %s", file)
		}
		if want := BuildTable(ff); !reflect.DeepEqual(want, got) {
			t.Errorf("%s: metrics_gen.go is out of date with the font file; run go generate ./internal/timeline/style", file)
		}
	}
}

// Every font and style of the registry is usable, and every font is in the licence manifest with the hash of the vendored file.
func TestRegistryIsConsistent(t *testing.T) {
	manifest := map[string]string{} // font file -> sha256 from the manifest
	spdx := map[string]string{}
	f, err := os.Open(filepath.Join(fontsDir, "manifest.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) < 9 || cols[0] == "kind" {
			continue
		}
		if cols[0] == "font" {
			manifest[cols[4]], spdx[cols[4]] = cols[5], cols[3]
		}
		if cols[0] == "licence" {
			if _, err := os.Stat(filepath.Join(fontsDir, cols[4])); err != nil {
				t.Errorf("licence text %s of %s is missing: %v", cols[4], cols[1], err)
			}
		}
	}
	for _, fo := range Fonts() {
		sum := sha256.Sum256(readFont(t, fo.File))
		if got := hex.EncodeToString(sum[:]); got != manifest[fo.File] {
			t.Errorf("%s: sha256 %s, manifest %q", fo.File, got, manifest[fo.File])
		}
		if spdx[fo.File] != "OFL-1.1" {
			t.Errorf("%s is %q, only OFL-1.1 fonts are allowed (D-16)", fo.File, spdx[fo.File])
		}
		ff, _ := ParseFont(readFont(t, fo.File))
		if ff.Family == "" || (ff.Family != fo.Family && ff.Full != fo.Family) {
			t.Errorf("%s: the ASS font name %q is neither the family %q nor the full name %q", fo.File, fo.Family, ff.Family, ff.Full)
		}
	}
	for file := range manifest {
		if !slices.Contains(FontFiles(), file) {
			t.Errorf("%s is vendored but no style can use it: drop it (the repository stays small) or add a font to the registry", file)
		}
	}
	ids := IDs()
	if !slices.IsSorted(ids) || len(ids) < 6 || len(ids) > 8 {
		t.Errorf("styles %v: want 6 to 8, sorted", ids)
	}
	for _, s := range All() {
		if _, ok := LookupFont(s.Font); !ok {
			t.Errorf("style %s names the unknown font %s", s.ID, s.Font)
		}
		if s.SizePct <= 0 || s.Description == "" || s.Role == RoleWatermark {
			t.Errorf("style %s is incomplete: %+v", s.ID, s)
		}
		if s.Pad(s.FontSizePx(1920)) < 1 {
			t.Errorf("style %s has no padding", s.ID)
		}
		got, ok := Lookup(s.ID)
		if !ok || got.ID != s.ID {
			t.Errorf("Lookup(%s) failed", s.ID)
		}
	}
	if _, ok := Lookup("watermark"); ok {
		t.Error("the watermark is the renderer's own style, not one the spec can use")
	}
	for _, kind := range []string{CaptionChunk, CaptionWordHighlight} {
		if s, ok := CaptionStyle(kind); !ok || (kind == CaptionWordHighlight) != (s.Highlight != Colour{}) {
			t.Errorf("caption style for %s: %+v %v", kind, s, ok)
		}
	}
}

func TestColourASS(t *testing.T) {
	if got := Opaque(0x11, 0x22, 0x33).ASS(); got != "&H00332211" {
		t.Errorf("opaque: %s", got)
	}
	if got := (Colour{R: 1, G: 2, B: 3, A: 0}).ASS(); got != "&HFF030201" {
		t.Errorf("transparent: %s", got)
	}
}

// The measured widths follow the font: advance widths in font units times size / (winAscent + winDescent), the scale libass
// uses, which the ink tests in package render verify against real renders.
func TestMeasureUsesRealAdvanceWidths(t *testing.T) {
	st, _ := Lookup("title_bold")
	lay := Measure(st, 1920, "Hi", 10000)
	tab := generatedTables["Montserrat-ExtraBold.ttf"]
	hAdv, _ := tab.advance('H')
	iAdv, _ := tab.advance('i')
	want := float64(hAdv+iAdv) * 72 / float64(tab.WinAscent+tab.WinDescent)
	if got := lay.Widths[0]; got < int(want) || got > int(want)+1 {
		t.Errorf("Hi is %d px wide, the advances give %.2f", got, want)
	}
	if lay.FontSize != 72 || lay.LineHeight != 72 {
		t.Errorf("size %d, line %d", lay.FontSize, lay.LineHeight)
	}
	w, h := lay.Box()
	if w != lay.Widths[0]+2*lay.Pad || h != 72+2*lay.Pad {
		t.Errorf("box %dx%d", w, h)
	}
	// Zero-width characters take no room; an unmapped character takes a full em (an upper bound).
	zw := Measure(st, 1920, "H\u2060\u200BH", 10000).Widths[0]
	hh := Measure(st, 1920, "HH", 10000).Widths[0]
	if zw != hh {
		t.Errorf("zero-width characters add width: %d vs %d", zw, hh)
	}
	if em := Measure(st, 1920, "\U0001F600", 10000).Widths[0]; em < 51 || em > 53 {
		t.Errorf("an unmapped character is %d px wide, want one em (1000 of 1379 units of 72 px = 52 px)", em)
	}
}

func TestWrap(t *testing.T) {
	st, _ := Lookup("title_bold")
	maxW := 400
	lay := Measure(st, 1920, "Never skip leg day and drive through your heels", maxW)
	if len(lay.Lines) < 3 {
		t.Fatalf("lines %q", lay.Lines)
	}
	for i, l := range lay.Lines {
		if lay.Widths[i]+2*lay.Pad > maxW {
			t.Errorf("line %q is %d px wide, the limit is %d", l, lay.Widths[i]+2*lay.Pad, maxW)
		}
		if l != strings.TrimSpace(l) {
			t.Errorf("line %q has spaces at its ends", l)
		}
	}
	if strings.Join(lay.Lines, " ") != "Never skip leg day and drive through your heels" {
		t.Errorf("words were lost or reordered: %q", lay.Lines)
	}
	// A word wider than a line is broken between characters; the rest goes on.
	long := Measure(st, 1920, "supercalifragilisticexpialidocious ok", maxW)
	if len(long.Lines) < 3 || strings.ReplaceAll(strings.Join(long.Lines, ""), " ", "") != "supercalifragilisticexpialidociousok" {
		t.Errorf("long word: %q", long.Lines)
	}
	for i := range long.Lines {
		if long.Widths[i]+2*long.Pad > maxW {
			t.Errorf("long word line %d is %d px wide", i, long.Widths[i])
		}
	}
	// Explicit lines stay, empty ones too.
	if got := Measure(st, 1920, "a\n\nb", maxW).Lines; !reflect.DeepEqual(got, []string{"a", "", "b"}) {
		t.Errorf("explicit lines: %q", got)
	}
}

func TestZone(t *testing.T) {
	z, _ := ZoneFor(Format9x16, 1080, 1920)
	if c := z.Column(1080); c.Left != 180 || c.Right != 900 {
		t.Errorf("9:16 column %+v, want x 180 to 900 (the action column starts at 900)", c)
	}
	if c := (Zone{Inner: Rect{Left: 100, Top: 0, Right: 980, Bottom: 10}}).Column(1080); c.Width() != 880 {
		t.Errorf("a zone with nothing blocked keeps its width: %+v", c)
	}
	box := Rect{Left: 700, Top: 800, Right: 1000, Bottom: 900}
	if z.Safe(box) {
		t.Fatal("a box into the action column and past the right margin is not safe")
	}
	fit, ok := z.Fit(box)
	if !ok || !z.Safe(fit) || fit.Width() != 300 || fit.Height() != 100 {
		t.Errorf("Fit moved it to %+v (%v)", fit, ok)
	}
	if fit, ok := z.Fit(Rect{Left: 200, Top: 300, Right: 400, Bottom: 400}); !ok || fit != (Rect{Left: 200, Top: 300, Right: 400, Bottom: 400}) {
		t.Errorf("a safe box must not move: %+v", fit)
	}
	if _, ok := z.Fit(Rect{Left: 0, Top: 0, Right: 900, Bottom: 100}); ok {
		t.Error("a box wider than the zone cannot be fitted")
	}
	if room := z.SlideRoom(Rect{Left: 200, Top: 1000, Right: 400, Bottom: 1200}); room != 60 {
		t.Errorf("slide room %d, want 60 (bottom of the zone at 1260)", room)
	}
	if room := z.SlideRoom(Rect{Left: 800, Top: 600, Right: 950, Bottom: 700}); room != 140 {
		t.Errorf("slide room %d, want 140 (the action column starts at y 840)", room)
	}
}
