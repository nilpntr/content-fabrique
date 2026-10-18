package editor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
)

// MaxImageBytes is the largest picture sent to the model: the API's own limit is 5 MB per image.
const MaxImageBytes = 5 << 20

// ImageGetter reads an object. *storage.Client is the real one.
type ImageGetter interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// ImageResolver is the llm.ImageResolver of the editor: it turns the object key in an image block (a frame, a contact sheet)
// into the picture's bytes when a request is sent. Events keep only the key, never the bytes.
//
// It reads only what it is meant to: the key must be a clean path under ws/{workspace}/, where the workspace is the one of the
// client that sends the request (llm.WorkspaceFromContext), and must name a picture (.jpg, .jpeg or .png). So a reference that
// leaked from another workspace, the original of an asset, or a path with dots in it, is refused.
type ImageResolver struct{ Objects ImageGetter }

var _ llm.ImageResolver = ImageResolver{}

// ResolveImage implements llm.ImageResolver.
func (r ImageResolver) ResolveImage(ctx context.Context, ref string) (string, []byte, error) {
	// The stills of the motion-graphics templates are in the binary: an exact reference of the library, never a path.
	if data, ok := builtinImage(ref); ok {
		return "image/png", data, nil
	}
	ws, ok := llm.WorkspaceFromContext(ctx)
	if !ok {
		return "", nil, errors.New("editor: no workspace in the context of an image reference")
	}
	mediaType, err := CheckImageRef(ws, ref)
	if err != nil {
		return "", nil, err
	}
	if r.Objects == nil {
		return "", nil, errors.New("editor: no object storage to read images from")
	}
	obj, err := r.Objects.Get(ctx, ref)
	if err != nil {
		return "", nil, fmt.Errorf("editor: read image: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > MaxImageBytes {
		return "", nil, fmt.Errorf("editor: the image is %d bytes; at most %d are allowed", obj.Size, MaxImageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, MaxImageBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("editor: read image: %w", err)
	}
	if int64(len(data)) != obj.Size {
		return "", nil, fmt.Errorf("editor: the image read is %d bytes, storage reported %d", len(data), obj.Size)
	}
	switch {
	case mediaType == "image/jpeg" && !bytes.HasPrefix(data, jpegMagic):
		return "", nil, errors.New("editor: the object is not a JPEG")
	case mediaType == "image/png" && !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "", nil, errors.New("editor: the object is not a PNG")
	}
	return mediaType, data, nil
}

// CheckImageRef checks that ref may be sent to the model for workspace ws and returns its media type. It is also what a tool
// uses before it puts a reference in a result.
func CheckImageRef(ws ids.WorkspaceID, ref string) (string, error) {
	prefix := "ws/" + ws.String() + "/"
	switch {
	case ws.IsZero():
		return "", errors.New("editor: an image reference needs a workspace")
	case len(ref) > 300 || !strings.HasPrefix(ref, prefix):
		return "", errors.New("editor: the image reference is not under the workspace")
	case strings.ContainsAny(ref, "\\\x00") || path.Clean(ref) != ref || strings.Contains(ref, "//"):
		return "", errors.New("editor: the image reference is not a clean path")
	}
	switch strings.ToLower(path.Ext(ref)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".png":
		return "image/png", nil
	}
	return "", errors.New("editor: the image reference does not name a JPEG or PNG picture")
}
