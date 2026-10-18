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
	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/credits/creditstest"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/spend/spendtest"
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
	// cassetteRunBudget is the dollar budget (in micro-dollars) of the run the cassettes were recorded with: the editor's own default.
	cassetteRunBudget = 5_000_000
	editorStage       = "editor"
)

// The generated project: three clips with fixed ids (the ids are part of what the model reads, so the cassettes need them fixed).
var (
	clipA = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000a")}
	clipB = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000b")}
	clipC = ids.AssetID{UUID: uuid.MustParse("01930000-0000-7000-8000-00000000000c")}
)

const projectBrief = "A 20 second back-day short. Open on the heaviest set. Keep it punchy."

// fakeAnthropic plays the Messages API for the editor: find_moments, view_clip, edit_timeline, get_timeline, render_preview, finish.
type fakeAnthropic struct {
	mu       sync.Mutex
	messages int
	requests []map[string]any
	bodies   []string
	// scale multiplies the cache and output tokens of every turn (0 and 1: as scripted), to make a scripted run cost what a real Opus run
	// does; an answer never has more output tokens than the max_tokens the request allowed.
	scale int
	// lastMax is the max_tokens of the newest request.
	lastMax int
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
		f.lastMax, _ = req["max_tokens"].(int)
		if mt, ok := req["max_tokens"].(float64); ok {
			f.lastMax = int(mt)
		}
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

// The model's turns, scripted: understand (find_moments, view_clip), plan, edit, preview, check, one revision, preview, check, finish.
// Each turn has its usage (input, cache write, cache read, output tokens): the first call writes the cache (system prompt, tools and run
// context), the later ones read it and write the new turn.
var turnUsage = [][4]int{
	{410, 9200, 0, 61}, {380, 900, 9200, 74}, {430, 700, 10100, 210}, {450, 2100, 10800, 301}, {410, 600, 12900, 40},
	{420, 700, 13500, 38}, {440, 800, 14200, 120}, {400, 600, 15000, 40}, {420, 700, 15600, 38}, {400, 650, 16300, 92},
}

// planInput is the creative plan of the scripted run: a workout of 14 seconds at 124 BPM.
func planInput() string {
	a, b, c := `"`+clipA.String()+`"`, `"`+clipB.String()+`"`, `"`+clipC.String()+`"`
	return `{"content_type":"workout","story":"A heavy lat day in six quick cuts, ending on the wide gym.","hook":"The lat pulldown at full stretch, with the words HEAVY LAT DAY.",` +
		`"arc":"Hook on the heaviest rep, the cable row, a second look at the pulldown, the wide gym and a last pulldown rep.","ending":"Back to the cable row, held a little longer.",` +
		`"playbook":"workout","target_length_s":14,"tempo_bpm":124,"pace":"building","shots":[` +
		`{"role":"hook","what":"Heaviest pulldown rep, first half","asset_id":` + a + `,"beats":3,"effect":"punch_in"},` +
		`{"role":"build","what":"Cable row","asset_id":` + b + `,"beats":4},` +
		`{"role":"peak","what":"The first pulldown rep, first half","asset_id":` + a + `,"beats":3},` +
		`{"role":"detail","what":"The wide gym","asset_id":` + c + `,"beats":6,"effect":"flash"},` +
		`{"role":"peak","what":"Last pulldown rep, second half","asset_id":` + a + `,"beats":3},` +
		`{"role":"ending","what":"Cable row, held","asset_id":` + b + `,"beats":6}]}`
}

// editInput is the first edit: the clips, the flash into the wide shot, the beat grid, the punch-in and the title, in the order of
// the prompt's "order of the ops".
func editInput() string {
	a, b, c := clipA.String(), clipB.String(), clipC.String()
	clipOp := func(asset string, in, out int, extra string) string {
		return fmt.Sprintf(`{"op":"add_clip","asset_id":%q,"source":{"in_ms":%d,"out_ms":%d}%s}`, asset, in, out, extra)
	}
	return `{"base_version":0,"ops":[` +
		clipOp(a, 5000, 6500, `,"why":"The heaviest rep of the set, half of it, so it opens the video."`) + `,` +
		clipOp(b, 1000, 3000, "") + `,` +
		clipOp(a, 2000, 3500, "") + `,` +
		clipOp(c, 3000, 6000, `,"reframe":{"mode":"blur_fill"}`) + `,` +
		clipOp(a, 6500, 8000, "") + `,` +
		clipOp(b, 4000, 7000, "") + `,` +
		`{"op":"set_transition","id":"c4","type":"flash","duration_ms":150},` +
		`{"op":"snap_cuts_to_beats","grid":{"bpm":124,"first_beat_ms":0},"options":{"write_markers":true,"max_shift_ms":80}},` +
		`{"op":"set_motion","id":"c1","preset":"punch_in","at_ms":484},` +
		`{"op":"add_text","text":"HEAVY LAT DAY","style":"title_bold","at_ms":0,"duration_ms":2000,"anchor":"upper_third"}]}`
}

// reviseInput is the revision pass after the first check, which said the cuts slow down toward the end: the wide shot loses half of
// its length and the cuts snap to the beat again.
func reviseInput() string {
	return `{"base_version":1,"ops":[{"op":"trim_item","id":"c4","out_ms":4500},` +
		`{"op":"snap_cuts_to_beats","grid":{"bpm":124,"first_beat_ms":0},"options":{"write_markers":true,"max_shift_ms":60}}]}`
}

// turn is the n-th answer.
func (f *fakeAnthropic) turn(n int) string {
	asset := func(a ids.AssetID) string { return `"` + a.String() + `"` }
	u := turnUsage[min(n, len(turnUsage))-1]
	if f.scale > 1 {
		u[1], u[2], u[3] = u[1]*f.scale, u[2]*f.scale, u[3]*f.scale
	}
	if f.lastMax > 0 {
		u[3] = min(u[3], f.lastMax)
	}
	m := func(block string) string { return message(block, u[0], u[1], u[2], u[3]) }
	switch n {
	case 1:
		return m(use("toolu_ed_1", "find_moments", `{"asset_id":`+asset(clipA)+`,"kind":"rep","min_score":0.7}`))
	case 2:
		return m(use("toolu_ed_2", "view_clip", `{"asset_id":`+asset(clipA)+`}`))
	case 3:
		return m(use("toolu_ed_3", "plan_edit", planInput()))
	case 4:
		return m(use("toolu_ed_4", "edit_timeline", editInput()))
	case 5:
		return m(use("toolu_ed_5", "render_preview", `{}`))
	case 6:
		return m(use("toolu_ed_6", "check_edit", `{}`))
	case 7:
		return m(use("toolu_ed_7", "edit_timeline", reviseInput()))
	case 8:
		return m(use("toolu_ed_8", "render_preview", `{}`))
	case 9:
		return m(use("toolu_ed_9", "check_edit", `{}`))
	default:
		return m(use("toolu_ed_10", "finish", `{"summary":"I cut a 12 second video from your three clips. It opens on half of the heaviest lat pulldown rep with the words HEAVY LAT DAY and a punch-in on the beat, then the cable row, the pulldown again, the wide gym with a flash and a last rep. Check the wide shot: I framed it with a blurred fill so nobody is cropped out."}`))
	}
}

func message(block string, in, cacheWrite, cacheRead, out int) string {
	return `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[` + block + `],"stop_reason":"tool_use","stop_sequence":null,` +
		fmt.Sprintf(`"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":0},"output_tokens":%d}}`, in, cacheWrite, cacheRead, cacheWrite, out)
}

// e2e is one editor run over the generated project.
type e2e struct {
	*env
	prj    ids.ProjectID
	run    store.AgentRun
	runner *runtime.Runner
	api    *fakeAnthropic
}

// e2eOption changes how newE2E starts the run.
type e2eOption func(*e2eConfig)

type e2eConfig struct {
	// profile is the routing profile of the run ("" the default, dev-cheap).
	profile string
	// planBudgets starts the run with the plan's real budgets (no pin to the cassettes' $5.00).
	planBudgets bool
	// charged gives the project an edit that was charged first, so that the run is a revision.
	charged bool
	// quality is the quality of the run (credits.Standard when empty).
	quality credits.Quality
}

// newE2E seeds the project and builds the runtime over the real llm layer in the mode the variables name.
func newE2E(t *testing.T, vars map[string]string, api *fakeAnthropic, options ...e2eOption) *e2e {
	t.Helper()
	var cfg e2eConfig
	for _, o := range options {
		o(&cfg)
	}
	e := newEnv(t)
	e.asset(e.wsA, clip{id: clipA, title: "Lat pulldown", desc: "Top set, 8 reps, heavy.", durMs: 12000})
	e.asset(e.wsA, clip{id: clipB, title: "Cable row", desc: "Two sets of 10.", durMs: 9000})
	e.asset(e.wsA, clip{id: clipC, title: "Gym wide", w: 1920, h: 1080, durMs: 15000, silent: true})
	// Things the editor must never reach: a clip of another workspace, and one of another project of this workspace.
	e.asset(e.wsB, clip{title: "private clip of workspace B"})
	e.asset(e.wsA, clip{title: "private clip of another project"})
	// The analysis of the heaviest set: two clean reps from 2 s to 8 s, rest before and after.
	res := store.AnalysisResult{Version: 1, StepMs: 100, DurationMs: 12000, Series: store.PackedSeries{}, Shots: []store.NewShot{{Idx: 0, StartMs: 0, EndMs: 12000, Signals: []byte(`{}`)}},
		Summary: []byte(`{"version":1,"duration_ms":12000,"shots":1,"sets":1,"reps":2,"clean_reps":2,"period_ms":3000,"dead_ms":4000,"mean_y":0.3,"exposure_level":"ok","exposure_gain":1.2,"best_rep":{"kind":"rep","start_ms":5000,"end_ms":8000,"score":0.88}}`),
		Moments: []store.NewMoment{
			{Kind: store.MomentRep, StartMs: 2000, EndMs: 5000, Score: 0.81, Attrs: []byte(`{}`), Description: "rep 1 of 2, clean, full range"},
			{Kind: store.MomentRep, StartMs: 5000, EndMs: 8000, Score: 0.88, Attrs: []byte(`{}`), Description: "rep 2 of 2, clean, full range"},
			{Kind: store.MomentDead, StartMs: 0, EndMs: 2000, Attrs: []byte(`{}`), Description: "dead time, 2.0 s: still between sets"},
			{Kind: store.MomentDead, StartMs: 8000, EndMs: 12000, Attrs: []byte(`{}`), Description: "dead time, 4.0 s: still between sets"},
		}}
	if err := e.st.SaveAnalysis(context.Background(), e.wsA.ID, clipA, res); err != nil {
		t.Fatal(err)
	}
	x := &e2e{env: e, api: api}
	x.prj = e.project(e.wsA, "9:16", projectBrief, clipA, clipB, clipC)
	// The preview renderer answers with a contact sheet the image resolver can read.
	e.sheet(x.prj)
	e.objs.set(string(e.preview.ref), testJPEG("contact sheet", 384, 384))

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
		opts.HTTPClient, opts.Ledger = api.client(), &spendtest.Memory{}
	}
	factory, err := llm.NewFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	e.deps.noPlanGate, e.deps.noCheckGate = false, false // the real run: a plan before the first edit, a check before finish
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
	// An editor run is never started without a hold: the workspace has a plan and credits, as it would in production.
	creditstest.Fund(t, e.st, e.wsA.ID, plans.Creator, 50)
	// The cassettes were recorded with a run budget of $5.00 (the editor's default, before the plan set it), and the tool answers say how
	// much of it is left, so the budget is part of every cassette's key: the creator plan's first edit is pinned to it here, and the
	// credits and the hold are the real ones.
	creditsSvc := creditstest.ServiceWith(t, e.st, func(l *credits.PriceList) {
		if !cfg.planBudgets {
			l.Plans[plans.Creator].COGSBudgetMicros[credits.EditFirst] = cassetteRunBudget
		}
	})
	svc := &runtime.Service{Store: e.st, Router: router, Factory: factory, Agents: agents, Inline: true, Credits: creditsSvc}
	if cfg.charged {
		// An earlier edit of the project that was charged (a zero-credit hold, settled), so that this run is the project's revision.
		earlier := uuid.Must(uuid.NewV7())
		if _, err := e.st.HoldCredits(context.Background(), store.HoldCreditsParams{
			Workspace: e.wsA.ID, Action: store.ActionEditFirst, ActionID: earlier, Credits: 0, Project: x.prj, ListVersion: "test", Now: time.Now(), TTL: time.Hour,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.SettleCredits(context.Background(), e.wsA.ID, earlier, true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	input, err := NewInput("Lead with the heaviest set.")
	if err != nil {
		t.Fatal(err)
	}
	if x.run, err = svc.Create(context.Background(), e.st, runtime.StartParams{Agent: Name, WorkspaceID: e.wsA.ID, ProjectID: x.prj.UUID, Input: input, Profile: cfg.profile, Quality: cfg.quality}); err != nil {
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
	if len(fresh) != len(turnUsage) {
		t.Fatalf("recorded %d cassettes, want %d: %v", len(fresh), len(turnUsage), fresh)
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
	if len(api.requests) != len(turnUsage) {
		t.Fatalf("%d model calls, want %d", len(api.requests), len(turnUsage))
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
	if tools, _ := first["tools"].([]any); len(tools) != 13 {
		t.Errorf("%d tools sent", len(tools))
	} else {
		var names []string
		for _, tl := range tools {
			m, _ := tl.(map[string]any)
			name, _ := m["name"].(string)
			names = append(names, name)
		}
		if strings.Join(names, ",") != "get_brief,list_footage,view_clip,find_moments,get_music,plan_edit,get_timeline,get_comments,edit_timeline,match_exposure,render_preview,check_edit,finish" {
			t.Errorf("tools: %v", names)
		}
		editTool, _ := tools[8].(map[string]any)
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
	for _, want := range []string{"Lead with the heaviest set.", `kind=\"instructions\"`, `kind=\"brief\"`, projectBrief, "Footage: 3 clips", clipA.String(), "analysis: 1 set, 2 reps"} {
		if !strings.Contains(string(opening), want) {
			t.Errorf("the first message lacks %q", want)
		}
	}
	// The answer of find_moments is in the next request: ranked reps with exact times, and the dead time to skip.
	for _, want := range []string{"rep 2 of 2, clean, full range", "5000 to 8000 ms", "Dead time to skip"} {
		if !strings.Contains(api.bodies[1], want) {
			t.Errorf("the second request lacks the find_moments answer %q", want)
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
	if !strings.HasPrefix(summary, "I cut a 12 second video") || version == nil || *version != 2 {
		t.Fatalf("output: %s", r.Output)
	}
	if r.ToolCalls != int32(len(turnUsage)) || r.BilledTo != "test" || r.Model != "claude-haiku-5-5" || r.ProjectID != x.prj {
		t.Fatalf("run: %+v", r)
	}

	// The cache numbers are in the run's totals and in every model call's event.
	var in, write, read, out int64
	for _, u := range turnUsage {
		in, write, read, out = in+int64(u[0]), write+int64(u[1]), read+int64(u[2]), out+int64(u[3])
	}
	if r.InputTokens != in || r.CacheWriteTokens != write || r.CacheReadTokens != read || r.OutputTokens != out {
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
	if calls != len(turnUsage) {
		t.Fatalf("%d model calls", calls)
	}
	if r.CostMicros <= 0 || r.CostMicros >= r.BudgetMicros {
		t.Fatalf("cost %d of budget %d", r.CostMicros, r.BudgetMicros)
	}
	// The run was a first edit on a creator plan: its budget is the plan's, and its 6 credits were charged when it succeeded.
	if r.BudgetMicros != cassetteRunBudget {
		t.Fatalf("the run's budget is %d, want the creator plan's budget for a first edit, %d", r.BudgetMicros, cassetteRunBudget)
	}
	bal, err := x.st.CreditBalance(context.Background(), x.wsA.ID, time.Now())
	if err != nil || bal.Available != 44 || bal.Held != 0 {
		t.Fatalf("credits after the run = %+v, %v; want 44 left of 50 and nothing held", bal, err)
	}
	if charged, err := x.st.ProjectHasChargedEdit(context.Background(), x.wsA.ID, x.prj); err != nil || !charged {
		t.Fatalf("the project's edit was not charged: %v %v", charged, err)
	}

	// Version 2 (the revision) exists next to version 1, is valid, is in the project's format and uses only the project's footage.
	var spec json.RawMessage
	var count int
	if err := x.pool.QueryRow(context.Background(), `SELECT count(*), max(spec::text)::jsonb FROM timeline_versions WHERE project_id = $1`, x.prj).Scan(&count, &spec); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("%d versions, want 2", count)
	}
	vd, perr := x.svc.Version(context.Background(), projects.Actor{Workspace: x.wsA.ID}, x.prj, 2)
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
	if len(main.Items) != 6 {
		t.Fatalf("%d clips, want 6", len(main.Items))
	}
	allowed := map[string]bool{clipA.String(): true, clipB.String(): true, clipC.String(): true}
	for _, it := range main.Items {
		if !allowed[it.AssetID] {
			t.Errorf("the timeline uses %s, which is not the project's footage", it.AssetID)
		}
	}
	if d := timeline.TotalDuration(parsed); d != 10850 {
		t.Errorf("duration %d ms", d)
	}
	if vd.Version.AuthorKind != "agent" || vd.Version.AuthorID != ids.Encode(ids.PrefixRun, x.run.ID) {
		t.Errorf("author: %s %s", vd.Version.AuthorKind, vd.Version.AuthorID)
	}

	// The view_clip result is in the run's events as references, and the frames are in the object store under the asset's prefix.
	var sawFrames, sawSheet int
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
			if b.Type == "image" && p.Name == ToolRenderPreview {
				sawSheet++
				continue
			}
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
	if sawSheet != 2 {
		t.Errorf("%d contact sheets in the events, want 2 (one per version)", sawSheet)
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
	checkPlanAndChecks(t, x)
}

func (x *e2e) events(run store.AgentRun) []store.AgentEvent {
	x.t.Helper()
	ev, err := x.st.AgentEvents(context.Background(), x.wsA.ID, run.ID, 0, 0)
	if err != nil {
		x.t.Fatal(err)
	}
	return ev
}

// The credits package prices the runs of the agent it calls "editor"; the two names are one.
func TestTheEditorIsTheAgentThatCostsCredits(t *testing.T) {
	if credits.EditorAgent != Name || !credits.BillsEdits(Name) {
		t.Fatalf("credits.EditorAgent = %q, editor.Name = %q", credits.EditorAgent, Name)
	}
}
