package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
)

// An argument after a lone `--` is a file, even when it starts with a dash.
func TestAssetsImportDoubleDash(t *testing.T) {
	r := newImportRig(t)
	dir, err := os.MkdirTemp(".", "-import-dash-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte(strings.Repeat("clip", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "clip.mp4") // Join cleans "./-import-dash-.../clip.mp4" to a name that starts with "-"

	if code, _, _ := r.run("--workspace", r.ws.ID.String(), file); code != 2 {
		t.Fatalf("without --, the name is taken for a flag: exit %d, want 2", code)
	}
	code, out, errOut := r.run("--workspace", r.ws.ID.String(), "--", file)
	if code != 0 || strings.Count(out, "ast_") != 1 {
		t.Fatalf("with --: exit %d\n%s%s", code, out, errOut)
	}
	// Flags after the dashes are files, not flags: --wait here names a file that does not exist.
	if code, _, errOut := r.run("--workspace", r.ws.ID.String(), "--", file, "--wait"); code != 2 || !strings.Contains(errOut, "--wait") {
		t.Fatalf("a flag after --: exit %d %s", code, errOut)
	}
}

// refusing403 answers 403 to a part's first PUT (an expired signature), and counts the requests.
type refusing403 struct {
	rt     http.RoundTripper
	mu     sync.Mutex
	refuse map[string]bool // part number marker ("partNumber=2") -> still to refuse
	always bool
	puts   int
}

func (r *refusing403) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.puts++
	deny := r.always
	for marker, pending := range r.refuse {
		if pending && strings.Contains(req.URL.RawQuery, marker) {
			r.refuse[marker] = false
			deny = true
		}
	}
	r.mu.Unlock()
	if deny {
		return &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Body: http.NoBody, Header: http.Header{}, Request: req}, nil
	}
	return r.rt.RoundTrip(req)
}

func (r *importRig) importer(t *testing.T, now func() time.Time, rt http.RoundTripper) *importer {
	t.Helper()
	client, err := jobs.NewInsertClient(r.pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := assets.NewService(r.st, r.objs, jobs.NewEnqueuer(client, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Now = now
	return &importer{svc: svc, who: assets.Actor{Workspace: r.ws.ID, User: r.owner.ID}, http: &http.Client{Transport: rt, Timeout: time.Minute}}
}

func (r *importRig) threeParts(name string) importPlan {
	r.t.Helper()
	path := r.big(name) // 16 MiB + 5 bytes: two parts
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(make([]byte, 16<<20)); err != nil { // and one more part
		r.t.Fatal(err)
	}
	info, _ := os.Stat(path)
	return importPlan{path: path, name: name, contentType: "video/quicktime", size: info.Size()}
}

// A URL that is about to expire is not used: new ones are fetched first. The service's clock says the first URLs have ten
// seconds left (the real signatures are still good), which is inside the importer's one-minute margin.
func TestImportRefreshesExpiringURLs(t *testing.T) {
	r := newImportRig(t)
	plan := r.threeParts("expiring.mov")
	short := func() time.Time { return time.Now().Add(-storage.PartURLExpiry + 10*time.Second) }

	im := r.importer(t, short, http.DefaultTransport)
	a, err := im.importFile(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "uploaded" || im.presigns != 3 {
		t.Fatalf("status %s, %d presign calls; want uploaded and one before each of the 3 parts", a.Status, im.presigns)
	}

	// With a normal clock the first batch is used as it is: no further presign call.
	normal := r.importer(t, time.Now, http.DefaultTransport)
	if _, err := normal.importFile(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if normal.presigns != 0 {
		t.Fatalf("presign calls with fresh URLs = %d, want 0", normal.presigns)
	}
}

// A 403 means the URL is dead: the importer fetches a new one instead of retrying it, and gives up if that is refused too.
func TestImportRefreshesOn403(t *testing.T) {
	r := newImportRig(t)
	plan := r.threeParts("refused.mov")

	flaky := &refusing403{rt: http.DefaultTransport, refuse: map[string]bool{"partNumber=2": true}}
	im := r.importer(t, time.Now, flaky)
	if _, err := im.importFile(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if im.presigns != 1 || flaky.puts != 4 { // 3 parts, and part 2 twice: no retry of the dead URL, one request with the new one
		t.Fatalf("presign calls = %d, PUTs = %d; want 1 and 4", im.presigns, flaky.puts)
	}

	dead := &refusing403{rt: http.DefaultTransport, always: true}
	im = r.importer(t, time.Now, dead)
	_, err := im.importFile(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "upload part 1 of 3") {
		t.Fatalf("a part that is always refused: %v", err)
	}
	if im.presigns != 1 || dead.puts != 2 { // the first PUT, one refresh, one PUT with the new URL, then it stops
		t.Fatalf("presign calls = %d, PUTs = %d; want 1 and 2", im.presigns, dead.puts)
	}
	// The failed upload was aborted, so it does not count against the 25 in progress.
	n, err := r.st.CountActiveUploads(context.Background(), r.ws.ID)
	if err != nil || n != 0 {
		t.Fatalf("uploads in progress = %d, %v", n, err)
	}
}
