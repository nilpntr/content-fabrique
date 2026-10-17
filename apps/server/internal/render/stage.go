package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoFonts is returned when a plan draws text and the font files it names cannot be found.
var ErrNoFonts = errors.New("render: font files not found")

// containerFontsDir is where the media-tools image keeps the fonts (fonts.conf points fontconfig there).
const containerFontsDir = "/usr/share/fonts/cf"

// stageSubtitles writes the plan's ASS script and copies the fonts it names into the work directory, where the ass filter
// reads them by relative name (ffmpeg runs with the work directory as its working directory in every provider). Staging the
// same font files in every provider, instead of relying on whatever fonts the provider has installed, is what makes the
// layout the validator measured the layout libass draws. A plan without subtitles stages nothing.
func stageSubtitles(plan Plan, work, fontsDir string) error {
	s := plan.Subtitles
	if s == nil {
		return nil
	}
	src, err := findFontsDir(fontsDir, s.Fonts)
	if err != nil {
		return err
	}
	dst := filepath.Join(work, s.FontsDir)
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // read by the sandbox user, which may not be ours
		return fmt.Errorf("render: stage fonts: %w", err)
	}
	for _, f := range s.Fonts {
		data, err := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // a font file named by the style registry
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNoFonts, err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), data, 0o644); err != nil { //nolint:gosec // read by the sandbox user
			return fmt.Errorf("render: stage fonts: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, s.File), []byte(s.ASS), 0o644); err != nil { //nolint:gosec // read by the sandbox user
		return fmt.Errorf("render: stage subtitles: %w", err)
	}
	return nil
}

// findFontsDir returns the first candidate directory that holds every font file in names.
func findFontsDir(explicit string, names []string) (string, error) {
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.Join(dir, "packages", "media-tools", "fonts"))
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	candidates = append(candidates, containerFontsDir)
	for _, dir := range candidates {
		if hasAll(dir, names) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: looked for %v in %v; set ExecuteOptions.FontsDir", ErrNoFonts, names, candidates)
}

func hasAll(dir string, names []string) bool {
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(dir, n)); err != nil || !st.Mode().IsRegular() { //nolint:gosec // a font file name from the registry in a directory the operator configured
			return false
		}
	}
	return true
}
