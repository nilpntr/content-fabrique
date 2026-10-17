package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// What the run detail adds for the UI: the call id on events, the artifacts with their whole data, and the credits of the run.

// The tool_use id pairs a tool_call with its tool_result and with the artifact that call made, and an artifact event says its kind;
// the other events carry neither (the keys are absent, not empty).
func TestRunsEventsCarryTheCallId(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	run := e.startEdit("ada", p, nil)

	e.appendEvent("ada", run, store.EventStatus, `{"state":"running","epoch":1}`)
	e.appendEvent("ada", run, store.EventToolCall, `{"tool_use_id":"toolu_A","name":"plan_edit","input":{}}`)
	e.appendEvent("ada", run, store.EventToolCall, `{"tool_use_id":"toolu_B","name":"check_edit","input":{}}`)
	// Results arrive in the other order: the pairing is by id.
	e.appendEvent("ada", run, store.EventToolResult, `{"tool_use_id":"toolu_B","name":"check_edit","content":[{"type":"text","text":"ok"}]}`)
	e.appendEvent("ada", run, store.EventArtifact, `{"kind":"edit_check","tool_use_id":"toolu_B","data":{"checks":[]}}`)
	e.appendEvent("ada", run, store.EventToolResult, `{"tool_use_id":"toolu_A","name":"plan_edit","content":[{"type":"text","text":"planned"}]}`)
	e.appendEvent("ada", run, store.EventArtifact, `{"kind":"creative_plan","tool_use_id":"toolu_A","data":{}}`)

	var got api.Run
	e.call(http.MethodGet, runPath(run, ""), "ada", nil, http.StatusOK, &got)
	if len(got.Events) != 7 {
		t.Fatalf("%d events", len(got.Events))
	}
	wantCall := []string{"", "toolu_A", "toolu_B", "toolu_B", "toolu_B", "toolu_A", "toolu_A"}
	wantKind := []string{"", "", "", "", "edit_check", "", "creative_plan"}
	for i, ev := range got.Events {
		if c := deref(ev.CallId); c != wantCall[i] {
			t.Errorf("event %d (%s): call_id %q, want %q", ev.Seq, ev.Type, c, wantCall[i])
		}
		if k := deref(ev.Kind); k != wantKind[i] {
			t.Errorf("event %d (%s): kind %q, want %q", ev.Seq, ev.Type, k, wantKind[i])
		}
	}
	// Absent, not empty, on the events without one.
	var raw struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	e.call(http.MethodGet, runPath(run, ""), "ada", nil, http.StatusOK, &raw)
	if _, ok := raw.Events[0]["call_id"]; ok {
		t.Error("a status event has a call_id key")
	}
	if _, ok := raw.Events[1]["kind"]; ok {
		t.Error("a tool_call event has a kind key")
	}
}

// The artifacts come with their whole data, up to the event limit, apart from the run so that polling it stays small. They are paged
// and filtered by kind, scoped to the workspace, and open to every member.
func TestRunsArtifacts(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true })
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	run := e.startEdit("ada", p, nil)

	// A plan with more shots than the one-line summary can hold, and a payload near the 64 KB limit.
	shots := make([]map[string]any, 12)
	for i := range shots {
		shots[i] = map[string]any{"clip": "ast_" + strings.Repeat("x", 8), "role": "b-roll", "note": "shot " + strings.Repeat("n", 100)}
	}
	plan, _ := json.Marshal(map[string]any{"kind": "creative_plan", "tool_use_id": "toolu_1", "data": map[string]any{"story": "a < b & \"c\"", "shots": shots}})
	big, _ := json.Marshal(map[string]any{"kind": "edit_check", "tool_use_id": "toolu_2", "data": map[string]any{"reason": strings.Repeat("r", store.MaxEventPayloadBytes-200)}})
	e.appendEvent("ada", run, store.EventStatus, `{"state":"running","epoch":1}`)
	e.appendEvent("ada", run, store.EventArtifact, string(plan))
	e.appendEvent("ada", run, store.EventToolResult, `{"tool_use_id":"toolu_1","name":"plan_edit","content":[]}`)
	e.appendEvent("ada", run, store.EventArtifact, string(big))
	e.appendEvent("ada", run, store.EventArtifact, `{"kind":"preview","tool_use_id":"toolu_3","data":{"version":2,"duration_ms":15000}}`)
	e.appendEvent("ada", run, store.EventArtifact, `{"kind":"edit_check"}`) // no data: null, not an error

	var all api.RunArtifacts
	e.call(http.MethodGet, runPath(run, "/artifacts"), "ada", nil, http.StatusOK, &all)
	if len(all.Artifacts) != 4 || all.NextAfterSeq != nil {
		t.Fatalf("artifacts: %d, next %v", len(all.Artifacts), all.NextAfterSeq)
	}
	a := all.Artifacts
	if a[0].Seq != 2 || a[0].Kind != "creative_plan" || deref(a[0].CallId) != "toolu_1" || a[1].Seq != 4 || a[3].CallId != nil {
		t.Errorf("artifacts: %+v", a)
	}
	// The data is whole and equal to what was stored.
	var want, have map[string]any
	_ = json.Unmarshal(plan, &want)
	if err := json.Unmarshal(*a[0].Data, &have); err != nil {
		t.Fatal(err)
	}
	wj, _ := json.Marshal(want["data"])
	hj, _ := json.Marshal(have)
	if shotList, _ := have["shots"].([]any); !bytes.Equal(wj, hj) || len(shotList) != 12 {
		t.Errorf("plan data differs:\n%s\n%s", wj, hj)
	}
	if len(*a[1].Data) < store.MaxEventPayloadBytes-300 {
		t.Errorf("the big artifact was cut to %d bytes", len(*a[1].Data))
	}
	if a[3].Data != nil {
		t.Errorf("an artifact without data: %s", *a[3].Data)
	}

	// The run itself stays small: the summaries, none of the data.
	rec := e.do(http.MethodGet, runPath(run, ""), "ada", nil)
	if rec.Body.Len() > 8*1024 || strings.Contains(rec.Body.String(), "shot nnnn") {
		t.Errorf("GET run is %d bytes and carries artifact data", rec.Body.Len())
	}

	// A kind narrows, and pages go by after_seq.
	var checks api.RunArtifacts
	e.call(http.MethodGet, runPath(run, "/artifacts?kind=edit_check"), "ada", nil, http.StatusOK, &checks)
	if len(checks.Artifacts) != 2 {
		t.Errorf("edit_check artifacts: %d", len(checks.Artifacts))
	}
	var page api.RunArtifacts
	e.call(http.MethodGet, runPath(run, "/artifacts?limit=2"), "ada", nil, http.StatusOK, &page)
	if len(page.Artifacts) != 2 || page.NextAfterSeq == nil || *page.NextAfterSeq != int(page.Artifacts[1].Seq) {
		t.Fatalf("first page: %+v", page)
	}
	e.call(http.MethodGet, runPath(run, "/artifacts?limit=2&after_seq="+strconv.Itoa(*page.NextAfterSeq)), "ada", nil, http.StatusOK, &page)
	if len(page.Artifacts) != 2 || page.NextAfterSeq != nil || page.Artifacts[0].Kind != "preview" {
		t.Fatalf("second page: %+v", page)
	}
	e.call(http.MethodGet, runPath(run, "/artifacts?after_seq=99"), "ada", nil, http.StatusOK, &page)
	if len(page.Artifacts) != 0 {
		t.Errorf("after the last: %+v", page)
	}
	for _, q := range []string{"?limit=0", "?limit=51", "?after_seq=-1", "?limit=x", "?kind=" + strings.Repeat("k", 65), "?kind=%00", "?kind=%ff", "?kind=", "?kind=Plan", "?kind=a%20b"} {
		e.fail(http.MethodGet, runPath(run, "/artifacts"+q), "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	}

	// Any member reads; another workspace gets the same 404 as for a run that does not exist; a stranger is unauthenticated.
	e.call(http.MethodGet, runPath(run, "/artifacts"), "viewer", nil, http.StatusOK, nil)
	e.fail(http.MethodGet, runPath(run, "/artifacts"), "bob", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodGet, runPath(run, "/artifacts"), "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	e.fail(http.MethodGet, "/v1/runs/"+ids.Encode(ids.PrefixRun, uuid.Must(uuid.NewV7()))+"/artifacts", "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodGet, "/v1/runs/nope/artifacts", "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)

	// The paywall covers the new path.
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM workspace_plans WHERE workspace_id = $1`, e.ws["ada"]); err != nil {
		t.Fatal(err)
	}
	e.fail(http.MethodGet, runPath(run, "/artifacts"), "ada", nil, http.StatusPaymentRequired, api.ErrorCodePlanRequired)
}

// The credits of a run: held while it works, charged when it succeeds (with the action), refunded when the charge is given back, and
// nothing for a run that was released.
func TestRunsCredits(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.runs = true; o.credits = 40 })
	ctx := context.Background()
	p := e.newProject("ada", "9:16", e.readyAsset("ada", "lat.mp4"))
	get := func(r api.Run) api.Run {
		t.Helper()
		var got api.Run
		e.call(http.MethodGet, runPath(r, ""), "ada", nil, http.StatusOK, &got)
		return got
	}

	first := e.startEdit("ada", p, nil)
	if first.Credits != nil {
		t.Errorf("the answer of start carries credits: %+v", first.Credits)
	}
	if c := get(first).Credits; c == nil || c.State != api.RunCreditsStateHeld || c.Action != api.RunCreditsActionEditFirst || c.Credits != 6 {
		t.Fatalf("while it works: %+v", c)
	}

	e.endRun("ada", first, store.AgentSucceeded)
	if c := get(first).Credits; c == nil || c.State != api.RunCreditsStateCharged || c.Action != api.RunCreditsActionEditFirst || c.Credits != 6 {
		t.Fatalf("after success: %+v", c)
	}

	// The next run on the project is a revision, and one that fails is released: no credits object.
	rev := e.startEdit("ada", p, nil)
	if c := get(rev).Credits; c == nil || c.State != api.RunCreditsStateHeld || c.Action != api.RunCreditsActionEditRevision || c.Credits != 2 {
		t.Fatalf("revision held: %+v", c)
	}
	e.endRun("ada", rev, store.AgentFailed)
	var raw map[string]json.RawMessage
	e.call(http.MethodGet, runPath(rev, ""), "ada", nil, http.StatusOK, &raw)
	if _, ok := raw["credits"]; ok {
		t.Errorf("a released run has credits: %s", raw["credits"])
	}

	// A charge given back.
	id, _ := ids.Decode(ids.PrefixRun, first.Id)
	if _, _, err := e.st.RefundAction(ctx, e.ws["ada"], id, "test", "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if c := get(first).Credits; c == nil || c.State != api.RunCreditsStateRefunded || c.Credits != 6 {
		t.Fatalf("after a refund: %+v", c)
	}

	// Only members of the workspace see it: another workspace's read of the run is a 404, with no credits in it.
	rec := e.do(http.MethodGet, runPath(first, ""), "bob", nil)
	wantError(t, rec, http.StatusNotFound, string(api.ErrorCodeNotFound))
}
