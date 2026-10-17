//go:build upload20

package auth

// The server half of the acceptance proof of P1-02 (web upload manager), run by hand, never in CI:
//
//	UPLOAD20_DIR=/tmp/cf-upload20 go test -tags upload20 -run TestUploadHarness -v -timeout 90m ./internal/auth/
//
// It starts the real API (the Clerk middleware over a locally signed token, the paywall, the assets handlers, Postgres,
// River's insert client) behind the real CORS middleware at http://localhost:3000, and a MinIO, both in throwaway
// containers on random ports (testcontainers), then writes env.json to UPLOAD20_DIR with the API address and a token. A
// browser script (apps/web/e2e/upload20) uploads 20 files through apps/web/src/lib/upload on a throttled connection,
// reloads the page half way, resumes, and creates browser-done. This test then checks every object in MinIO against
// expected.json (name, size, SHA-256) and the database, and writes report.json.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
)

func TestUploadHarness(t *testing.T) {
	dir := os.Getenv("UPLOAD20_DIR")
	if dir == "" {
		t.Skip("run by hand with UPLOAD20_DIR")
	}
	e := newAssetsEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: httpapi.CORS(testOrigin, quiet())(e.mux), ReadHeaderTimeout: time.Minute}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// The most uploads that were in progress at once, sampled from the database.
	var peak atomic.Int64
	stopSampler := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopSampler:
				return
			case <-time.After(100 * time.Millisecond):
				var n int64
				if e.pool.QueryRow(context.Background(), `SELECT count(*) FROM uploads WHERE completed_at IS NULL AND aborted_at IS NULL`).Scan(&n) == nil && n > peak.Load() {
					peak.Store(n)
				}
			}
		}
	}()

	env, _ := json.Marshal(map[string]string{"api": "http://" + ln.Addr().String(), "token": e.token["ada"], "origin": testOrigin, "minio": e.cfg.Endpoint})
	if err := os.WriteFile(filepath.Join(dir, "env.json"), env, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("API at http://%s, MinIO at %s; waiting for the browser (%s/browser-done)", ln.Addr(), e.cfg.Endpoint, dir)

	deadline := time.Now().Add(85 * time.Minute)
	for {
		if _, err := os.Stat(filepath.Join(dir, "browser-done")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the browser did not finish in time")
		}
		time.Sleep(time.Second)
	}
	close(stopSampler)

	var expected []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Sha256 string `json:"sha256"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}

	type row struct{ status, key string; size int64 }
	rows := map[string][]row{}
	res, err := e.pool.Query(context.Background(), `SELECT filename, status, r2_key, size_bytes FROM assets WHERE deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for res.Next() {
		var name string
		var r row
		if err := res.Scan(&name, &r.status, &r.key, &r.size); err != nil {
			t.Fatal(err)
		}
		rows[name] = append(rows[name], r)
	}
	res.Close()
	var active, assetsTotal int64
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM uploads WHERE completed_at IS NULL AND aborted_at IS NULL`).Scan(&active)
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM assets WHERE deleted_at IS NULL`).Scan(&assetsTotal)
	var ingestJobs int64
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'asset_ingest'`).Scan(&ingestJobs)

	type result struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Stored int64  `json:"stored_size"`
		Sha256 string `json:"sha256"`
		OK     bool   `json:"ok"`
	}
	var results []result
	var failed int
	started := time.Now()
	for _, want := range expected {
		got := rows[want.Name]
		r := result{Name: want.Name, Size: want.Size}
		if len(got) != 1 || got[0].status != "uploaded" || got[0].size != want.Size {
			t.Errorf("%s: database rows %+v, want one uploaded asset of %d bytes", want.Name, got, want.Size)
			failed++
			results = append(results, r)
			continue
		}
		obj, err := storagetest.S3(e.cfg).GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(e.cfg.Bucket), Key: aws.String(got[0].key)})
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		n, err := io.Copy(h, obj.Body)
		_ = obj.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		r.Stored, r.Sha256 = n, hex.EncodeToString(h.Sum(nil))
		r.OK = n == want.Size && r.Sha256 == want.Sha256
		if !r.OK {
			t.Errorf("%s: stored %d bytes sha256 %s, want %d bytes sha256 %s", want.Name, n, r.Sha256, want.Size, want.Sha256)
			failed++
		}
		results = append(results, r)
	}
	if int(assetsTotal) != len(expected) || active != 0 || int(ingestJobs) != len(expected) {
		t.Errorf("assets %d (want %d), uploads still active %d (want 0), asset_ingest jobs %d (want %d)", assetsTotal, len(expected), active, ingestJobs, len(expected))
		failed++
	}
	report, _ := json.MarshalIndent(map[string]any{
		"files": len(expected), "verified_ok": len(expected) - failed, "assets_in_database": assetsTotal, "active_uploads_left": active,
		"ingest_jobs": ingestJobs, "peak_active_uploads_on_server": peak.Load(), "verify_seconds": time.Since(started).Seconds(), "results": results,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "report.json"), report, 0o600)
	t.Logf("RESULT: %d of %d objects match name, size and SHA-256; %d assets, %d uploads still active, %d ingest jobs, peak %d uploads in progress at once", len(expected)-failed, len(expected), assetsTotal, active, ingestJobs, peak.Load())
}
