package renderjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// PipelineObjects is the object storage the worker needs. *storage.Client is the real one.
type PipelineObjects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error
	// Delete removes one object; one that is not there is not an error.
	Delete(ctx context.Context, key string) error
}

// Config builds a Pipeline.
type Config struct {
	Store   *store.Store
	Objects PipelineObjects
	// Runner runs ffmpeg: a *media.SandboxRunner over the sandbox provider, with render.DefaultLimits (or a test's own). A
	// *media.SandboxRunner gets the job's labels.
	Runner media.Runner
	// TempDir holds one directory per running job. It must be inside the sandbox provider's allowed roots and its path must
	// have no symlinks in it (resolve it with filepath.EvalSymlinks first).
	TempDir string
	// CPUs is the CPU limit of the runner's sandbox, for the cpu_ms estimate (wall time of the sandbox runs x CPUs). Zero
	// means render.DefaultLimits().CPU.
	CPUs   float64
	Logger *slog.Logger
}

// Pipeline renders. It holds no per-job state, so one Pipeline serves all the workers. It implements jobs.Renderer.
type Pipeline struct {
	cfg Config
}

var _ jobs.Renderer = (*Pipeline)(nil)

// New returns a Pipeline.
func New(cfg Config) (*Pipeline, error) {
	switch {
	case cfg.Store == nil || cfg.Objects == nil || cfg.Runner == nil:
		return nil, errors.New("renderjobs: a store, object storage and a runner are required")
	case !filepath.IsAbs(cfg.TempDir) || filepath.Clean(cfg.TempDir) != cfg.TempDir:
		return nil, fmt.Errorf("renderjobs: the temp dir %q must be an absolute, clean path", cfg.TempDir)
	}
	if worst := WorstCase(); worst > jobs.RenderTimeout {
		return nil, fmt.Errorf("renderjobs: a render needs up to %v, more than the job timeout of %v", worst, jobs.RenderTimeout)
	}
	if cfg.CPUs <= 0 {
		cfg.CPUs = render.DefaultLimits().CPU
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.TempDir, 0o750); err != nil {
		return nil, fmt.Errorf("renderjobs: temp dir: %w", err)
	}
	p := &Pipeline{cfg: cfg}
	p.sweepStale(time.Now())
	return p, nil
}

const jobDirPrefix = "render-"

// sweepStale removes the job directories of runs that died without cleaning up (a crashed or killed worker). A directory
// counts as stale when nothing in it changed for longer than jobs.RenderTimeout, which is longer than any run lives, so a
// directory of a run on another worker that shares the temp dir is never touched.
func (p *Pipeline) sweepStale(now time.Time) {
	entries, err := os.ReadDir(p.cfg.TempDir)
	if err != nil {
		p.cfg.Logger.Warn("could not list the render temp dir", "dir", p.cfg.TempDir, "error", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), jobDirPrefix) {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) <= jobs.RenderTimeout {
			continue
		}
		if err := os.RemoveAll(filepath.Join(p.cfg.TempDir, e.Name())); err != nil {
			p.cfg.Logger.Warn("could not remove a stale render directory", "dir", e.Name(), "error", err)
			continue
		}
		p.cfg.Logger.Info("removed a stale render directory", "dir", e.Name())
	}
}

// Fail marks a render failed, for a job that has used its last attempt; cause is the error that ended it. Whatever the attempts
// left in object storage under the render's prefix is removed. A render that finished or failed already, or is not in this
// workspace, is not an error.
func (p *Pipeline) Fail(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID, cause error) error {
	r, err := p.cfg.Store.FailRender(ctx, ws, id, reasonFor(cause), -1, -1)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	p.deleteObjects(ctx, r.WorkspaceID, r.ProjectID, r.ID, storage.RenderMP4Name, storage.RenderContactSheetName, storage.RenderTimeMapName)
	return nil
}

// deleteObjects removes files of a render, best effort: a failure is logged, and the clean-up of a deleted project's renders
// removes the prefix in the end.
func (p *Pipeline) deleteObjects(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, id ids.RenderID, names ...string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, name := range names {
		key, err := storage.RenderKey(ws, project, id, name)
		if err != nil {
			continue
		}
		if err := p.cfg.Objects.Delete(cctx, key); err != nil {
			p.cfg.Logger.Warn("could not remove a file of a render that did not finish", "render_id", id.String(), "name", name, "error", err)
		}
	}
}

// Run renders. See the package comment for what its results and errors mean.
func (p *Pipeline) Run(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (jobs.RenderOutcome, error) {
	rr, err := p.cfg.Store.ClaimRender(ctx, ws, id)
	if errors.Is(err, store.ErrNotFound) {
		// Finished, failed, not in this workspace, or its project was deleted. A render that is still waiting on a deleted
		// project is failed, so that it does not hold a place in the workspace's in-flight count for ever.
		if _, ferr := p.cfg.Store.FailRender(ctx, ws, id, ReasonProjectDeleted, -1, -1); ferr != nil && !errors.Is(ferr, store.ErrNotFound) {
			p.cfg.Logger.WarnContext(ctx, "could not fail a render of a deleted project", "render_id", id.String(), "error", ferr)
		}
		return jobs.RenderOutcome{}, jobs.ErrRenderGone
	}
	if err != nil {
		return jobs.RenderOutcome{}, fmt.Errorf("claim render: %w", err)
	}
	log := p.cfg.Logger.With("workspace_id", ws.String(), "render_id", id.String(), "project_id", rr.ProjectID.String(), "profile", rr.Profile, "version", rr.TimelineVersion)

	dir, err := os.MkdirTemp(p.cfg.TempDir, jobDirPrefix)
	if err != nil {
		return jobs.RenderOutcome{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	runner := p.cfg.Runner
	if lr, ok := runner.(interface {
		WithLabels(map[string]string) media.Runner
	}); ok {
		runner = lr.WithLabels(map[string]string{"cf.job": "render", "cf.workspace": ws.String(), "cf.render": id.String()})
	}
	r := &run{p: p, ws: ws, render: rr, dir: dir, runner: runner, log: log, began: time.Now()}

	done, err := r.execute(ctx)
	wall := time.Since(r.began)
	var fe *fileError
	switch {
	case errors.As(err, &fe):
		log.WarnContext(ctx, "render failed: the timeline or its footage cannot be rendered", "reason", fe.reason, "error", fe.err)
		r.removeUploaded(ctx)
		return jobs.RenderOutcome{Status: store.RenderFailed, Reason: fe.reason, WallMs: wall.Milliseconds()}, r.markFailed(ctx, fe.reason, wall)
	case errors.Is(err, jobs.ErrRenderGone):
		// Its project was deleted (or it failed) while the job worked: nobody will use what it wrote. A row that is still
		// running is failed, so that it does not hold a place in the workspace's in-flight count for ever.
		r.removeUploaded(ctx)
		if merr := r.markFailed(ctx, ReasonProjectDeleted, wall); merr != nil {
			return jobs.RenderOutcome{}, merr
		}
		return jobs.RenderOutcome{}, err
	case err != nil:
		return jobs.RenderOutcome{}, err
	}
	log.InfoContext(ctx, "render ready", "wall", wall.Round(time.Millisecond), "sandbox", r.sandbox.Round(time.Millisecond), "duration_ms", done.durationMs, "size_bytes", done.sizeBytes)
	return jobs.RenderOutcome{Status: store.RenderReady, WallMs: wall.Milliseconds()}, nil
}

// markFailed records the failure even if ctx is cancelled meanwhile. A render that is not running any more is fine.
func (r *run) markFailed(ctx context.Context, reason string, wall time.Duration) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err := r.p.cfg.Store.FailRender(cctx, r.ws, r.render.ID, reason, wall.Milliseconds(), r.cpuMs())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("mark render failed: %w", err)
	}
	return nil
}

// run is one execution of the pipeline.
type run struct {
	p      *Pipeline
	ws     ids.WorkspaceID
	render store.Render
	dir    string
	runner media.Runner
	log    *slog.Logger

	// uploaded are the file names this run has written to object storage so far.
	uploaded []string
	// began is when the attempt started; sandbox is the wall time of its sandbox runs (the render and the contact sheet).
	began   time.Time
	sandbox time.Duration

	lastPct   int
	lastStage string
	lastWrite time.Time
}

// finished is what a successful run reports for its log line.
type finished struct {
	durationMs, sizeBytes int64
}

func (r *run) cpuMs() int64 { return int64(float64(r.sandbox.Milliseconds()) * r.p.cfg.CPUs) }

// Where the progress bar stands when a stage starts.
const (
	pctStaging   = 2
	pctStaged    = 10
	pctMeasured  = 20
	pctRendered  = 90
	pctSheet     = 95
	pctUploading = 97
)

// progress records how far the render is. A render that is not running any more ends the job (ErrRenderGone); a failure to
// write the number is cosmetic and logged. The writes are throttled: a new stage is always written, the same stage only when
// the number moved and the last write is not recent.
func (r *run) progress(ctx context.Context, pct int, stage string) error {
	pct = min(max(pct, 0), 100)
	// Within a stage the bar only moves forward: ffmpeg's progress blocks sometimes carry no output time (N/A while it drains
	// its encoder under load), which would read as a fraction of 0.
	if stage == r.lastStage && (pct <= r.lastPct || time.Since(r.lastWrite) < 750*time.Millisecond) {
		return nil
	}
	err := r.p.cfg.Store.SetRenderProgress(ctx, r.ws, r.render.ID, pct, stage)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return jobs.ErrRenderGone
	case err != nil:
		r.log.WarnContext(ctx, "could not record render progress", "error", err)
		return nil
	}
	r.lastPct, r.lastStage, r.lastWrite = pct, stage, time.Now()
	return nil
}

func (r *run) execute(ctx context.Context) (finished, error) {
	st := r.p.cfg.Store
	if err := r.progress(ctx, pctStaging, store.RenderStageStaging); err != nil {
		return finished{}, err
	}
	ver, err := st.GetTimelineVersion(ctx, r.ws, r.render.ProjectID, int(r.render.TimelineVersion))
	if errors.Is(err, store.ErrNotFound) {
		return finished{}, fileErr(ReasonInvalidTimeline, errors.New("the timeline version does not exist"))
	}
	if err != nil {
		return finished{}, fmt.Errorf("get timeline version: %w", err)
	}
	catalogRows, err := st.ListProjectCatalogAssets(ctx, r.ws, r.render.ProjectID)
	if err != nil {
		return finished{}, fmt.Errorf("project catalog: %w", err)
	}
	// The version was valid when it was made; the footage may not be (an asset deleted since). Compile expects a valid spec.
	if res := timeline.Validate(ver.Spec, projects.CatalogOf(catalogRows)); !res.OK() {
		return finished{}, fileErr(ReasonInvalidTimeline, errors.New(invalidMessage(res.Errors())))
	}
	spec, err := timeline.Parse(ver.Spec)
	if err != nil {
		return finished{}, fileErr(ReasonInvalidTimeline, err)
	}
	profile, err := render.ParseProfile(r.render.Profile)
	if err != nil {
		return finished{}, fileErr(ReasonInvalidTimeline, err)
	}
	inputs, err := st.ListRenderInputs(ctx, r.ws, r.render.ProjectID)
	if err != nil {
		return finished{}, fmt.Errorf("render inputs: %w", err)
	}

	for _, sub := range []string{"in", "work", "out"} {
		if err := os.Mkdir(filepath.Join(r.dir, sub), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
			return finished{}, err
		}
	}
	stager := newStager(ctx, r, inputs)
	plan, cerr := render.Compile(spec, profile, stager)
	switch {
	case stager.infra != nil:
		return finished{}, stager.infra
	case stager.fatal != nil:
		return finished{}, stager.fatal
	case cerr != nil && errors.Is(cerr, render.ErrInvalidSpec):
		return finished{}, fileErr(ReasonInvalidTimeline, cerr)
	case cerr != nil:
		return finished{}, fileErr(ReasonRenderFailed, cerr)
	}

	mp4, err := r.renderVideo(ctx, plan)
	if err != nil {
		return finished{}, err
	}
	fi, err := os.Lstat(mp4)
	if err != nil {
		return finished{}, fileErr(ReasonRenderFailed, fmt.Errorf("the render wrote no video: %w", err))
	}
	if !fi.Mode().IsRegular() || fi.Size() < 1 || fi.Size() > MaxOutputBytes {
		return finished{}, fileErr(ReasonRenderFailed, fmt.Errorf("the rendered video is %d bytes, which is not acceptable", fi.Size()))
	}

	if err := r.progress(ctx, pctRendered, store.RenderStageContactSheet); err != nil {
		return finished{}, err
	}
	t0 := time.Now()
	sheet, layout, err := r.makeSheet(ctx, plan, mp4)
	r.sandbox += time.Since(t0)
	if err != nil {
		return finished{}, err
	}
	timeMap, err := plan.TimeMapJSON()
	if err != nil {
		return finished{}, err
	}
	cues := plan.SoundCues
	if cues == nil {
		cues = []render.SoundCue{}
	}
	cueJSON, err := json.Marshal(cues)
	if err != nil {
		return finished{}, err
	}
	layoutJSON, err := json.Marshal(layout)
	if err != nil {
		return finished{}, err
	}

	if err := r.progress(ctx, pctUploading, store.RenderStageUploading); err != nil {
		return finished{}, err
	}
	f, err := os.Open(mp4) //nolint:gosec // the render's own output in our job directory
	if err != nil {
		return finished{}, err
	}
	defer f.Close() //nolint:errcheck // read-only
	keys := map[string]string{}
	for _, up := range []struct {
		name, contentType string
		body              io.ReadSeeker
		size              int64
	}{
		{storage.RenderMP4Name, "video/mp4", f, fi.Size()},
		{storage.RenderContactSheetName, "image/jpeg", bytes.NewReader(sheet), int64(len(sheet))},
		{storage.RenderTimeMapName, "application/json", bytes.NewReader(timeMap), int64(len(timeMap))},
	} {
		key, err := r.put(ctx, up.name, up.body, up.size, up.contentType)
		if err != nil {
			return finished{}, fmt.Errorf("upload %s: %w", up.name, err)
		}
		keys[up.name] = key
	}

	_, err = st.FinishRender(ctx, r.ws, r.render.ID, store.RenderResult{
		MP4Key: keys[storage.RenderMP4Name], ContactSheetKey: keys[storage.RenderContactSheetName], TimeMapKey: keys[storage.RenderTimeMapName],
		DurationMs: plan.DurationMs, Width: plan.OutputSize.Width, Height: plan.OutputSize.Height,
		SizeBytes: fi.Size(), ContactSheetBytes: int64(len(sheet)), TimeMapBytes: int64(len(timeMap)),
		ContactSheet: layoutJSON, SoundCues: cueJSON, CPUMs: r.cpuMs(), WallMs: time.Since(r.began).Milliseconds(),
	})
	if errors.Is(err, store.ErrNotFound) {
		return finished{}, jobs.ErrRenderGone
	}
	if err != nil {
		return finished{}, fmt.Errorf("finish render: %w", err)
	}
	return finished{durationMs: plan.DurationMs, sizeBytes: fi.Size()}, nil
}

// renderVideo runs the plan through the sandbox and returns the path of the MP4.
func (r *run) renderVideo(ctx context.Context, plan render.Plan) (string, error) {
	if err := r.progress(ctx, pctStaged, store.RenderStageRendering); err != nil {
		return "", err
	}
	twoPass := plan.Loudnorm != nil && plan.Loudnorm.TwoPass
	out := filepath.Join(r.dir, "out", storage.RenderMP4Name)

	execCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	onProgress := func(p render.Progress) {
		lo, hi, stage := pctStaged, pctRendered, store.RenderStageRendering
		if twoPass {
			if p.Stage == "measure" {
				hi, stage = pctMeasured, store.RenderStageMeasuring
			} else {
				lo = pctMeasured
			}
		}
		pct := lo + int(float64(hi-lo)*p.Fraction)
		if err := r.progress(execCtx, pct, stage); err != nil {
			cancel(err) // the render was deleted or failed: stop the encode
		}
	}
	t0 := time.Now()
	err := render.Execute(execCtx, plan, r.runner, render.ExecuteOptions{Output: out, WorkDir: filepath.Join(r.dir, "work"), Progress: onProgress})
	r.sandbox += time.Since(t0)
	if err != nil {
		if cause := context.Cause(execCtx); errors.Is(cause, jobs.ErrRenderGone) {
			return "", jobs.ErrRenderGone
		}
		var te *media.ToolError
		switch {
		case errors.As(err, &te):
			return "", toolFailure(te.Tool, te.ExitCode, []byte(te.Stderr))
		case errors.Is(err, render.ErrRemoteInput):
			return "", fileErr(ReasonRenderFailed, err)
		}
		return "", err // the sandbox, a timeout or the context: not the timeline's fault
	}
	return out, nil
}

// put writes a file of the render and records it, so a render that does not finish can remove it.
func (r *run) put(ctx context.Context, name string, body io.ReadSeeker, size int64, contentType string) (string, error) {
	key, err := storage.RenderKey(r.ws, r.render.ProjectID, r.render.ID, name)
	if err != nil {
		return "", err
	}
	if err := r.p.cfg.Objects.Put(ctx, key, body, size, contentType); err != nil {
		return "", err
	}
	r.uploaded = append(r.uploaded, name)
	return key, nil
}

// removeUploaded deletes the files this run wrote, for a run that ended without making the render ready. It uses a fresh
// context, as the run's may be the thing that ended.
func (r *run) removeUploaded(ctx context.Context) {
	if len(r.uploaded) == 0 {
		return
	}
	r.p.deleteObjects(ctx, r.ws, r.render.ProjectID, r.render.ID, r.uploaded...)
}

// errKilled marks a tool that was killed by a signal (the out-of-memory killer's 137, a 143 from a stopping container): the
// machine's doing, not the timeline's. It is retried, never reported as a bad timeline.
var errKilled = errors.New("the tool was killed")

// killedExit reports whether an exit code is the shell's 128 plus a signal number (SIGHUP to SIGTERM).
func killedExit(code int) bool { return code >= 129 && code <= 143 }

// toolFailure turns a tool that ran and failed into the error to report: a signal kill is retried, anything else is
// ffmpeg refusing what it was given, which a retry would repeat.
func toolFailure(tool string, code int, stderr []byte) error {
	te := &media.ToolError{Tool: tool, ExitCode: code, Stderr: media.StderrSnippet(stderr)}
	if killedExit(code) {
		return fmt.Errorf("%s: %w (exit code %d)", tool, errKilled, code)
	}
	return fileErr(ReasonRenderFailed, te)
}
