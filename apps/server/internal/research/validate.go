package research

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/taxonomy"
)

// PostRef is a post page, parsed from a link: the platform, the platform's id of the post and the canonical link we store.
type PostRef struct {
	Platform   Platform
	ExternalID string
	// URL is the canonical https link of the post page, without query or fragment.
	URL string
	// Handle is the creator's handle when the link carries it (TikTok), lower-case without the @; empty otherwise.
	Handle string
}

var (
	tiktokPath    = regexp.MustCompile(`^/@([A-Za-z0-9._]{1,64})/video/(\d{6,25})/?$`)
	instagramPath = regexp.MustCompile(`^(?:/[A-Za-z0-9._]{1,64})?/(?:p|reel|tv)/([A-Za-z0-9_-]{5,32})/?$`)
	youtubeShorts = regexp.MustCompile(`^/shorts/([A-Za-z0-9_-]{11})/?$`)
	youtubeID     = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
)

// ParsePostURL accepts the link of one post page of an allowed platform and refuses everything else: a profile or hashtag page, a search,
// a short link that needs a network call to resolve, another host, a link with credentials or a port. Only a post page can become a
// research post, so a link to a media file or a profile can never be stored as one.
func ParsePostURL(raw string) (PostRef, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return PostRef{}, fmt.Errorf("research: %q is not a web link to a post", clip(raw))
	}
	if u.User != nil || u.Port() != "" {
		return PostRef{}, errors.New("research: a post link has no credentials and no port")
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	path := u.EscapedPath()
	switch host {
	case "tiktok.com":
		if m := tiktokPath.FindStringSubmatch(path); m != nil {
			return PostRef{Platform: TikTok, ExternalID: m[2], Handle: strings.ToLower(m[1]), URL: "https://www.tiktok.com/@" + m[1] + "/video/" + m[2]}, nil
		}
	case "instagram.com":
		if m := instagramPath.FindStringSubmatch(path); m != nil {
			kind := "p"
			switch {
			case strings.Contains(path, "/reel/"):
				kind = "reel"
			case strings.Contains(path, "/tv/"):
				kind = "tv"
			}
			return PostRef{Platform: Instagram, ExternalID: m[1], URL: "https://www.instagram.com/" + kind + "/" + m[1] + "/"}, nil
		}
	case "youtube.com":
		if m := youtubeShorts.FindStringSubmatch(path); m != nil {
			return PostRef{Platform: YouTube, ExternalID: m[1], URL: "https://www.youtube.com/shorts/" + m[1]}, nil
		}
		if path == "/watch" {
			if id := u.Query().Get("v"); youtubeID.MatchString(id) {
				return PostRef{Platform: YouTube, ExternalID: id, URL: "https://www.youtube.com/watch?v=" + id}, nil
			}
		}
	case "youtu.be":
		if id := strings.Trim(path, "/"); youtubeID.MatchString(id) {
			return PostRef{Platform: YouTube, ExternalID: id, URL: "https://www.youtube.com/watch?v=" + id}, nil
		}
	}
	return PostRef{}, fmt.Errorf("research: %q is not the link of a TikTok, Instagram or YouTube post page", clip(raw))
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

var handleRe = regexp.MustCompile(`^[a-z0-9._]{1,64}$`)

// NormalizeHandle lower-cases a creator handle and removes the @. It refuses anything that is not a plain handle.
func NormalizeHandle(h string) (string, error) {
	h = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(h), "@"))
	if !handleRe.MatchString(h) {
		return "", fmt.Errorf("research: %q is not a creator handle (letters, digits, dot and underscore, at most 64)", clip(h))
	}
	return h, nil
}

// ProfileURL returns the canonical profile link of a handle. Every profile link we store is built here, never taken from a response.
func ProfileURL(p Platform, handle string) (string, error) {
	h, err := NormalizeHandle(handle)
	if err != nil {
		return "", err
	}
	switch p {
	case TikTok:
		return "https://www.tiktok.com/@" + h, nil
	case Instagram:
		return "https://www.instagram.com/" + h + "/", nil
	case YouTube:
		return "https://www.youtube.com/@" + h, nil
	}
	return "", fmt.Errorf("research: unknown platform %q", p)
}

// CheckObservation validates what an observation carries before it is stored: the source and method belong together, the platform is one
// the source can see, and the metrics are only the counts the source gives, as non-negative whole numbers.
func CheckObservation(source Source, method Method, platform Platform, metrics map[string]float64) error {
	if !slices.Contains(Sources(), source) {
		return fmt.Errorf("research: %q is not an allowed source", source)
	}
	if !slices.Contains(source.Methods(), method) {
		return fmt.Errorf("research: source %s cannot be read by method %q", source, method)
	}
	if !slices.Contains(source.Platforms(), platform) {
		return fmt.Errorf("research: source %s cannot observe %s", source, platform)
	}
	allowed := source.Metrics()
	for k, v := range metrics {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("research: source %s does not give %q (it gives %v)", source, k, allowed)
		}
		if v < 0 || v != float64(int64(v)) || v > 1e12 {
			return fmt.Errorf("research: metric %s must be a whole number from 0 to 1e12", k)
		}
	}
	return nil
}

// TextPatterns are the four text patterns of the edit style (D-38).
func TextPatterns() []string { return []string{"single_title", "story_captions", "dialogue", "none"} }

// CTAs are the calls to action an annotation can record.
func CTAs() []string { return []string{"save", "follow", "comment", "none"} }

var (
	hookKindRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	slotRe     = regexp.MustCompile(`<[A-Z]{1,12}>`)
)

// Annotation is what is noted about a post. Every field is optional except what the caller sets; empty strings and nil pointers mean
// "not noted". It holds no caption text and no creator wording: the hook text is a title FORM.
type Annotation struct {
	FormatID        string
	HookKind        string
	HookTextPattern string
	TextPattern     string
	CutsEstimate    *int
	FirstCutS       *float64
	CaptionLen      *int
	HashtagsN       *int
	CTA             string
	// Topic is a value of the clip vocabulary (config/tags) as "facet:value", such as "body:back".
	Topic      string
	Annotator  string
	Confidence float64
}

// Vocabulary is what annotations are checked against: the format cards and the clip vocabulary.
type Vocabulary struct {
	Formats  *formats.Library
	Taxonomy *taxonomy.Taxonomy
}

// DefaultVocabulary is the format library and the tag vocabulary embedded in the binary (config/formats, config/tags).
func DefaultVocabulary() Vocabulary {
	return Vocabulary{Formats: formats.Default(), Taxonomy: taxonomy.Default()}
}

// CheckAnnotation validates an annotation: the format is an id of the library, the topic a value of the vocabulary, the text pattern one
// of the four, and the hook text pattern a title form with slots and capitals only (so no creator wording can be kept in it).
func (v Vocabulary) CheckAnnotation(a Annotation) error {
	var errs []error
	if a.Annotator = strings.TrimSpace(a.Annotator); a.Annotator == "" || len(a.Annotator) > 80 {
		errs = append(errs, errors.New("annotator: 1 to 80 characters"))
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		errs = append(errs, errors.New("confidence: 0 to 1"))
	}
	if a.FormatID != "" {
		if _, ok := v.Formats.Card(a.FormatID); !ok {
			errs = append(errs, fmt.Errorf("format_id %q is not a format of the library", a.FormatID))
		}
	}
	if a.HookKind != "" && !hookKindRe.MatchString(a.HookKind) {
		errs = append(errs, fmt.Errorf("hook_kind %q: lower-case words with underscores, at most 32 characters", a.HookKind))
	}
	if a.HookTextPattern != "" {
		if err := CheckTitleForm(a.HookTextPattern); err != nil {
			errs = append(errs, fmt.Errorf("hook_text_pattern: %w", err))
		}
	}
	if a.TextPattern != "" && !slices.Contains(TextPatterns(), a.TextPattern) {
		errs = append(errs, fmt.Errorf("text_pattern %q is not one of %v", a.TextPattern, TextPatterns()))
	}
	if a.CTA != "" && !slices.Contains(CTAs(), a.CTA) {
		errs = append(errs, fmt.Errorf("cta %q is not one of %v", a.CTA, CTAs()))
	}
	if a.Topic != "" {
		ref, ok := taxonomy.ParseRef(a.Topic)
		if !ok || !v.Taxonomy.Known(ref) {
			errs = append(errs, fmt.Errorf("topic %q is not a value of the tag vocabulary", a.Topic))
		}
	}
	for name, n := range map[string]*int{"cuts_estimate": a.CutsEstimate, "caption_len": a.CaptionLen, "hashtags_n": a.HashtagsN} {
		if n != nil && *n < 0 {
			errs = append(errs, fmt.Errorf("%s must not be negative", name))
		}
	}
	if a.FirstCutS != nil && *a.FirstCutS < 0 {
		errs = append(errs, errors.New("first_cut_s must not be negative"))
	}
	return errors.Join(errs...)
}

// CheckTitleForm validates a hook text pattern: a title FORM such as "<TOPIC> 101": slots (<TOPIC>, <N>), capital letters, digits and
// punctuation, at most 60 characters. A lower-case letter is refused, so a creator's actual words cannot be kept as a "pattern".
func CheckTitleForm(f string) error {
	if f == "" || len(f) > 60 {
		return errors.New("1 to 60 characters")
	}
	rest := slotRe.ReplaceAllString(f, "")
	if strings.ContainsAny(rest, "<>") {
		return errors.New("a slot is a word in capitals between angle brackets, such as <TOPIC>")
	}
	if rest != strings.ToUpper(rest) {
		return errors.New("a title form has slots and capitals only, never the creator's words")
	}
	return nil
}

// Dimensions are the dimensions a finding can be about.
func Dimensions() []string {
	return []string{"hook", "length", "cadence", "format", "caption", "time", "saves_shares"}
}

// Confidences are the confidence levels of a finding.
func Confidences() []string { return []string{"low", "medium", "high"} }

// CheckPostTime validates a post time and its source: both set or both empty, the source one of id, api or manual, and the time not in the
// future (now is passed in, so the check is deterministic).
func CheckPostTime(t *time.Time, source string, now time.Time) error {
	if (t == nil) != (source == "") {
		return errors.New("posted_at and posted_at_source are set together")
	}
	if t == nil {
		return nil
	}
	if !slices.Contains([]string{"id", "api", "manual"}, source) {
		return fmt.Errorf("posted_at_source %q is not id, api or manual", source)
	}
	if t.After(now.Add(Day)) {
		return errors.New("posted_at is in the future")
	}
	return nil
}
