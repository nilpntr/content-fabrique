package storage_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/storage"
)

// A remote sandbox job uploads its outputs to presigned PUT URLs: the URL writes exactly that key, with the signed content
// type, and refuses another one.
func TestPresignPut(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ws, asset := newIDs()
	key, _ := storage.DerivativeKey(ws, asset, "render.mp4")
	put := func(url, contentType, body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	url, err := c.PresignPut(ctx, key, "video/mp4", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if code := put(url, "text/html", "wrong type"); code == http.StatusOK {
		t.Error("a PUT with another content type than the signed one was accepted")
	}
	if code := put(url, "video/mp4", "rendered"); code != http.StatusOK {
		t.Fatalf("presigned PUT: %d", code)
	}
	info, err := c.Head(ctx, key)
	if err != nil || info.Size != 8 || info.ContentType != "video/mp4" {
		t.Fatalf("after the PUT: %+v %v", info, err)
	}
	plain, err := c.PresignPut(ctx, key+".log", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if code := put(plain, "", "log"); code != http.StatusOK {
		t.Fatalf("presigned PUT without a content type: %d", code)
	}
	for _, bad := range []time.Duration{0, 8 * 24 * time.Hour} {
		if _, err := c.PresignPut(ctx, key, "", bad); err == nil {
			t.Errorf("expiry %v was accepted", bad)
		}
	}
}
