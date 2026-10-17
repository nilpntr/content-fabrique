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
