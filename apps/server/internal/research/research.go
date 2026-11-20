// Package research holds the rules of the creator research engine (docs/05-decisions.md D-41): which sources may write evidence, what an
// observation may contain, how long it is kept and what a post link looks like.
//
// It is pure: no network, no database, no clock. The store (package store, research.go) applies these rules before every write, and the
// database repeats the important ones as CHECK constraints (migration 00027), so a bug here cannot let media or an unknown source in.
//
// The compliance rules, in one place:
//   - Public data only. Nothing here logs in to anyone's account or reads a private page.
//   - We store links, metadata, short notes in our own words and our own derived numbers. Never a video file, a media URL or a transcript.
//   - The sources are a fixed list (Sources). A source that is not in it cannot write evidence.
//   - Every observation has a retention time; the sweeper deletes what is past it.
//   - A creator is removed on request (store.ForgetResearchCreator, `cfctl research forget`).
package research

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Platform is a social platform a creator or post lives on.
type Platform string

// The platforms. TikTok is the only publishing target (D-25); Instagram and YouTube are research sources only ("learning, not publishing").
const (
	TikTok    Platform = "tiktok"
	Instagram Platform = "instagram"
	YouTube   Platform = "youtube"
)

// Platforms lists the platforms, in a fixed order.
func Platforms() []Platform { return []Platform{TikTok, Instagram, YouTube} }

// ParsePlatform returns the platform named s (case and surrounding spaces ignored).
func ParsePlatform(s string) (Platform, error) {
	p := Platform(strings.ToLower(strings.TrimSpace(s)))
	if !slices.Contains(Platforms(), p) {
		return "", fmt.Errorf("unknown platform %q (one of tiktok, instagram, youtube)", s)
	}
	return p, nil
}

// Source is where an observation came from. The list is closed: see Sources.
type Source string

// The sources of evidence (the allow-list). Adding one is a change to this file, to the CHECK in migration 00027 and to the design note.
const (
	// SourceOEmbed is TikTok's own oEmbed endpoint: the caption, the author and the embed. No counts.
	SourceOEmbed Source = "oembed"
	// SourceWatchSheet is a person who watched a public post and typed in what a viewer sees: the counts and the hook.
	SourceWatchSheet Source = "watch_sheet"
	// SourceWeb is a public web page found by search (a roundup, a ranking page): facts and links, never copied text.
	SourceWeb Source = "web"
	// SourceIGBusinessDiscovery is the Instagram Graph API's Business Discovery for a known professional account (phase R6).
	SourceIGBusinessDiscovery Source = "ig_business_discovery"
	// SourceYouTubeAPI is the YouTube Data API v3 (phase R6). Its data may be kept 30 days at most.
	SourceYouTubeAPI Source = "youtube_api"
	// SourceCreatorOAuth is a creator's own analytics, read with their consent.
	SourceCreatorOAuth Source = "creator_oauth"
)

// Sources lists the allowed sources, in a fixed order.
func Sources() []Source {
	return []Source{SourceOEmbed, SourceWatchSheet, SourceWeb, SourceIGBusinessDiscovery, SourceYouTubeAPI, SourceCreatorOAuth}
}

// ParseSource returns the source named s, or an error for anything that is not on the list.
func ParseSource(s string) (Source, error) {
	src := Source(s)
	if !slices.Contains(Sources(), src) {
		return "", fmt.Errorf("research: %q is not an allowed source", s)
	}
	return src, nil
}

// Method is how a source was read: by a program calling an API or by a person.
type Method string

// The methods.
const (
	MethodAPI    Method = "api"
	MethodManual Method = "manual"
)

// ParseMethod returns the method named s.
func ParseMethod(s string) (Method, error) {
	m := Method(s)
	if m != MethodAPI && m != MethodManual {
		return "", fmt.Errorf("research: %q is not a method (api or manual)", s)
	}
	return m, nil
}

// Methods returns the methods the source may be read with. A person fills the watch sheet (a platform's terms forbid automating that
// reading), the platform sources are API calls and a web search may be either.
func (s Source) Methods() []Method {
	switch s {
	case SourceWatchSheet:
		return []Method{MethodManual}
	case SourceWeb:
		return []Method{MethodAPI, MethodManual}
	case SourceOEmbed, SourceIGBusinessDiscovery, SourceYouTubeAPI, SourceCreatorOAuth:
		return []Method{MethodAPI}
	}
	return nil
}

// Metric names: the only keys an observation's metrics may have.
const (
	MetricViews    = "views"
	MetricLikes    = "likes"
	MetricComments = "comments"
	MetricSaves    = "saves"
	MetricShares   = "shares"
)

// Metrics lists the allowed metric names.
func Metrics() []string {
	return []string{MetricViews, MetricLikes, MetricComments, MetricSaves, MetricShares}
}

// Metrics returns the metrics the source gives, and so the only ones an observation from it may carry ("only what the source gives"):
// oEmbed gives none, the Instagram and YouTube APIs no saves or shares, and a creator's own analytics no saves.
func (s Source) Metrics() []string {
	switch s {
	case SourceWatchSheet:
		return Metrics()
	case SourceWeb, SourceIGBusinessDiscovery, SourceYouTubeAPI:
		return []string{MetricViews, MetricLikes, MetricComments}
	case SourceCreatorOAuth:
		return []string{MetricViews, MetricLikes, MetricComments, MetricShares}
	}
	return nil
}

// Platforms returns the platforms the source can observe.
func (s Source) Platforms() []Platform {
	switch s {
	case SourceOEmbed:
		return []Platform{TikTok}
	case SourceIGBusinessDiscovery:
		return []Platform{Instagram}
	case SourceYouTubeAPI:
		return []Platform{YouTube}
	}
	return Platforms()
}

// Environment variables of the retention (listed in .env.example).
const (
	EnvRetentionDays        = "RESEARCH_RETENTION_DAYS"
	EnvYouTubeRetentionDays = "RESEARCH_YOUTUBE_RETENTION_DAYS"
)

// Retention limits and defaults, in days. The default of 12 months is the design note's; it waits for the founder's confirmation (legal
// item P0-12), which is why it is a setting.
const (
	DefaultRetentionDays = 365
	MaxRetentionDays     = 730
	// YouTubeRetentionDays is the most the YouTube API terms allow without a refresh: data is refreshed or deleted within 30 days. The
	// database enforces it too, so the setting can lower it but never raise it.
	YouTubeRetentionDays = 30
)

// Retention is how long an observation is kept.
type Retention struct {
	// Default is for every source except YouTube.
	Default time.Duration
	// YouTube is for the YouTube API (at most 30 days).
	YouTube time.Duration
}

// DefaultRetention is 12 months, and 30 days for YouTube.
func DefaultRetention() Retention {
	return Retention{Default: DefaultRetentionDays * Day, YouTube: YouTubeRetentionDays * Day}
}

// Day is 24 hours.
const Day = 24 * time.Hour

// LoadRetention reads RESEARCH_RETENTION_DAYS (1 to 730, default 365) and RESEARCH_YOUTUBE_RETENTION_DAYS (1 to 30, default 30).
// An unset or empty variable keeps the default; a value out of range is an error, not a clamp.
func LoadRetention(getenv func(string) string) (Retention, error) {
	r := DefaultRetention()
	var errs []error
	read := func(name string, limit int, into *time.Duration) {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > limit {
			errs = append(errs, fmt.Errorf("%s=%q: a whole number of days from 1 to %d", name, v, limit))
			return
		}
		*into = time.Duration(n) * Day
	}
	read(EnvRetentionDays, MaxRetentionDays, &r.Default)
	read(EnvYouTubeRetentionDays, YouTubeRetentionDays, &r.YouTube)
	return r, errors.Join(errs...)
}

// For returns how long an observation from the source is kept.
func (r Retention) For(s Source) time.Duration {
	if s == SourceYouTubeAPI {
		return r.YouTube
	}
	return r.Default
}

// RetainUntil is the time an observation made at observed from the source must be deleted at the latest.
func (r Retention) RetainUntil(s Source, observed time.Time) time.Time {
	return observed.Add(r.For(s))
}

// Valid reports whether the retention is inside the limits.
func (r Retention) Valid() error {
	if r.Default < Day || r.Default > MaxRetentionDays*Day {
		return fmt.Errorf("research: retention %s is outside 1 to %d days", r.Default, MaxRetentionDays)
	}
	if r.YouTube < Day || r.YouTube > YouTubeRetentionDays*Day {
		return fmt.Errorf("research: YouTube retention %s is outside 1 to %d days", r.YouTube, YouTubeRetentionDays)
	}
	return nil
}
