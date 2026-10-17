package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/media"
)

// ErrNoFonts is returned when a plan draws text, runs outside the media-tools image and the font files it names cannot be found.
var ErrNoFonts = errors.New("render: font files not found")

// ImageFontsDir is where the media-tools image keeps the fonts (its fonts.conf points fontconfig there too).
const ImageFontsDir = "/usr/share/fonts/cf"

// runsInImage reports whether the runner executes ffmpeg inside the media-tools image: a sandbox runner over a provider other
// than local (docker, hardened). The image carries the fonts, so nothing needs staging, and a render works anywhere, outside a
// repository checkout too. The local provider runs the host's ffmpeg, which has no fonts of ours.
func runsInImage(runner media.Runner) bool {
	sr, ok := runner.(*media.SandboxRunner)
	return ok && sr.Provider != nil && sr.Provider.Name() != "local"
}

// stageSubtitles writes the plan's ASS script into the work directory, where the ass filter reads it by relative name (ffmpeg
// runs with the work directory as its working directory in every provider). Unless the render runs in the image, it also copies
// the fonts the script names next to it, from fontsDir (see findFontsDir), and the filter reads them there. A plan without
// subtitles stages nothing.
func stageSubtitles(plan Plan, work, fontsDir string, inImage bool) error {
	s := plan.Subtitles
	if s == nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(work, s.File), []byte(s.ASS), 0o644); err != nil { //nolint:gosec // read by the sandbox user, which may not be ours
		return fmt.Errorf("render: stage subtitles: %w", err)
	}
	if inImage {
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
	return nil
}

// useImageFonts returns a copy of args whose ass filter reads the image's fonts directory instead of the staged one.
func useImageFonts(args []string, s *Subtitles) []string {
	if s == nil {
		return args
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "fontsdir="+s.FontsDir, "fontsdir="+ImageFontsDir)
	}
	return out
}

// findFontsDir returns the first candidate directory that holds every font file in names: the explicit one, then
// packages/media-tools/fonts above the working directory (a repository checkout, for development and tests).
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
	for _, dir := range candidates {
		if hasAll(dir, names) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: looked for %v in %v; pass ExecuteOptions.FontsDir (cfctl render --fonts-dir) when running the local provider outside a checkout, or render through the media-tools image, which has them", ErrNoFonts, names, candidates)
}

func hasAll(dir string, names []string) bool {
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(dir, n)); err != nil || !st.Mode().IsRegular() { //nolint:gosec // a font file name from the registry in a directory the operator configured
			return false
		}
	}
	return true
}
