package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func waitForStatus(t *testing.T, e *env, run store.AgentRun, want string) store.AgentRun {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := e.get(run)
		if r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s (reason %v), waiting for %s", run.ID, r.Status, r.FailureReason, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The agent_run job end to end: Start enqueues it in the run's transaction, the River worker works it, an ask_user parks
// the run, Reply enqueues the job again and the run finishes.
func TestRiverWorkerRunsAgentJobs(t *testing.T) {
	asker := Agent{Name: "asker", Stage: "ideation", System: "Ask, then finish.", Tools: []Tool{AskUserTool(), FinishTool()}}
	model := newScripted(append(noteAndFinish(),
		use(toolUse("toolu_a1", "ask_user", `{"question":"Which colour?"}`)),
		use(toolUse("toolu_a2", "finish", `{"summary":"blue"}`)))...)
	e := newEnv(t, model, func(c *envConfig) { c.extra = []Agent{asker} })
	client, err := jobs.NewWorkerClient(e.st.Pool(), jobs.DefaultConfig(), jobs.Deps{Store: e.st, Logger: quiet, Register: Register(e.runner)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stop)
	})

	// A plain run.
	run, err := e.svc.Start(ctx, StartParams{Agent: "echo", WorkspaceID: e.ws.ID, Input: []byte(`{"message":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForStatus(t, e, run, store.AgentSucceeded)
	if outputOf(t, done)["summary"] != "noted hello" || e.sink.effects() != 1 {
		t.Fatalf("output %s, effects %d", done.Output, e.sink.effects())
	}

	// A run that asks.
	ask, err := e.svc.Start(ctx, StartParams{Agent: "asker", WorkspaceID: e.ws.ID, Input: []byte(`{"message":"pick one"}`)})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, e, ask, store.AgentWaitingInput)
	if err := e.svc.Reply(ctx, e.ws.ID, ask.ID, "blue"); err != nil {
		t.Fatal(err)
	}
	done = waitForStatus(t, e, ask, store.AgentSucceeded)
	if outputOf(t, done)["summary"] != "blue" {
		t.Fatalf("output = %s", done.Output)
	}

	// A run whose worker was killed: the sweeper finds its stale heartbeat and enqueues the job; the run resumes and the
	// mutating tool's effect stays single.
	before := e.sink.effects()
	model.mu.Lock()
	model.steps = append(model.steps, noteAndFinish()...)
	model.mu.Unlock()
	dead := e.create("echo", "resume me", Budgets{})
	if !e.crashAt(dead, "tool_call_persisted") {
		t.Fatal("not reached")
	}
	e.ageHeartbeat(dead)
	if _, err := client.Insert(ctx, jobs.AgentRunsResumeArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	done = waitForStatus(t, e, dead, store.AgentSucceeded)
	if done.Epoch != 2 || e.sink.effects() != before+1 {
		t.Fatalf("epoch %d, effects %d (was %d)", done.Epoch, e.sink.effects(), before)
	}
}
