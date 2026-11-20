package research

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
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
		if err := checkString(at, x); err != nil {
			return err
		}
	}
	return nil
}

// mediaMarkers are fragments that say a string describes video or audio content, wherever they sit in it (a query string, an embed's
// HTML, a pasted JSON string).
var mediaMarkers = []string{
	"mime_type=video", "mime=video", "mime_type=audio", "video/mp4", "video/webm", "video/quicktime", "application/x-mpegurl", "audio/mpeg",
	"video_mp4", "play_addr", "playaddr", "download_addr", "downloadaddr", "<video", "<source ",
}

// videoHosts are the hosts that serve video files: a URL on one of them is refused whatever its path says. pictureHosts (thumbnails,
// avatars) are not refused for their host, because oEmbed gives a thumbnail link; they are judged by their path.
var videoHosts = regexp.MustCompile(`(?i)^(` +
	`v\d+[a-z0-9-]*\.(tiktokcdn[a-z-]*\.com|tiktok\.com|tiktokv\.com|muscdn\.com|byteoversea\.com)` + // v16-webapp.tiktok.com, v19.tiktokcdn.com
	`|([a-z0-9-]+\.)*googlevideo\.com` +
	`|video[a-z0-9.-]*\.cdninstagram\.com` +
	`|video[a-z0-9.-]*\.fbcdn\.net` +
	`)$`)

var pictureHosts = regexp.MustCompile(`(?i)^(p\d+[a-z0-9-]*|[a-z0-9-]*sign[a-z0-9-]*)\.(tiktokcdn[a-z-]*\.com|muscdn\.com)$`)

// urlInText finds the links inside a longer string.
var urlInText = regexp.MustCompile(`(?i)https?://[^\s"'<>\\)]+`)

func checkString(at, x string) error {
	l := strings.ToLower(strings.TrimSpace(x))
	if strings.HasPrefix(l, "data:") {
		return fmt.Errorf("%w: data URI at %s", ErrMedia, at)
	}
	if len(x) > MaxFixtureString {
		return fmt.Errorf("%w: string of %d characters at %s", ErrMedia, len(x), at)
	}
	for _, m := range mediaMarkers {
		if strings.Contains(l, m) {
			return fmt.Errorf("%w: %q marker at %s", ErrMedia, m, at)
		}
	}
	for _, link := range urlInText.FindAllString(x, -1) {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if videoHosts.MatchString(host) && !pictureHosts.MatchString(host) {
			return fmt.Errorf("%w: link to a video host at %s", ErrMedia, at)
		}
		if IsMediaName(u.Path) {
			return fmt.Errorf("%w: link to a media file at %s", ErrMedia, at)
		}
	}
	return nil
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
