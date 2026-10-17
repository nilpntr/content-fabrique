package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Record and replay of the echo agent (D-22, docs/design/agent-runtime-v1.md section 2).
//
// The cassettes in testdata/cassettes are recorded here, through the real llm layer in record mode, against a fake HTTP
// transport that plays the Anthropic API: no real model is called, ever, and no key is needed. CI runs the loop in replay
// mode over them, so a change to anything that is part of the request (the system prompt, a tool schema, the cache marks,
// the order of messages) makes a replay miss, and TestEchoCassettesAreCurrent says so. Re-record with:
//
//	CF_UPDATE_CASSETTES=1 go test ./internal/agents/runtime -run TestEchoCassettesAreCurrent
//
// (against the fake transport; to record a real conversation, run `LLM_MODE=record cfctl agent run --agent echo ...`).

const (
	cassetteDir     = "testdata/cassettes"
	echoMessage     = "hello cassette"
	echoNoteTool    = "toolu_echo_note"
	echoFinishTool  = "toolu_echo_finish"
	echoCassetteDir = cassetteDir + "/ideation"
)

// fakeAnthropic plays the Messages API for the echo agent: first a note call, then a finish call.
type fakeAnthropic struct {
	mu       sync.Mutex
	messages int
	counts   int
}

func (f *fakeAnthropic) client() *http.Client { return &http.Client{Transport: f} }

func (f *fakeAnthropic) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	body := ""
	f.mu.Lock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/count_tokens"):
		f.counts++
		body = `{"input_tokens": 700}`
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.messages++
		switch f.messages {
		case 1:
			body = anthropicMessage(`{"type":"tool_use","id":"`+echoNoteTool+`","name":"note","input":{"text":"`+echoMessage+`"}}`, 412, 0, 38)
		case 2:
			body = anthropicMessage(`{"type":"tool_use","id":"`+echoFinishTool+`","name":"finish","input":{"summary":"Noted: `+echoMessage+`"}}`, 220, 300, 41)
		default:
			body = anthropicMessage(`{"type":"text","text":"unexpected extra call"}`, 1, 0, 1)
		}
	}
	f.mu.Unlock()
	if body == "" {
		return &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"not_found_error","message":"no route"}}`)), Request: r}, nil
	}
	h := http.Header{"Content-Type": {"application/json"}, "Request-Id": {"req_fake_" + r.URL.Path}}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
}

func anthropicMessage(block string, in, cacheRead, out int) string {
	stop := "tool_use"
	return `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[` + block + `],"stop_reason":"` + stop + `","stop_sequence":null,` +
		`"usage":{"input_tokens":` + itoa(in) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":` + itoa(cacheRead) + `,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":` + itoa(out) + `}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// nopLedger stands in for the usage ledger in the record test.
type nopLedger struct{}

func (nopLedger) InsertUsage(context.Context, store.UsageEntry) error { return nil }
func (nopLedger) UsageCostMicros(context.Context, ids.WorkspaceID, time.Time, time.Time) (int64, error) {
	return 0, nil
}

// llmFactory builds the real llm layer in the given mode over the repository's price table.
func llmFactory(t *testing.T, vars map[string]string, api *fakeAnthropic) *llm.Factory {
	t.Helper()
	vars["CF_ENV"] = "ci"
	getenv := func(k string) string { return vars[k] }
	settings, err := llm.LoadSettings(getenv)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := llm.LoadPriceTable(os.DirFS("../../../config/pricing"), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	opts := llm.FactoryOptions{Settings: settings, Getenv: getenv, Prices: prices, Logger: quiet}
	if api != nil {
		opts.HTTPClient, opts.Ledger = api.client(), nopLedger{}
	}
	f, err := llm.NewFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func withFactory(f llm.ClientFactory) envOption { return func(c *envConfig) { c.factory = f } }

// recordEcho runs the echo agent in record mode against the fake API, writing cassettes into dir.
func recordEcho(t *testing.T, dir string) {
	t.Helper()
	api := &fakeAnthropic{}
	f := llmFactory(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir, "ANTHROPIC_API_KEY": "sk-ant-api03-FAKE-not-a-real-key-0123456789"}, api)
	e := newEnv(t, nil, withFactory(f))
	run := e.create("echo", echoMessage, Budgets{})
	e.execute(run)
	if r := e.get(run); r.Status != store.AgentSucceeded {
		t.Fatalf("recording run = %s %v %s", r.Status, r.FailureReason, r.Output)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.messages != 2 {
		t.Fatalf("the fake API saw %d message calls, want 2", api.messages)
	}
}

func cassetteNames(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "ideation", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = filepath.Base(f)
	}
	slices.Sort(names)
	return names
}

// The committed cassettes are what the loop sends today: recording again (against the fake API) produces the same keys.
func TestEchoCassettesAreCurrent(t *testing.T) {
	tmp := t.TempDir()
	recordEcho(t, tmp)
	fresh := cassetteNames(t, tmp)
	if len(fresh) != 2 {
		t.Fatalf("recorded %d cassettes, want 2: %v", len(fresh), fresh)
	}
	if os.Getenv("CF_UPDATE_CASSETTES") == "1" {
		if err := os.RemoveAll(echoCassetteDir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(echoCassetteDir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, n := range fresh {
			data, err := os.ReadFile(filepath.Join(tmp, "ideation", n)) //nolint:gosec // a file this test just wrote
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(echoCassetteDir, n), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("cassettes updated: %v", fresh)
		return
	}
	if committed := cassetteNames(t, cassetteDir); !slices.Equal(fresh, committed) {
		t.Fatalf("the committed cassettes are out of date.\n  recorded now: %v\n  committed:    %v\nRe-record: CF_UPDATE_CASSETTES=1 go test ./internal/agents/runtime -run TestEchoCassettesAreCurrent", fresh, committed)
	}
}

// CI: the whole loop in replay mode. No API, no key, no network: the cassettes answer, and the run's budget and totals
// behave as they did when the calls were live.
func TestEchoRunInReplayMode(t *testing.T) {
	f := llmFactory(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil)
	e := newEnv(t, nil, withFactory(f))
	run := e.create("echo", echoMessage, Budgets{})
	if got := e.execute(run); got != OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	r := e.get(run)
	if r.Status != store.AgentSucceeded || outputOf(t, r)["summary"] != "Noted: "+echoMessage {
		t.Fatalf("run = %s %v %s", r.Status, r.FailureReason, r.Output)
	}
	if r.InputTokens != 412+220 || r.CacheReadTokens != 300 || r.OutputTokens != 38+41 || r.ToolCalls != 2 || r.BilledTo != "test" || r.Model != "claude-haiku-5-5" {
		t.Fatalf("totals: %+v", r)
	}
	// Haiku 5.5 at $0.10 in / $0.50 out per MTok: the recorded usage costs a few hundredths of a cent.
	if r.CostMicros <= 0 || r.CostMicros > 1000 {
		t.Fatalf("cost = %d micro-dollars", r.CostMicros)
	}
	if e.sink.effects() != 1 {
		t.Fatalf("effects = %d", e.sink.effects())
	}
	var ledger int
	if err := e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM usage_ledger WHERE run_id = $1`, run.ID).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("replay wrote %d ledger rows, %v", ledger, err)
	}

	// Budget behaviour matches live: a replayed call reserves and settles against the run's budget.
	small := e.create("echo", echoMessage, Budgets{Micros: 1})
	e.execute(small)
	assertBudgetStop(t, e, small, LimitDollars)
}

// A request with no cassette fails the run with a miss that names the key. It never falls back to the API.
func TestReplayMissFailsTheRun(t *testing.T) {
	f := llmFactory(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil)
	e := newEnv(t, nil, withFactory(f))
	run := e.create("echo", "a message nobody recorded", Budgets{})
	e.execute(run)
	r := e.get(run)
	if r.Status != store.AgentFailed || r.FailureReason == nil || *r.FailureReason != FailReplayMiss {
		t.Fatalf("run = %s %v", r.Status, r.FailureReason)
	}
	errs := eventsOf(e.events(run), store.EventError)
	var p errorPayload
	_ = json.Unmarshal(errs[0].Payload, &p)
	if !strings.Contains(p.Message, "no cassette") || !errors.Is(&llm.ReplayMissError{}, llm.ErrReplayMiss) {
		t.Fatalf("error = %+v", p)
	}
}
