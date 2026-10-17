//go:build large

package auth

// The acceptance proof of P1-01, run by hand (not in CI): a 3 GB file uploads with parallel parts, the client is killed in
// the middle, restarted, and resumes from the parts the server reports as completed; the stored object has the checksum of
// the file.
//
//	go test -tags large -run TestLargeResume -v -timeout 30m ./internal/auth/
//
// It needs Docker (a Postgres and a MinIO container, started and removed by the test) and about 3 GB of free disk for the
// generated file (LARGE_DIR picks the directory, LARGE_BYTES another size). The API under test is the real one: the Clerk
// middleware over a locally signed token, the paywall, the handlers, Postgres and River. The client is a separate process
// (this test binary run again as TestLargeResumeClient) that talks to the API over HTTP and to MinIO over the presigned URLs.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
)

const (
	defaultLargeBytes = 3<<30 + 12345 // 3 GiB and a short last part
	clientWorkers     = 8
)

// generate writes size bytes of reproducible pseudo-random data to path and returns its SHA-256.
func generate(t *testing.T, path string, size int64) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	w := bufio.NewWriterSize(io.MultiWriter(f, h), 4<<20)
	rng := rand.NewChaCha8([32]byte{'c', 'f', '-', 'p', '1', '-', '0', '1'})
	buf := make([]byte, 1<<20)
	for left := size; left > 0; {
		n := int64(len(buf))
		if left < n {
			n = left
		}
		_, _ = rng.Read(buf[:n])
		if _, err := w.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestLargeResume(t *testing.T) {
	size := int64(defaultLargeBytes)
	if v := os.Getenv("LARGE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			t.Fatalf("LARGE_BYTES=%q", v)
		}
		size = n
	}
	dir := t.TempDir()
	if d := os.Getenv("LARGE_DIR"); d != "" {
		var err error
		if dir, err = os.MkdirTemp(d, "cf-large-*"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	file := dir + "/video.mp4"
	started := time.Now()
	wantSum := generate(t, file, size)
	t.Logf("generated %d bytes (%.2f GiB) in %s, sha256 %s", size, float64(size)/(1<<30), time.Since(started).Round(time.Millisecond), wantSum)

	e := newAssetsEnv(t)
	srv := httptest.NewServer(e.mux)
	t.Cleanup(srv.Close)
	state := dir + "/state.json"

	type client struct {
		cmd   *exec.Cmd
		lines chan string
		done  chan error
		out   *bytes.Buffer
		mu    sync.Mutex
	}
	start := func(label string) *client {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLargeResumeClient$", "-test.v", "-test.timeout=0")
		cmd.Env = append(os.Environ(), "LARGE_CLIENT=1", "LARGE_API="+srv.URL, "LARGE_TOKEN="+e.token["ada"], "LARGE_FILE="+file,
			"LARGE_STATE="+state, "LARGE_WORKERS="+strconv.Itoa(clientWorkers), "LARGE_SHA256="+wantSum)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		c := &client{cmd: cmd, lines: make(chan string, 1024), done: make(chan error, 1), out: &bytes.Buffer{}}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		go func() {
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				line := sc.Text()
				c.mu.Lock()
				c.out.WriteString(line + "\n")
				c.mu.Unlock()
				if strings.HasPrefix(line, "CLIENT ") {
					select {
					case c.lines <- line:
					default:
					}
				}
			}
			close(c.lines)
			c.done <- cmd.Wait()
		}()
		t.Logf("%s: client started (pid %d)", label, cmd.Process.Pid)
		return c
	}

	// Run 1: kill the client once about 40% of the parts are done.
	run1 := time.Now()
	c1 := start("run 1")
	var partCount, doneByClient int
	killed := false
	for line := range c1.lines {
		switch {
		case strings.HasPrefix(line, "CLIENT created "):
			fmt.Sscanf(line, "CLIENT created parts=%d", &partCount)
		case strings.HasPrefix(line, "CLIENT part-done "):
			doneByClient++
			if partCount > 0 && doneByClient >= partCount*4/10 && !killed {
				killed = true
				if err := c1.cmd.Process.Kill(); err != nil { // SIGKILL: no clean-up, like closing the tab
					t.Fatal(err)
				}
				t.Logf("run 1: killed the client with SIGKILL after %s, with %d of %d parts reported done by the client", time.Since(run1).Round(time.Millisecond), doneByClient, partCount)
			}
		}
	}
	<-c1.done
	if !killed {
		t.Fatalf("the client finished before it could be killed:\n%s", c1.out.String())
	}
	run1Wall := time.Since(run1)

	// What does the server say is done?
	assetID := readState(t, state)
	var st api.UploadStateResponse
	e.call(http.MethodGet, "/v1/assets/"+assetID+"/upload", "ada", nil, http.StatusOK, &st)
	haveAfterKill := len(st.CompletedParts)
	if st.Upload.Status != api.UploadStatusActive || haveAfterKill < 1 || haveAfterKill >= st.Upload.PartCount {
		t.Fatalf("after the kill the server reports %d of %d parts (%s), want some but not all", haveAfterKill, st.Upload.PartCount, st.Upload.Status)
	}
	t.Logf("after the kill the server reports %d of %d parts completed (%d are in the object store, the rest were in flight or not started)", haveAfterKill, st.Upload.PartCount, haveAfterKill)

	// Run 2: a new process resumes from the server's list and completes.
	run2 := time.Now()
	c2 := start("run 2")
	var resume string
	for line := range c2.lines {
		if strings.HasPrefix(line, "CLIENT resume ") {
			resume = line
		}
	}
	if err := <-c2.done; err != nil {
		t.Fatalf("the resumed client failed: %v\n%s", err, c2.out.String())
	}
	run2Wall := time.Since(run2)
	var have, missing, uploaded int
	fmt.Sscanf(resume, "CLIENT resume have=%d missing=%d", &have, &missing)
	for _, l := range strings.Split(c2.out.String(), "\n") {
		if strings.HasPrefix(l, "CLIENT part-done ") {
			uploaded++
		}
	}
	if have < haveAfterKill || have+missing != st.Upload.PartCount || uploaded != missing {
		t.Fatalf("the resumed client: have=%d missing=%d uploaded=%d (parts %d, server had %d after the kill)\n%s", have, missing, uploaded, st.Upload.PartCount, haveAfterKill, c2.out.String())
	}
	if !strings.Contains(c2.out.String(), "CLIENT complete ") {
		t.Fatalf("the resumed client did not complete:\n%s", c2.out.String())
	}

	// The asset is uploaded, the object has the right size, and the stored bytes hash to the file's SHA-256.
	var asset api.Asset
	e.call(http.MethodGet, "/v1/assets/"+assetID, "ada", nil, http.StatusOK, &asset)
	if asset.Status != api.AssetStatusUploaded || asset.SizeBytes != size {
		t.Fatalf("asset after resume: %+v", asset)
	}
	if n := e.jobCount("asset_ingest", assetID); n != 1 {
		t.Errorf("asset_ingest jobs = %d, want 1", n)
	}
	var key string
	if err := e.pool.QueryRow(context.Background(), `SELECT r2_key FROM assets`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	head, err := e.objs.Head(context.Background(), key)
	if err != nil || head.Size != size {
		t.Fatalf("stored object: %+v, %v, want %d bytes", head, err, size)
	}
	verify := time.Now()
	gotSum := hashObject(t, e.cfg, key)
	if gotSum != wantSum {
		t.Fatalf("the stored object has sha256 %s, the file has %s", gotSum, wantSum)
	}
	t.Logf("stored object: %d bytes, sha256 %s matches the file (read back in %s)", head.Size, gotSum, time.Since(verify).Round(time.Millisecond))

	partSize := st.Upload.PartSize
	perRun := func(parts int) string {
		return fmt.Sprintf("%d parts, %.0f MiB", parts, float64(parts)*float64(partSize)/(1<<20))
	}
	t.Logf("RESULT: %d bytes in %d parts of %d MiB with %d parallel parts per client run", size, st.Upload.PartCount, partSize>>20, clientWorkers)
	t.Logf("RESULT: run 1 (killed): %s wall, %s done by the client, %d parts in the store at the kill", run1Wall.Round(time.Millisecond), perRun(doneByClient), haveAfterKill)
	t.Logf("RESULT: run 2 (resumed from %d parts, uploaded the other %d): %s wall, %s", have, missing, run2Wall.Round(time.Millisecond), perRun(uploaded))
	t.Logf("RESULT: throughput run 2: %.0f MiB/s; whole upload (both runs, %d parts uploaded in total): %.0f MiB/s", float64(uploaded)*float64(partSize)/(1<<20)/run2Wall.Seconds(),
		doneByClient+uploaded, float64(size)/(1<<20)/(run1Wall+run2Wall).Seconds())
}

func readState(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		AssetID string `json:"asset_id"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.AssetID == "" {
		t.Fatalf("state file: %s %v", raw, err)
	}
	return s.AssetID
}

func hashObject(t *testing.T, cfg storage.Config, key string) string {
	t.Helper()
	out, err := storagetest.S3(cfg).GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, out.Body); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestLargeResumeClient is the uploader, run as its own process by TestLargeResume (it does nothing otherwise). It is what
// the browser's upload manager (P1-02) does: create the upload once, remember the asset id, ask the server which parts it
// has, upload the others with parallel PUTs to presigned URLs, and complete.
func TestLargeResumeClient(t *testing.T) {
	if os.Getenv("LARGE_CLIENT") != "1" {
		t.Skip("run by TestLargeResume")
	}
	base, token, path, statePath := os.Getenv("LARGE_API"), os.Getenv("LARGE_TOKEN"), os.Getenv("LARGE_FILE"), os.Getenv("LARGE_STATE")
	workers, _ := strconv.Atoi(os.Getenv("LARGE_WORKERS"))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, _ := f.Stat()
	size := fi.Size()
	say := func(format string, args ...any) { fmt.Printf("CLIENT "+format+"\n", args...) }

	hc := &http.Client{Timeout: 10 * time.Minute}
	callAPI := func(method, p string, in, out any, want int) error {
		var body io.Reader
		if in != nil {
			raw, _ := json.Marshal(in)
			body = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, base+p, body)
		req.Header.Set("Authorization", "Bearer "+token)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			return fmt.Errorf("%s %s = %d: %s", method, p, resp.StatusCode, raw)
		}
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	// mustAPI is for the main goroutine of the client.
	mustAPI := func(method, p string, in, out any, want int) {
		t.Helper()
		if err := callAPI(method, p, in, out, want); err != nil {
			t.Fatal(err)
		}
	}

	// Create the upload the first time; after a restart the asset id is in the state file.
	var assetID string
	if raw, err := os.ReadFile(statePath); err == nil {
		var s struct {
			AssetID string `json:"asset_id"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		assetID = s.AssetID
	} else {
		var c api.CreateAssetResponse
		mustAPI(http.MethodPost, "/v1/assets", api.CreateAssetRequest{Filename: "large.mp4", SizeBytes: size, ContentType: "video/mp4", Sha256: strPtr(os.Getenv("LARGE_SHA256"))}, &c, http.StatusCreated)
		assetID = c.Asset.Id
		raw, _ := json.Marshal(map[string]string{"asset_id": assetID})
		if err := os.WriteFile(statePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		say("created parts=%d part_size=%d asset=%s", c.Upload.PartCount, c.Upload.PartSize, assetID)
	}

	// What does the server have?
	var st api.UploadStateResponse
	mustAPI(http.MethodGet, "/v1/assets/"+assetID+"/upload", nil, &st, http.StatusOK)
	if st.Upload.Status != api.UploadStatusActive {
		t.Fatalf("upload is %s", st.Upload.Status)
	}
	have := map[int]bool{}
	for _, p := range st.CompletedParts {
		have[p.PartNumber] = true
	}
	var missing []int
	for n := 1; n <= st.Upload.PartCount; n++ {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	say("resume have=%d missing=%d", len(have), len(missing))

	// A feeder asks for URLs in batches of 20 as the workers need them; the workers PUT parts in parallel.
	type job struct{ api.PartUrl }
	jobs := make(chan job, 40)
	go func() {
		defer close(jobs)
		for len(missing) > 0 {
			batch := missing[:min(len(missing), 20)]
			missing = missing[len(batch):]
			var out api.PresignPartsResponse
			if err := callAPI(http.MethodPost, "/v1/assets/"+assetID+"/parts", api.PresignPartsRequest{PartNumbers: batch}, &out, http.StatusOK); err != nil {
				t.Errorf("presign: %v", err)
				return
			}
			for _, p := range out.Parts {
				jobs <- job{p}
			}
		}
	}()
	var wg sync.WaitGroup
	var uploaded atomic.Int64
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				off := int64(j.PartNumber-1) * st.Upload.PartSize
				req, _ := http.NewRequest(http.MethodPut, j.Url, io.NopCloser(io.NewSectionReader(f, off, j.SizeBytes)))
				req.ContentLength = j.SizeBytes
				resp, err := hc.Do(req)
				if err != nil {
					t.Errorf("PUT part %d: %v", j.PartNumber, err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("PUT part %d: %s", j.PartNumber, resp.Status)
					return
				}
				uploaded.Add(1)
				say("part-done %d", j.PartNumber)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	// The ETags come from the server's own list, as after any resume.
	mustAPI(http.MethodGet, "/v1/assets/"+assetID+"/upload", nil, &st, http.StatusOK)
	parts := make([]api.CompletedPartRef, len(st.CompletedParts))
	for i, p := range st.CompletedParts {
		parts[i] = api.CompletedPartRef{PartNumber: p.PartNumber, Etag: p.Etag}
	}
	var done api.Asset
	mustAPI(http.MethodPost, "/v1/assets/"+assetID+"/complete", api.CompleteAssetRequest{Parts: parts}, &done, http.StatusOK)
	say("complete status=%s uploaded_this_run=%d", done.Status, uploaded.Load())
}

func strPtr(s string) *string { return &s }
