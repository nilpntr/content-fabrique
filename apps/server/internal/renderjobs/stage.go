package renderjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// stager is the render.AssetResolver of a render job. The sandbox has no network, so the footage is downloaded into the job
// directory before ffmpeg runs; the compiler asks for each asset it plays (once per asset and variant), and the stager fetches
// it then: only the footage the timeline uses is downloaded, and a file is fetched once however many clips use it.
//
// What it may read is exactly the project's ready footage (the inputs the job listed with the workspace and project of the
// render). A preview reads each video's 720p proxy (an audio file has none and is read as it is), the final profile reads the
// originals, and every key is checked against the ids of the row it came from before it is fetched.
//
// A failure cannot be told apart from a compile error once the compiler wraps it, so the stager remembers it: fatal is the
// timeline's or the footage's fault (the render fails), infra is ours (the job is retried). The compiler's own error is
// ignored when either is set.
type stager struct {
	r      *run
	ctx    context.Context
	inputs map[ids.AssetID]store.RenderInput
	cache  map[string]render.ResolvedAsset
	total  int64
	// captions says whether the timeline has an enabled captions track: only then does a clip need the transcript of its asset.
	captions bool

	fatal *fileError
	infra error
}

func newStager(ctx context.Context, r *run, in []store.RenderInput, captions bool) *stager {
	m := make(map[ids.AssetID]store.RenderInput, len(in))
	for _, i := range in {
		m[i.AssetID] = i
	}
	return &stager{r: r, ctx: ctx, inputs: m, cache: map[string]render.ResolvedAsset{}, captions: captions}
}

// Resolve implements render.AssetResolver.
func (s *stager) Resolve(assetID string, variant render.Variant) (render.ResolvedAsset, error) {
	cacheKey := assetID + "/" + string(variant)
	if ra, ok := s.cache[cacheKey]; ok {
		return ra, nil
	}
	ra, err := s.stage(assetID, variant)
	if err != nil {
		return render.ResolvedAsset{}, err
	}
	s.cache[cacheKey] = ra
	return ra, nil
}

func (s *stager) stage(assetID string, variant render.Variant) (render.ResolvedAsset, error) {
	ctx := s.ctx
	ws := s.r.ws
	id, err := ids.ParseAssetID(assetID)
	if err != nil {
		return s.refuse(ReasonInvalidTimeline, errors.New("the timeline names an asset id that is not valid"))
	}
	in, ok := s.inputs[id]
	if !ok {
		return s.refuse(ReasonSourceUnavailable, fmt.Errorf("asset %s is not available in this project (deleted, not ready, or not part of it)", assetID))
	}

	var key string
	var size int64
	switch {
	case variant == render.VariantProxy && in.Kind == store.AssetKindVideo:
		if in.ProxyKey == nil || in.ProxySizeBytes == nil {
			return s.refuse(ReasonSourceUnavailable, fmt.Errorf("asset %s has no 720p proxy", assetID))
		}
		want, kerr := storage.DerivativeKey(ws, id, "proxy_720.mp4")
		if kerr != nil || *in.ProxyKey != want {
			return s.refuse(ReasonProcessing, fmt.Errorf("the proxy key of asset %s is not the key of its ids", assetID))
		}
		key, size = want, *in.ProxySizeBytes
	case variant == render.VariantProxy || variant == render.VariantOriginal:
		// The original: for the final profile, and for an audio file in the preview (audio has no proxy).
		want := storage.OriginalKey(ws, id)
		if in.R2Key != want {
			return s.refuse(ReasonProcessing, fmt.Errorf("the object key of asset %s is not the key of its ids", assetID))
		}
		key, size = want, in.SizeBytes
	default:
		return s.refuse(ReasonInvalidTimeline, fmt.Errorf("unknown asset variant %q", variant))
	}
	if s.total += size; s.total > MaxInputBytes {
		return s.refuse(ReasonInputsTooLarge, fmt.Errorf("the footage of this timeline is over %d bytes", int64(MaxInputBytes)))
	}

	dest := filepath.Join(s.r.dir, "in", strconv.Itoa(len(s.cache)))
	if err := s.r.download(ctx, key, dest, size); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return s.refuse(ReasonSourceUnavailable, fmt.Errorf("the file of asset %s is missing from storage", assetID))
		}
		s.infra = fmt.Errorf("stage asset %s: %w", assetID, err)
		return render.ResolvedAsset{}, s.infra
	}
	if perr := s.r.progress(ctx, pctStaging+min(len(s.cache)+1, pctStaged-pctStaging-1), store.RenderStageStaging); perr != nil {
		s.infra = perr // the render is gone: the job ends
		return render.ResolvedAsset{}, perr
	}
	ra := render.ResolvedAsset{Location: dest, HasAudio: in.HasAudio != nil && *in.HasAudio}
	if s.captions {
		words, err := s.transcript(id)
		if err != nil {
			s.infra = fmt.Errorf("transcript of asset %s: %w", assetID, err)
			return render.ResolvedAsset{}, s.infra
		}
		ra.Transcript = words
	}
	return ra, nil
}

// transcript loads the words of the asset's transcript for the captions track. An asset without one (a clip without speech, a
// transcription that has not run or failed) has no captions: the renderer draws none for its clips.
func (s *stager) transcript(id ids.AssetID) ([]render.Word, error) {
	t, err := s.r.p.cfg.Store.GetTranscript(s.ctx, s.r.ws, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !t.Speech) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var words []render.Word
	if err := json.Unmarshal(t.Words, &words); err != nil {
		return nil, fmt.Errorf("decode words: %w", err)
	}
	return words, nil
}

// refuse records a failure that is the timeline's or the footage's, and returns the error for the compiler.
func (s *stager) refuse(reason string, err error) (render.ResolvedAsset, error) {
	if s.fatal == nil {
		s.fatal = fileErr(reason, err)
	}
	return render.ResolvedAsset{}, err
}

// download writes the object to path, checking its size against the row.
func (r *run) download(ctx context.Context, key, path string, size int64) error {
	obj, err := r.p.cfg.Objects.Get(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size != size {
		return fmt.Errorf("storage reports %d bytes, the row has %d", obj.Size, size)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("got %d bytes, want %d", n, size)
	}
	return nil
}
