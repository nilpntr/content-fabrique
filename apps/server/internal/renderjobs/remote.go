package renderjobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The render on the remote sandbox provider (MEDIA_SANDBOX_PROVIDER=remote, docs/design/render-runtime.md). Nothing is staged on
// the worker's disk: the stager checks each object it would have downloaded, render.ExecuteRemote sends the runner presigned GET
// URLs of the footage and presigned PUT URLs of the video and the audio stem, and the contact sheet's frames are extracted in the
// same job and come back as scratch files under the render's prefix. Before the render row is committed the worker checks the
// video object (Head), as it checks a local output. A finished render removes its scratch files; a failed one loses them with
// its whole prefix.

// Remote is the remote half of a Config. Config.Objects must then also be RemoteObjects (*storage.Client is).
type Remote struct {
	// Provider is the remote sandbox provider (sandbox.Remote at MEDIA_RUNNER_URL).
	Provider sandbox.Provider
	// Limits bound each ffmpeg run; zero means render.DefaultLimits.
	Limits sandbox.Limits
	// FontsDir is the runner's fonts directory; empty means the media-tools image's (render.ImageFontsDir).
	FontsDir string
}

// RemoteObjects is the object storage a remote render needs on top of PipelineObjects: presigned URLs for the runner, and a look at
// what it uploaded.
type RemoteObjects interface {
	Head(ctx context.Context, key string) (storage.ObjectInfo, error)
	PresignGet(ctx context.Context, key string, expires time.Duration) (string, error)
	PresignPut(ctx context.Context, key, contentType string, expires time.Duration) (string, error)
	DeleteRenderScratch(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, render ids.RenderID) (int, error)
}

// remoteURLExpiry is how long the URLs of a remote render are valid: the attempt's own limit, so none outlives its job.
const remoteURLExpiry = jobs.RenderTimeout

// goneEvery is how often a remote render looks whether its render still runs: the runner reports no progress during a pass, so this
// is what stops the encode of a render that was deleted meanwhile (Remote.Run then deletes the job at the runner).
var goneEvery = 5 * time.Second

// tileFetches is how many contact-sheet frames are read back from object storage at once.
const tileFetches = 8

// checkRemote is the stager's download for a remote render: the object must be there with the row's size; the runner fetches it.
func (r *run) checkRemote(ctx context.Context, key, _ string, size int64) error {
	info, err := r.p.remote.Head(ctx, key)
	if err != nil {
		return err
	}
	if info.Size != size {
		return fmt.Errorf("storage reports %d bytes, the row has %d", info.Size, size)
	}
	return nil
}

// jobID names the attempt's jobs at the runner: the render id, the River job and its attempt (a retry is a new id, the same
// attempt asked again is the same one). Outside a job (a test, a tool) it is unique.
func (r *run) jobID(ctx context.Context) string {
	if ji, ok := jobs.JobInfoFrom(ctx); ok {
		return fmt.Sprintf("%s-j%d-a%d", r.render.ID, ji.ID, ji.Attempt)
	}
	return r.render.ID.String() + "-" + uuid.NewString()[:8]
}

// renderRemote renders through the remote provider and makes the contact sheet from the frames the same job extracted.
func (r *run) renderRemote(ctx context.Context, plan render.Plan, keys map[string]string) (rendered, error) {
	rm, objs := r.p.cfg.Remote, r.p.remote
	if err := r.progress(ctx, pctStaged, store.RenderStageRendering); err != nil {
		return rendered{}, err
	}
	layout, frames, err := sheetPlan(plan)
	if err != nil {
		return rendered{}, err
	}
	files := &remoteFiles{r: r, keys: keys}
	videoKey, err := storage.RenderKey(r.ws, r.render.ProjectID, r.render.ID, storage.RenderMP4Name)
	if err != nil {
		return rendered{}, err
	}
	// Recorded before the runner can write anything, so an attempt that fails removes what it may have left.
	r.uploaded = append(r.uploaded, storage.RenderMP4Name)
	videoURL, err := objs.PresignPut(ctx, videoKey, "video/mp4", remoteURLExpiry)
	if err != nil {
		return rendered{}, err
	}
	limits := rm.Limits
	if limits == (sandbox.Limits{}) {
		limits = render.DefaultLimits()
	}
	opts := render.RemoteOptions{
		Provider: rm.Provider, JobID: r.jobID(ctx), Limits: limits, FontsDir: rm.FontsDir, Files: files,
		Labels:        map[string]string{"cf.job": "render", "cf.workspace": r.ws.String(), "cf.render": r.render.ID.String()},
		MaxInputBytes: MaxInputBytes + 64<<20, // the footage (the stager's cap) and the subtitles script
		Video:         sandbox.Output{URL: videoURL, ContentType: "video/mp4", MaxBytes: MaxOutputBytes},
		After: []render.RemoteStep{{Name: "contact sheet", Step: sandbox.Step{
			Args: extractArgs(render.RemoteVideoPath, frames, layout.TileWidth, layout.TileHeight), Timeout: SheetTimeout}}},
	}
	if plan.StemLabel != "" {
		key, err := storage.RenderKey(r.ws, r.render.ProjectID, r.render.ID, storage.RenderAudioStemName)
		if err != nil {
			return rendered{}, err
		}
		r.uploaded = append(r.uploaded, storage.RenderAudioStemName)
		u, err := objs.PresignPut(ctx, key, "audio/mp4", remoteURLExpiry)
		if err != nil {
			return rendered{}, err
		}
		opts.Stem = &sandbox.Output{URL: u, ContentType: "audio/mp4", MaxBytes: maxStemBytes}
	}
	for i := range frames {
		name := tileName(i)
		u, err := files.ScratchURL(ctx, name, "image/png")
		if err != nil {
			return rendered{}, err
		}
		opts.AfterOutputs = append(opts.AfterOutputs, sandbox.Output{Path: "/work/" + name, URL: u, ContentType: "image/png", MaxBytes: maxTilePNG})
	}

	execCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	opts.Progress = r.onProgress(execCtx, plan, cancel)
	stopWatch := r.watchGone(execCtx, cancel)
	execCtx, span := stageSpan(execCtx, "render.remote")
	ran, err := render.ExecuteRemote(execCtx, plan, opts)
	stopWatch()
	// Metered by what the runner reports for its jobs (their downloads, steps and uploads), not the wait for a busy runner.
	r.sandbox += ran
	span.SetAttributes(attribute.Bool("cf.two_pass", plan.Loudnorm != nil && plan.Loudnorm.TwoPass), attribute.Int64("cf.planned_duration_ms", plan.DurationMs))
	if err != nil {
		span.RecordError(err)
	}
	span.End()
	switch {
	case err != nil && errors.Is(context.Cause(execCtx), jobs.ErrRenderGone):
		return rendered{}, jobs.ErrRenderGone
	case err != nil && errors.Is(err, sandbox.ErrFiles):
		// An output the runner refused (missing, not a regular file, over its cap): the render is not what its plan said.
		return rendered{}, fileErr(ReasonRenderFailed, err)
	case err != nil:
		return rendered{}, renderError(execCtx, err)
	}

	// The runner's upload is checked before anything trusts it.
	info, err := objs.Head(ctx, videoKey)
	if err != nil {
		return rendered{}, fmt.Errorf("check the rendered video: %w", err)
	}
	if info.Size < 1 || info.Size > MaxOutputBytes {
		return rendered{}, fileErr(ReasonRenderFailed, fmt.Errorf("the rendered video is %d bytes, which is not acceptable", info.Size))
	}

	if err := r.progress(ctx, pctRendered, store.RenderStageContactSheet); err != nil {
		return rendered{}, err
	}
	tiles := files.getAll(ctx, len(frames))
	sheet, layout, err := buildSheet(plan, layout, frames, func(i int) ([]byte, error) { return tiles[i].data, tiles[i].err })
	if err != nil {
		return rendered{}, err
	}
	return rendered{mp4Key: videoKey, size: info.Size, sheet: sheet, layout: layout}, nil
}

// watchGone cancels ctx with jobs.ErrRenderGone once the render is no longer running (deleted, or failed meanwhile), checking every
// goneEvery until the returned stop is called. A failed read is not a verdict: the next tick tries again.
func (r *run) watchGone(ctx context.Context, cancel context.CancelCauseFunc) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(goneEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			cur, err := r.p.cfg.Store.GetRender(ctx, r.ws, r.render.ID)
			if errors.Is(err, store.ErrNotFound) || (err == nil && cur.Status != store.RenderRendering) {
				cancel(jobs.ErrRenderGone)
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// removeScratch removes the scratch files of a finished remote render. Best effort: what is left goes with the render's prefix.
func (r *run) removeScratch(ctx context.Context) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := r.p.remote.DeleteRenderScratch(cctx, r.ws, r.render.ProjectID, r.render.ID); err != nil {
		r.log.WarnContext(ctx, "could not remove the scratch files of a remote render", "render_id", r.render.ID.String(), "error", err)
	}
}

// remoteFiles is the render.RemoteFiles of one run: the stager's objects, and scratch files under the render's prefix.
type remoteFiles struct {
	r    *run
	keys map[string]string // staged location -> object key
}

func (f *remoteFiles) scratchKey(name string) (string, error) {
	key, err := storage.RenderScratchKey(f.r.ws, f.r.render.ProjectID, f.r.render.ID, name)
	if err != nil {
		return "", err
	}
	f.r.uploaded = append(f.r.uploaded, "scratch/"+name)
	return key, nil
}

// InputURL implements render.RemoteFiles.
func (f *remoteFiles) InputURL(ctx context.Context, location string) (string, error) {
	key, ok := f.keys[location]
	if !ok {
		return "", fmt.Errorf("%w: %q was not staged", render.ErrRemoteInput, location)
	}
	return f.r.p.remote.PresignGet(ctx, key, remoteURLExpiry)
}

// PutScratch implements render.RemoteFiles.
func (f *remoteFiles) PutScratch(ctx context.Context, name string, data []byte, contentType string) (string, error) {
	key, err := f.scratchKey(name)
	if err != nil {
		return "", err
	}
	if err := f.r.p.cfg.Objects.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		return "", err
	}
	return f.r.p.remote.PresignGet(ctx, key, remoteURLExpiry)
}

// ScratchURL implements render.RemoteFiles.
func (f *remoteFiles) ScratchURL(ctx context.Context, name, contentType string) (string, error) {
	key, err := f.scratchKey(name)
	if err != nil {
		return "", err
	}
	return f.r.p.remote.PresignPut(ctx, key, contentType, remoteURLExpiry)
}

// GetScratch implements render.RemoteFiles. A file that is not there is os.ErrNotExist; one over limit is errBadTile's kind of
// failure only for the tiles (the runner caps every output it uploads, so it does not happen).
func (f *remoteFiles) GetScratch(ctx context.Context, name string, limit int64) ([]byte, error) {
	key, err := storage.RenderScratchKey(f.r.ws, f.r.render.ProjectID, f.r.render.ID, name)
	if err != nil {
		return nil, err
	}
	obj, err := f.r.p.cfg.Objects.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%s: %w", name, os.ErrNotExist)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size > limit {
		return nil, fmt.Errorf("%s is %d bytes, over its cap of %d", name, obj.Size, limit)
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is over its cap of %d bytes", name, limit)
	}
	return data, nil
}

type fetched struct {
	data []byte
	err  error
}

// getAll reads the n contact-sheet frames, tileFetches at a time.
func (f *remoteFiles) getAll(ctx context.Context, n int) []fetched {
	out := make([]fetched, n)
	sem := make(chan struct{}, tileFetches)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i].data, out[i].err = f.GetScratch(ctx, tileName(i), maxTilePNG)
		}()
	}
	wg.Wait()
	return out
}
