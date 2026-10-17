package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---- a scripted model ----------------------------------------------------------------------------------------

type scriptStep func(n int, req llm.Request) (llm.Response, error)

// scripted is a fake llm.Client: it answers from a script, and it behaves like the metered client with respect to the
// run's budget: it reserves reserveMicros before the call and settles with costMicros (the reserve when the outcome is
// unknown, nothing on a definite failure).
type scripted struct {
	mu      sync.Mutex
	steps   []scriptStep
	reqs    []llm.Request
	reserve int64
	cost    int64
}

func newScripted(steps ...scriptStep) *scripted {
	return &scripted{steps: steps, reserve: 150, cost: 100}
}

func (s *scripted) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }

func (s *scripted) request(i int) llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

func (s *scripted) CountTokens(context.Context, llm.Request) (int64, error) { return 1000, nil }

func (s *scripted) Messages(_ context.Context, req llm.Request) (llm.Response, error) {
	s.mu.Lock()
	n := len(s.reqs)
	s.reqs = append(s.reqs, req)
	var step scriptStep
	if n < len(s.steps) {
		step = s.steps[n]
	}
	s.mu.Unlock()

	release := func(int64) {}
	if req.Run.Budget != nil {
		var err error
		if release, err = req.Run.Budget.Reserve(s.reserve); err != nil {
			return llm.Response{}, err
		}
	}
	if step == nil {
		release(0)
		return llm.Response{}, fmt.Errorf("the script has no step %d", n)
	}
	resp, err := step(n, req)
	switch {
	case err == nil:
		release(s.cost)
	case errors.Is(err, llm.ErrOutcomeUnknown):
		release(s.reserve)
	default:
		release(0)
	}
	return resp, err
}

type fakeFactory struct{ client llm.Client }

func (f fakeFactory) For(context.Context, ids.WorkspaceID, string) (llm.Client, llm.Billing, error) {
	return f.client, llm.BilledToTest, nil
}

var stdUsage = llm.Usage{InputTokens: 100, OutputTokens: 20}

func toolUse(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

// say answers with blocks and a stop reason.
func say(stop string, blocks ...llm.Block) scriptStep {
	return func(int, llm.Request) (llm.Response, error) {
		return llm.Response{ID: "msg_x", RequestID: "req_x", Model: "claude-haiku-5-5", Content: blocks, StopReason: stop, Usage: stdUsage}, nil
	}
}

func use(blocks ...llm.Block) scriptStep { return say("tool_use", blocks...) }

func endTurn(text string) scriptStep { return say("end_turn", llm.TextBlock(text)) }

func failCall(err error) scriptStep {
	return func(int, llm.Request) (llm.Response, error) { return llm.Response{}, err }
}

// ---- a note sink that counts effects ---------------------------------------------------------------------------

type memSink struct {
	mu      sync.Mutex
	applied map[string]string
	calls   int
	// delay makes Record slow; onRecord runs inside it, before the effect.
	delay    time.Duration
	onRecord func()
}

func (m *memSink) Record(_ context.Context, key, text string) (bool, error) {
	if m.onRecord != nil {
		m.onRecord()
	}
	time.Sleep(m.delay)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.applied == nil {
		m.applied = map[string]string{}
	}
	if _, ok := m.applied[key]; ok {
		return false, nil
	}
	m.applied[key] = text
	return true, nil
}

func (m *memSink) effects() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.applied) }

// ---- the environment -----------------------------------------------------------------------------------------------

type env struct {
	t      *testing.T
	st     *store.Store
	ws     store.Workspace
	dsn    string
	model  *scripted
	sink   *memSink
	agents *Agents
	runner *Runner
	svc    *Service
	router *llm.Router
	enq    *jobs.Enqueuer
}

type envOption func(*envConfig)

type envConfig struct {
	extra   []Agent
	factory llm.ClientFactory
	now     func() time.Time
}

// newEnv migrates a fresh database and builds a runner over a scripted model.
func newEnv(t *testing.T, model *scripted, opts ...envOption) *env {
	t.Helper()
	cfg := &envConfig{}
	for _, o := range opts {
		o(cfg)
	}
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(context.Background(), dsn, store.Options{AppName: "runtime-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ws, err := st.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_rt_" + uuid.NewString(), Name: "Runtime"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, ws: ws, dsn: dsn, model: model, sink: &memSink{}}
	defs := append([]Agent{NewEchoAgent(e.sink)}, cfg.extra...)
	if e.agents, err = NewAgents(defs...); err != nil {
		t.Fatal(err)
	}
	var factory llm.ClientFactory = fakeFactory{client: model}
	if cfg.factory != nil {
		factory = cfg.factory
	}
	if e.router, err = llm.NewRouter(os.DirFS("../../../config/routing"), "dev-cheap"); err != nil {
		t.Fatal(err)
	}
	if e.runner, err = NewRunner(Deps{Store: st, Factory: factory, Agents: e.agents, Logger: quiet, Now: cfg.now}); err != nil {
		t.Fatal(err)
	}
	e.svc = &Service{Store: st, Router: e.router, Factory: factory, Agents: e.agents}
	client, err := jobs.NewInsertClient(st.Pool(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	e.enq = jobs.NewEnqueuer(client, quiet)
	e.svc.Enqueuer = e.enq
	return e
}

// create makes a queued run of the agent without enqueuing a job: the test calls Execute itself.
func (e *env) create(agent, message string, b Budgets) store.AgentRun {
	e.t.Helper()
	input, _ := json.Marshal(Input{Message: message})
	run, err := e.svc.Create(context.Background(), e.st, StartParams{Agent: agent, WorkspaceID: e.ws.ID, Input: input, Budgets: b})
	if err != nil {
		e.t.Fatal(err)
	}
	return run
}

func (e *env) execute(run store.AgentRun) Outcome {
	e.t.Helper()
	out, err := e.runner.Execute(context.Background(), e.ws.ID, run.ID)
	if err != nil {
		e.t.Fatalf("Execute: %v", err)
	}
	return out
}

// crashAt runs Execute and simulates a killed worker at the named point: the loop stops there, nothing after it runs and
// nothing is cleaned up. It reports whether the crash happened.
func (e *env) crashAt(run store.AgentRun, point string) bool {
	e.t.Helper()
	fired := false
	e.runner.hook = func(p string) {
		if p == point && !fired {
			fired = true
			panic(crashSignal{point: p})
		}
	}
	defer func() { e.runner.hook = nil }()
	crashed := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(crashSignal); !ok {
					panic(r)
				}
				crashed = true
			}
		}()
		_, err := e.runner.Execute(context.Background(), e.ws.ID, run.ID)
		if err != nil {
			e.t.Fatalf("Execute: %v", err)
		}
	}()
	return crashed
}

// ageHeartbeat makes the run's worker look dead.
func (e *env) ageHeartbeat(run store.AgentRun) {
	e.t.Helper()
	if _, err := e.st.Pool().Exec(context.Background(), `UPDATE agent_runs SET heartbeat_at = now() - interval '1 hour' WHERE id = $1`, run.ID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) get(run store.AgentRun) store.AgentRun {
	e.t.Helper()
	r, err := e.st.GetAgentRun(context.Background(), e.ws.ID, run.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) events(run store.AgentRun) []store.AgentEvent {
	e.t.Helper()
	ev, err := e.st.AgentEvents(context.Background(), e.ws.ID, run.ID, 0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return ev
}

// kinds lists the event types, with the state of a status event, e.g. "status:running", "tool_call".
func kinds(events []store.AgentEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		k := e.Type
		if e.Type == store.EventStatus {
			var p statusPayload
			_ = json.Unmarshal(e.Payload, &p)
			k += ":" + p.State
		}
		out = append(out, k)
	}
	return out
}

func eventsOf(events []store.AgentEvent, typ string) []store.AgentEvent {
	var out []store.AgentEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func equalStrings(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// noteAndFinish is the happy script of the echo agent: note, then finish.
func noteAndFinish() []scriptStep {
	return []scriptStep{
		use(toolUse("toolu_1", "note", `{"text":"hello"}`)),
		use(toolUse("toolu_2", "finish", `{"summary":"noted hello"}`)),
	}
}
