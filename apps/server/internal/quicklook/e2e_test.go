package quicklook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	configfiles "github.com/nilpntr/content-fabrique/config"
	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Run(m)) }

// The committed cassette lives in testdata/cassettes/quicklook/<key>.json. It was recorded by TestRecordAgainstAFakeTransport
// (record mode over a fake HTTP transport, never the real API) and CI replays it. A change to the prompt, the tool schema,
// the route or the frames the fixtures produce changes the key: that test then fails and says to re-record with
//
//	UPDATE_CASSETTES=1 go test ./internal/quicklook -run TestRecordAgainstAFakeTransport
const cassetteDir = "testdata/cassettes"

func TestRecordAgainstAFakeTransport(t *testing.T) {
	tmp := t.TempDir()
	r := newRig(t, rigOptions{mode: llm.ModeRecord, cassetteDir: tmp})
	res := mustRun(t, r, Options{})
	if res.Outcome != OutcomeDone {
		t.Fatalf("result = %+v", res)
	}

	// The call went through the fake transport and was metered like a live one.
	if len(r.api.messageCalls()) != 1 {
		t.Fatalf("%d messages calls", len(r.api.messageCalls()))
	}
	rows := r.ledger.snapshot()
	if len(rows) != 1 || rows[0].Stage != "quicklook" || rows[0].BilledTo != "test" || rows[0].RunID != r.asset.ID.UUID || rows[0].ListCostMicros <= 0 || rows[0].ListCostMicros > MaxMicros {
		t.Fatalf("ledger rows = %+v", rows)
	}

	// The cassette: one file named by the request key, holding no key, no header and no picture bytes.
	files, _ := filepath.Glob(filepath.Join(tmp, Stage, "*.json"))
	if len(files) != 1 {
		t.Fatalf("recorded %d cassettes, want 1: %v", len(files), files)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testKey, "x-api-key", "Authorization", string(fixtureFrame(1)[:20])} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the cassette holds %q", secret)
		}
	}
	if !strings.Contains(string(body), `"count_tokens": 1750`) || !strings.Contains(string(body), "describe_clip") {
		t.Errorf("the cassette lacks the stored token count or the answer:\n%s", body)
	}

	name := filepath.Base(files[0])
	committed := filepath.Join(cassetteDir, Stage, name)
	if os.Getenv("UPDATE_CASSETTES") == "1" {
		if err := os.RemoveAll(filepath.Join(cassetteDir, Stage)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(committed), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(committed, body, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", committed)
		return
	}
	if _, err := os.Stat(committed); err != nil {
		t.Fatalf("the committed cassette %s is missing: the request changed (prompt, schema, route or frames). Re-record: UPDATE_CASSETTES=1 go test ./internal/quicklook -run TestRecordAgainstAFakeTransport", committed)
	}
	if entries, _ := filepath.Glob(filepath.Join(cassetteDir, Stage, "*.json")); len(entries) != 1 {
		t.Errorf("%d committed cassettes, want exactly 1 (a stale one is left over): %v", len(entries), entries)
	}
}

// CI replays. This is the whole path of the criterion: a video is marked ready by the ingest's own transaction, the job it
// enqueued runs on its queue, the model is replayed from the cassette (no HTTP at all), and the asset has a title and labels and
// is found by a search for "row".
func TestReplayEndToEndFromIngestReadyToSearch(t *testing.T) {
	ctx := context.Background()
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	st, err := store.Open(ctx, dsn, store.Options{AppName: "quicklook-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	super, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(super.Close)

	ws, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_quicklook", Name: "Quick look", Region: "eu", BillingMode: store.BillingManaged})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_quicklook_other", Name: "Other", Region: "eu", BillingMode: store.BillingManaged})
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.UpsertUserByClerkID(ctx, "user_quicklook", "quick@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// An uploaded video, claimed by the ingest.
	id, err := st.NewAssetID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateAssetWithUpload(ctx, ws.ID, store.NewAsset{
		ID: id, Kind: store.AssetKindVideo, Filename: "IMG_0042.MOV", ContentType: "video/quicktime", SizeBytes: 40 << 20, Title: "IMG_0042",
		R2Key: storage.OriginalKey(ws.ID, id), CreatedBy: user.ID, R2UploadID: "r2", PartSize: 16 << 20, PartCount: 3, ExpiresAt: time.Now().Add(time.Hour),
	}, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(tx *store.Store) error { _, err := tx.CompleteUpload(ctx, ws.ID, id); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAssetForIngest(ctx, ws.ID, id); err != nil {
		t.Fatal(err)
	}

	// The service, replaying: any HTTP request fails the test.
	objs := newMemObjects()
	proxyKey := storage.AssetPrefix(ws.ID, id) + "proxy_720.mp4"
	objs.objs[proxyKey] = []byte("the fake sandbox never reads this")
	api := &fakeAPI{forbid: t, reply: goodReply, tokens: 1750}
	ledger := &fakeLedger{}
	factory := newFactory(t, rigOptions{mode: llm.ModeReplay, cassetteDir: cassetteDir, profile: "dev-cheap"}, ledger, api)
	router, err := llm.NewRouter(configfiles.Routing(), "dev-cheap")
	if err != nil {
		t.Fatal(err)
	}
	frames := &editor.FrameService{Store: st, Objects: objs, Runner: &fakeRunner{}, TempDir: t.TempDir(), Logger: quiet}
	svc, err := New(Config{Store: st, Objects: objs, Frames: frames, Models: factory, Router: router, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}

	w, err := jobs.StartWorker(ctx, jobs.WorkerOptions{DirectURL: dsn, Store: st, Logger: quiet, QuickLook: svc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(context.Background()); err != nil {
			t.Errorf("worker stop: %v", err)
		}
	})

	// The ingest finishes: ready, the quick look owed and its job enqueued in the same transaction.
	finish := store.IngestResult{
		DurationMs: 12_000, Width: 1080, Height: 1920, FPS: 30, VideoCodec: "h264",
		Derivatives: []store.NewDerivative{{Kind: store.DerivativeProxy720, Key: proxyKey, ContentType: "video/mp4", SizeBytes: 33, Meta: []byte(`{"duration_ms":12000}`)}},
	}
	start := time.Now()
	ready, err := st.FinishAssetIngest(ctx, ws.ID, id, finish)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != store.AssetReady || ready.QuickLookStatus == nil || *ready.QuickLookStatus != store.QuickLookPending {
		t.Fatalf("after the ingest: status %s, quick look %v", ready.Status, ready.QuickLookStatus)
	}
	jobsFor := func() int {
		var n int
		err := super.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'quick_look' AND queue = $1 AND args->>'asset_id' = $2`,
			jobs.QueueQuickLook, id.String()).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := jobsFor(); n != 1 {
		t.Fatalf("%d quick_look jobs on the quicklook queue for the asset, want 1", n)
	}

	var got store.Asset
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err = st.GetAsset(ctx, ws.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.QuickLookStatus != nil && *got.QuickLookStatus != store.QuickLookPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the quick look did not finish within 30 s (asset: %+v)", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("quick look finished %s after the ingest", time.Since(start).Round(time.Millisecond))
	if *got.QuickLookStatus != store.QuickLookSucceeded {
		t.Fatalf("quick look %s: %s", *got.QuickLookStatus, got.QuickLook)
	}
	if got.Title != "Barbell row" || got.TitleSource != store.TitleFromQuickLook || !strings.Contains(got.Description, "barbell rows") ||
		got.Exercise != "barbell row" || strings.Join(got.Facets, ",") != "type:lift,body:back,movement:row,angle:side,equipment:barbell" {
		t.Fatalf("asset = title %q (%s), description %q, exercise %q, facets %v", got.Title, got.TitleSource, got.Description, got.Exercise, got.Facets)
	}

	// Searching "row" finds it, in its own workspace only.
	found, err := st.SearchAssets(ctx, ws.ID, store.SearchWords("row"), nil, 10)
	if err != nil || len(found) != 1 || found[0].ID != id {
		t.Fatalf("search for row = %d assets, %v", len(found), err)
	}
	if found, _ := st.SearchAssets(ctx, other.ID, store.SearchWords("row"), nil, 10); len(found) != 0 {
		t.Fatalf("another workspace found the clip: %v", found)
	}
	// The labels are searchable by their words ("lifting", "side view") and filter the library.
	for _, q := range []string{"lifting", "side view", "barbell row"} {
		if found, err := st.SearchAssets(ctx, ws.ID, store.SearchWords(q), nil, 10); err != nil || len(found) != 1 {
			t.Errorf("search for %q = %d assets, %v", q, len(found), err)
		}
	}
	if found, err := st.FindAssets(ctx, ws.ID, store.AssetQuery{Facets: []string{"body:back", "angle:side"}, Limit: 10}); err != nil || len(found) != 1 {
		t.Errorf("filter back + side = %d assets, %v", len(found), err)
	}
	if found, _ := st.FindAssets(ctx, ws.ID, store.AssetQuery{Facets: []string{"body:back", "angle:front"}, Limit: 10}); len(found) != 0 {
		t.Errorf("filter back + front found the side view: %v", found)
	}

	// CI made no model call, and a replay writes no ledger row (nothing was spent).
	if api.total() != 0 || len(ledger.snapshot()) != 0 {
		t.Errorf("replay reached the API %d times and wrote %d ledger rows", api.total(), len(ledger.snapshot()))
	}

	// The frames are cached for the editor to reuse, in the asset's own frames/ prefix, and the detail lists the four chosen.
	var d detail
	if err := json.Unmarshal(got.QuickLook, &d); err != nil || len(d.Frames) != Frames {
		t.Fatalf("detail = %s %v", got.QuickLook, err)
	}
	for _, f := range d.Frames {
		if _, ok := objs.objs[f.Ref]; !ok || !strings.HasPrefix(f.Ref, storage.FramePrefix(ws.ID, id)) || !strings.HasSuffix(f.Ref, "_512.jpg") {
			t.Errorf("frame %+v is not cached under the asset's frames/ prefix at 512 px", f)
		}
	}

	// A re-ingest does not run the quick look again.
	if _, err := st.ClaimAssetForIngest(ctx, ws.ID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAssetIngest(ctx, ws.ID, id, finish); err != nil {
		t.Fatal(err)
	}
	if n := jobsFor(); n != 1 {
		t.Errorf("%d quick_look jobs after a re-ingest, want still 1", n)
	}
	if again, _ := st.GetAsset(ctx, ws.ID, id); again.Title != "Barbell row" || *again.QuickLookStatus != store.QuickLookSucceeded {
		t.Errorf("the re-ingest changed the quick look: %+v", again)
	}
}

// Replay with a request nobody recorded is an error naming the cassette, never a call to the API.
func TestReplayMissIsAFailedQuickLookNotACall(t *testing.T) {
	api := &fakeAPI{forbid: t, reply: goodReply, tokens: 1750}
	r := newRig(t, rigOptions{mode: llm.ModeReplay, cassetteDir: t.TempDir(), api: api})
	res := mustRun(t, r, Options{})
	if res.Outcome != OutcomeFailed || res.Reason != FailReplayMiss || api.total() != 0 {
		t.Errorf("result = %+v, %d API calls", res, api.total())
	}
}
