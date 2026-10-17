package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/storage/storagetest"
)

func TestMain(m *testing.M) { os.Exit(storagetest.Run(m)) }

const mib = 1 << 20

func newClient(t *testing.T) (*storage.Client, storage.Config) {
	t.Helper()
	cfg := storagetest.NewBucket(t)
	c, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}

func newIDs() (ids.WorkspaceID, ids.AssetID) {
	return ids.WorkspaceID{UUID: uuid.Must(uuid.NewV7())}, ids.AssetID{UUID: uuid.Must(uuid.NewV7())}
}

// putResult is what a test looks at in the answer to a part upload.
type putResult struct {
	StatusCode int
	Status     string
	ETag       string
}

// put sends data to a presigned URL the way a browser does (a plain PUT).
func put(t *testing.T, url string, data []byte) putResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return putResult{StatusCode: resp.StatusCode, Status: resp.Status, ETag: resp.Header.Get("ETag")}
}

func fill(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%251)
	}
	return b
}

func TestKeysAreBuiltFromIDs(t *testing.T) {
	ws, asset := newIDs()
	want := "ws/" + ws.String() + "/assets/" + asset.String() + "/original"
	if got := storage.OriginalKey(ws, asset); got != want {
		t.Errorf("OriginalKey = %q, want %q", got, want)
	}
	if !strings.HasPrefix(storage.OriginalKey(ws, asset), storage.AssetPrefix(ws, asset)) {
		t.Error("the original is not under the asset prefix")
	}
	if !strings.HasPrefix(want, "ws/wks_") || !strings.Contains(want, "/assets/ast_") {
		t.Errorf("key %q does not use TypeIDs", want)
	}
}

func TestConfigIsRedacted(t *testing.T) {
	cfg := storage.Config{Endpoint: "http://x", Bucket: "b", AccessKeyID: "AKIDSECRET", SecretAccessKey: "s3cr3t-value"}
	for _, s := range []string{cfg.String(), (&cfg).String()} {
		if strings.Contains(s, "s3cr3t-value") || strings.Contains(s, "AKIDSECRET") {
			t.Errorf("config printed a credential: %s", s)
		}
	}
	if _, err := storage.New(storage.Config{}); !errors.Is(err, storage.ErrNotConfigured) {
		t.Errorf("New with an empty config = %v, want ErrNotConfigured", err)
	}
}

// The whole round trip a browser makes: create, PUT parts to presigned URLs, list, complete, head, delete.
func TestMultipartRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, cfg := newClient(t)
	ws, asset := newIDs()
	key := storage.OriginalKey(ws, asset)

	uploadID, err := c.CreateMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{5 * mib, 5 * mib, 1*mib + 17} // the last part may be small
	var all []byte
	for i, size := range sizes {
		data := fill(size, byte(i))
		all = append(all, data...)
		url, err := c.PresignPart(ctx, key, uploadID, i+1, int64(size))
		if err != nil {
			t.Fatal(err)
		}
		resp := put(t, url, data)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT part %d: %s", i+1, resp.Status)
		}
		if resp.ETag == "" {
			t.Fatalf("PUT part %d returned no ETag", i+1)
		}
	}

	parts, err := c.ListParts(ctx, key, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("ListParts = %+v, want 3 parts", parts)
	}
	for i, p := range parts {
		if p.Number != i+1 || p.Size != int64(sizes[i]) || p.ETag == "" {
			t.Errorf("part %d = %+v, want number %d size %d", i, p, i+1, sizes[i])
		}
	}
	if err := c.Complete(ctx, key, uploadID, parts); err != nil {
		t.Fatal(err)
	}

	info, err := c.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(all)) || info.ContentType != "video/mp4" {
		t.Errorf("Head = %+v, want %d bytes of video/mp4", info, len(all))
	}
	got, err := storagetest.S3(cfg).GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Body.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, got.Body); err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256(all); !bytes.Equal(h.Sum(nil), want[:]) {
		t.Error("the stored object differs from what was uploaded")
	}

	// A completed upload is gone: its id is no longer valid, and aborting it is not an error.
	if _, err := c.ListParts(ctx, key, uploadID); !errors.Is(err, storage.ErrNoSuchUpload) {
		t.Errorf("ListParts after complete = %v, want ErrNoSuchUpload", err)
	}
	if err := c.Abort(ctx, key, uploadID); err != nil {
		t.Errorf("Abort after complete = %v, want nil", err)
	}

	// Deleting the asset's prefix removes the original and nothing of another asset.
	_, other := newIDs()
	otherKey := storage.OriginalKey(ws, other)
	if _, err := storagetest.S3(cfg).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(otherKey), Body: strings.NewReader("keep")}); err != nil {
		t.Fatal(err)
	}
	n, err := c.DeletePrefix(ctx, storage.AssetPrefix(ws, asset))
	if err != nil || n != 1 {
		t.Fatalf("DeletePrefix = %d, %v, want 1, nil", n, err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Head after delete = %v, want ErrNotFound", err)
	}
	if _, err := c.Head(ctx, otherKey); err != nil {
		t.Errorf("another asset's object was touched: %v", err)
	}
}

// Resume: the parts that made it are listed, the rest is uploaded later, and the result is one object.
func TestListPartsShowsWhatToResume(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ws, asset := newIDs()
	key := storage.OriginalKey(ws, asset)
	uploadID, err := c.CreateMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{5 * mib, 5 * mib, 5 * mib, 100}
	upload := func(n int) {
		url, err := c.PresignPart(ctx, key, uploadID, n, int64(sizes[n-1]))
		if err != nil {
			t.Fatal(err)
		}
		if resp := put(t, url, fill(sizes[n-1], byte(n))); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT part %d: %s", n, resp.Status)
		}
	}
	upload(1)
	upload(3)
	parts, err := c.ListParts(ctx, key, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].Number != 1 || parts[1].Number != 3 {
		t.Fatalf("ListParts = %+v, want parts 1 and 3", parts)
	}
	// S3 would complete an upload with part 2 missing (part numbers may have gaps), which is why the assets API checks the
	// parts against ListParts and the declared size before it completes.
	upload(2)
	upload(4)
	parts, err = c.ListParts(ctx, key, uploadID)
	if err != nil || len(parts) != 4 {
		t.Fatalf("ListParts = %+v, %v, want 4 parts", parts, err)
	}
	if err := c.Complete(ctx, key, uploadID, parts); err != nil {
		t.Fatal(err)
	}
	if info, err := c.Head(ctx, key); err != nil || info.Size != int64(15*mib+100) {
		t.Errorf("Head = %+v, %v, want %d bytes", info, err, 15*mib+100)
	}
}

// The Content-Length is signed: a part of another size than the one presigned is refused by the service.
func TestPresignedPartPinsItsSize(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ws, asset := newIDs()
	key := storage.OriginalKey(ws, asset)
	uploadID, err := c.CreateMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	url, err := c.PresignPart(ctx, key, uploadID, 1, 6*mib)
	if err != nil {
		t.Fatal(err)
	}
	if resp := put(t, url, fill(6*mib+1, 1)); resp.StatusCode == http.StatusOK {
		t.Fatal("a part one byte too long was accepted")
	}
	if resp := put(t, url, fill(6*mib-1, 1)); resp.StatusCode == http.StatusOK {
		t.Fatal("a part one byte too short was accepted")
	}
	if resp := put(t, url, fill(6*mib, 1)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the right size was refused: %s", resp.Status)
	}
	// The URL is for this part of this upload: it does not work for another part number.
	other := strings.Replace(url, "partNumber=1", "partNumber=2", 1)
	if resp := put(t, other, fill(6*mib, 1)); resp.StatusCode == http.StatusOK {
		t.Fatal("a presigned URL worked for another part number")
	}
}

func TestAbortDiscardsTheUpload(t *testing.T) {
	ctx := context.Background()
	c, _ := newClient(t)
	ws, asset := newIDs()
	key := storage.OriginalKey(ws, asset)
	uploadID, err := c.CreateMultipart(ctx, key, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Abort(ctx, key, uploadID); err != nil {
		t.Fatal(err)
	}
	if err := c.Abort(ctx, key, uploadID); err != nil {
		t.Errorf("a second Abort = %v, want nil", err)
	}
	if _, err := c.ListParts(ctx, key, uploadID); !errors.Is(err, storage.ErrNoSuchUpload) {
		t.Errorf("ListParts after abort = %v, want ErrNoSuchUpload", err)
	}
	if err := c.Complete(ctx, key, uploadID, []storage.Part{{Number: 1, ETag: `"x"`}}); !errors.Is(err, storage.ErrNoSuchUpload) {
		t.Errorf("Complete after abort = %v, want ErrNoSuchUpload", err)
	}
	if _, err := c.Head(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Head of an aborted upload = %v, want ErrNotFound", err)
	}
}

// DeletePrefix takes exactly the prefix of one asset, with valid ids, and nothing broader or odder.
func TestDeletePrefixRefusesAnythingButOneAssetsPrefix(t *testing.T) {
	ctx := context.Background()
	c, cfg := newClient(t)
	ws, asset := newIDs()
	good := storage.AssetPrefix(ws, asset)
	wsPart, assetPart := ws.String(), asset.String()
	for _, prefix := range []string{
		"", "/", "ws/", "ws/" + wsPart, "ws/" + wsPart + "/", "ws/" + wsPart + "/assets", "ws/" + wsPart + "/assets/",
		"ws/" + wsPart + "/assets//",                                                      // an empty asset segment
		"ws/" + wsPart + "/assets/" + assetPart,                                           // no trailing slash
		"ws/" + wsPart + "/assets/" + assetPart + "/original",                             // an object, not a prefix
		"ws/" + wsPart + "/assets/" + assetPart + "/x/",                                   // an extra segment
		"ws/" + wsPart + "/assets/ast_short/", "ws/" + wsPart + "/assets/" + wsPart + "/", // wrong or malformed asset id
		"ws/wks_short/assets/" + assetPart + "/", "ws/" + assetPart + "/assets/" + assetPart + "/", // wrong or malformed workspace id
		"ws/" + wsPart + "/other/" + assetPart + "/", "wx/" + wsPart + "/assets/" + assetPart + "/",
		"ws/" + wsPart + "/assets/ast_00000000000000000000000000/", // the nil id
		"ws/" + wsPart + "/assets/" + strings.ToUpper(assetPart) + "/",
		"../" + good, good + "../",
	} {
		if _, err := c.DeletePrefix(ctx, prefix); err == nil {
			t.Errorf("DeletePrefix(%q) was accepted", prefix)
		}
	}
	// A workspace's other assets survive a refused call, and the exact prefix works.
	_, other := newIDs()
	for _, key := range []string{storage.OriginalKey(ws, other), good + "original"} {
		if _, err := storagetest.S3(cfg).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key), Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DeletePrefix(ctx, "ws/"+wsPart+"/"); err == nil {
		t.Fatal("a workspace-level prefix was accepted")
	}
	if n, err := c.DeletePrefix(ctx, good); err != nil || n != 1 {
		t.Fatalf("DeletePrefix(%q) = %d, %v", good, n, err)
	}
	if _, err := c.Head(ctx, storage.OriginalKey(ws, other)); err != nil {
		t.Errorf("another asset of the workspace was touched: %v", err)
	}
}

func TestParseAssetPrefix(t *testing.T) {
	ws, asset := newIDs()
	gotWS, gotAsset, ok := storage.ParseAssetPrefix(storage.AssetPrefix(ws, asset))
	if !ok || gotWS != ws || gotAsset != asset {
		t.Fatalf("ParseAssetPrefix = %v %v %v", gotWS, gotAsset, ok)
	}
	if _, _, ok := storage.ParseAssetPrefix(storage.OriginalKey(ws, asset)); ok {
		t.Error("an object key was accepted as a prefix")
	}
}

func TestReady(t *testing.T) {
	c, _ := newClient(t)
	if err := c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A bucket that does not exist is not ready.
	cfg := storagetest.NewBucket(t)
	cfg.Bucket = "no-such-bucket-here"
	bad, err := storage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Ready(context.Background()); err == nil {
		t.Error("Ready passed for a bucket that does not exist")
	}
}

func TestNormalizeETag(t *testing.T) {
	for in, want := range map[string]string{`"abc"`: "abc", `abc`: "abc", `W/"abc"`: "abc", ` "abc-3" `: "abc-3"} {
		if got := storage.NormalizeETag(in); got != want {
			t.Errorf("NormalizeETag(%q) = %q, want %q", in, got, want)
		}
	}
}
