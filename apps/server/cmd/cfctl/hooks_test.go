package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/hooklab"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func (r *projectRig) runHooks(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	deps := agentDeps{openStore: func(ctx context.Context, dsn string) (*store.Store, error) {
		return store.Open(ctx, dsn, store.Options{AppName: "hooks-test", MaxConns: 2})
	}}
	code = cmdHooks(append(args, "--env-file", r.envFile), &out, &errb, fakeProcessEnv(map[string]string{}), deps)
	return code, out.String(), errb.String()
}

// The checked-in cases pass through the command, with no database and no env file.
func TestHooksEvalRunsTheRepoCases(t *testing.T) {
	var out, errb bytes.Buffer
	if code := cmdHooks([]string{"eval"}, &out, &errb, fakeProcessEnv(map[string]string{}), agentDeps{}); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errb.String())
	}
	if s := out.String(); !strings.Contains(s, "PASS clean-line-wins: pick b") || !strings.Contains(s, "hook-lab: ") || strings.Contains(s, "FAIL") {
		t.Errorf("output:\n%s", s)
	}
	out.Reset()
	if code := cmdHooks([]string{"eval", "--json"}, &out, &errb, fakeProcessEnv(map[string]string{}), agentDeps{}); code != 0 {
		t.Fatalf("json: exit %d", code)
	}
	var got struct {
		Suite   string               `json:"suite"`
		Failed  int                  `json:"failed"`
		Results []hooklab.CaseResult `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Suite != "hook-lab" || got.Failed != 0 || len(got.Results) < 12 {
		t.Errorf("json: %v %+v", err, got)
	}

	// A case the lab answers differently fails the command; a file that is not a case file is a usage error.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	body := "suite: s\ncases:\n  - id: one\n    clips: [{asset: x, duration_ms: 1000}]\n    variants: [{id: a, asset_id: x, in_ms: 0, out_ms: 500}]\n    expect: {pick: z}\n"
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdHooks([]string{"eval", "--suite", bad}, &out, &errb, fakeProcessEnv(map[string]string{}), agentDeps{}); code != 1 || !strings.Contains(out.String(), "FAIL one: the pick is") {
		t.Errorf("failing case: exit %d\n%s", code, out.String())
	}
	for _, args := range [][]string{{"eval", "--suite", filepath.Join(dir, "missing.yaml")}, {"eval", "extra"}, {"nope"}, {}} {
		errb.Reset()
		if code := cmdHooks(args, &out, &errb, fakeProcessEnv(map[string]string{}), agentDeps{}); code != 2 || errb.Len() == 0 {
			t.Errorf("%v: exit %d %q", args, code, errb.String())
		}
	}
	out.Reset()
	if code := cmdHooks([]string{"help"}, &out, &errb, fakeProcessEnv(map[string]string{}), agentDeps{}); code != 0 || !strings.Contains(out.String(), "cfctl hooks lab") {
		t.Errorf("help: exit %d", code)
	}
}

func TestHooksLabOnAProject(t *testing.T) {
	r := newProjectRig(t)
	a := r.asset(r.ws, store.AssetKindVideo, "ready", "Row")
	b := r.asset(r.ws, store.AssetKindVideo, "ready", "Talk")
	code, out, errOut := r.run("create", "--workspace", r.ws.ID.String(), "--format", "9:16", "--assets", a.ID.String()+","+b.ID.String(), "--yes")
	if code != 0 {
		t.Fatalf("create: exit %d\n%s%s", code, out, errOut)
	}
	pid := r.projectIDIn(out)
	ctx := context.Background()
	// What is known about the footage: the first clip has two reps after two seconds of dead time, the second one talks.
	res := store.AnalysisResult{Version: 1, StepMs: 100, DurationMs: 10000, Series: store.PackedSeries{}, Shots: []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 10000, Signals: []byte(`{}`)}},
		Summary: []byte(`{"version":1,"duration_ms":10000,"shots":1}`),
		Moments: []store.NewMoment{
			{Kind: store.MomentRep, StartMs: 2000, EndMs: 5000, Score: 0.9, Attrs: []byte(`{"sharpness_rel":0.95}`), Description: "rep 1 of 1"},
			{Kind: store.MomentDead, StartMs: 0, EndMs: 2000, Attrs: []byte(`{}`), Description: "dead time"},
		}}
	cur, err := r.st.GetAsset(ctx, r.ws.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	res.SourceIngestedAt = cur.IngestedAt // the analysis is of the proxy the asset has now
	if err := r.st.SaveAnalysis(ctx, r.ws.ID, a.ID, res); err != nil {
		t.Fatal(err)
	}
	words := `[{"text":"Stop","start_ms":500,"end_ms":800},{"text":"skipping","start_ms":840,"end_ms":1300},{"text":"your","start_ms":1340,"end_ms":1480},{"text":"warm-up!","start_ms":1520,"end_ms":2100}]`
	if _, err := r.st.Pool().Exec(ctx, `INSERT INTO transcripts (workspace_id, asset_id, speech, language, words, provider, model, audio_ms)
		VALUES ($1, $2, true, 'en', $3, 'whispercpp', 'm', 10000)`, r.ws.ID, b.ID, words); err != nil {
		t.Fatal(err)
	}

	// Before there is a timeline: the proposals.
	code, out, errOut = r.runHooks("lab", "--workspace", r.ws.ID.String(), "--project", pid.String())
	if code != 0 {
		t.Fatalf("lab: exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"no timeline yet", "Hook lab: 2 ways to open", "1. variant a, a spoken line: " + b.ID.String() + " from 420 to 2180 ms", "the best moment of the picture: " + a.ID.String() + " from 2000 to 4000 ms", "variant a: Stop skipping your warm-up!", "pick: variant a"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}

	// Version 1 opens on the dead time with no title: the edit as it is ranks last, with what fails.
	spec := fmt.Sprintf(`{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
		"tracks":[{"type":"video","id":"main","items":[
			{"id":"c1","asset_id":%[1]q,"source":{"in_ms":0,"out_ms":3000},"at_ms":0},
			{"id":"c2","asset_id":%[1]q,"source":{"in_ms":3000,"out_ms":6000},"at_ms":3000}]}],"markers":[]}`, a.ID.String())
	if _, err := r.st.AddTimelineVersion(ctx, r.ws.ID, pid, store.NewTimelineVersion{
		Version: 1, Parent: 0, Spec: json.RawMessage(spec), Summary: []string{"first cut"}, AuthorKind: store.AuthorUser, AuthorID: "usr_x",
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = r.runHooks("lab", "--workspace", "org_project", "--project", pid.String())
	if code != 0 {
		t.Fatalf("lab with a timeline: exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"version 1", "Hook lab: 3 ways to open", "3. variant current, the edit as it is", "fails hook_in_first_2s", "fails first_frame_alive", "pick: variant a"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}

	// JSON is the record of the hook_lab artifact; a playbook file moves the weights.
	playbook := filepath.Join(t.TempDir(), "findings.yaml")
	if err := os.WriteFile(playbook, []byte("playbook_version: \"2026-47\"\nfindings:\n  - {id: f1, dimension: hook, criterion: first_frame_alive, effect: 1.6, confidence: high}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = r.runHooks("lab", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--version", "1", "--playbook", playbook, "--json")
	if code != 0 {
		t.Fatalf("json: exit %d\n%s%s", code, out, errOut)
	}
	var rec editor.HookRecord
	if err := json.Unmarshal([]byte(out), &rec); err != nil || rec.RecordVersion != editor.HookRecordVersion || rec.Version != 1 || len(rec.Ranked) != 3 || rec.WeightsFrom != "playbook 2026-47" || rec.Weights[hooklab.FirstFrame] != 2.4 {
		t.Errorf("json: %v %+v", err, rec)
	}

	// Usage errors and things that are not there.
	for name, args := range map[string][]string{
		"no project":   {"lab", "--workspace", r.ws.ID.String()},
		"no workspace": {"lab", "--project", pid.String()},
		"bad version":  {"lab", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--version", "-1"},
		"bad playbook": {"lab", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--playbook", filepath.Join(t.TempDir(), "none.yaml")},
		"extra":        {"lab", "--workspace", r.ws.ID.String(), "--project", pid.String(), "extra"},
	} {
		if code, _, errOut := r.runHooks(args...); code != 2 || errOut == "" {
			t.Errorf("%s: exit %d %q", name, code, errOut)
		}
	}
	if code, _, errOut := r.runHooks("lab", "--workspace", r.ws.ID.String(), "--project", pid.String(), "--version", "7"); code != 1 || !strings.Contains(errOut, "timeline version does not exist") {
		t.Errorf("a missing version: exit %d %q", code, errOut)
	}
}
