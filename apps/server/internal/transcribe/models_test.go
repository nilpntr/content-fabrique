package transcribe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func serve(t *testing.T, body []byte, hits *atomic.Int32) ModelSpec {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(body)
	return ModelSpec{Name: "t", File: "ggml-t.bin", URL: srv.URL + "/m.bin", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

func TestEnsureDownloadsOnceAndVerifies(t *testing.T) {
	var hits atomic.Int32
	spec := serve(t, []byte("model bytes"), &hits)
	c := &ModelCache{Dir: filepath.Join(t.TempDir(), "cache")}
	p, err := c.Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("%v %v", fi, err)
	}
	if _, err := c.Ensure(context.Background(), spec); err != nil || hits.Load() != 1 {
		t.Fatalf("second call: err %v, %d downloads", err, hits.Load())
	}
	// A new cache object over the same directory trusts nothing: it hashes the file, finds it good, and does not download.
	c2 := &ModelCache{Dir: c.Dir}
	if _, err := c2.Ensure(context.Background(), spec); err != nil || hits.Load() != 1 {
		t.Fatalf("restart: err %v, %d downloads", err, hits.Load())
	}
	entries, _ := os.ReadDir(c.Dir)
	if len(entries) != 1 {
		t.Errorf("leftovers in the cache: %v", entries)
	}
}

func TestEnsureReplacesACorruptFile(t *testing.T) {
	var hits atomic.Int32
	spec := serve(t, []byte("model bytes"), &hits)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, spec.File), []byte("tampered!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &ModelCache{Dir: dir}
	p, err := c.Ensure(context.Background(), spec)
	if err != nil || hits.Load() != 1 {
		t.Fatalf("%v, %d downloads", err, hits.Load())
	}
	if b, _ := os.ReadFile(p); string(b) != "model bytes" {
		t.Errorf("file holds %q", b)
	}
	// A file changed after it was checked is caught the next time.
	if err := os.WriteFile(p, []byte("model bytez"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ensure(context.Background(), spec); err != nil || hits.Load() != 2 {
		t.Fatalf("%v, %d downloads", err, hits.Load())
	}
}

func TestEnsureRefusesADownloadWithTheWrongHash(t *testing.T) {
	var hits atomic.Int32
	spec := serve(t, []byte("model bytes"), &hits)
	spec.SHA256 = hex.EncodeToString(make([]byte, 32))
	dir := t.TempDir()
	c := &ModelCache{Dir: dir}
	if _, err := c.Ensure(context.Background(), spec); !errors.Is(err, ErrModelHash) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a file that failed the check was kept: %v", entries)
	}
}

func TestEnsureStopsAServerThatSendsTooMuchOrTheWrongSize(t *testing.T) {
	var hits atomic.Int32
	spec := serve(t, []byte("model bytes plus a lot more"), &hits)
	spec.Size = 5 // the model is said to be 5 bytes
	c := &ModelCache{Dir: t.TempDir()}
	if _, err := c.Ensure(context.Background(), spec); err == nil {
		t.Fatal("a download larger than the model was accepted")
	}
}

func TestEnsureRejectsBadSpecsAndPlainHTTP(t *testing.T) {
	c := &ModelCache{Dir: t.TempDir()}
	good := hex.EncodeToString(make([]byte, 32))
	for name, spec := range map[string]ModelSpec{
		"path in the file name": {Name: "x", File: "../x.bin", URL: "https://example.com/x", SHA256: good},
		"no hash":               {Name: "x", File: "x.bin", URL: "https://example.com/x"},
		"plain http":            {Name: "x", File: "x.bin", URL: "http://example.com/x", SHA256: good},
	} {
		if _, err := c.Ensure(context.Background(), spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPinnedModelsAreWellFormed(t *testing.T) {
	for _, m := range append(SpeechModels, ModelSileroVAD) {
		if len(m.SHA256) != 64 || !fileNameRE.MatchString(m.File) || m.Size <= 0 {
			t.Errorf("%s: %+v", m.Name, m)
		}
		if got := m.URL; len(got) < 8 || got[:8] != "https://" {
			t.Errorf("%s: URL %q", m.Name, got)
		}
		if m.DTW == "" && m.Name != ModelSileroVAD.Name {
			t.Errorf("%s: no DTW preset, so no usable word timings", m.Name)
		}
	}
	if _, ok := SpeechModel("large-v3-turbo-q5_0"); !ok {
		t.Error("the default model is missing")
	}
	if _, ok := SpeechModel("nope"); ok {
		t.Error("an unknown model was found")
	}
}
