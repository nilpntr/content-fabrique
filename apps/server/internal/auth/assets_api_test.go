package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nilpntr/content-fabrique/internal/agents/agentset"
	"github.com/nilpntr/content-fabrique/internal/agents/runapi"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

const mib = 1 << 20

// assetsEnv is the whole path of the assets API with locally signed tokens: the Clerk middleware over a fake JWKS, the
// paywall, the real handlers and store (Postgres), the real S3 client (a MinIO container) and River's insert client. Two
// workspaces (ada's and bob's) have a plan; ada's has a second member who is only a viewer.
type assetsEnv struct {
	t     *testing.T
	mux   *http.ServeMux
	f     *fixture
	st    *store.Store
	pool  *pgxpool.Pool
	cfg   storage.Config
	objs  *storage.Client
	svc   *assets.Service
	token map[string]string // ada, bob, viewer (in ada's workspace)
	ws    map[string]ids.WorkspaceID

	mu  sync.Mutex
	now time.Time // zero: the real clock
}

// assetsEnvOptions changes the wiring of newAssetsEnv.
type assetsEnvOptions struct {
	noStorage   bool                                // Mount without an assets service
	wrapObjects func(assets.Objects) assets.Objects // sits between the service and the S3 client
	runs        bool                                // Mount with the agent runs service (the editor agent, definition only, replay mode)
	runOptions  runapi.Options
}

func newAssetsEnv(t *testing.T, opts ...func(*assetsEnvOptions)) *assetsEnv {
	t.Helper()
	var o assetsEnvOptions
	for _, fn := range opts {
		fn(&o)
	}
	srv := storetest.Start(t)
	dsn := srv.SuperDSN(srv.NewDatabase(t, ""))
	jobstest.Migrate(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	client, err := jobs.NewInsertClient(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	st.SetWorkspaceCreatedHook(jobs.NewEnqueuer(client, nil).WorkspaceCreated)

	e := &assetsEnv{t: t, st: st, pool: pool, f: newFixture(t), token: map[string]string{}, ws: map[string]ids.WorkspaceID{}}
	if !o.noStorage {
		e.cfg = storagetest.NewBucket(t)
		if e.objs, err = storage.New(e.cfg); err != nil {
			t.Fatal(err)
		}
		var objects assets.Objects = e.objs
		if o.wrapObjects != nil {
			objects = o.wrapObjects(objects)
		}
		if e.svc, err = assets.NewService(st, objects, jobs.NewEnqueuer(client, nil), quiet()); err != nil {
			t.Fatal(err)
		}
		e.svc.Now = e.clock
	}

	dir := newFakeDir()
	for _, u := range []string{"ada", "bob", "vic"} {
		dir.users["user_"+u] = ClerkUser{ID: "user_" + u, Email: u + "@example.com", UpdatedAt: 1}
	}
	dir.orgs["org_ada"] = ClerkOrg{ID: "org_ada", Name: "Ada Studio", UpdatedAt: 1}
	dir.orgs["org_bob"] = ClerkOrg{ID: "org_bob", Name: "Bob Studio", UpdatedAt: 1}
	e.mux = http.NewServeMux()
	var runs *runtime.Service
	if o.runs {
		vars := map[string]string{"CF_ENV": "ci", "LLM_MODE": "replay", "ROUTING_PROFILE": "dev-cheap"}
		rt, err := agentset.Open(agentset.Options{
			Store: st, Logger: quiet(), Getenv: func(k string) string { return vars[k] }, Enqueuer: jobs.NewEnqueuer(client, nil), DefinitionOnly: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		runs = rt.Service
	}
	if _, err := Mount(e.mux, Deps{
		Config:     config.Config{AppOrigin: testOrigin, ClerkSecretKey: "sk_test_unused", ClerkWebhookSecret: testWebhookSecret},
		Store:      st,
		Directory:  dir,
		JWKSClient: &jwks.Client{Backend: e.f.backend},
		Assets:     e.svc,
		Runs:       runs,
		RunOptions: o.runOptions,
	}); err != nil {
		t.Fatal(err)
	}
	tok := func(user, org, role string) string {
		return e.f.token(t, map[string]any{"sub": "user_" + user, "o": map[string]any{"id": org, "rol": role, "slg": "x"}, "exp": time.Now().Add(2 * time.Hour).Unix()})
	}
	e.token["ada"], e.token["bob"], e.token["viewer"] = tok("ada", "org_ada", "admin"), tok("bob", "org_bob", "admin"), tok("vic", "org_ada", "guest")
	for _, who := range []string{"ada", "bob", "viewer"} {
		if rec := e.do(http.MethodGet, "/v1/me", who, nil); rec.Code != http.StatusOK {
			t.Fatalf("/v1/me as %s: %d %s", who, rec.Code, rec.Body)
		}
	}
	for who, org := range map[string]string{"ada": "org_ada", "bob": "org_bob"} {
		w, err := st.GetWorkspaceByClerkOrgID(context.Background(), org)
		if err != nil {
			t.Fatal(err)
		}
		e.ws[who] = w.ID
		if _, err := st.GrantPlan(context.Background(), store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator}); err != nil {
			t.Fatal(err)
		}
	}
	e.ws["viewer"] = e.ws["ada"]
	return e
}

// clock is the service's clock: the real one, or the fixed time a test set.
func (e *assetsEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.now.IsZero() {
		return time.Now()
	}
	return e.now
}

func (e *assetsEnv) setNow(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = t
}

// do sends a request to the API as who ("" sends no token). body is marshalled to JSON unless it is a string or []byte.
func (e *assetsEnv) do(method, path, who string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who != "" {
		req.Header.Set("Authorization", "Bearer "+e.token[who])
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// call is do plus a check of the status and a decode of the JSON answer into out (when out is not nil).
func (e *assetsEnv) call(method, path, who string, body any, wantStatus int, out any) {
	e.t.Helper()
	rec := e.do(method, path, who, body)
	if rec.Code != wantStatus {
		e.t.Fatalf("%s %s as %s = %d, want %d: %s", method, path, who, rec.Code, wantStatus, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: decoding %s: %v", method, path, rec.Body, err)
		}
	}
}

// fail is do plus a check of the status and of the error code.
func (e *assetsEnv) fail(method, path, who string, body any, status int, code api.ErrorCode) {
	e.t.Helper()
	rec := e.do(method, path, who, body)
	wantError(e.t, rec, status, string(code))
}

// create starts an upload of size bytes as who and returns the answer.
func (e *assetsEnv) create(who string, name string, size int64) api.CreateAssetResponse {
	e.t.Helper()
	var out api.CreateAssetResponse
	e.call(http.MethodPost, "/v1/assets", who, api.CreateAssetRequest{Filename: name, SizeBytes: size, ContentType: "video/mp4"}, http.StatusCreated, &out)
	return out
}

// putPart sends the bytes of a part to its presigned URL, as a browser does, and returns the ETag.
func (e *assetsEnv) putPart(p api.PartUrl, data []byte) string {
	e.t.Helper()
	if int64(len(data)) != p.SizeBytes {
		e.t.Fatalf("part %d: %d bytes, the URL is for %d", p.PartNumber, len(data), p.SizeBytes)
	}
	req, err := http.NewRequest(http.MethodPut, p.Url, bytes.NewReader(data))
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("PUT part %d: %s", p.PartNumber, resp.Status)
	}
	return resp.Header.Get("ETag")
}

func partData(n int, size int64) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(n*31 + i%253)
	}
	return b
}

// uploadAll puts every part of a created upload (asking for the URLs past the first batch) and returns the parts to complete with.
func (e *assetsEnv) uploadAll(who string, c api.CreateAssetResponse) []api.CompletedPartRef {
	e.t.Helper()
	urls := map[int]api.PartUrl{}
	for _, p := range c.Parts {
		urls[p.PartNumber] = p
	}
	var missing []int
	for n := 1; n <= c.Upload.PartCount; n++ {
		if _, ok := urls[n]; !ok {
			missing = append(missing, n)
		}
	}
	for len(missing) > 0 {
		batch := missing[:min(len(missing), assets.MaxPartBatch)]
		var out api.PresignPartsResponse
		e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", who, api.PresignPartsRequest{PartNumbers: batch}, http.StatusOK, &out)
		for _, p := range out.Parts {
			urls[p.PartNumber] = p
		}
		missing = missing[len(batch):]
	}
	var parts []api.CompletedPartRef
	for n := 1; n <= c.Upload.PartCount; n++ {
		parts = append(parts, api.CompletedPartRef{PartNumber: n, Etag: e.putPart(urls[n], partData(n, urls[n].SizeBytes))})
	}
	return parts
}

func (e *assetsEnv) jobCount(kind, assetID string) int {
	e.t.Helper()
	var n int
	err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'asset_id' = $2`, kind, assetID).Scan(&n)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ---- The happy path -----------------------------------------------------------------------------------------------

func TestAssetsUploadFlow(t *testing.T) {
	e := newAssetsEnv(t)
	size := int64(2*16*mib + 1234) // 3 parts: 16 MiB, 16 MiB, 1234 bytes

	c := e.create("ada", "../../My Clip.MP4", size)
	if !strings.HasPrefix(c.Asset.Id, "ast_") || !strings.HasPrefix(c.Upload.Id, "upl_") || c.Upload.AssetId != c.Asset.Id {
		t.Fatalf("ids: %+v", c)
	}
	if c.Asset.Status != api.AssetStatusUploading || c.Asset.Filename != "My Clip.MP4" || c.Asset.Title != "My Clip" || c.Asset.Kind != api.AssetKindVideo ||
		c.Asset.ContentType != "video/mp4" || c.Asset.SizeBytes != size || c.Asset.Sha256 != nil || c.Asset.FailureReason != nil || len(c.Asset.Tags) != 0 {
		t.Fatalf("asset: %+v", c.Asset)
	}
	if c.Upload.PartSize != 16*mib || c.Upload.PartCount != 3 || c.Upload.Status != api.UploadStatusActive || !c.Upload.ExpiresAt.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("upload: %+v", c.Upload)
	}
	if len(c.Parts) != 3 || c.Parts[2].SizeBytes != 1234 || !c.UrlsExpireAt.After(time.Now()) || c.UrlsExpireAt.After(time.Now().Add(16*time.Minute)) {
		t.Fatalf("parts: %+v expire %v", c.Parts, c.UrlsExpireAt)
	}
	// The server chose the key from the ids; R2's own upload id is not in the answer.
	var key string
	if err := e.pool.QueryRow(context.Background(), `SELECT r2_key FROM assets`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	wsA := e.ws["ada"]
	aid, _ := ids.ParseAssetID(c.Asset.Id)
	if key != "ws/"+wsA.String()+"/assets/"+aid.String()+"/original" || strings.Contains(key, "..") {
		t.Errorf("r2_key = %q", key)
	}
	var r2upload string
	if err := e.pool.QueryRow(context.Background(), `SELECT r2_upload_id FROM uploads`).Scan(&r2upload); err != nil || r2upload == "" {
		t.Fatal(err)
	}
	// R2's upload id is only inside the signed part URLs, never a field of the answer.
	if raw, _ := json.Marshal(struct {
		A api.Asset
		U api.UploadInfo
	}{c.Asset, c.Upload}); strings.Contains(string(raw), r2upload) {
		t.Error("the asset or upload in the answer contains R2's multipart upload id")
	}

	// One part goes up; the state reports it.
	etag1 := e.putPart(c.Parts[0], partData(1, c.Parts[0].SizeBytes))
	var st api.UploadStateResponse
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusOK, &st)
	if st.Upload.Status != api.UploadStatusActive || len(st.CompletedParts) != 1 || st.CompletedParts[0].PartNumber != 1 ||
		st.CompletedParts[0].SizeBytes != 16*mib || strings.Trim(st.CompletedParts[0].Etag, `"`) != strings.Trim(etag1, `"`) {
		t.Fatalf("state after one part: %+v", st)
	}

	// The others go up and the upload is completed.
	rest := []api.CompletedPartRef{{PartNumber: 1, Etag: etag1}}
	for _, p := range c.Parts[1:] {
		rest = append(rest, api.CompletedPartRef{PartNumber: p.PartNumber, Etag: e.putPart(p, partData(p.PartNumber, p.SizeBytes))})
	}
	var done api.Asset
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: rest}, http.StatusOK, &done)
	if done.Status != api.AssetStatusUploaded || done.Id != c.Asset.Id {
		t.Fatalf("completed asset: %+v", done)
	}
	if n := e.jobCount("asset_ingest", c.Asset.Id); n != 1 {
		t.Errorf("asset_ingest jobs = %d, want 1", n)
	}
	info, err := e.objs.Head(context.Background(), key)
	if err != nil || info.Size != size {
		t.Errorf("stored object: %+v, %v, want %d bytes", info, err, size)
	}

	// Completing again answers the same, and enqueues nothing more.
	var again api.Asset
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: rest}, http.StatusOK, &again)
	if again.Status != api.AssetStatusUploaded || e.jobCount("asset_ingest", c.Asset.Id) != 1 {
		t.Errorf("second completion: %+v, jobs %d", again, e.jobCount("asset_ingest", c.Asset.Id))
	}

	// The state of a completed upload, and the asset itself.
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusOK, &st)
	if st.Upload.Status != api.UploadStatusCompleted || len(st.CompletedParts) != 0 {
		t.Errorf("state after completion: %+v", st)
	}
	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusUploaded || !got.UpdatedAt.After(got.CreatedAt.Add(-time.Second)) {
		t.Errorf("GET asset: %+v", got)
	}
	// It cannot be completed, aborted, or given more part URLs any more.
	e.fail(http.MethodDelete, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusConflict, api.ErrorCodeUploadNotActive)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", api.PresignPartsRequest{PartNumbers: []int{1}}, http.StatusConflict, api.ErrorCodeUploadNotActive)
}

// The reload story: parts that made it are reported, the rest is uploaded with fresh URLs, and the object is whole.
func TestAssetsResume(t *testing.T) {
	e := newAssetsEnv(t)
	size := int64(3*16*mib + 5)
	c := e.create("ada", "resume.mp4", size) // 4 parts
	if c.Upload.PartCount != 4 {
		t.Fatalf("part count = %d", c.Upload.PartCount)
	}
	// "Tab 1" uploads parts 1 and 3, then is closed.
	e.putPart(c.Parts[0], partData(1, c.Parts[0].SizeBytes))
	e.putPart(c.Parts[2], partData(3, c.Parts[2].SizeBytes))

	// "Tab 2" asks what is there and uploads what is not.
	var st api.UploadStateResponse
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusOK, &st)
	have := map[int]string{}
	for _, p := range st.CompletedParts {
		have[p.PartNumber] = p.Etag
	}
	if len(have) != 2 || have[1] == "" || have[3] == "" || st.Upload.PartSize != 16*mib || st.Upload.PartCount != 4 {
		t.Fatalf("state: %+v", st)
	}
	var need []int
	for n := 1; n <= st.Upload.PartCount; n++ {
		if have[n] == "" {
			need = append(need, n)
		}
	}
	var urls api.PresignPartsResponse
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", api.PresignPartsRequest{PartNumbers: need}, http.StatusOK, &urls)
	if len(urls.Parts) != 2 || urls.Parts[0].PartNumber != 2 || urls.Parts[1].PartNumber != 4 || urls.Parts[1].SizeBytes != 5 {
		t.Fatalf("urls: %+v", urls.Parts)
	}
	for _, p := range urls.Parts {
		have[p.PartNumber] = e.putPart(p, partData(p.PartNumber, p.SizeBytes))
	}
	var parts []api.CompletedPartRef
	for n := 1; n <= 4; n++ {
		parts = append(parts, api.CompletedPartRef{PartNumber: n, Etag: have[n]})
	}
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, http.StatusOK, nil)
}

// ---- Tenancy ------------------------------------------------------------------------------------------------------

// Workspace B cannot read, resume, complete, abort or delete workspace A's asset: every call is 404 not_found, and the
// answer is the one an id that never existed gets.
func TestAssetsTenancy(t *testing.T) {
	e := newAssetsEnv(t)
	size := int64(16*mib + 7)
	c := e.create("ada", "ada-only.mp4", size)
	parts := e.uploadAll("ada", c)

	missing := ids.AssetID{UUID: uuid.Must(uuid.NewV7())}.String() // well-formed, never issued
	calls := []struct {
		name, method, path string
		body               any
	}{
		{"get", http.MethodGet, "/v1/assets/%s", nil},
		{"state", http.MethodGet, "/v1/assets/%s/upload", nil},
		{"presign", http.MethodPost, "/v1/assets/%s/parts", api.PresignPartsRequest{PartNumbers: []int{1}}},
		{"complete", http.MethodPost, "/v1/assets/%s/complete", api.CompleteAssetRequest{Parts: parts}},
		{"abort", http.MethodDelete, "/v1/assets/%s/upload", nil},
		{"delete", http.MethodDelete, "/v1/assets/%s", nil},
	}
	for _, call := range calls {
		bobs := e.do(call.method, fmt.Sprintf(call.path, c.Asset.Id), "bob", call.body)
		wantError(t, bobs, http.StatusNotFound, "not_found")
		never := e.do(call.method, fmt.Sprintf(call.path, missing), "bob", call.body)
		if bobs.Body.String() != never.Body.String() {
			t.Errorf("%s: another workspace's asset answers %s, a missing one %s", call.name, bobs.Body, never.Body)
		}
	}
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "bob", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("bob lists %d assets of ada's", len(list.Items))
	}

	// All of that left ada's upload as it was: still in progress, and it completes.
	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusUploading {
		t.Fatalf("ada's asset after bob's attempts: %+v", got)
	}
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, http.StatusOK, nil)
	if n := e.jobCount("asset_object_delete", c.Asset.Id); n != 0 {
		t.Errorf("bob's attempts enqueued %d deletion jobs", n)
	}

	// Bob's assets are his: his own id works for him and 404s for ada.
	b := e.create("bob", "bob.mp4", 1000)
	e.call(http.MethodGet, "/v1/assets/"+b.Asset.Id, "bob", nil, http.StatusOK, nil)
	e.fail(http.MethodGet, "/v1/assets/"+b.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	var bobs api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "bob", nil, http.StatusOK, &bobs)
	if len(bobs.Items) != 1 || bobs.Items[0].Id != b.Asset.Id {
		t.Errorf("bob's list: %+v", bobs.Items)
	}
	// The object keys are under the workspace of whoever created them.
	rows, err := e.pool.Query(context.Background(), `SELECT workspace_id, r2_key FROM assets`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var ws ids.WorkspaceID
		var key string
		if err := rows.Scan(&ws, &key); err != nil {
			t.Fatal(err)
		}
		n++
		if !strings.HasPrefix(key, "ws/"+ws.String()+"/assets/") {
			t.Errorf("key %q is not under its workspace %s", key, ws)
		}
	}
	if n != 2 {
		t.Errorf("%d assets, want 2", n)
	}
}

// ---- Roles, the paywall and authentication --------------------------------------------------------------------------

func TestAssetsAccess(t *testing.T) {
	e := newAssetsEnv(t)
	c := e.create("ada", "viewed.mp4", 1000)

	// A viewer looks and does not touch.
	var got api.Asset
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id, "viewer", nil, http.StatusOK, &got)
	e.call(http.MethodGet, "/v1/assets", "viewer", nil, http.StatusOK, nil)
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "viewer", nil, http.StatusOK, nil)
	body := api.CreateAssetRequest{Filename: "v.mp4", SizeBytes: 10, ContentType: "video/mp4"}
	e.fail(http.MethodPost, "/v1/assets", "viewer", body, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "viewer", api.PresignPartsRequest{PartNumbers: []int{1}}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "viewer", api.CompleteAssetRequest{Parts: []api.CompletedPartRef{{PartNumber: 1, Etag: "x"}}}, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/assets/"+c.Asset.Id+"/upload", "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodDelete, "/v1/assets/"+c.Asset.Id, "viewer", nil, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id, "ada", nil, http.StatusOK, &got)
	if got.Status != api.AssetStatusUploading {
		t.Errorf("a viewer changed the asset: %+v", got)
	}

	// No token: 401. No plan: 402 (before anything else about the request is looked at).
	e.fail(http.MethodGet, "/v1/assets", "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	e.fail(http.MethodPost, "/v1/assets", "", body, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
	if _, err := e.st.RevokePlan(context.Background(), store.RevokeInput{WorkspaceID: e.ws["ada"], GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/v1/assets"}, {http.MethodPost, "/v1/assets"}, {http.MethodGet, "/v1/assets/" + c.Asset.Id},
		{http.MethodGet, "/v1/assets/" + c.Asset.Id + "/upload"}, {http.MethodPost, "/v1/assets/" + c.Asset.Id + "/parts"},
		{http.MethodPost, "/v1/assets/" + c.Asset.Id + "/complete"}, {http.MethodDelete, "/v1/assets/" + c.Asset.Id + "/upload"},
		{http.MethodDelete, "/v1/assets/" + c.Asset.Id},
	} {
		e.fail(call.method, call.path, "ada", "{}", http.StatusPaymentRequired, api.ErrorCodePlanRequired)
	}
}

func TestAssetsWithoutObjectStorage(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) { o.noStorage = true })
	body := api.CreateAssetRequest{Filename: "v.mp4", SizeBytes: 10, ContentType: "video/mp4"}
	e.fail(http.MethodPost, "/v1/assets", "ada", body, http.StatusServiceUnavailable, api.ErrorCodeStorageNotConfigured)
	e.fail(http.MethodGet, "/v1/assets", "ada", nil, http.StatusServiceUnavailable, api.ErrorCodeStorageNotConfigured)
	// Authentication and the role are checked first, so storage settings are not revealed to a caller who may not use them.
	e.fail(http.MethodPost, "/v1/assets", "viewer", body, http.StatusForbidden, api.ErrorCodeInsufficientRole)
	e.fail(http.MethodGet, "/v1/assets", "", nil, http.StatusUnauthorized, api.ErrorCodeUnauthenticated)
}

// ---- Validation ---------------------------------------------------------------------------------------------------

func TestAssetsCreateValidation(t *testing.T) {
	e := newAssetsEnv(t)
	post := func(body any, status int, code api.ErrorCode) {
		t.Helper()
		e.fail(http.MethodPost, "/v1/assets", "ada", body, status, code)
	}
	ok := func(name, ctype string, size int64) api.CreateAssetRequest {
		return api.CreateAssetRequest{Filename: name, SizeBytes: size, ContentType: ctype}
	}
	post(ok("a.zip", "application/zip", 10), 415, api.ErrorCodeUnsupportedContentType)
	post(ok("a.gif", "image/gif", 10), 415, api.ErrorCodeUnsupportedContentType)
	post(ok("a.mp4", "", 10), 415, api.ErrorCodeUnsupportedContentType)
	post(ok("a.mp4", "video/mp4", 0), 400, api.ErrorCodeInvalidRequest)
	post(ok("a.mp4", "video/mp4", -1), 400, api.ErrorCodeInvalidRequest)
	post(ok("a.mp4", "video/mp4", assets.MaxFileSize+1), 413, api.ErrorCodeFileTooLarge)
	post(ok("", "video/mp4", 10), 400, api.ErrorCodeInvalidRequest)
	post(ok("../..", "video/mp4", 10), 400, api.ErrorCodeInvalidRequest)
	post(map[string]any{"filename": "a.mp4", "content_type": "video/mp4"}, 400, api.ErrorCodeInvalidRequest) // no size
	post(map[string]any{"filename": "a.mp4", "size_bytes": "ten", "content_type": "video/mp4"}, 400, api.ErrorCodeInvalidRequest)
	post("not json", 400, api.ErrorCodeInvalidRequest)
	post("", 400, api.ErrorCodeInvalidRequest)
	sum := "xyz"
	bad := ok("a.mp4", "video/mp4", 10)
	bad.Sha256 = &sum
	post(bad, 400, api.ErrorCodeInvalidRequest)
	// The body of a request is capped.
	post(`{"filename":"`+strings.Repeat("a", 3*mib)+`"}`, 400, api.ErrorCodeInvalidRequest)

	// Allowed types: audio/* of any kind, and the listed video and image types.
	for ctype, kind := range map[string]api.AssetKind{
		"video/quicktime": api.AssetKindVideo, "video/webm": api.AssetKindVideo, "audio/mpeg": api.AssetKindAudio,
		"audio/x-m4a": api.AssetKindAudio, "image/jpeg": api.AssetKindImage, "image/png": api.AssetKindImage, "image/heic": api.AssetKindImage,
	} {
		var out api.CreateAssetResponse
		e.call(http.MethodPost, "/v1/assets", "ada", ok("f", ctype, 100), http.StatusCreated, &out)
		if out.Asset.Kind != kind || out.Asset.ContentType != ctype || len(out.Parts) != 1 {
			t.Errorf("%s: %+v", ctype, out.Asset)
		}
	}
	// Exactly 20 GiB is fine and is cut into 1,280 parts of 16 MiB; only the first 20 URLs come back.
	var big api.CreateAssetResponse
	e.call(http.MethodPost, "/v1/assets", "ada", ok("big.mp4", "video/mp4", assets.MaxFileSize), http.StatusCreated, &big)
	if big.Upload.PartCount != 1280 || big.Upload.PartSize != 16*mib || len(big.Parts) != assets.FirstBatch {
		t.Errorf("20 GiB: %d parts of %d, %d URLs", big.Upload.PartCount, big.Upload.PartSize, len(big.Parts))
	}
	// A hash that was claimed is stored, in lower case.
	withSum := ok("sum.mp4", "video/mp4", 10)
	up := strings.ToUpper(strings.Repeat("ab", 32))
	withSum.Sha256 = &up
	var s api.CreateAssetResponse
	e.call(http.MethodPost, "/v1/assets", "ada", withSum, http.StatusCreated, &s)
	if s.Asset.Sha256 == nil || *s.Asset.Sha256 != strings.Repeat("ab", 32) {
		t.Errorf("sha256 = %v", s.Asset.Sha256)
	}
}

func TestAssetsPartAndIDValidation(t *testing.T) {
	e := newAssetsEnv(t)
	c := e.create("ada", "parts.mp4", 3*16*mib) // 3 parts
	presign := func(nums []int, status int, code api.ErrorCode) {
		t.Helper()
		e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", api.PresignPartsRequest{PartNumbers: nums}, status, code)
	}
	presign(nil, 400, api.ErrorCodeInvalidRequest)
	presign([]int{}, 400, api.ErrorCodeInvalidRequest)
	presign([]int{0}, 400, api.ErrorCodeInvalidRequest)
	presign([]int{4}, 400, api.ErrorCodeInvalidRequest)
	presign([]int{-1}, 400, api.ErrorCodeInvalidRequest)
	presign([]int{2, 2}, 400, api.ErrorCodeInvalidRequest)
	many := make([]int, assets.MaxPartBatch+1)
	for i := range many {
		many[i] = i + 1
	}
	presign(many, 400, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", "{}", http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", "", http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	var out api.PresignPartsResponse
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", api.PresignPartsRequest{PartNumbers: []int{3, 1}}, http.StatusOK, &out)
	if len(out.Parts) != 2 || out.Parts[0].PartNumber != 3 || out.Parts[1].PartNumber != 1 {
		t.Errorf("presign: %+v", out.Parts)
	}

	// Ids: a wrong prefix, a wrong shape and the nil id are 400; they never reach a query.
	for _, id := range []string{"usr_01j9z3x7m8c4v6n2q5r0t1y8ab", "wks_" + strings.TrimPrefix(c.Asset.Id, "ast_"), "ast_short", "not-an-id", "ast_00000000000000000000000000", strings.ToUpper(c.Asset.Id)} {
		for _, call := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodGet, "/upload"}, {http.MethodDelete, ""}, {http.MethodDelete, "/upload"}} {
			e.fail(call.method, "/v1/assets/"+id+call.suffix, "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
		}
	}
	// A cursor and a limit are checked too.
	for _, q := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "cursor=nonsense", "cursor=" + "usr_01j9z3x7m8c4v6n2q5r0t1y8ab"} {
		e.fail(http.MethodGet, "/v1/assets?"+q, "ada", nil, http.StatusBadRequest, api.ErrorCodeInvalidRequest)
	}
}

// ---- Complete: what is refused -------------------------------------------------------------------------------------

func TestAssetsCompleteChecksTheParts(t *testing.T) {
	e := newAssetsEnv(t)
	c := e.create("ada", "check.mp4", 2*16*mib) // 2 parts
	complete := func(parts []api.CompletedPartRef, status int, code api.ErrorCode) {
		t.Helper()
		e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, status, code)
	}
	etag1 := e.putPart(c.Parts[0], partData(1, c.Parts[0].SizeBytes))

	// Part 2 was never uploaded.
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag1}, {PartNumber: 2, Etag: `"00"`}}, 422, api.ErrorCodePartsMismatch)
	// The claim is malformed: too few, too many, repeated, out of range, without an ETag.
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag1}}, 422, api.ErrorCodePartsMismatch)
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag1}, {PartNumber: 1, Etag: etag1}}, 422, api.ErrorCodePartsMismatch)
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag1}, {PartNumber: 3, Etag: etag1}}, 422, api.ErrorCodePartsMismatch)
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag1}, {PartNumber: 2, Etag: ""}}, 400, api.ErrorCodeInvalidRequest)
	complete(nil, 400, api.ErrorCodeInvalidRequest)

	etag2 := e.putPart(c.Parts[1], partData(2, c.Parts[1].SizeBytes))
	// The ETags are swapped: they are not what storage holds.
	complete([]api.CompletedPartRef{{PartNumber: 1, Etag: etag2}, {PartNumber: 2, Etag: etag1}}, 422, api.ErrorCodePartsMismatch)
	var st api.UploadStateResponse
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusOK, &st)
	if st.Upload.Status != api.UploadStatusActive || len(st.CompletedParts) != 2 {
		t.Fatalf("refused completions changed the upload: %+v", st)
	}
	// With the right ETags (in any order, with or without quotes) it completes.
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: []api.CompletedPartRef{
		{PartNumber: 2, Etag: strings.Trim(etag2, `"`)}, {PartNumber: 1, Etag: etag1},
	}}, http.StatusOK, nil)
}

// objectsWrap lets a test change what the service sees of object storage.
type objectsWrap struct {
	assets.Objects
	head     func(context.Context, string, assets.Objects) (storage.ObjectInfo, error)
	complete func(ctx context.Context, key, uploadID string, parts []storage.Part, inner assets.Objects) error
	presign  func(ctx context.Context, key, uploadID string, n int, size int64, inner assets.Objects) (string, error)
}

func (w objectsWrap) Complete(ctx context.Context, key, uploadID string, parts []storage.Part) error {
	if w.complete != nil {
		return w.complete(ctx, key, uploadID, parts, w.Objects)
	}
	return w.Objects.Complete(ctx, key, uploadID, parts)
}

func (w objectsWrap) PresignPart(ctx context.Context, key, uploadID string, n int, size int64) (string, error) {
	if w.presign != nil {
		return w.presign(ctx, key, uploadID, n, size, w.Objects)
	}
	return w.Objects.PresignPart(ctx, key, uploadID, n, size)
}

func (w objectsWrap) Head(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if w.head != nil {
		return w.head(ctx, key, w.Objects)
	}
	return w.Objects.Head(ctx, key)
}

// An object that is not the size the client announced fails the upload, hides the asset and queues the object's deletion.
func TestAssetsSizeMismatch(t *testing.T) {
	e := newAssetsEnv(t, func(o *assetsEnvOptions) {
		o.wrapObjects = func(inner assets.Objects) assets.Objects {
			return objectsWrap{Objects: inner, head: func(ctx context.Context, key string, in assets.Objects) (storage.ObjectInfo, error) {
				info, err := in.Head(ctx, key)
				info.Size++ // storage reports one byte more than was announced
				return info, err
			}}
		}
	})
	c := e.create("ada", "liar.mp4", 16*mib+1)
	parts := e.uploadAll("ada", c)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, 422, api.ErrorCodeSizeMismatch)
	e.fail(http.MethodGet, "/v1/assets/"+c.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	if n := e.jobCount("asset_ingest", c.Asset.Id); n != 0 {
		t.Errorf("a failed upload enqueued %d ingest jobs", n)
	}
	if n := e.jobCount("asset_object_delete", c.Asset.Id); n != 1 {
		t.Errorf("deletion jobs = %d, want 1", n)
	}
	var status, reason string
	if err := e.pool.QueryRow(context.Background(), `SELECT status, failure_reason FROM assets`).Scan(&status, &reason); err != nil || status != "failed" || reason != "size_mismatch" {
		t.Errorf("asset row: %s %s %v", status, reason, err)
	}
}

// A completion whose answer was lost (the request died after storage had finished, before the commit) is finished by the
// retry: the upload is gone from storage, the object is whole, and the asset becomes uploaded.
func TestAssetsCompleteRecoversAfterAFailedCommit(t *testing.T) {
	e := newAssetsEnv(t)
	c := e.create("ada", "retry.mp4", 16*mib+3)
	parts := e.uploadAll("ada", c)
	// Complete in storage behind the API's back, as an earlier request that died before its transaction committed would have.
	aid, _ := ids.ParseAssetID(c.Asset.Id)
	key := storage.OriginalKey(e.ws["ada"], aid)
	var r2upload string
	if err := e.pool.QueryRow(context.Background(), `SELECT r2_upload_id FROM uploads`).Scan(&r2upload); err != nil {
		t.Fatal(err)
	}
	remote, err := e.objs.ListParts(context.Background(), key, r2upload)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.objs.Complete(context.Background(), key, r2upload, remote); err != nil {
		t.Fatal(err)
	}

	var done api.Asset
	e.call(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, http.StatusOK, &done)
	if done.Status != api.AssetStatusUploaded || e.jobCount("asset_ingest", c.Asset.Id) != 1 {
		t.Errorf("recovered completion: %+v, jobs %d", done, e.jobCount("asset_ingest", c.Asset.Id))
	}
}

// ---- Abort and delete ---------------------------------------------------------------------------------------------

func TestAssetsAbort(t *testing.T) {
	e := newAssetsEnv(t)
	c := e.create("ada", "abort.mp4", 2*16*mib)
	e.putPart(c.Parts[0], partData(1, c.Parts[0].SizeBytes))
	var r2upload string
	if err := e.pool.QueryRow(context.Background(), `SELECT r2_upload_id FROM uploads`).Scan(&r2upload); err != nil {
		t.Fatal(err)
	}
	aid, _ := ids.ParseAssetID(c.Asset.Id)
	key := storage.OriginalKey(e.ws["ada"], aid)

	e.call(http.MethodDelete, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusNoContent, nil)
	// The multipart upload is gone from storage, the asset from the library, and a deletion job is queued.
	if _, err := e.objs.ListParts(context.Background(), key, r2upload); err == nil {
		t.Error("the multipart upload still exists in storage")
	}
	e.fail(http.MethodGet, "/v1/assets/"+c.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("an aborted asset is listed: %+v", list.Items)
	}
	if n := e.jobCount("asset_object_delete", c.Asset.Id); n != 1 {
		t.Errorf("deletion jobs = %d, want 1", n)
	}
	var status, reason string
	if err := e.pool.QueryRow(context.Background(), `SELECT status, failure_reason FROM assets`).Scan(&status, &reason); err != nil || status != "failed" || reason != "upload_aborted" {
		t.Errorf("asset row: %s %s %v", status, reason, err)
	}
	// Presigned URLs of the aborted upload no longer lead anywhere.
	req, _ := http.NewRequest(http.MethodPut, c.Parts[1].Url, bytes.NewReader(partData(2, c.Parts[1].SizeBytes)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("a part could be uploaded to an aborted upload")
	}
}

// Deleting an asset whose upload is in progress aborts it; deleting a finished one hides it and queues the objects' removal.
func TestAssetsDelete(t *testing.T) {
	e := newAssetsEnv(t)
	inProgress := e.create("ada", "inprogress.mp4", 16*mib+1)
	e.call(http.MethodDelete, "/v1/assets/"+inProgress.Asset.Id, "ada", nil, http.StatusNoContent, nil)
	e.fail(http.MethodGet, "/v1/assets/"+inProgress.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	if n := e.jobCount("asset_object_delete", inProgress.Asset.Id); n != 1 {
		t.Errorf("deletion jobs of an aborted upload = %d, want 1", n)
	}

	done := e.create("ada", "done.mp4", 16*mib+1)
	e.call(http.MethodPost, "/v1/assets/"+done.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: e.uploadAll("ada", done)}, http.StatusOK, nil)
	e.call(http.MethodDelete, "/v1/assets/"+done.Asset.Id, "ada", nil, http.StatusNoContent, nil)
	e.fail(http.MethodGet, "/v1/assets/"+done.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	e.fail(http.MethodDelete, "/v1/assets/"+done.Asset.Id, "ada", nil, http.StatusNotFound, api.ErrorCodeNotFound)
	if n := e.jobCount("asset_object_delete", done.Asset.Id); n != 1 {
		t.Errorf("deletion jobs of a finished asset = %d, want 1", n)
	}
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("deleted assets are listed: %+v", list.Items)
	}
}

// ---- Expiry -------------------------------------------------------------------------------------------------------

func TestAssetsExpiredUpload(t *testing.T) {
	e := newAssetsEnv(t)
	e.setNow(time.Now().Add(-48 * time.Hour)) // the upload starts two days ago, so it expired a day ago
	c := e.create("ada", "old.mp4", 16*mib+1)
	e.setNow(time.Time{})

	var st api.UploadStateResponse
	e.call(http.MethodGet, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusOK, &st)
	if st.Upload.Status != api.UploadStatusExpired || len(st.CompletedParts) != 0 {
		t.Errorf("state of an expired upload: %+v", st)
	}
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/parts", "ada", api.PresignPartsRequest{PartNumbers: []int{1}}, http.StatusConflict, api.ErrorCodeUploadExpired)
	e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: []api.CompletedPartRef{{PartNumber: 1, Etag: "a"}, {PartNumber: 2, Etag: "b"}}}, http.StatusConflict, api.ErrorCodeUploadExpired)
	// It can still be aborted.
	e.call(http.MethodDelete, "/v1/assets/"+c.Asset.Id+"/upload", "ada", nil, http.StatusNoContent, nil)
}

func TestAssetsTooManyUploads(t *testing.T) {
	e := newAssetsEnv(t)
	for i := range assets.MaxActiveUploads {
		e.create("ada", fmt.Sprintf("u%d.mp4", i), 10)
	}
	body := api.CreateAssetRequest{Filename: "one-too-many.mp4", SizeBytes: 10, ContentType: "video/mp4"}
	e.fail(http.MethodPost, "/v1/assets", "ada", body, http.StatusTooManyRequests, api.ErrorCodeTooManyUploads)
	// The cap is per workspace, and finishing one makes room.
	e.create("bob", "bobs.mp4", 10)
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets?limit=1", "ada", nil, http.StatusOK, &list)
	e.call(http.MethodDelete, "/v1/assets/"+list.Items[0].Id, "ada", nil, http.StatusNoContent, nil)
	e.create("ada", "now-it-fits.mp4", 10)
}

// ---- Listing ------------------------------------------------------------------------------------------------------

func TestAssetsListPaginates(t *testing.T) {
	e := newAssetsEnv(t)
	var made []string
	for i := range 5 {
		made = append(made, e.create("ada", fmt.Sprintf("item%d.mp4", i), 10).Asset.Id)
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		var page api.ListAssetsResponse
		path := "/v1/assets?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		e.call(http.MethodGet, path, "ada", nil, http.StatusOK, &page)
		for _, a := range page.Items {
			seen = append(seen, a.Id)
		}
		if page.NextCursor == nil {
			if len(page.Items) > 2 {
				t.Fatalf("page of %d", len(page.Items))
			}
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d assets: %v", len(seen), seen)
	}
	for i, id := range seen {
		if want := made[len(made)-1-i]; id != want {
			t.Fatalf("order: %v, want newest first %v", seen, made)
		}
	}
	// The default page holds everything here, and the last page has no cursor.
	var all api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &all)
	if len(all.Items) != 5 || all.NextCursor != nil {
		t.Errorf("default page: %d items, cursor %v", len(all.Items), all.NextCursor)
	}
}

// ---- Races ----------------------------------------------------------------------------------------------------------

// A completion that wins in object storage but loses in the database, to an abort that landed in between (the expiry job,
// the client's own abort or a delete), says the upload is not in progress any more (409, not 404) and leaves no object.
func TestAssetsCompleteLosesARace(t *testing.T) {
	for name, lose := range map[string]func(t *testing.T, e *assetsEnv, ws ids.WorkspaceID, id ids.AssetID){
		"the expiry job": func(t *testing.T, e *assetsEnv, ws ids.WorkspaceID, id ids.AssetID) {
			if _, err := e.st.AbortUpload(context.Background(), ws, id, store.FailureUploadExpired, false); err != nil {
				t.Fatal(err)
			}
		},
		"an abort": func(t *testing.T, e *assetsEnv, ws ids.WorkspaceID, id ids.AssetID) {
			if _, err := e.st.AbortUpload(context.Background(), ws, id, store.FailureUploadAborted, true); err != nil {
				t.Fatal(err)
			}
		},
		"a delete": func(t *testing.T, e *assetsEnv, ws ids.WorkspaceID, id ids.AssetID) {
			ctx := context.Background()
			if _, err := e.st.AbortUpload(ctx, ws, id, store.FailureUploadAborted, true); err != nil {
				t.Fatal(err)
			}
			if _, err := e.st.SoftDeleteAsset(ctx, ws, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var e *assetsEnv
			var onComplete func(key string)
			e = newAssetsEnv(t, func(o *assetsEnvOptions) {
				o.wrapObjects = func(inner assets.Objects) assets.Objects {
					return objectsWrap{Objects: inner, complete: func(ctx context.Context, key, uploadID string, parts []storage.Part, in assets.Objects) error {
						if err := in.Complete(ctx, key, uploadID, parts); err != nil {
							return err
						}
						onComplete(key) // the other request commits now, after storage finished and before this one's transaction
						return nil
					}}
				}
			})
			c := e.create("ada", "race.mp4", 16*mib+9)
			parts := e.uploadAll("ada", c)
			aid, _ := ids.ParseAssetID(c.Asset.Id)
			onComplete = func(string) { lose(t, e, e.ws["ada"], aid) }

			e.fail(http.MethodPost, "/v1/assets/"+c.Asset.Id+"/complete", "ada", api.CompleteAssetRequest{Parts: parts}, http.StatusConflict, api.ErrorCodeUploadNotActive)
			if _, err := e.objs.Head(context.Background(), storage.OriginalKey(e.ws["ada"], aid)); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("the object of the lost completion is still there: %v", err)
			}
			if n := e.jobCount("asset_ingest", c.Asset.Id); n != 0 {
				t.Errorf("a lost completion enqueued %d ingest jobs", n)
			}
		})
	}
}

// If the part URLs of a new upload cannot be made, the upload is taken back: no multipart upload lingers in storage and
// nothing counts against the cap.
func TestAssetsCreateCleansUpWhenPresigningFails(t *testing.T) {
	var fail atomic.Bool
	e := newAssetsEnv(t, func(o *assetsEnvOptions) {
		o.wrapObjects = func(inner assets.Objects) assets.Objects {
			return objectsWrap{Objects: inner, presign: func(ctx context.Context, key, uploadID string, n int, size int64, in assets.Objects) (string, error) {
				if fail.Load() {
					return "", errors.New("signing failed")
				}
				return in.PresignPart(ctx, key, uploadID, n, size)
			}}
		}
	})
	fail.Store(true)
	body := api.CreateAssetRequest{Filename: "x.mp4", SizeBytes: 100, ContentType: "video/mp4"}
	e.fail(http.MethodPost, "/v1/assets", "ada", body, http.StatusInternalServerError, api.ErrorCodeInternalError)

	var r2upload, key, status string
	if err := e.pool.QueryRow(context.Background(), `SELECT u.r2_upload_id, a.r2_key, a.status FROM uploads u JOIN assets a ON a.id = u.asset_id`).Scan(&r2upload, &key, &status); err != nil {
		t.Fatal(err)
	}
	if _, err := e.objs.ListParts(context.Background(), key, r2upload); !errors.Is(err, storage.ErrNoSuchUpload) {
		t.Errorf("the multipart upload still exists in storage: %v", err)
	}
	if n, _ := e.st.CountActiveUploads(context.Background(), e.ws["ada"]); n != 0 || status != "failed" {
		t.Errorf("active uploads = %d, asset status %s, want 0 and failed", n, status)
	}
	var list api.ListAssetsResponse
	e.call(http.MethodGet, "/v1/assets", "ada", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Errorf("the failed create is listed: %+v", list.Items)
	}
	fail.Store(false)
	e.create("ada", "works-now.mp4", 100)
}

// Ten creates at once with 24 uploads in progress and a cap of 25: exactly one gets in.
func TestAssetsCapHoldsUnderConcurrentCreates(t *testing.T) {
	e := newAssetsEnv(t)
	for i := range assets.MaxActiveUploads - 1 {
		e.create("ada", fmt.Sprintf("u%d.mp4", i), 10)
	}
	const racers = 10
	codes := make(chan int, racers)
	start := make(chan struct{})
	body := api.CreateAssetRequest{Filename: "race.mp4", SizeBytes: 10, ContentType: "video/mp4"}
	for range racers {
		go func() {
			<-start
			codes <- e.do(http.MethodPost, "/v1/assets", "ada", body).Code
		}()
	}
	close(start)
	got := map[int]int{}
	for range racers {
		got[<-codes]++
	}
	if got[http.StatusCreated] != 1 || got[http.StatusTooManyRequests] != racers-1 {
		t.Errorf("answers = %v, want one 201 and %d 429", got, racers-1)
	}
	if n, _ := e.st.CountActiveUploads(context.Background(), e.ws["ada"]); n != assets.MaxActiveUploads {
		t.Errorf("uploads in progress = %d, want %d", n, assets.MaxActiveUploads)
	}
	// The multipart uploads of the refused creates were aborted: storage holds one per upload in progress.
	out, err := storagetest.S3(e.cfg).ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(e.cfg.Bucket)})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Uploads) != assets.MaxActiveUploads {
		t.Errorf("multipart uploads in storage = %d, want %d", len(out.Uploads), assets.MaxActiveUploads)
	}
}
