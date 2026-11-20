package research

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Replay fixtures (R2 onwards) are metadata only: the oEmbed fields, an API's JSON with the media URLs stripped, the watch sheet's rows.
// CheckFixture is the guard that keeps it so; a test runs it over every fixture directory, and the importers run it before they record.

// mediaKeyParts are parts of a JSON key that name a media file or a way to fetch one. A thumbnail is a link to a picture on the
// platform's server, and oEmbed gives one, so thumbnails are not listed: what must never appear is the video itself.
var mediaKeyParts = []string{
	"media_url", "video_url", "videourl", "play_url", "playurl", "play_addr", "playaddr", "download", "stream_url", "streamurl",
	"video_file", "videofile", "mp4", "hls", "dash_url", "audio_url", "video_versions", "video_data", "bitrate", "transcript",
}

// mediaExtensions are the extensions of video and audio files, in a file name or at the end of a URL path.
var mediaExtensions = []string{".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi", ".flv", ".3gp", ".m3u8", ".mpd", ".ts", ".mp3", ".m4a", ".aac", ".wav", ".ogg"}

// ErrMedia is wrapped by every media refusal.
var ErrMedia = errors.New("research: media is not allowed in research data")

// CheckFixture reads a JSON document and refuses it when it holds a media key, a link to a media file, a data: URI or a very long
// string (a pasted transcript or an encoded file). The error names the path of the offence, never its value.
func CheckFixture(raw []byte) error {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("research: fixture is not JSON: %w", err)
	}
	return walk("$", v)
}

// MaxFixtureString is the longest string a fixture may hold: a caption is at most 4,000 characters on any platform we read.
const MaxFixtureString = 4000

func walk(at string, v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			lk := strings.ToLower(k)
			for _, part := range mediaKeyParts {
				if strings.Contains(lk, part) {
					return fmt.Errorf("%w: key %s.%s", ErrMedia, at, k)
				}
			}
			if err := walk(at+"."+k, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range x {
			if err := walk(fmt.Sprintf("%s[%d]", at, i), child); err != nil {
				return err
			}
		}
	case string:
		l := strings.ToLower(strings.TrimSpace(x))
		switch {
		case strings.HasPrefix(l, "data:"):
			return fmt.Errorf("%w: data URI at %s", ErrMedia, at)
		case len(x) > MaxFixtureString:
			return fmt.Errorf("%w: string of %d characters at %s", ErrMedia, len(x), at)
		case isLink(l) && hasMediaExtension(l):
			return fmt.Errorf("%w: link to a media file at %s", ErrMedia, at)
		}
	}
	return nil
}

func isLink(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func hasMediaExtension(link string) bool {
	if i := strings.IndexAny(link, "?#"); i >= 0 {
		link = link[:i]
	}
	return IsMediaName(link)
}

// IsMediaName says whether a file name or URL path ends in the extension of a video or audio file.
func IsMediaName(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, e := range mediaExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// CheckFixtureDir walks a directory of fixtures: a media file by name is refused, a .json file goes through CheckFixture.
func CheckFixtureDir(fsys fs.FS, dir string) error {
	var errs []error
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if IsMediaName(p) {
			errs = append(errs, fmt.Errorf("%w: file %s", ErrMedia, p))
			return nil
		}
		if strings.EqualFold(path.Ext(p), ".json") {
			raw, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if err := CheckFixture(raw); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
