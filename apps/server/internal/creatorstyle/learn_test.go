package creatorstyle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
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

// readyReference makes a style reference asset whose ingest finished: its row is queued, as after an upload.
func readyReference(t *testing.T, st *store.Store, ws ids.WorkspaceID, user ids.UserID, name string) ids.AssetID {
	t.Helper()
	ctx := context.Background()
	id, err := st.NewAssetID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateAssetWithUpload(ctx, ws, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: name, ContentType: "video/mp4", SizeBytes: 1 << 20, Title: name,
		R2Key: "ws/" + ws.String() + "/assets/" + id.String() + "/original", CreatedBy: user, Purpose: store.PurposeStyleReference,
		R2UploadID: "r2-" + id.String(), PartSize: 16 << 20, PartCount: 1, ExpiresAt: time.Now().Add(time.Hour),
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteUpload(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAssetForIngest(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAssetIngest(ctx, ws, id, store.IngestResult{DurationMs: 20000, Width: 1080, Height: 1920}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A pass of "learn again" makes one profile and one summary, when every reference is measured again: until then the profile job waits
// and the editor keeps the profile and summary it had (nothing is cleared or summarised from a part of the set). A second pass is
// refused while one is in flight, the passes are capped per day, a pass whose references all fail keeps the profile, and a row left
// queued by a job that gave up long ago holds nothing back.
func TestALearnPassMakesOneSummaryAndKeepsTheProfileUntilItIsDone(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ws, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_style_pass", Name: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UpsertUserByClerkID(ctx, "user_style_pass", "style-pass@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ms := referenceMeasures(t)
	refs := []ids.AssetID{readyReference(t, st, ws.ID, u.ID, "one.mp4"), readyReference(t, st, ws.ID, u.ID, "two.mp4")}
	measure := func(i int) {
		t.Helper()
		raw, _ := json.Marshal(ms[i])
		if _, err := st.FinishStyleReference(ctx, ws.ID, refs[i], raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	router, err := llm.NewRouter(configfiles.Routing(), "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	model := &summaryModel{}
	deps := LearnDeps{Store: st, Writer: &styleprofile.Writer{Models: modelFactory{model}, Router: router}, Logger: quiet}
	svc, err := NewService(st, nil, &recorder{}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	who := Actor{Workspace: ws.ID, User: u.ID}

	// The first upload: one reference measured, the other still queued. The job waits.
	measure(0)
	if _, err := Learn(ctx, deps, ws.ID, false); !errors.Is(err, jobs.ErrStyleLearning) || model.calls != 0 {
		t.Fatalf("with a reference queued: %v (%d calls)", err, model.calls)
	}
	measure(1)
	if res, err := Learn(ctx, deps, ws.ID, false); err != nil || res.Outcome != "learnt" || res.References != 2 || model.calls != 1 {
		t.Fatalf("learn: %+v %v (%d calls)", res, err, model.calls)
	}
	before, err := st.GetCreatorStyle(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Learning again: the references go back to the queue; a second pass is refused while they are.
	if _, fail := svc.Relearn(ctx, who); fail != nil {
		t.Fatal(fail)
	}
	if _, fail := svc.Relearn(ctx, who); fail == nil || fail.Status != 409 || fail.Code != api.ErrorCodeStyleLearningInProgress {
		t.Fatalf("a second pass in flight: %v", fail)
	}
	measure(0)
	for _, step := range []string{"one measured", "none measured"} {
		if _, err := Learn(ctx, deps, ws.ID, false); !errors.Is(err, jobs.ErrStyleLearning) {
			t.Fatalf("%s: %v", step, err)
		}
	}
	mid, err := st.GetCreatorStyle(ctx, ws.ID)
	if err != nil || string(mid.Summary) != string(before.Summary) || !slices.Equal(mid.ReferenceIds, before.ReferenceIds) || model.calls != 1 {
		t.Fatalf("the profile changed during the pass: %v (%d calls)", err, model.calls)
	}
	if v, _ := svc.Get(ctx, who); v.Status != StatusLearning || v.Style.Profile == nil || v.Style.Summary == nil {
		t.Errorf("during the pass the style is %s, profile %v", v.Status, v.Style.Profile != nil)
	}
	measure(1)
	if res, err := Learn(ctx, deps, ws.ID, false); err != nil || res.Outcome != "learnt" || model.calls != 2 {
		t.Fatalf("the pass done: %+v %v (%d calls)", res, err, model.calls)
	}
	if res, err := Learn(ctx, deps, ws.ID, false); err != nil || res.Outcome != "up to date" || model.calls != 2 {
		t.Fatalf("again: %+v %v (%d calls)", res, err, model.calls)
	}
	if v, _ := svc.Get(ctx, who); v.Status != StatusReady {
		t.Errorf("after the pass: %s", v.Status)
	}

	// A pass whose references all fail keeps the profile the editor has.
	if _, fail := svc.Relearn(ctx, who); fail != nil {
		t.Fatal(fail)
	}
	for _, id := range refs {
		if _, err := st.FailStyleReference(ctx, ws.ID, id, FailAnalysisFailed, nil); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := Learn(ctx, deps, ws.ID, false); err != nil || res.Outcome != "kept: no reference was measured" || model.calls != 2 {
		t.Fatalf("every reference failed: %+v %v", res, err)
	}
	if _, err := st.GetCreatorStyle(ctx, ws.ID); err != nil {
		t.Fatalf("the profile went: %v", err)
	}

	// The third pass of the day is the last (no price list: the default of 3).
	if _, fail := svc.Relearn(ctx, who); fail != nil {
		t.Fatal(fail)
	}
	for _, id := range refs {
		if _, err := st.FailStyleReference(ctx, ws.ID, id, FailAnalysisFailed, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, fail := svc.Relearn(ctx, who); fail == nil || fail.Status != 429 || fail.Code != api.ErrorCodeTooManyRelearns {
		t.Fatalf("a fourth pass: %v", fail)
	}

	// Tomorrow: a pass again; its rows are left queued by jobs that gave up, and a day later they hold nothing back.
	tomorrow := time.Now().Add(24 * time.Hour)
	svc.Now = func() time.Time { return tomorrow }
	if _, fail := svc.Relearn(ctx, who); fail != nil {
		t.Fatalf("the next day: %v", fail)
	}
	deps.Now = func() time.Time { return tomorrow.Add(store.StyleInFlightMaxAge + time.Hour) }
	if res, err := Learn(ctx, deps, ws.ID, false); err != nil || res.Outcome != "kept: no reference was measured" {
		t.Fatalf("stale rows: %+v %v", res, err)
	}
}
