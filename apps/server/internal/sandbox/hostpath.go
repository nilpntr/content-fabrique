package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveHost validates one mount's host path and returns it with symlinks resolved. The resolved path is what gets
// mounted, so a symlink swapped in after this check cannot point the mount somewhere else.
func resolveHost(m Mount, roots []string) (string, error) {
	h := m.Host
	if !filepath.IsAbs(h) || filepath.Clean(h) != h {
		return "", fmt.Errorf("sandbox: mount host path %q must be absolute and clean", h)
	}
	if strings.ContainsAny(h, "\x00\n\r") {
		return "", fmt.Errorf("sandbox: mount host path %q has control characters", h)
	}
	resolved, err := filepath.EvalSymlinks(h)
	if err != nil {
		return "", fmt.Errorf("sandbox: mount host path: %w", err)
	}
	if !insideAny(resolved, roots) {
		return "", fmt.Errorf("sandbox: mount host path %q is outside the allowed roots", h)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("sandbox: mount host path: %w", err)
	}
	switch {
	case m.Writable && !fi.IsDir():
		return "", fmt.Errorf("sandbox: writable mount %q must be a directory", h)
	case !fi.IsDir() && !fi.Mode().IsRegular():
		return "", fmt.Errorf("sandbox: mount host path %q is neither a regular file nor a directory", h)
	}
	return resolved, nil
}

// insideAny reports whether p is one of the roots or below one.
func insideAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
