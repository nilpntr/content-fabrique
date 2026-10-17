package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// testKey looks like a real key and appears nowhere it should not: tests scan logs, errors and cassettes for it.
const testKey = "sk-ant-api03-TESTKEY-do-not-leak-0123456789abcdef"

// testToken stands in for CF_AIG_TOKEN.
const testToken = "aig-token-do-not-leak-fedcba9876543210"

var (
	pricingFS = os.DirFS("../../../config/pricing")
	routingFS = os.DirFS("../../../config/routing")
)

// asMap and asSlice read decoded JSON: a wrong shape gives nil, which the test's comparison then fails on.
func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asSlice(v any) []any        { s, _ := v.([]any); return s }

// asMetered unwraps the Client the factory returns.
func asMetered(t testing.TB, c Client) *metered {
	t.Helper()
	m, ok := c.(*metered)
	if !ok {
		t.Fatalf("client is %T, want *metered", c)
	}
	return m
}

func newWorkspaceID() ids.WorkspaceID { return ids.WorkspaceID{UUID: uuid.New()} }

// ---- a fake Anthropic API behind an http.RoundTripper --------------------------------------------------------

type recordedCall struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func (c recordedCall) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, c.Body)
	}
	return m
}

type fakeReply struct {
	Status int
	Header map[string]string
	Body   string
}

type fakeAPI struct {
	mu    sync.Mutex
	calls []recordedCall
	// messages answers the n-th (1-based) POST /v1/messages. Default: a short text reply with defaultUsage.
	messages func(n int, c recordedCall) fakeReply
	// countTokens answers POST /v1/messages/count_tokens. Default: 1000 tokens.
	countTokens func(n int, c recordedCall) fakeReply
	nMessages   int
	nCount      int
	reqID       int
}

func (f *fakeAPI) client() *http.Client { return &http.Client{Transport: f} }

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	c := recordedCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var rep fakeReply
	var handler func() fakeReply // run outside the lock: a handler may block
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
		f.nCount++
		n, h := f.nCount, f.countTokens
		handler = func() fakeReply {
			if h != nil {
				return h(n, c)
			}
			return fakeReply{Status: 200, Body: `{"input_tokens": 1000}`}
		}
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.nMessages++
		n, h := f.nMessages, f.messages
		handler = func() fakeReply {
			if h != nil {
				return h(n, c)
			}
			return okReply("hello", defaultUsage)
		}
	default:
		rep = fakeReply{Status: 404, Body: `{"type":"error","error":{"type":"not_found_error","message":"no such route"}}`}
	}
	f.reqID++
	reqID := fmt.Sprintf("req_fake_%d", f.reqID)
	f.mu.Unlock()
	if handler != nil {
		rep = handler()
	}

	h := http.Header{"Content-Type": {"application/json"}, "Request-Id": {reqID}}
	for k, v := range rep.Header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: rep.Status, Status: http.StatusText(rep.Status), Header: h, Body: io.NopCloser(strings.NewReader(rep.Body)), Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
}

func (f *fakeAPI) callsTo(suffix string) []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedCall
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) messageCalls() []recordedCall { return f.callsTo("/v1/messages") }
func (f *fakeAPI) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type apiUsage struct {
	Input, CacheWrite5m, CacheWrite1h, CacheRead, Output int64
}

var defaultUsage = apiUsage{Input: 1000, Output: 200}

func okReply(text string, u apiUsage) fakeReply {
	return fakeReply{Status: 200, Body: messageJSON(`[{"type":"text","text":`+jsonString(text)+`}]`, "end_turn", u)}
}

func messageJSON(content, stop string, u apiUsage) string {
	return fmt.Sprintf(`{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":%s,"stop_reason":%q,"stop_sequence":null,`+
		`"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d},"output_tokens":%d}}`,
		content, stop, u.Input, u.CacheWrite5m+u.CacheWrite1h, u.CacheRead, u.CacheWrite5m, u.CacheWrite1h, u.Output)
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func errReply(status int, typ, msg string, header map[string]string) fakeReply {
	return fakeReply{Status: status, Header: header, Body: fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, typ, msg)}
}

// ---- a fake ledger -------------------------------------------------------------------------------------------

type fakeLedger struct {
	mu   sync.Mutex
	rows []store.UsageEntry
	// failInserts makes the next n InsertUsage calls fail.
	failInserts int
	failSum     bool
	inserts     int
}

func (l *fakeLedger) InsertUsage(_ context.Context, e store.UsageEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inserts++
	if l.failInserts > 0 {
		l.failInserts--
		return fmt.Errorf("ledger down")
	}
	if e.RequestID != "" {
		for _, r := range l.rows {
			if r.RequestID == e.RequestID {
				return nil
			}
		}
	}
	l.rows = append(l.rows, e)
	return nil
}

func (l *fakeLedger) UsageCostMicros(_ context.Context, ws ids.WorkspaceID, from, to time.Time) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failSum {
		return 0, fmt.Errorf("ledger down")
	}
	var sum int64
	for _, r := range l.rows {
		if r.WorkspaceID == ws && !r.CreatedAt.Before(from) && r.CreatedAt.Before(to) {
			sum += r.ListCostMicros
		}
	}
	return sum, nil
}

func (l *fakeLedger) snapshot() []store.UsageEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]store.UsageEntry(nil), l.rows...)
}

// ---- a factory over the fakes --------------------------------------------------------------------------------

// env returns a getenv over a map.
func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type rig struct {
	api     *fakeAPI
	ledger  *fakeLedger
	factory *Factory
	logs    *bytes.Buffer
	sleeps  *[]time.Duration
	now     *time.Time
}

// newRig builds a Factory over a fake API and ledger. vars override the defaults (CF_ENV=local, a test key).
func newRig(t *testing.T, vars map[string]string, mutate ...func(*FactoryOptions)) *rig {
	t.Helper()
	m := map[string]string{"CF_ENV": "local", "ANTHROPIC_API_KEY": testKey, "ROUTING_PROFILE": "dev-cheap"}
	for k, v := range vars {
		m[k] = v
	}
	settings, err := LoadSettings(env(m))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	prices, err := LoadPriceTable(pricingFS, PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{api: &fakeAPI{}, ledger: &fakeLedger{}, logs: &bytes.Buffer{}, sleeps: new([]time.Duration)}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	r.now = &now
	var smu sync.Mutex
	opts := FactoryOptions{
		Settings: settings, Getenv: env(m), Prices: prices, Ledger: r.ledger, HTTPClient: r.api.client(),
		Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		retry: retryPolicy{
			Rand: func() float64 { return 0.5 },
			Now:  func() time.Time { smu.Lock(); defer smu.Unlock(); return now },
			Sleep: func(ctx context.Context, d time.Duration) error {
				smu.Lock()
				*r.sleeps = append(*r.sleeps, d)
				now = now.Add(d)
				smu.Unlock()
				return ctx.Err()
			},
		},
		now: func() time.Time { smu.Lock(); defer smu.Unlock(); return now },
	}
	for _, f := range mutate {
		f(&opts)
	}
	r.factory, err = NewFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) client(t *testing.T, ws ids.WorkspaceID, stage string) (Client, Billing) {
	t.Helper()
	c, b, err := r.factory.For(context.Background(), ws, stage)
	if err != nil {
		t.Fatal(err)
	}
	return c, b
}

// simpleRequest is a small valid request on Haiku.
func simpleRequest() Request {
	return Request{
		Model: "claude-haiku-5-5", MaxTokens: 1000,
		Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hello there")}}},
	}
}

// fixedBudget is a Budget over a plain number.
type fixedBudget struct {
	mu     sync.Mutex
	left   int64
	spends []int64
}

func (b *fixedBudget) Remaining() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.left }
func (b *fixedBudget) Spend(m int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.left -= m
	b.spends = append(b.spends, m)
}

func mustNotContain(t *testing.T, what, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("%s contains a secret (%q...)", what, n[:8])
		}
	}
}
