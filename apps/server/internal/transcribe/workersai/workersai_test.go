package workersai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/transcribe"
)

func writeWAV(t *testing.T, seconds int) transcribe.Audio {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(p, transcribe.EncodeWAV(make([]int16, seconds*transcribe.SampleRate)), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcribe.Audio{Path: p, DurationMs: int64(seconds) * 1000}
}

// fake is a fake Workers AI: it records the requests and answers with the words the test queues.
type fake struct {
	mu       sync.Mutex
	requests []request
	auth     []string
	paths    []string
	replies  []string
	status   int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req request
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.requests, f.auth, f.paths = append(f.requests, req), append(f.auth, r.Header.Get("Authorization")), append(f.paths, r.URL.Path)
	reply := `{"success":true,"errors":[],"result":{"words":[],"transcription_info":{"language":"en"}}}`
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_, _ = w.Write([]byte(reply))
}

func newProvider(t *testing.T, f *fake, cfg Config) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	if cfg.AccountID == "" {
		cfg.AccountID, cfg.APIToken = "acct", "secret-token"
	}
	return New(cfg)
}

func TestTranscribeCallsTheModelAndMapsWords(t *testing.T) {
	f := &fake{replies: []string{`{"success":true,"errors":[],"result":{"text":"Hello world","words":[
		{"word":" Hello","start":0.52,"end":0.9},{"word":"world.","start":1.0,"end":1.4},{"word":"  ","start":2,"end":2.1}],
		"transcription_info":{"language":"en"}}}`}}
	p := newProvider(t, f, Config{})
	tr, err := p.Transcribe(context.Background(), writeWAV(t, 2), transcribe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Language != "en" || len(tr.Words) != 2 || tr.Words[0].Text != "Hello" || tr.Words[0].StartMs != 520 || tr.Words[1].EndMs != 1400 {
		t.Fatalf("%+v", tr)
	}
	if f.paths[0] != "/accounts/acct/ai/run/@cf/openai/whisper-large-v3-turbo" || f.auth[0] != "Bearer secret-token" {
		t.Errorf("path %q auth %q", f.paths[0], f.auth[0])
	}
	audio, err := base64.StdEncoding.DecodeString(f.requests[0].Audio)
	if err != nil {
		t.Fatal(err)
	}
	if pcm, err := transcribe.ParseWAV(audio); err != nil || len(pcm) != 2*transcribe.SampleRate {
		t.Errorf("the audio sent: %d samples, %v", len(pcm), err)
	}
	if f.requests[0].Task != "transcribe" || f.requests[0].Language != "" {
		t.Errorf("%+v", f.requests[0])
	}
}

func TestLongAudioIsSentInChunksAndTimesShifted(t *testing.T) {
	word := func(s string, a, b float64) string {
		return `{"success":true,"errors":[],"result":{"words":[{"word":"` + s + `","start":` + ftoa(a) + `,"end":` + ftoa(b) + `}],"transcription_info":{"language":"nl"}}}`
	}
	f := &fake{replies: []string{word("een", 0.5, 0.9), word("twee", 0.5, 0.9), word("drie", 0.1, 0.4)}}
	p := newProvider(t, f, Config{ChunkSeconds: 2})
	tr, err := p.Transcribe(context.Background(), writeWAV(t, 5), transcribe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d requests for 5 s in 2 s chunks", len(f.requests))
	}
	if len(tr.Words) != 3 || tr.Words[0].StartMs != 500 || tr.Words[1].StartMs != 2500 || tr.Words[2].StartMs != 4100 || tr.Language != "nl" {
		t.Fatalf("%+v", tr)
	}
	// The language found in the first chunk is asked for in the rest.
	if f.requests[0].Language != "" || f.requests[1].Language != "nl" || f.requests[2].Language != "nl" {
		t.Errorf("%q %q %q", f.requests[0].Language, f.requests[1].Language, f.requests[2].Language)
	}
}

func ftoa(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestNotConfiguredIsSwitchedOffWithoutCallingAnything(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	for name, cfg := range map[string]Config{"nothing": {}, "no token": {AccountID: "a"}, "no account": {APIToken: "t"}} {
		cfg.BaseURL = srv.URL
		p := New(cfg)
		if p.Configured() {
			t.Errorf("%s: configured", name)
		}
		_, err := p.Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{})
		if !errors.Is(err, transcribe.ErrNotConfigured) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if calls != 0 {
		t.Errorf("%d calls to a provider that is switched off", calls)
	}
}

func TestEveryChunkIsACallAndPaidChunksSurviveAFailure(t *testing.T) {
	ok := `{"success":true,"errors":[],"result":{"words":[{"word":"een","start":0.1,"end":0.4}],"transcription_info":{"language":"nl"}}}`
	f := &fake{replies: []string{ok, ok}}
	p := newProvider(t, f, Config{ChunkSeconds: 2})
	tr, err := p.Transcribe(context.Background(), writeWAV(t, 5), transcribe.Options{})
	if err != nil || len(tr.Calls) != 3 || tr.Calls[0].AudioMs != 2000 || tr.Calls[1].AudioMs != 2000 || tr.Calls[2].AudioMs != 1000 {
		t.Fatalf("calls %+v, err %v: the last chunk is a call of its own length", tr.Calls, err)
	}

	// The third request fails: the two that were answered were billed and are reported with the error.
	f = &fake{replies: []string{ok, ok, `{"success":false,"errors":[{"code":5,"message":"overloaded"}]}`}}
	p = newProvider(t, f, Config{ChunkSeconds: 2})
	tr, err = p.Transcribe(context.Background(), writeWAV(t, 5), transcribe.Options{})
	if err == nil || len(tr.Calls) != 2 || len(tr.Words) != 2 {
		t.Fatalf("err %v, calls %+v, words %d", err, tr.Calls, len(tr.Words))
	}
	// A request that is refused with an HTTP error was not billed.
	f = &fake{status: 500}
	tr, err = newProvider(t, f, Config{}).Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{})
	if err == nil || len(tr.Calls) != 0 {
		t.Fatalf("err %v, calls %+v", err, tr.Calls)
	}
}

func TestAHostedProviderWithoutAPriceSendsNothing(t *testing.T) {
	f := &fake{}
	p := newProvider(t, f, Config{RequirePrice: true})
	if p.Configured() {
		t.Error("a provider that requires a price is configured without one")
	}
	if _, err := p.Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{}); !errors.Is(err, transcribe.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("%d requests were sent without a price", len(f.requests))
	}
	// With a price it works, and the price is used.
	p = newProvider(t, f, Config{RequirePrice: true, PriceMicrosPerMinute: 1500})
	if _, err := p.Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{}); err != nil || len(f.requests) != 1 {
		t.Fatalf("%v, %d requests", err, len(f.requests))
	}
	// Local development may run unpriced (no real call is made without an account anyway).
	if !newProvider(t, f, Config{}).Configured() {
		t.Error("an unpriced provider is configured when the price is not required")
	}
}

func TestErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status    int
		unusable  bool
		retryable bool
	}{{400, true, false}, {413, true, false}, {401, false, false}, {429, false, true}, {500, false, true}} {
		f := &fake{status: tc.status, replies: []string{`{"success":false,"errors":[{"code":1,"message":"nope, secret-token"}]}`}}
		p := newProvider(t, f, Config{})
		_, err := p.Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{})
		if err == nil {
			t.Fatalf("%d: no error", tc.status)
		}
		if errors.Is(err, transcribe.ErrUnusableAudio) != tc.unusable {
			t.Errorf("%d: unusable = %v, err %v", tc.status, !tc.unusable, err)
		}
		if errors.Is(err, transcribe.ErrNotConfigured) {
			t.Errorf("%d: a rejected token is not 'not configured'", tc.status)
		}
	}
	// Success false with a 200, and an answer that is not JSON.
	f := &fake{replies: []string{`{"success":false,"errors":[{"code":2,"message":"model busy"}]}`}}
	if _, err := newProvider(t, f, Config{}).Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{}); err == nil || !strings.Contains(err.Error(), "model busy") {
		t.Errorf("%v", err)
	}
	f = &fake{replies: []string{`<html>`}}
	if _, err := newProvider(t, f, Config{}).Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{}); err == nil {
		t.Error("an HTML answer was accepted")
	}
}

func TestTheTokenNeverAppearsInAnError(t *testing.T) {
	p := New(Config{AccountID: "a", APIToken: "secret-token", BaseURL: "http://127.0.0.1:1"}) // nothing listens there
	_, err := p.Transcribe(context.Background(), writeWAV(t, 1), transcribe.Options{})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("%v", err)
	}
}

func TestCostIsUnpricedUntilAPriceIsConfigured(t *testing.T) {
	if c, v := New(Config{}).CostMicros(90_000); c != 0 || v != "unpriced" {
		t.Errorf("%d %s", c, v)
	}
	if c, v := New(Config{PriceMicrosPerMinute: 1000}).CostMicros(90_000); c != 1500 || v != "workers-ai-config" {
		t.Errorf("%d %s", c, v)
	}
	if c, _ := New(Config{PriceMicrosPerMinute: 1000}).CostMicros(1); c != 1 {
		t.Errorf("a fraction of a minute rounds up: %d", c)
	}
	p := New(Config{})
	if p.Name() != "workersai" || p.Model() != "whisper-large-v3-turbo" {
		t.Errorf("%s %s", p.Name(), p.Model())
	}
}

// A chunk whose request went out and got no usable answer may have been billed: the provider reports it as an estimated call, after the
// chunks that were answered. A definite refusal (an error status, a dial failure) is not reported.
func TestChunkWithAnUnknownOutcomeIsReportedAsEstimated(t *testing.T) {
	ok := `{"success":true,"errors":[],"result":{"words":[],"transcription_info":{"language":"en"}}}`
	for name, tc := range map[string]struct {
		handler   func(n int, w http.ResponseWriter)
		client    *http.Client
		estimated bool
	}{
		"timeout": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					time.Sleep(400 * time.Millisecond)
				}
				_, _ = w.Write([]byte(ok))
			},
			client:    &http.Client{Timeout: 100 * time.Millisecond},
			estimated: true,
		},
		"body cut off": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					w.Header().Set("Content-Length", "500")
					_, _ = w.Write([]byte(`{"success":tr`))
					panic(http.ErrAbortHandler)
				}
				_, _ = w.Write([]byte(ok))
			},
			estimated: true,
		},
		"unparseable 200": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					_, _ = w.Write([]byte(`<html>gateway</html>`))
					return
				}
				_, _ = w.Write([]byte(ok))
			},
			estimated: true,
		},
		"server error": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"bad gateway"}]}`))
					return
				}
				_, _ = w.Write([]byte(ok))
			},
		},
		"rejected audio": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"bad audio"}]}`))
					return
				}
				_, _ = w.Write([]byte(ok))
			},
		},
		"success false": {
			handler: func(n int, w http.ResponseWriter) {
				if n == 2 {
					_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9,"message":"no"}]}`))
					return
				}
				_, _ = w.Write([]byte(ok))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				n++
				k := n
				mu.Unlock()
				tc.handler(k, w)
			}))
			defer srv.Close()
			p := New(Config{AccountID: "a", APIToken: "tok", BaseURL: srv.URL, ChunkSeconds: 2, Client: tc.client, PriceMicrosPerMinute: 6000})
			tr, err := p.Transcribe(context.Background(), writeWAV(t, 5), transcribe.Options{})
			if err == nil {
				t.Fatal("the failed chunk was swallowed")
			}
			// Chunk 1 was answered, chunk 2 failed, chunk 3 was never sent.
			want := []transcribe.Call{{AudioMs: 2000}}
			if tc.estimated {
				want = append(want, transcribe.Call{AudioMs: 2000, Estimated: true})
			}
			if len(tr.Calls) != len(want) {
				t.Fatalf("calls = %+v, want %+v", tr.Calls, want)
			}
			for i := range want {
				if tr.Calls[i] != want[i] {
					t.Errorf("call %d = %+v, want %+v", i, tr.Calls[i], want[i])
				}
			}
		})
	}
	// A request that never connected is not billed.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens any more: the dial is refused
	p := New(Config{AccountID: "a", APIToken: "tok", BaseURL: url, ChunkSeconds: 2, PriceMicrosPerMinute: 6000})
	if tr, err := p.Transcribe(context.Background(), writeWAV(t, 3), transcribe.Options{}); err == nil || len(tr.Calls) != 0 {
		t.Errorf("a refused connection: calls %+v, err %v", tr.Calls, err)
	}
}

func TestPrice(t *testing.T) {
	p := New(Config{AccountID: "a", APIToken: "t", PriceMicrosPerMinute: 500, PriceVersion: "2026-10b"})
	if micros, v := p.CostMicros(90_000); micros != 750 || v != "2026-10b" {
		t.Errorf("a minute and a half: %d %s", micros, v)
	}
	if micros, _ := p.CostMicros(1); micros != 1 {
		t.Errorf("rounds up to a micro-dollar: %d", micros)
	}
	if micros, v := New(Config{}).CostMicros(60_000); micros != 0 || v != "unpriced" {
		t.Errorf("unpriced: %d %s", micros, v)
	}
}
