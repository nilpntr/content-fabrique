package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/storage"
)

func TestDerivativeKeys(t *testing.T) {
	ws, asset := newIDs()
	key, err := storage.DerivativeKey(ws, asset, "proxy_720.mp4")
	if err != nil || key != storage.AssetPrefix(ws, asset)+"proxy_720.mp4" {
		t.Fatalf("%q %v", key, err)
	}
	for _, name := range []string{"", "original", ".hidden", "../x", "a/b", "/abs", "a b", "a\x00b", "é.jpg", strings.Repeat("a", 65)} {
		if _, err := storage.DerivativeKey(ws, asset, name); err == nil {
			t.Errorf("derivative name %q was accepted", name)
		}
	}
}

// What the ingest does with storage: read the original as a stream, write derivatives (replacing what is there) and hand
// out presigned GET URLs that expire.
func TestGetPutAndPresignGet(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ws, asset := newIDs()
	key, _ := storage.DerivativeKey(ws, asset, "poster.jpg")

	if _, err := c.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get of a missing object = %v", err)
	}
	data := fill(3*mib+17, 3)
	if err := c.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	obj, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if err != nil || obj.Size != int64(len(data)) || !bytes.Equal(got, data) || obj.ContentType != "image/jpeg" {
		t.Fatalf("Get = %d bytes (size %d, type %q) %v", len(got), obj.Size, obj.ContentType, err)
	}

	// Put replaces.
	if err := c.Put(ctx, key, strings.NewReader("second"), 6, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if info, err := c.Head(ctx, key); err != nil || info.Size != 6 {
		t.Fatalf("after the second Put: %+v %v", info, err)
	}
	// An empty object works; a negative size is refused.
	if err := c.Put(ctx, key+".empty", strings.NewReader(""), 0, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, key, strings.NewReader("x"), -1, "x"); err == nil {
		t.Error("a negative size was accepted")
	}

	url, err := c.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(url) // #nosec G107 -- a test URL
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "second" || !strings.Contains(url, "X-Amz-Expires=300") {
		t.Fatalf("presigned GET: %s %q %s", resp.Status, body, url)
	}
	for _, bad := range []time.Duration{0, -time.Second, 8 * 24 * time.Hour} {
		if _, err := c.PresignGet(ctx, key, bad); err == nil {
			t.Errorf("expiry %v was accepted", bad)
		}
	}
}
