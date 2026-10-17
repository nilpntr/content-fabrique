package styleprofile

import (
	"fmt"
	"os"
	"time"
)

// readRegular reads a file the sandbox wrote: a regular file (not a link) under a size cap.
func readRegular(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("styleprofile: ffmpeg wrote no %s", baseName(path))
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, fmt.Errorf("styleprofile: %s is not a regular file of a plausible size (%d bytes)", baseName(path), fi.Size())
	}
	return os.ReadFile(path) // #nosec G304 -- a file this process just checked, in its own job dir
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// runTimeout is how long the style pass may take: a fixed part and a share of the video's length (the pass decodes at many times
// real time, but a 4K HEVC reference at 60 fps is slower than the 720 px proxies the analysis reads).
func runTimeout(durationS float64) time.Duration {
	return 2*time.Minute + time.Duration(durationS*float64(time.Second))
}
