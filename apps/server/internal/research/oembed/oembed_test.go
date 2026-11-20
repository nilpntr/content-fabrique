package oembed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/research"
)

// No test here reaches the network: every request goes to fakeTikTok, a RoundTripper that answers like the oEmbed endpoint.

const (
	postURL  = "https://www.tiktok.com/@synthetic_lifter_a/video/7657610837863983488"
	fixtures = "../testdata/r2/oembed"
)

type fakeTikTok struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
	body     string
}

func (f *fakeTikTok) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	status, body := f.status, f.body
	if status == 0 {
		status = http.StatusOK
	}
	if body == "" {
		body = answer("synthetic_lifter_a", "https://p16-sign-va.tiktokcdn.com/obj/synthetic~tplv-noop.image")
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
}

func (f *fakeTikTok) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func answer(handle, thumb string) string {
	b, _ := json.Marshal(map[string]any{
		"version": "1.0", "type": "video", "title": "Back day the slow way #gym #backday", "author_name": "Synthetic Lifter",
		"author_unique_id": handle, "author_url": "https://www.tiktok.com/@" + handle, "thumbnail_url": thumb, "provider_name": "TikTok",
		"html": `<blockquote class="tiktok-embed" cite="x"></blockquote> <script async src="https://www.tiktok.com/embed.js"></script>`,
	})
	return string(b)
}

// fakeClock records the waits instead of sleeping.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.t = c.t.Add(d)
	return nil
}

func newLive(t *testing.T, tt *fakeTikTok, dir string) (*Client, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	c, err := New(Config{Mode: Live, Dir: dir, Transport: tt, endpoint: "https://oembed.test/oembed", now: clock.now, sleep: clock.sleep})
	if err != nil {
		t.Fatal(err)
	}
	return c, clock
}

func TestLiveRecordsMetadataOnly(t *testing.T) {
	tt := &fakeTikTok{}
	dir := t.TempDir()
	c, _ := newLive(t, tt, dir)
	r, err := c.Resolve(context.Background(), postURL+"?is_from_webapp=1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Handle != "synthetic_lifter_a" || r.Title == "" || r.ThumbnailURL == "" || len(r.RawRef) != 64 || r.Cached {
		t.Fatalf("result: %+v", r)
	}
	req := tt.requests[0]
	if req.Method != http.MethodGet || req.URL.Query().Get("url") != postURL || req.Header.Get("User-Agent") != UserAgent {
		t.Fatalf("request: %s %s %v", req.Method, req.URL, req.Header)
	}
	// The User-Agent names the product, never a person.
	if strings.Contains(UserAgent, "@") || !strings.Contains(UserAgent, "ContentFabrique") {
		t.Errorf("user agent %q", UserAgent)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tiktok_7657610837863983488.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "embed.js") || strings.Contains(string(raw), "blockquote") || strings.Contains(string(raw), `"html"`) {
		t.Errorf("the embed HTML was recorded:\n%s", raw)
	}
	if err := research.CheckFixture(raw); err != nil {
		t.Errorf("recorded fixture fails the guard: %v", err)
	}

	// The memory cache answers the same link again; a new client reads the fixture (the disk cache). Neither calls TikTok.
	if r2, err := c.Resolve(context.Background(), postURL); err != nil || !r2.Cached || tt.count() != 1 {
		t.Fatalf("memory cache: %+v %v, %d requests", r2, err, tt.count())
	}
	c2, _ := newLive(t, tt, dir)
	if r3, err := c2.Resolve(context.Background(), postURL); err != nil || !r3.Cached || r3.RawRef != r.RawRef || tt.count() != 1 {
		t.Fatalf("disk cache: %+v %v, %d requests", r3, err, tt.count())
	}
}

func TestOneRequestASecond(t *testing.T) {
	tt := &fakeTikTok{}
	c, clock := newLive(t, tt, "")
	for i, id := range []string{"7657610837863983488", "7657610837863983489", "7657610837863983490"} {
		if _, err := c.Resolve(context.Background(), "https://www.tiktok.com/@synthetic_lifter_a/video/"+id); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if len(clock.waits) != 2 || clock.waits[0] != time.Second || clock.waits[1] != time.Second {
		t.Fatalf("waits = %v, want two of 1s", clock.waits)
	}
	// A setting cannot make it faster.
	fast, err := New(Config{Interval: time.Millisecond})
	if err != nil || fast.cfg.Interval != MinInterval {
		t.Fatalf("interval = %v %v", fast.cfg.Interval, err)
	}
}

func TestRefusals(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		status int
		body   string
		want   error
		text   string
	}{
		"429 stops the run":          {status: 429, want: ErrRateLimited},
		"a removed post":             {status: 404, want: ErrUnavailable},
		"a server error":             {status: 503, text: "503"},
		"another author":             {body: answer("someone_else", ""), text: "@someone_else"},
		"a photo post":               {body: `{"type":"photo","author_unique_id":"synthetic_lifter_a","title":"x"}`, want: ErrUnavailable},
		"a video link in the answer": {body: answer("synthetic_lifter_a", "https://v16-webapp.tiktok.com/video/tos/x"), want: research.ErrMedia},
		"not JSON":                   {body: "<html>", text: "not oEmbed JSON"},
	} {
		tt := &fakeTikTok{status: c.status, body: c.body}
		dir := t.TempDir()
		cl, _ := newLive(t, tt, dir)
		_, err := cl.Resolve(ctx, postURL)
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) || (c.text != "" && !strings.Contains(err.Error(), c.text)) {
			t.Errorf("%s: err = %v", name, err)
		}
		if files, _ := os.ReadDir(dir); len(files) != 0 {
			t.Errorf("%s: recorded %d file(s)", name, len(files))
		}
	}
	// Links that are not TikTok post pages are refused before any request.
	tt := &fakeTikTok{}
	cl, _ := newLive(t, tt, "")
	for _, link := range []string{"https://www.tiktok.com/@synthetic_lifter_a", "https://www.instagram.com/reel/AbCdEf123/", "https://vm.tiktok.com/ZMabc/"} {
		if _, err := cl.Resolve(ctx, link); err == nil {
			t.Errorf("%s accepted", link)
		}
	}
	if tt.count() != 0 {
		t.Errorf("%d request(s) for refused links", tt.count())
	}
}

func TestRedirectsStayOnTheEndpoint(t *testing.T) {
	c, err := New(Config{endpoint: "https://oembed.test/oembed"})
	if err != nil {
		t.Fatal(err)
	}
	away, _ := http.NewRequest(http.MethodGet, "https://v16-webapp.tiktok.com/x.mp4", nil)
	if err := c.http.CheckRedirect(away, []*http.Request{{}}); err == nil {
		t.Error("a redirect to another host was followed")
	}
	same, _ := http.NewRequest(http.MethodGet, "https://oembed.test/oembed?url=x", nil)
	if err := c.http.CheckRedirect(same, []*http.Request{{}}); err != nil {
		t.Errorf("a redirect on the endpoint: %v", err)
	}
}

func TestReplayReadsTheRecordedFixtures(t *testing.T) {
	tt := &fakeTikTok{}
	c, err := New(Config{Mode: Replay, Dir: fixtures, Transport: tt})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(context.Background(), postURL)
	if err != nil || r.Handle != "synthetic_lifter_a" || !r.Cached {
		t.Fatalf("replay: %+v %v", r, err)
	}
	if _, err := c.Resolve(context.Background(), "https://www.tiktok.com/@synthetic_lifter_a/video/7657610837863983400"); !errors.Is(err, ErrNoFixture) {
		t.Fatalf("missing fixture: %v", err)
	}
	if tt.count() != 0 {
		t.Fatalf("replay made %d request(s)", tt.count())
	}
	if _, err := New(Config{Mode: Replay}); err == nil {
		t.Error("replay without a directory")
	}
	// A fixture with media is refused when it is read (CheckFixture guard).
	dir := t.TempDir()
	bad := `{"request_url":"` + postURL + `","raw_sha256":"` + strings.Repeat("a", 64) + `","response":{"title":"x","author_unique_id":"synthetic_lifter_a","play_addr":"x"}}`
	if err := os.WriteFile(filepath.Join(dir, "tiktok_7657610837863983488.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	c3, _ := New(Config{Mode: Replay, Dir: dir})
	if _, err := c3.Resolve(context.Background(), postURL); !errors.Is(err, research.ErrMedia) {
		t.Fatalf("media fixture: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	c, err := LoadConfig(env(nil))
	if err != nil || c.Interval != time.Second || c.Timeout != DefaultTimeout || c.Mode != Live {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = LoadConfig(env(map[string]string{EnvInterval: "2500", EnvTimeout: "5"}))
	if err != nil || c.Interval != 2500*time.Millisecond || c.Timeout != 5*time.Second {
		t.Fatalf("set: %+v %v", c, err)
	}
	for _, bad := range []map[string]string{{EnvInterval: "500"}, {EnvInterval: "x"}, {EnvTimeout: "0"}, {EnvTimeout: "61"}} {
		if _, err := LoadConfig(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
