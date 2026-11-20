// Package oembed resolves TikTok post links through TikTok's own public oEmbed endpoint (https://www.tiktok.com/oembed), the embed tool
// TikTok provides (developers.tiktok.com/docs/en/embed-videos), which its terms allow. It reads metadata only: the caption (title), the
// author's handle and name and the thumbnail's link. It never fetches the thumbnail, the video or the embed's script, and it keeps none
// of the embed HTML.
//
// It is a polite client: one request a second at most (RESEARCH_OEMBED_INTERVAL_MS can make it slower, never faster), a timeout, a cache
// keyed by the post link, a User-Agent that names the product and nothing about a person, and a 429 stops the caller (ErrRateLimited).
//
// Modes. Live calls the endpoint. Live with a fixture directory (record) reads a fixture when there is one (the disk cache) and writes
// one for each new answer. Replay reads fixtures only and makes no network call; a missing fixture is an error. Every fixture is metadata
// only and passes research.CheckFixture before it is written and after it is read.
package oembed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nilpntr/content-fabrique/internal/research"
)

// Endpoint is TikTok's public oEmbed endpoint. It is the only host the client calls.
const Endpoint = "https://www.tiktok.com/oembed"

// UserAgent names the product generically: no person, no e-mail address.
const UserAgent = "ContentFabrique-Research/1.0 (oEmbed metadata client; one request a second)"

// Environment variables (listed in .env.example).
const (
	EnvInterval = "RESEARCH_OEMBED_INTERVAL_MS"
	EnvTimeout  = "RESEARCH_OEMBED_TIMEOUT_S"
)

// Defaults and limits.
const (
	// MinInterval is the cap: one request a second. A setting can make the client slower, never faster.
	MinInterval     = time.Second
	MaxInterval     = time.Minute
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 60 * time.Second
	maxResponseSize = 256 << 10
)

// Mode is how the client gets an answer.
type Mode string

// The modes.
const (
	Live   Mode = "live"
	Replay Mode = "replay"
)

// Errors.
var (
	// ErrRateLimited is TikTok's 429: the caller stops the run (design note section 7).
	ErrRateLimited = errors.New("oembed: TikTok answered 429 Too Many Requests; stopping")
	// ErrUnavailable is a post oEmbed does not show (removed, private, age-gated or not a video): a 400 or 404.
	ErrUnavailable = errors.New("oembed: TikTok does not embed this post (removed, private or not public)")
	// ErrNoFixture is a replay without a fixture for the link.
	ErrNoFixture = errors.New("oembed: no recorded fixture for this post (replay makes no network call)")
)

// Config configures a Client. The zero value is a live client at one request a second with a 10 second timeout and no fixture
// directory (an in-memory cache only).
type Config struct {
	Mode Mode
	// Dir is the fixture directory: required for replay; for live it is the disk cache and where new answers are recorded.
	Dir string
	// Interval is the least time between two requests; it is raised to MinInterval when lower.
	Interval time.Duration
	// Timeout of one request; zero is DefaultTimeout.
	Timeout time.Duration

	// For tests: the HTTP transport (no test reaches the network), the endpoint, the clock and the sleep.
	Transport http.RoundTripper
	endpoint  string
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// LoadConfig reads RESEARCH_OEMBED_INTERVAL_MS (1000 to 60000, default 1000) and RESEARCH_OEMBED_TIMEOUT_S (1 to 60, default 10). An
// empty variable keeps the default; a value out of range is an error, not a clamp.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{Mode: Live, Interval: MinInterval, Timeout: DefaultTimeout}
	var errs []error
	if v := strings.TrimSpace(getenv(EnvInterval)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < int(MinInterval/time.Millisecond) || n > int(MaxInterval/time.Millisecond) {
			errs = append(errs, fmt.Errorf("%s=%q: milliseconds from 1000 (one request a second, the cap) to 60000", EnvInterval, v))
		} else {
			c.Interval = time.Duration(n) * time.Millisecond
		}
	}
	if v := strings.TrimSpace(getenv(EnvTimeout)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > int(MaxTimeout/time.Second) {
			errs = append(errs, fmt.Errorf("%s=%q: seconds from 1 to 60", EnvTimeout, v))
		} else {
			c.Timeout = time.Duration(n) * time.Second
		}
	}
	return c, errors.Join(errs...)
}

// Result is what oEmbed says about a post: metadata only.
type Result struct {
	// URL is the canonical post link that was resolved.
	URL string
	// Handle is the author's handle (lower-case, no @), from author_unique_id or the author's link.
	Handle     string
	AuthorName string
	// Title is the post's caption. It is the creator's text: untrusted, never shown to a model undelimited and never copied into the
	// playbook (the verbatim check).
	Title string
	// ThumbnailURL is the link TikTok gives for the post's picture. It is metadata only: never fetched, never stored in the database.
	ThumbnailURL string
	// RawRef is what research_evidence.raw_ref keeps instead of the response: the hex SHA-256 of the answer's stable metadata (the
	// post id, the author, the caption). The thumbnail link is left out: TikTok signs it with an expiry, so it changes on every request,
	// and the same answer must give the same RawRef for a second import to store nothing twice.
	RawRef string
	// Cached says the answer came from the memory cache or a fixture, not from the network.
	Cached bool

	externalID string // the cache key: the post id
}

// Fixture is what is recorded for one post: the request and the metadata fields of the answer. The embed HTML and every field not
// listed are dropped.
type Fixture struct {
	RequestURL string   `json:"request_url"`
	Response   Response `json:"response"`
}

// Response holds the oEmbed fields kept.
type Response struct {
	Version         string `json:"version,omitempty"`
	Type            string `json:"type,omitempty"`
	Title           string `json:"title"`
	AuthorName      string `json:"author_name,omitempty"`
	AuthorUniqueID  string `json:"author_unique_id,omitempty"`
	AuthorURL       string `json:"author_url,omitempty"`
	ProviderName    string `json:"provider_name,omitempty"`
	ThumbnailURL    string `json:"thumbnail_url,omitempty"`
	ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
	ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
	EmbedProductID  string `json:"embed_product_id,omitempty"`
}

// Client resolves post links. It is safe for concurrent use; requests are serialized by the rate limit.
type Client struct {
	cfg  Config
	http *http.Client

	mu    sync.Mutex // guards cache and last
	cache map[string]Result
	last  time.Time
	gate  sync.Mutex // one request at a time
}

// New returns a client. Replay needs a fixture directory.
func New(cfg Config) (*Client, error) {
	if cfg.Mode == "" {
		cfg.Mode = Live
	}
	if cfg.Mode != Live && cfg.Mode != Replay {
		return nil, fmt.Errorf("oembed: unknown mode %q (live or replay)", cfg.Mode)
	}
	if cfg.Mode == Replay && cfg.Dir == "" {
		return nil, errors.New("oembed: replay needs a fixture directory")
	}
	if cfg.Interval < MinInterval {
		cfg.Interval = MinInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.endpoint == "" {
		cfg.endpoint = Endpoint
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.sleep == nil {
		cfg.sleep = sleepCtx
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	endpointHost := mustHost(cfg.endpoint)
	hc := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		// A redirect may only stay on the endpoint's host: the client never follows a link elsewhere (a CDN, a media file).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Host != endpointHost || req.URL.Scheme != "https" {
				return errors.New("oembed: refused a redirect away from the oEmbed endpoint")
			}
			return nil
		},
	}
	return &Client{cfg: cfg, http: hc, cache: map[string]Result{}}, nil
}

func mustHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Mode returns the client's mode.
func (c *Client) Mode() Mode { return c.cfg.Mode }

// FixtureName is the file a post's fixture is recorded in.
func FixtureName(ref research.PostRef) string { return "tiktok_" + ref.ExternalID + ".json" }

// Resolve returns oEmbed's metadata for a TikTok post link. A link that is not a TikTok post page is refused before any call.
func (c *Client) Resolve(ctx context.Context, link string) (Result, error) {
	ref, err := research.ParsePostURL(link)
	if err != nil {
		return Result{}, err
	}
	if ref.Platform != research.TikTok {
		return Result{}, fmt.Errorf("oembed: only TikTok links are resolved, not %s", ref.Platform)
	}
	c.mu.Lock()
	if r, ok := c.cache[ref.ExternalID]; ok {
		c.mu.Unlock()
		r.Cached = true
		return r, nil
	}
	c.mu.Unlock()

	if c.cfg.Dir != "" {
		r, err := c.readFixture(ref)
		switch {
		case err == nil:
			c.remember(r)
			r.Cached = true
			return r, nil
		case !errors.Is(err, os.ErrNotExist):
			return Result{}, err
		case c.cfg.Mode == Replay:
			return Result{}, fmt.Errorf("%w: %s", ErrNoFixture, FixtureName(ref))
		}
	}
	fx, err := c.fetch(ctx, ref)
	if err != nil {
		return Result{}, err
	}
	raw, err := encodeFixture(ref, fx)
	if err != nil {
		return Result{}, err
	}
	r, err := toResult(ref, fx)
	if err != nil {
		return Result{}, err
	}
	if c.cfg.Dir != "" {
		if err := c.writeFixture(ref, raw); err != nil {
			return Result{}, err
		}
	}
	c.remember(r)
	return r, nil
}

func (c *Client) remember(r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[r.externalID] = r
}

// wait holds the caller until the interval since the last request has passed.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	next := c.last.Add(c.cfg.Interval)
	c.mu.Unlock()
	if d := next.Sub(c.cfg.now()); d > 0 {
		if err := c.cfg.sleep(ctx, d); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.last = c.cfg.now()
	c.mu.Unlock()
	return nil
}

func (c *Client) fetch(ctx context.Context, ref research.PostRef) (Fixture, error) {
	c.gate.Lock()
	defer c.gate.Unlock()
	if err := c.wait(ctx); err != nil {
		return Fixture{}, err
	}
	reqURL := c.cfg.endpoint + "?url=" + url.QueryEscape(ref.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Fixture{}, fmt.Errorf("oembed: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Fixture{}, fmt.Errorf("oembed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return Fixture{}, fmt.Errorf("oembed: reading the answer: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return Fixture{}, ErrRateLimited
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
		return Fixture{}, fmt.Errorf("%w: %s", ErrUnavailable, ref.URL)
	case resp.StatusCode != http.StatusOK:
		return Fixture{}, fmt.Errorf("oembed: TikTok answered %d for %s", resp.StatusCode, ref.URL)
	case len(body) > maxResponseSize:
		return Fixture{}, fmt.Errorf("oembed: the answer is over %d bytes", maxResponseSize)
	}
	var full Response
	if err := json.Unmarshal(body, &full); err != nil {
		return Fixture{}, fmt.Errorf("oembed: the answer is not oEmbed JSON: %w", err)
	}
	// Only the fields of Response survive: the embed HTML (with its script) and anything else TikTok adds are dropped here.
	return Fixture{RequestURL: ref.URL, Response: full}, nil
}

// toResult checks a fixture's answer against the link: the same post (by its id: a handle's case in a link does not make another post),
// a video, by the creator in the link.
func toResult(ref research.PostRef, fx Fixture) (Result, error) {
	if req, err := research.ParsePostURL(fx.RequestURL); err != nil || req.Platform != ref.Platform || req.ExternalID != ref.ExternalID {
		return Result{}, fmt.Errorf("oembed: the fixture is for %s, not %s", fx.RequestURL, ref.URL)
	}
	r := fx.Response
	if r.Type != "" && r.Type != "video" {
		return Result{}, fmt.Errorf("%w: type %q", ErrUnavailable, r.Type)
	}
	handle := r.AuthorUniqueID
	if handle == "" && r.AuthorURL != "" {
		if u, err := url.Parse(r.AuthorURL); err == nil {
			handle = strings.TrimPrefix(strings.Trim(u.Path, "/"), "@")
		}
	}
	h, err := research.NormalizeHandle(handle)
	if err != nil {
		return Result{}, fmt.Errorf("oembed: the answer names no author: %w", err)
	}
	if h != ref.Handle {
		return Result{}, fmt.Errorf("oembed: the post is by @%s, but the link names @%s", h, ref.Handle)
	}
	stable, _ := json.Marshal([]string{string(ref.Platform), ref.ExternalID, h, r.AuthorName, r.Title})
	sum := sha256.Sum256(stable)
	return Result{
		URL: ref.URL, Handle: h, AuthorName: r.AuthorName, Title: r.Title, ThumbnailURL: r.ThumbnailURL, RawRef: hex.EncodeToString(sum[:]),
		externalID: ref.ExternalID,
	}, nil
}

func (c *Client) fixturePath(ref research.PostRef) string {
	return filepath.Join(c.cfg.Dir, FixtureName(ref))
}

func (c *Client) readFixture(ref research.PostRef) (Result, error) {
	raw, err := os.ReadFile(c.fixturePath(ref))
	if err != nil {
		return Result{}, err
	}
	if err := research.CheckFixture(raw); err != nil {
		return Result{}, fmt.Errorf("oembed: fixture %s: %w", FixtureName(ref), err)
	}
	var fx Fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return Result{}, fmt.Errorf("oembed: fixture %s: %w", FixtureName(ref), err)
	}
	r, err := toResult(ref, fx)
	if err != nil {
		return Result{}, fmt.Errorf("fixture %s: %w", FixtureName(ref), err)
	}
	return r, nil
}

// encodeFixture renders a fixture and runs the media guard on it. It runs on every live answer, recorded or not, so live and replay accept
// the same answers: one with media is refused before it is used or written.
func encodeFixture(ref research.PostRef, fx Fixture) ([]byte, error) {
	raw, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("oembed: %w", err)
	}
	if err := research.CheckFixture(raw); err != nil {
		return nil, fmt.Errorf("oembed: refused the answer for %s: %w", FixtureName(ref), err)
	}
	return append(raw, '\n'), nil
}

func (c *Client) writeFixture(ref research.PostRef, raw []byte) error {
	if err := os.MkdirAll(c.cfg.Dir, 0o750); err != nil {
		return fmt.Errorf("oembed: %w", err)
	}
	tmp, err := os.CreateTemp(c.cfg.Dir, ".oembed-*.json")
	if err != nil {
		return fmt.Errorf("oembed: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("oembed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("oembed: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.fixturePath(ref)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("oembed: %w", err)
	}
	return nil
}
