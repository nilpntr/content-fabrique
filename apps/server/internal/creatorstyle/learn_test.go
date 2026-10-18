package creatorstyle

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// recorder is a job inserter that keeps what was enqueued (no River in these tests).
type recorder struct {
	mu   sync.Mutex
	args []river.JobArgs
}

func (r *recorder) InsertInStore(_ context.Context, _ *store.Store, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, args)
	return &rivertype.JobInsertResult{}, nil
}

type summaryModel struct {
	mu    sync.Mutex
	calls int
}

func (m *summaryModel) Messages(context.Context, llm.Request) (llm.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	in := `{"summary":"Long takes, music on every video, cream capitals at the top.","vibe":"lively","transitions":"subtle","text_animation":"on","camera_motion":"subtle","title_colour":"#F2E9A0","title_position":"top","title_case":"upper","music":"always","pace":"like_mine","chapter_titles":"off","ending_hold":"long","cut_timing":"full_reps","angle_switches":"off"}`
	return llm.Response{Model: "claude-haiku-5-5", StopReason: "tool_use", Content: []llm.Block{{Type: llm.BlockToolUse, ID: "t", Name: styleprofile.SummaryToolName, Input: json.RawMessage(in)}}}, nil
}

func (m *summaryModel) CountTokens(context.Context, llm.Request) (int64, error) { return 100, nil }

type modelFactory struct{ m *summaryModel }

func (f modelFactory) For(context.Context, ids.WorkspaceID, string) (llm.Client, llm.Billing, error) {
	return f.m, llm.BilledToTest, nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool)
}

func referenceMeasures(t *testing.T) []styleprofile.Measures {
	t.Helper()
	raw, err := os.ReadFile("../styleprofile/testdata/reference_measures.json")
	if err != nil {
		t.Fatal(err)
	}
	var ms []styleprofile.Measures
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

// The profile job makes the profile and one summary from the measured references, does nothing (no model call) when the profile is
// made from them already, and makes it again when a reference comes; the style reads "learning" until it has. Another workspace sees
// nothing of it.
func TestLearnMakesTheProfileOnceAndTheViewFollowsIt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	wa, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_style_a", Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	wb, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_style_b", Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	ms := referenceMeasures(t)
	for _, m := range ms[:4] {
		raw, _ := json.Marshal(m)
		if _, err := st.InsertFileStyleReference(ctx, wa.ID, m.Name, raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	svc, err := NewService(st, nil, rec, quiet)
	if err != nil {
		t.Fatal(err)
	}
	v, fail := svc.Get(ctx, Actor{Workspace: wa.ID})
	if fail != nil || v.Status != StatusLearning || v.Done != 4 || v.Total != 4 {
		t.Fatalf("before the profile: %+v %v", v, fail)
	}

	router, err := llm.NewRouter(configfiles.Routing(), "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	model := &summaryModel{}
	deps := LearnDeps{Store: st, Writer: &styleprofile.Writer{Models: modelFactory{model}, Router: router}, Logger: quiet}
	res, err := Learn(ctx, deps, wa.ID, false)
	if err != nil || res.Outcome != "learnt" || res.References != 4 || res.Summary != "written" {
		t.Fatalf("learn: %+v %v", res, err)
	}
	if res, err := Learn(ctx, deps, wa.ID, false); err != nil || res.Outcome != "up to date" || model.calls != 1 {
		t.Fatalf("learning again with nothing new: %+v %v (%d calls)", res, err, model.calls)
	}
	v, _ = svc.Get(ctx, Actor{Workspace: wa.ID})
	if v.Status != StatusReady || v.Style.Summary == nil || v.Style.Profile.References != 4 || v.LearnedAt == nil {
		t.Fatalf("after: %+v", v)
	}
	// The summary's suggestion is a layer: the effective title colour is the suggested one.
	if v.Style.Resolved.TitleColour != "#F2E9A0" || v.Style.Resolved.Sources.TitleColour != styleprofile.SourceSuggested {
		t.Errorf("effective %+v", v.Style.Resolved)
	}

	// A fifth reference: learning until the profile is made again.
	raw, _ := json.Marshal(ms[4])
	if _, err := st.InsertFileStyleReference(ctx, wa.ID, ms[4].Name, raw, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ = svc.Get(ctx, Actor{Workspace: wa.ID}); v.Status != StatusLearning {
		t.Errorf("a new reference: %s", v.Status)
	}
	if res, err := Learn(ctx, deps, wa.ID, false); err != nil || res.References != 5 || model.calls != 2 {
		t.Fatalf("a new reference: %+v %v", res, err)
	}

	// Workspace B has nothing.
	vb, _ := svc.Get(ctx, Actor{Workspace: wb.ID})
	if vb.Status != StatusEmpty || len(vb.References) != 0 || vb.Style.Profile != nil {
		t.Errorf("workspace b: %+v", vb)
	}
	if res, err := Learn(ctx, deps, wb.ID, false); err != nil || res.Outcome != "nothing to learn" || model.calls != 2 {
		t.Errorf("workspace b: %+v %v", res, err)
	}
	// Learning again (the creator's button) re-queues the profile; there is no asset reference to measure again.
	rec.args = nil
	if _, fail := svc.Relearn(ctx, Actor{Workspace: wa.ID}); fail != nil {
		t.Fatal(fail)
	}
	if len(rec.args) != 1 || rec.args[0].Kind() != "style_profile" {
		t.Errorf("relearn enqueued %+v", rec.args)
	}
}
