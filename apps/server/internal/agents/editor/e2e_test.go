package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// An editor run from start to end over a small generated project, in replay mode (D-22, docs/design/agent-runtime-v1.md section 2).
//
// The cassettes in testdata/cassettes/editor are recorded here, through the real llm layer in record mode, against a fake HTTP
// transport that plays the Anthropic API and scripts plausible model turns: no real model is called and no key is needed.
// CI runs the whole thing in replay mode over them, so a change to anything that is part of a request (the system prompt, a tool
// schema, a tool's wording, the run context) makes a replay miss, and TestEditorCassettesAreCurrent says so. Re-record with:
//
//	CF_UPDATE_CASSETTES=1 go test ./internal/agents/editor -run TestEditorCassettesAreCurrent
//
// To record a real conversation instead, run `LLM_MODE=record cfctl agent run --agent editor --project prj_...`.

const (
	cassetteDir = "testdata/cassettes"
	editorStage = "editor"
)

// The generated project: three clips with fixed ids (the ids are part of what the model reads, so the cassettes need them fixed).
var (
	clipA = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000a")}
	clipB = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000b")}
	clipC = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000c")}
)

const projectBrief = "A 20 second back-day short. Open on the heaviest set. Keep it punchy."

// fakeAnthropic plays the Messages API for the editor: list_footage, view_clip, edit_timeline, get_timeline, finish.
type fakeAnthropic struct {
	mu       sync.Mutex
	messages int
	requests []map[string]any
	bodies   []string
}

func (f *fakeAnthropic) client() *http.Client { return &http.Client{Transport: f} }

func (f *fakeAnthropic) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	body := ""
	f.mu.Lock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/count_tokens"):
		body = `{"input_tokens": 9000}`
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		f.messages++
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		f.requests = append(f.requests, req)
		f.bodies = append(f.bodies, string(raw))
		body = f.turn(f.messages)
	}
	f.mu.Unlock()
	if body == "" {
		return &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"not_found_error","message":"no route"}}`)), Request: r}, nil
	}
	h := http.Header{"Content-Type": {"application/json"}, "Request-Id": {"req_fake_editor"}}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
}

func use(id, name, input string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}`
}

// turn is the n-th answer. The usage follows what a real run does with the prompt cache: the first call writes the cache (system
// prompt, tools and run context), the later ones read it and write the new turn.
func (f *fakeAnthropic) turn(n int) string {
	asset := func(a ids.AssetID) string { return `"` + a.String() + `"` }
	switch n {
	case 1:
		return message(use("toolu_ed_1", "list_footage", `{}`), 410, 9200, 0, 61)
	case 2:
		return message(use("toolu_ed_2", "view_clip", `{"asset_id":`+asset(clipA)+`}`), 380, 900, 9200, 74)
	case 3:
		ops := `{"ops":[` +
			`{"op":"add_clip","asset_id":` + asset(clipA) + `,"source":{"in_ms":2000,"out_ms":8000},"why":"The heaviest set of the lat pulldown, so it opens the video."},` +
			`{"op":"add_clip","asset_id":` + asset(clipB) + `,"source":{"in_ms":1000,"out_ms":6000}},` +
			`{"op":"add_clip","asset_id":` + asset(clipC) + `,"source":{"in_ms":3000,"out_ms":9000},"reframe":{"mode":"blur_fill"}},` +
			`{"op":"add_text","text":"HEAVY LAT DAY","style":"title_bold","at_ms":0,"duration_ms":2000,"anchor":"upper_third"}]}`
		return message(use("toolu_ed_3", "edit_timeline", ops), 450, 2100, 10100, 301)
	case 4:
		return message(use("toolu_ed_4", "get_timeline", `{}`), 420, 700, 12200, 58)
	default:
		return message(use("toolu_ed_5", "finish", `{"summary":"I cut a 17 second video from your three clips. It opens on the heaviest lat pulldown rep with the words HEAVY LAT DAY, then the cable row, then the wide gym shot. Check the wide shot: I framed it with a blurred fill so nobody is cropped out."}`), 400, 650, 12900, 92)
	}
}

func message(block string, in, cacheWrite, cacheRead, out int) string {
	return `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[` + block + `],"stop_reason":"tool_use","stop_sequence":null,` +
		fmt.Sprintf(`"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":0},"output_tokens":%d}}`, in, cacheWrite, cacheRead, cacheWrite, out)
}

type nopLedger struct{}

func (nopLedger) InsertUsage(context.Context, store.UsageEntry) error { return nil }
func (nopLedger) UsageCostMicros(context.Context, ids.WorkspaceID, time.Time, time.Time) (int64, error) {
	return 0, nil
}

// e2e is one editor run over the generated project.
type e2e struct {
	*env
	prj    ids.ProjectID
	run    store.AgentRun
	runner *runtime.Runner
	api    *fakeAnthropic
}

// newE2E seeds the project and builds the runtime over the real llm layer in the mode the variables name.
func newE2E(t *testing.T, vars map[string]string, api *fakeAnthropic) *e2e {
	t.Helper()
	e := newEnv(t)
	e.asset(e.wsA, clip{id: clipA, title: "Lat pulldown", desc: "Top set, 8 reps, heavy.", durMs: 12000})
	e.asset(e.wsA, clip{id: clipB, title: "Cable row", desc: "Two sets of 10.", durMs: 9000})
	e.asset(e.wsA, clip{id: clipC, title: "Gym wide", w: 1920, h: 1080, durMs: 15000, silent: true})
	// Things the editor must never reach: a clip of another workspace, and one of another project of this workspace.
	e.asset(e.wsB, clip{title: "private clip of workspace B"})
	e.asset(e.wsA, clip{title: "private clip of another project"})
	x := &e2e{env: e, api: api}
	x.prj = e.project(e.wsA, "9:16", projectBrief, clipA, clipB, clipC)

	// Frames go through the real frame service with a fake ffmpeg, into the in-memory store the image resolver reads.
	e.deps.Frames = newFrameService(e, &fakeRunner{})
	e.rebuild()

	vars["CF_ENV"] = "ci"
	getenv := func(k string) string { return vars[k] }
	settings, err := llm.LoadSettings(getenv)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := llm.LoadPriceTable(configfiles.Pricing(), llm.PriceFile)
	if err != nil {
		t.Fatal(err)
	}
	opts := llm.FactoryOptions{Settings: settings, Getenv: getenv, Prices: prices, Logger: quiet, Images: ImageResolver{Objects: e.objs}}
	if api != nil {
		opts.HTTPClient, opts.Ledger = api.client(), nopLedger{}
	}
	factory, err := llm.NewFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := NewAgent(e.deps)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := runtime.NewAgents(ag)
	if err != nil {
		t.Fatal(err)
	}
	router, err := llm.NewRouter(configfiles.Routing(), "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	if x.runner, err = runtime.NewRunner(runtime.Deps{Store: e.st, Factory: factory, Agents: agents, Logger: quiet}); err != nil {
		t.Fatal(err)
	}
	svc := &runtime.Service{Store: e.st, Router: router, Factory: factory, Agents: agents, Inline: true}
	input, err := NewInput("Lead with the heaviest set.")
	if err != nil {
		t.Fatal(err)
	}
	if x.run, err = svc.Create(context.Background(), e.st, runtime.StartParams{Agent: Name, WorkspaceID: e.wsA.ID, ProjectID: x.prj.UUID, Input: input}); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *e2e) execute() runtime.Outcome {
	x.t.Helper()
	out, err := x.runner.Execute(context.Background(), x.wsA.ID, x.run.ID)
	if err != nil {
		x.t.Fatalf("Execute: %v", err)
	}
	return out
}

func recordEditor(t *testing.T, dir string) *e2e {
	t.Helper()
	api := &fakeAnthropic{}
	x := newE2E(t, map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir, "ANTHROPIC_API_KEY": "sk-ant-api03-FAKE-not-a-real-key-0123456789"}, api)
	x.execute()
	return x
}

func cassetteNames(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, editorStage, "*.json"))
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
func TestEditorCassettesAreCurrent(t *testing.T) {
	tmp := t.TempDir()
	x := recordEditor(t, tmp)
	if r := x.get(); r.Status != store.AgentSucceeded {
		t.Fatalf("the recording run = %s %v %s", r.Status, r.FailureReason, r.Output)
	}
	fresh := cassetteNames(t, tmp)
	if len(fresh) != 5 {
		t.Fatalf("recorded %d cassettes, want 5: %v", len(fresh), fresh)
	}
	dest := filepath.Join(cassetteDir, editorStage)
	if os.Getenv("CF_UPDATE_CASSETTES") == "1" {
		if err := os.RemoveAll(dest); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dest, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, n := range fresh {
			data, err := os.ReadFile(filepath.Join(tmp, editorStage, n)) //nolint:gosec // a file this test just wrote
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, n), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("cassettes updated: %v", fresh)
		return
	}
	if committed := cassetteNames(t, cassetteDir); !slices.Equal(fresh, committed) {
		t.Fatalf("the committed cassettes are out of date (the prompt, a tool or the run context changed).\n  recorded now: %v\n  committed:    %v\nRe-record: CF_UPDATE_CASSETTES=1 go test ./internal/agents/editor -run TestEditorCassettesAreCurrent", fresh, committed)
	}
}

func (e *env) runRow(run store.AgentRun) store.AgentRun {
	e.t.Helper()
	r, err := e.st.GetAgentRun(context.Background(), e.wsA.ID, run.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (x *e2e) get() store.AgentRun { return x.runRow(x.run) }

// Record mode against the fake API: what the requests really contained.
func TestEditorRecordedRequests(t *testing.T) {
	x := recordEditor(t, t.TempDir())
	api := x.api
	if len(api.requests) != 5 {
		t.Fatalf("%d model calls, want 5", len(api.requests))
	}
	for i, body := range api.bodies {
		// An object key never goes to the model: images are bytes in the request, and the text names assets, not objects.
		if strings.Contains(body, "ws/"+x.wsA.ID.String()) || strings.Contains(body, "frames/") {
			t.Errorf("request %d carries an object key", i+1)
		}
		// Neither does anything of the other workspace or the other project.
		if strings.Contains(body, "private clip") {
			t.Errorf("request %d carries a clip that is not the project's", i+1)
		}
		if !strings.Contains(body, `"cache_control"`) {
			t.Errorf("request %d has no cache breakpoint", i+1)
		}
	}
	first := api.requests[0]
	if tools, _ := first["tools"].([]any); len(tools) != 7 {
		t.Errorf("%d tools sent", len(tools))
	} else {
		var names []string
		for _, tl := range tools {
			m, _ := tl.(map[string]any)
			name, _ := m["name"].(string)
			names = append(names, name)
		}
		if strings.Join(names, ",") != "get_brief,list_footage,view_clip,get_timeline,edit_timeline,render_preview,finish" {
			t.Errorf("tools: %v", names)
		}
		editTool, _ := tools[4].(map[string]any)
		viewTool, _ := tools[2].(map[string]any)
		if editTool["strict"] == true || viewTool["strict"] != true {
			t.Errorf("strict flags: edit_timeline %v, view_clip %v", editTool["strict"], viewTool["strict"])
		}
	}
	system, _ := first["system"].([]any)
	sys, _ := system[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(sys, runtime.SystemPreamble) || !strings.Contains(sys, SystemPrompt()) {
		t.Error("the system prompt is not the runtime preamble followed by the editor prompt")
	}
	// The opening and the run context come first, with the creator's words as data.
	msgs, _ := first["messages"].([]any)
	opening, _ := json.Marshal(msgs[0])
	for _, want := range []string{"Lead with the heaviest set.", `kind=\"instructions\"`, `kind=\"brief\"`, projectBrief, "Footage: 3 clips", clipA.String()} {
		if !strings.Contains(string(opening), want) {
			t.Errorf("the first message lacks %q", want)
		}
	}
	// After view_clip the pictures are in the next request, as base64 images next to their labels.
	third := api.bodies[2]
	if n := strings.Count(third, `"media_type":"image/jpeg"`); n != 6 {
		t.Errorf("the third request carries %d images, want the 6 of the overview", n)
	}
	if !strings.Contains(third, `"type":"base64"`) || !strings.Contains(third, "Frame at 10000 ms (10.00 s):") {
		t.Error("the images are not sent as base64 blocks with their time labels")
	}
}

// CI: the whole run in replay mode. No API, no key, no network: the cassettes answer, and the run produces a valid first version.
func TestEditorRunInReplayMode(t *testing.T) {
	x := newE2E(t, map[string]string{"LLM_MODE": "replay", "LLM_CASSETTE_DIR": cassetteDir}, nil)
	if got := x.execute(); got != runtime.OutcomeFinished {
		t.Fatalf("outcome = %s", got)
	}
	r := x.get()
	if r.Status != store.AgentSucceeded {
		events := x.events(x.run)
		t.Fatalf("run = %s %v %s (%d events)", r.Status, r.FailureReason, r.Output, len(events))
	}
	summary, version := runtime.OutputSummary(r.Output)
	if !strings.HasPrefix(summary, "I cut a 17 second video") || version == nil || *version != 1 {
		t.Fatalf("output: %s", r.Output)
	}
	if r.ToolCalls != 5 || r.BilledTo != "test" || r.Model != "claude-haiku-5-5" || r.ProjectID != x.prj {
		t.Fatalf("run: %+v", r)
	}

	// The cache numbers are in the run's totals and in every model call's event.
	if r.InputTokens != 410+380+450+420+400 || r.CacheWriteTokens != 9200+900+2100+700+650 || r.CacheReadTokens != 9200+10100+12200+12900 || r.OutputTokens != 61+74+301+58+92 {
		t.Fatalf("token totals: in %d write %d read %d out %d", r.InputTokens, r.CacheWriteTokens, r.CacheReadTokens, r.OutputTokens)
	}
	var calls int
	for _, ev := range x.events(x.run) {
		if ev.Type != store.EventAssistantMessage {
			continue
		}
		calls++
		var p struct {
			Usage struct {
				Write int64 `json:"cache_write_tokens"`
				Read  int64 `json:"cache_read_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Usage.Write+p.Usage.Read == 0 {
			t.Errorf("call %d: no cache numbers in the event: %s", calls, ev.Payload)
		}
	}
	if calls != 5 {
		t.Fatalf("%d model calls", calls)
	}
	if r.CostMicros <= 0 || r.CostMicros >= r.BudgetMicros {
		t.Fatalf("cost %d of budget %d", r.CostMicros, r.BudgetMicros)
	}

	// Version 1 exists, is valid, is in the project's format and uses only the project's footage.
	var spec json.RawMessage
	var count int
	if err := x.pool.QueryRow(context.Background(), `SELECT count(*), max(spec::text)::jsonb FROM timeline_versions WHERE project_id = $1`, x.prj).Scan(&count, &spec); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d versions, want 1", count)
	}
	vd, perr := x.svc.Version(context.Background(), projects.Actor{Workspace: x.wsA.ID}, x.prj, 1)
	if perr != nil {
		t.Fatal(perr)
	}
	for _, is := range vd.Issues {
		if is.Severity == timeline.SeverityError {
			t.Errorf("the version has an error: %+v", is)
		}
	}
	parsed, err := timeline.Parse(vd.Version.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Output.Format != timeline.Format("9:16") || parsed.Output.Width != 1080 || parsed.Output.Height != 1920 {
		t.Errorf("output: %+v", parsed.Output)
	}
	main, _, _ := timeline.MainTrack(parsed)
	if len(main.Items) != 3 {
		t.Fatalf("%d clips", len(main.Items))
	}
	allowed := map[string]bool{clipA.String(): true, clipB.String(): true, clipC.String(): true}
	for _, it := range main.Items {
		if !allowed[it.AssetID] {
			t.Errorf("the timeline uses %s, which is not the project's footage", it.AssetID)
		}
	}
	if d := timeline.TotalDuration(parsed); d != 17000 {
		t.Errorf("duration %d ms", d)
	}
	if vd.Version.AuthorKind != "agent" || !strings.HasPrefix(vd.Version.AuthorID, x.run.ID.String()+":toolu_") {
		t.Errorf("author: %s %s", vd.Version.AuthorKind, vd.Version.AuthorID)
	}

	// The view_clip result is in the run's events as references, and the frames are in the object store under the asset's prefix.
	var sawFrames int
	for _, ev := range x.events(x.run) {
		if ev.Type != store.EventToolResult {
			continue
		}
		if len(ev.Payload) > store.MaxEventPayloadBytes {
			t.Errorf("event %d is %d bytes", ev.Seq, len(ev.Payload))
		}
		var p struct {
			Name    string `json:"name"`
			Content []struct{ Type, Ref string }
		}
		_ = json.Unmarshal(ev.Payload, &p)
		for _, b := range p.Content {
			if b.Type == "image" {
				sawFrames++
				if !strings.HasPrefix(b.Ref, "ws/"+x.wsA.ID.String()+"/assets/"+clipA.String()+"/frames/") {
					t.Errorf("frame reference %q", b.Ref)
				}
				if _, ok := x.objs.m[b.Ref]; !ok {
					t.Errorf("%s is not in the object store", b.Ref)
				}
			}
		}
		if strings.Contains(string(ev.Payload), "base64") {
			t.Errorf("event %d carries image bytes", ev.Seq)
		}
	}
	if sawFrames != 6 {
		t.Errorf("%d frames in the events, want 6", sawFrames)
	}
	// And the artifact of the edit.
	var artifacts int
	for _, ev := range x.events(x.run) {
		var p struct {
			Kind string `json:"kind"`
			Data struct {
				Version int `json:"version"`
			} `json:"data"`
		}
		if ev.Type == store.EventArtifact && json.Unmarshal(ev.Payload, &p) == nil && p.Kind == "timeline_version" && p.Data.Version == 1 {
			artifacts++
		}
	}
	if artifacts != 1 {
		t.Errorf("%d timeline_version artifacts", artifacts)
	}
}

func (x *e2e) events(run store.AgentRun) []store.AgentEvent {
	x.t.Helper()
	ev, err := x.st.AgentEvents(context.Background(), x.wsA.ID, run.ID, 0, 0)
	if err != nil {
		x.t.Fatal(err)
	}
	return ev
}
