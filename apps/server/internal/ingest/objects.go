package ingest

import (
	"context"
	"fmt"
	"io"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/storage"
)

// scopedObjects is the only way the pipeline touches object storage. It is built for one asset of one workspace and can
// read that asset's original and write files under that asset's prefix; there is no call that takes a key, so a bug (or a
// crafted job) cannot read or write another asset's or another workspace's objects.
type scopedObjects struct {
	objs     Objects
	ws       ids.WorkspaceID
	asset    ids.AssetID
	original string // the key stored on the asset row, checked against the one the ids give
}

// newScopedObjects checks that the key on the asset row is the one its ids give. A row whose key points elsewhere is
// refused rather than followed.
func newScopedObjects(objs Objects, ws ids.WorkspaceID, asset ids.AssetID, originalKey string) (*scopedObjects, error) {
	if want := storage.OriginalKey(ws, asset); originalKey != want {
		return nil, fmt.Errorf("the asset's object key is not the key of its ids")
	}
	return &scopedObjects{objs: objs, ws: ws, asset: asset, original: originalKey}, nil
}

// getOriginal opens the uploaded file.
func (s *scopedObjects) getOriginal(ctx context.Context) (storage.Object, error) {
	return s.objs.Get(ctx, s.original)
}

// put writes a derivative named name under the asset's prefix and returns its key. A name that is not a plain file name,
// or that is "original", is refused.
func (s *scopedObjects) put(ctx context.Context, name string, body io.ReadSeeker, size int64, contentType string) (string, error) {
	key, err := storage.DerivativeKey(s.ws, s.asset, name)
	if err != nil {
		return "", err
	}
	if err := s.objs.Put(ctx, key, body, size, contentType); err != nil {
		return "", err
	}
	return key, nil
}

// delete removes a derivative this process wrote. The name must be a plain derivative name, as for put.
func (s *scopedObjects) delete(ctx context.Context, name string) error {
	key, err := storage.DerivativeKey(s.ws, s.asset, name)
	if err != nil {
		return err
	}
	return s.objs.Delete(ctx, key)
}
