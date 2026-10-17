// Package storage is the S3 client for object storage: Cloudflare R2 in staging and production (the EU jurisdiction
// endpoint, D-15), MinIO on a developer machine. It carries the multipart upload calls of the assets API (P1-01), the
// presigned part URLs a browser uploads to, and the object calls the worker needs.
//
// Keys are never taken from a client: they are built here from the workspace and asset ids (OriginalKey, AssetPrefix), and
// the ids come from the verified token and from rows of that workspace.
package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/nilpntr/content-fabrique/internal/ids"
)

// PartURLExpiry is how long a presigned part URL works.
const PartURLExpiry = 15 * time.Minute

// Limits of S3 multipart uploads (R2 has the same): parts of 5 MiB to 5 GiB (the last may be smaller), up to 10,000 parts.
const (
	MaxPartSize = 5 << 30
	MaxParts    = 10000
)

// Errors the callers branch on. Anything else is an unexpected failure of the service or the network.
var (
	// ErrNotFound: the object does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrNoSuchUpload: the multipart upload does not exist (any more): it was completed, aborted or expired.
	ErrNoSuchUpload = errors.New("storage: no such multipart upload")
	// ErrInvalidParts: the parts of a complete request were refused (missing part, wrong ETag, part too small).
	ErrInvalidParts = errors.New("storage: invalid parts")
	// ErrNotConfigured is returned by New when the settings are empty.
	ErrNotConfigured = errors.New("storage: object storage is not configured")
)

// Config is the connection to the bucket (the R2_* variables of .env.example).
type Config struct {
	// Endpoint is R2_ENDPOINT: http://localhost:9000 for MinIO, https://<account>.eu.r2.cloudflarestorage.com for R2.
	Endpoint string
	// Bucket is R2_BUCKET.
	Bucket string
	// AccessKeyID and SecretAccessKey are R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY. The secret is never logged.
	AccessKeyID     string
	SecretAccessKey string
	// Jurisdiction is R2_JURISDICTION: "eu" for real R2, empty for MinIO.
	Jurisdiction string
}

// Configured reports whether every setting a client needs is present (the jurisdiction may be empty). It is the one
// definition of "object storage is configured": config.Config.ObjectStorageConfigured asks it.
func (c Config) Configured() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// String describes the config without its secret, so a stray %v cannot leak it.
func (c Config) String() string {
	return fmt.Sprintf("storage{endpoint=%s bucket=%s jurisdiction=%q}", c.Endpoint, c.Bucket, c.Jurisdiction)
}

// GoString makes %#v print the same redacted form.
func (c Config) GoString() string { return c.String() }

// Client talks to one bucket.
type Client struct {
	s3      *s3.Client
	presign *s3.PresignClient
	bucket  string
	probeOK atomic.Int64 // unix time of the last passing readiness probe
}

// New builds a client. It makes no request. Options are explicit, so the SDK's default chain never reads AWS_* variables
// or files.
func New(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, ErrNotConfigured
	}
	region := "auto" // R2
	if cfg.Jurisdiction == "" {
		region = "us-east-1" // MinIO
	}
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// MinIO has no bucket subdomains. Real R2 is addressed by the jurisdiction's endpoint.
		UsePathStyle:     cfg.Jurisdiction == "",
		RetryMaxAttempts: 3,
		// Only where the operation requires it (DeleteObjects): a presigned part URL must not carry checksum parameters
		// that a browser's plain PUT would not send.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Client{s3: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket}, nil
}

// ParseAssetPrefix reads a prefix of the exact form AssetPrefix builds, ws/{workspace}/assets/{asset}/, with a valid
// workspace and asset id, and nothing else: a workspace-level prefix, an empty asset segment or an extra segment is not one.
func ParseAssetPrefix(prefix string) (ids.WorkspaceID, ids.AssetID, bool) {
	parts := strings.Split(prefix, "/")
	if len(parts) != 5 || parts[0] != "ws" || parts[2] != "assets" || parts[4] != "" {
		return ids.WorkspaceID{}, ids.AssetID{}, false
	}
	ws, err := ids.ParseWorkspaceID(parts[1])
	if err != nil {
		return ids.WorkspaceID{}, ids.AssetID{}, false
	}
	asset, err := ids.ParseAssetID(parts[3])
	if err != nil {
		return ids.WorkspaceID{}, ids.AssetID{}, false
	}
	return ws, asset, true
}

// AssetPrefix is the prefix of every object of one asset: ws/{workspace}/assets/{asset}/.
func AssetPrefix(ws ids.WorkspaceID, asset ids.AssetID) string {
	return "ws/" + ws.String() + "/assets/" + asset.String() + "/"
}

// OriginalKey is the key of the uploaded file: ws/{workspace}/assets/{asset}/original.
func OriginalKey(ws ids.WorkspaceID, asset ids.AssetID) string {
	return AssetPrefix(ws, asset) + "original"
}

// DerivativeKey is the key of a generated file of an asset: ws/{workspace}/assets/{asset}/{name}. name is a plain file
// name (ValidDerivativeName), so it cannot reach outside the asset's prefix.
func DerivativeKey(ws ids.WorkspaceID, asset ids.AssetID, name string) (string, error) {
	if !ValidDerivativeName(name) {
		return "", fmt.Errorf("storage: %q is not a valid derivative name", name)
	}
	return AssetPrefix(ws, asset) + name, nil
}

// FramePrefix is the prefix of the still frames cut from an asset's proxy for the editor agent: ws/{workspace}/assets/{asset}/frames/.
// It sits under the asset's prefix, so deleting the asset (DeletePrefix) deletes its frames too.
func FramePrefix(ws ids.WorkspaceID, asset ids.AssetID) string {
	return AssetPrefix(ws, asset) + "frames/"
}

// FrameKey is the key of the frame at atMs of an asset's proxy, scaled to longSide pixels on its long side:
// ws/{workspace}/assets/{asset}/frames/{at_ms}_{long_side}.jpg, the time zero-padded to eight digits (up to 27 hours) so the
// names sort by time. The same asset, time and size always give the same key, which is what makes the frames a cache.
func FrameKey(ws ids.WorkspaceID, asset ids.AssetID, atMs int64, longSide int) (string, error) {
	if atMs < 0 || atMs > 99_999_999 || longSide < 16 || longSide > 4096 {
		return "", fmt.Errorf("storage: frame at %d ms, %d px is out of range", atMs, longSide)
	}
	return fmt.Sprintf("%s%08d_%d.jpg", FramePrefix(ws, asset), atMs, longSide), nil
}

// ValidDerivativeName reports whether name is a short plain file name: letters, digits, '_', '-' and '.', not starting with
// a dot, and not "original" (the uploaded file is never overwritten by a derivative).
func ValidDerivativeName(name string) bool {
	if name == "" || len(name) > 64 || name[0] == '.' || name == "original" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// The files of a render (P3-06), under RenderPrefix. The names are fixed: a render writes each once.
const (
	RenderMP4Name          = "render.mp4"
	RenderContactSheetName = "contact_sheet.jpg"
	RenderTimeMapName      = "time_map.json"
)

// RenderPrefix is the prefix of every object of one render: ws/{workspace}/projects/{project}/renders/{render}/. It is under
// the project, not the asset, because a render belongs to a project's timeline and outlives none of it.
func RenderPrefix(ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID) string {
	return "ws/" + ws.String() + "/projects/" + project.String() + "/renders/" + render.String() + "/"
}

// RenderKey is the key of one file of a render. name must be one of the three RenderXxxName constants.
func RenderKey(ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID, name string) (string, error) {
	switch name {
	case RenderMP4Name, RenderContactSheetName, RenderTimeMapName:
		return RenderPrefix(ws, project, render) + name, nil
	}
	return "", fmt.Errorf("storage: %q is not a file of a render", name)
}

// Part is one uploaded part of a multipart upload.
type Part struct {
	Number int
	ETag   string // as the service returns it, usually with quotes
	Size   int64
}

// NormalizeETag removes the quotes and the weak-validator prefix, so two spellings of one ETag compare equal.
func NormalizeETag(etag string) string {
	etag = strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	return strings.Trim(etag, `"`)
}

// ObjectInfo is what HeadObject reports.
type ObjectInfo struct {
	Size        int64
	ETag        string
	ContentType string
}

// CreateMultipart starts a multipart upload of key and returns its upload id.
func (c *Client) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	out, err := c.s3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("create multipart upload: %w", mapError(err))
	}
	if out.UploadId == nil || *out.UploadId == "" {
		return "", errors.New("create multipart upload: no upload id in the response")
	}
	return *out.UploadId, nil
}

// PresignPart returns a URL for a PUT of exactly size bytes as part number partNumber of the upload. The Content-Length is
// part of the signature, so a client cannot send a part of another size with it. The URL works for PartURLExpiry.
func (c *Client) PresignPart(ctx context.Context, key, uploadID string, partNumber int, size int64) (string, error) {
	if partNumber < 1 || partNumber > MaxParts || size < 1 {
		return "", fmt.Errorf("presign part: part %d of %d bytes is not valid", partNumber, size)
	}
	req, err := c.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(int32(partNumber)), //nolint:gosec // 1..10000, checked above
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(PartURLExpiry))
	if err != nil {
		return "", fmt.Errorf("presign part: %w", mapError(err))
	}
	return req.URL, nil
}

// ListParts returns every part the upload holds so far, in part number order.
func (c *Client) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	var (
		parts  []Part
		marker *string
	)
	for {
		out, err := c.s3.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(c.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
			PartNumberMarker: marker, MaxParts: aws.Int32(1000),
		})
		if err != nil {
			return nil, fmt.Errorf("list parts: %w", mapError(err))
		}
		for _, p := range out.Parts {
			parts = append(parts, Part{Number: int(aws.ToInt32(p.PartNumber)), ETag: aws.ToString(p.ETag), Size: aws.ToInt64(p.Size)})
		}
		if !aws.ToBool(out.IsTruncated) || out.NextPartNumberMarker == nil {
			return parts, nil
		}
		marker = out.NextPartNumberMarker
	}
}

// Complete finishes the upload from parts, which must be in ascending part number order.
func (c *Client) Complete(ctx context.Context, key, uploadID string, parts []Part) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(int32(p.Number)), ETag: aws.String(p.ETag)} //nolint:gosec // 1..10000
	}
	_, err := c.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", mapError(err))
	}
	return nil
}

// Abort discards the upload and its parts. An upload that does not exist any more is not an error: the goal is reached.
func (c *Client) Abort(ctx context.Context, key, uploadID string) error {
	_, err := c.s3.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if err != nil {
		if mapped := mapError(err); !errors.Is(mapped, ErrNoSuchUpload) {
			return fmt.Errorf("abort multipart upload: %w", mapped)
		}
	}
	return nil
}

// Head reports the size of an object, or ErrNotFound.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("head object: %w", mapError(err))
	}
	return ObjectInfo{Size: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag), ContentType: aws.ToString(out.ContentType)}, nil
}

// Object is a stream from an object, with the size storage reports. The caller closes Body.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// Get opens an object for reading, or ErrNotFound. The size is what storage says the body holds.
func (c *Client) Get(ctx context.Context, key string) (Object, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return Object{}, fmt.Errorf("get object: %w", mapError(err))
	}
	return Object{Body: out.Body, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}, nil
}

// MaxPutSize is the largest object Put writes: one request, and R2 and S3 allow 5 GB for it. A derivative is far smaller.
const MaxPutSize = 4 << 30

// Put writes an object of exactly size bytes from body, replacing any object at key. body must be seekable (a file): the
// request is signed over its content and may be retried.
func (c *Client) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if size < 0 || size > MaxPutSize {
		return fmt.Errorf("put object: size %d is outside 0..%d", size, int64(MaxPutSize))
	}
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object: %w", mapError(err))
	}
	return nil
}

// Delete removes one object. An object that is not there is not an error: the goal is reached.
func (c *Client) Delete(ctx context.Context, key string) error {
	if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}); err != nil {
		if mapped := mapError(err); !errors.Is(mapped, ErrNotFound) {
			return fmt.Errorf("delete object: %w", mapped)
		}
	}
	return nil
}

// PresignGet returns a URL that reads the object for the given time. The key is built by the server from ids, never by a
// client.
func (c *Client) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	if expires <= 0 || expires > 7*24*time.Hour {
		return "", fmt.Errorf("presign get: expiry %v is outside (0, 7 days]", expires)
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}, s3.WithPresignExpires(expires))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", mapError(err))
	}
	return req.URL, nil
}

// DeletePrefix deletes every object of one asset and returns how many it deleted. The prefix must be exactly
// ws/{workspace}/assets/{asset}/ with valid ids (ParseAssetPrefix), so a bug can never empty a bucket, a workspace or a
// path that merely starts like one.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if _, _, ok := ParseAssetPrefix(prefix); !ok {
		return 0, fmt.Errorf("delete prefix: %q is not the prefix of one asset", prefix)
	}
	return c.deletePrefix(ctx, prefix)
}

// DeleteRenderObjects deletes every object of one render (ws/{workspace}/projects/{project}/renders/{render}/) and returns
// how many it deleted. The prefix is built from the three ids, so it can never reach another render, project or workspace.
func (c *Client) DeleteRenderObjects(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID) (int, error) {
	return c.deletePrefix(ctx, RenderPrefix(ws, project, render))
}

func (c *Client) deletePrefix(ctx context.Context, prefix string) (int, error) {
	deleted := 0
	pager := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{Bucket: aws.String(c.bucket), Prefix: aws.String(prefix)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return deleted, fmt.Errorf("list objects: %w", mapError(err))
		}
		if len(page.Contents) == 0 {
			continue
		}
		objs := make([]types.ObjectIdentifier, len(page.Contents))
		for i, o := range page.Contents {
			objs[i] = types.ObjectIdentifier{Key: o.Key}
		}
		out, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucket), Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return deleted, fmt.Errorf("delete objects: %w", mapError(err))
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return deleted, fmt.Errorf("delete objects: %d of %d failed (first: %s %s)", len(out.Errors), len(objs), aws.ToString(e.Code), aws.ToString(e.Message))
		}
		deleted += len(objs)
	}
	return deleted, nil
}

// readyCacheFor is how long a passing probe is trusted, so /readyz polling does not write every time.
const readyCacheFor = 30 * time.Second

// Ready is the object storage readiness check. It follows the doctor's storage check: put, get and delete a one-byte
// object, which proves the endpoint, signing and credentials. A pass is trusted for 30 seconds; a failure is not cached.
func (c *Client) Ready(ctx context.Context) error {
	if last := c.probeOK.Load(); last != 0 && time.Since(time.Unix(last, 0)) < readyCacheFor {
		return nil
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	key := "healthcheck/" + hex.EncodeToString(rnd[:])
	if _, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket), Key: aws.String(key), Body: bytes.NewReader([]byte{'x'}), ContentLength: aws.Int64(1),
	}); err != nil {
		return fmt.Errorf("object storage put: %w", mapError(err))
	}
	deleted := false
	defer func() {
		if !deleted {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = c.s3.DeleteObject(cctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
		}
	}()
	got, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("object storage get: %w", mapError(err))
	}
	data, err := io.ReadAll(io.LimitReader(got.Body, 16))
	_ = got.Body.Close()
	if err != nil || len(data) != 1 {
		return errors.New("object storage get: unexpected content")
	}
	deleted = true
	if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("object storage delete: %w", mapError(err))
	}
	c.probeOK.Store(time.Now().Unix())
	return nil
}

// mapError turns the service's answers into the package errors. Anything else is returned as it came.
func mapError(err error) error {
	var noUpload *types.NoSuchUpload
	var notFound *types.NotFound
	var noKey *types.NoSuchKey
	switch {
	case errors.As(err, &noUpload):
		return fmt.Errorf("%w: %w", ErrNoSuchUpload, err)
	case errors.As(err, &notFound), errors.As(err, &noKey):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchUpload":
			return fmt.Errorf("%w: %w", ErrNoSuchUpload, err)
		case "NoSuchKey", "NotFound":
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case "InvalidPart", "InvalidPartOrder", "EntityTooSmall":
			return fmt.Errorf("%w: %w", ErrInvalidParts, err)
		}
	}
	return err
}
