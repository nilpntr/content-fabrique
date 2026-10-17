package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

func (r *projectRig) runEval(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "eval-test", MaxConns: 2})
	}}
	code = cmdEval(append(args, "--env-file", r.envFile), &out, &errb, fakeProcessEnv(map[string]string{}), deps)
	return code, out.String(), errb.String()
}

func TestEvalEditorPrintsTheChecksOfAStoredVersion(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "Row")
	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--assets", a.ID.String(), "--yes")
	if code != 0 {
		t.Fatalf("create: exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)

	// A project with no timeline has nothing to evaluate.
	code, _, errOut = r.runEval("editor", "--workspace", r.ws.ID.String(), "--project", pid.String())
	if code != 1 || !strings.Contains(errOut, "has no timeline yet") {
		t.Fatalf("no timeline: exit %d %q", code, errOut)
	}

	// Version 1: a hook of 3 s (too long), no title, three cuts that are not on a beat grid.
	spec := fmt.Sprintf(`{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
		"tracks":[{"type":"video","id":"main","items":[
			{"id":"c1","asset_id":%[1]q,"source":{"in_ms":0,"out_ms":3000},"at_ms":0},
			{"id":"c2","asset_id":%[1]q,"source":{"in_ms":0,"out_ms":1000},"at_ms":3000}]}],"markers":[]}`, a.ID.String())
	if _, err := r.st.AddTimelineVersion(context.Background(), r.ws.ID, pid, store.NewTimelineVersion{
		Version: 1, Parent: 0, Spec: json.RawMessage(spec), Summary: []string{"first cut"}, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
	}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut = r.runEval("editor", "--workspace", "org_project", "--project", pid.String())
	if code != 0 {
		t.Fatalf("eval: exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"version 1, made by user", "plan: none found", "check_edit for version 1 (general)", "FAIL hook_length", "FAIL hook_title", "FAIL beat_sync", "metrics: duration 4000 ms, 2 shots"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// --json prints the record the editor's check_edit stores, for the same version.
	code, out, errOut = r.runEval("editor", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--version", "1", "--json")
	if code != 0 {
		t.Fatalf("eval --json: exit %d\n%s%s", code, out, errOut)
	}
	var rec struct {
		Version int `json:"version"`
		Failed  int `json:"failed"`
		Checks  []struct{ ID, Status string }
		Metrics struct {
			Shots int `json:"shots"`
		}
	}
	if err := json.Unmarshal([]byte(out), &rec); err != nil || rec.Version != 1 || rec.Failed < 3 || rec.Metrics.Shots != 2 || len(rec.Checks) != 12 {
		t.Fatalf("json: %v %+v\n%s", err, rec, out)
	}

	// Mistakes are said plainly, and nothing needs a model.
	if code, _, errOut = r.runEval("editor", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--version", "9"); code != 1 || !strings.Contains(errOut, "version 9") {
		t.Errorf("unknown version: exit %d %q", code, errOut)
	}
	if code, _, errOut = r.runEval("editor", "--workspace", r.ws.ID.String()); code != 2 || !strings.Contains(errOut, "--project") {
		t.Errorf("no project: exit %d %q", code, errOut)
	}
	if code, _, errOut = r.runEval("nope"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command: exit %d %q", code, errOut)
	}
}
