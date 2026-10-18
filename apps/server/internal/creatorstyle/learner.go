package creatorstyle

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
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

// The background work of the style (the style_reference and style_profile jobs of package jobs).

// The codes a reference fails with (style_references.failure_reason), safe to show.
const (
	FailTooLong        = "too_long"
	FailNoVideo        = "no_video_stream"
	FailAnalysisFailed = "analysis_failed"
)

// maxPosterBytes caps a poster read for the summary.
const maxPosterBytes = 2 << 20

// Objects is the object storage the originals and posters are read from. *storage.Client is the real one.
type Objects interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// LearnerConfig builds a Learner.
type LearnerConfig struct {
	Store   *store.Store
	Objects Objects
	// Analyzer measures a reference (the sandbox runner, and the title read when it has a Reader). Its TempDir must be inside the
	// sandbox's allowed roots.
	Analyzer *styleprofile.Analyzer
	// Writer writes the summary; nil makes profiles without one.
	Writer *styleprofile.Writer
	Jobs   jobs.TxInserter
	// TempDir is where the originals are downloaded; it must be inside the sandbox's allowed roots, without symlinks in its path.
	TempDir string
	Logger  *slog.Logger
	Now     func() time.Time
}

// Learner measures references and makes profiles.
type Learner struct{ cfg LearnerConfig }

var _ jobs.StyleLearner = (*Learner)(nil)

// NewLearner returns a Learner.
func NewLearner(cfg LearnerConfig) (*Learner, error) {
	if cfg.Store == nil || cfg.Objects == nil || cfg.Analyzer == nil || cfg.Analyzer.Runner == nil || cfg.Jobs == nil || cfg.TempDir == "" {
		return nil, errors.New("creatorstyle: a store, object storage, an analyzer with a runner, a job enqueuer and a temp dir are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Learner{cfg: cfg}, nil
}

func (l *Learner) now() time.Time {
	if l.cfg.Now != nil {
		return l.cfg.Now()
	}
	return time.Now()
}

// ReferenceResult is what a style_reference job records.
type ReferenceResult struct {
	Outcome string `json:"outcome"`
	// Vision is the outcome of the title read ("read", or why there is none).
	Vision string `json:"vision,omitempty"`
}

// AnalyzeReference measures one reference (jobs.StyleLearner).
func (l *Learner) AnalyzeReference(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, lastAttempt bool) (any, bool, error) {
	st := l.cfg.Store
	a, err := st.GetAsset(ctx, ws, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("get asset: %w", err)
	case a.Purpose != store.PurposeStyleReference || a.Status == store.AssetFailed || a.Kind != store.AssetKindVideo:
		return nil, true, nil
	case a.Status != store.AssetReady:
		return nil, false, fmt.Errorf("%w (status %s)", jobs.ErrStyleNotReady, a.Status)
	}
	log := l.cfg.Logger.With("workspace_id", ws.String(), "asset_id", id.String())
	if a.DurationMs != nil && time.Duration(*a.DurationMs)*time.Millisecond > styleprofile.MaxReferenceDuration+500*time.Millisecond {
		return l.fail(ctx, ws, id, FailTooLong, nil)
	}
	if _, err := st.StartStyleReference(ctx, ws, id); errors.Is(err, store.ErrNotFound) {
		// Its row was never made (or the reference was learnt again after a removal raced it): make it now.
		if _, err := st.QueueStyleReference(ctx, ws, id, a.Filename); err != nil {
			return nil, false, fmt.Errorf("queue the reference: %w", err)
		}
		if _, err := st.StartStyleReference(ctx, ws, id); err != nil {
			return nil, false, fmt.Errorf("start the reference: %w", err)
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("start the reference: %w", err)
	}
	if a.R2Key != storage.OriginalKey(ws, id) {
		return nil, false, fmt.Errorf("the object key of asset %s is not its original's", id)
	}

	dir, err := os.MkdirTemp(l.cfg.TempDir, "style-")
	if err != nil {
		return nil, false, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil { // #nosec G301 -- the container user may be another uid
		return nil, false, err
	}
	local := filepath.Join(dir, "in", "reference")
	proxy, err := l.download(ctx, a, local)
	if err != nil {
		return l.giveUp(ctx, ws, id, lastAttempt, FailAnalysisFailed, "download", err)
	}
	an, err := l.cfg.Analyzer.AnalyzeFile(ctx, ws, local)
	if err != nil {
		reason := FailAnalysisFailed
		if strings.Contains(err.Error(), "has no video") {
			reason = FailNoVideo
		}
		return l.giveUp(ctx, ws, id, lastAttempt, reason, "measure", err)
	}
	m := an.Measures
	m.Name = a.Filename
	if proxy && a.Fps != nil && *a.Fps > 0 {
		m.FPS = *a.Fps // the proxy runs at 30 frames a second; the creator's frame rate is the original's
	}
	detail := map[string]any{"vision": "skipped"}
	vision := "skipped"
	if an.Vision != nil {
		switch {
		case an.Vision.Text != nil:
			vision = "read"
		case an.VisionErr != nil:
			vision = "error"
		default:
			vision = an.Vision.Reason
		}
		detail = map[string]any{"vision": vision, "model": an.Vision.Model, "input_tokens": an.Vision.Usage.InputTokens, "output_tokens": an.Vision.Usage.OutputTokens}
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return nil, false, err
	}
	dj, _ := json.Marshal(detail)
	err = st.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.FinishStyleReference(ctx, ws, id, mj, dj); err != nil {
			return err
		}
		return jobs.EnqueueStyleProfile(ctx, l.cfg.Jobs, tx, ws, l.now())
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, true, nil // removed while it was measured
	case err != nil:
		return nil, false, fmt.Errorf("store the measures: %w", err)
	}
	log.InfoContext(ctx, "style reference measured", "duration_s", m.DurationS, "cuts", len(m.CutsS), "vision", vision)
	return ReferenceResult{Outcome: "measured", Vision: vision}, false, nil
}

// giveUp is a failed attempt: retried by the job, and on the last attempt the reference is marked failed (with a context that outlives
// a timeout), so that the profile job does not wait for it.
func (l *Learner) giveUp(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, lastAttempt bool, reason, step string, err error) (any, bool, error) {
	if !lastAttempt {
		return nil, false, fmt.Errorf("%s: %w", step, err)
	}
	l.cfg.Logger.WarnContext(ctx, "style reference could not be measured", "workspace_id", ws.String(), "asset_id", id.String(), "step", step, "error", err)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return l.fail(ctx, ws, id, reason, map[string]string{"error": step})
}

// fail marks the reference failed and makes the profile again without it.
func (l *Learner) fail(ctx context.Context, ws ids.WorkspaceID, id ids.AssetID, reason string, detail map[string]string) (any, bool, error) {
	dj, _ := json.Marshal(detail)
	err := l.cfg.Store.InTx(ctx, func(tx *store.Store) error {
		if _, err := tx.FailStyleReference(ctx, ws, id, reason, dj); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			// No row yet (it fails before its analysis started): make one to carry the reason.
			a, gerr := tx.GetAsset(ctx, ws, id)
			if gerr != nil {
				return gerr
			}
			if _, err := tx.QueueStyleReference(ctx, ws, id, a.Filename); err != nil {
				return err
			}
			if _, err := tx.FailStyleReference(ctx, ws, id, reason, dj); err != nil {
				return err
			}
		}
		return jobs.EnqueueStyleProfile(ctx, l.cfg.Jobs, tx, ws, l.now())
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("mark the reference failed: %w", err)
	}
	return ReferenceResult{Outcome: "failed: " + reason}, false, nil
}

// download copies the reference's 720 px proxy to path (proxy true), or its original when it has none, refusing an object over the
// limit or, for the original, not the size of the asset. The proxy is what the clip analysis measures too: a few MB a minute instead of
// up to 1 GiB, and the same cuts, text, motion and sound.
func (l *Learner) download(ctx context.Context, a store.Asset, path string) (proxy bool, err error) {
	key, size := a.R2Key, a.SizeBytes
	if k, ok := l.proxyKey(ctx, a); ok {
		key, size, proxy = k, 0, true
	}
	obj, err := l.cfg.Objects.Get(ctx, key)
	if err != nil {
		return proxy, fmt.Errorf("download the reference: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > styleprofile.MaxReferenceBytes || (size > 0 && obj.Size != size) {
		return proxy, fmt.Errorf("the reference object is %d bytes, which is not the asset's %d", obj.Size, a.SizeBytes)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302,G304 -- a path in our job dir; the container user must read it
	if err != nil {
		return proxy, err
	}
	n, err := io.Copy(f, io.LimitReader(obj.Body, styleprofile.MaxReferenceBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return proxy, fmt.Errorf("download the reference: %w", err)
	}
	if n != obj.Size {
		return proxy, fmt.Errorf("download the reference: %d of %d bytes", n, obj.Size)
	}
	return proxy, nil
}

// proxyKey is the key of the asset's 720 px proxy, when it has one whose key is a derivative key of this very asset.
func (l *Learner) proxyKey(ctx context.Context, a store.Asset) (string, bool) {
	derivs, err := l.cfg.Store.ListAssetDerivatives(ctx, a.WorkspaceID, []ids.AssetID{a.ID})
	if err != nil {
		return "", false
	}
	for _, d := range derivs {
		if d.Kind != store.DerivativeProxy720 || d.AssetID != a.ID || d.WorkspaceID != a.WorkspaceID {
			continue
		}
		name, ok := strings.CutPrefix(d.R2Key, storage.AssetPrefix(a.WorkspaceID, a.ID))
		if ok && storage.ValidDerivativeName(name) {
			return d.R2Key, true
		}
	}
	return "", false
}

// ProfileResult is what a style_profile job records.
type ProfileResult struct {
	Outcome    string `json:"outcome"`
	References int    `json:"references,omitempty"`
	// Summary is "written", "kept" (the call failed and the previous summary stays), or why there is none.
	Summary string `json:"summary,omitempty"`
}

// LearnProfile makes the workspace's profile from its measured references and writes the summary (jobs.StyleLearner).
func (l *Learner) LearnProfile(ctx context.Context, ws ids.WorkspaceID, lastAttempt bool) (any, error) {
	return Learn(ctx, LearnDeps{Store: l.cfg.Store, Objects: l.cfg.Objects, Writer: l.cfg.Writer, Logger: l.cfg.Logger, Now: l.cfg.Now}, ws, lastAttempt)
}

// LearnDeps are what making a profile needs. cfctl uses it too (without object storage: no frames for the summary).
type LearnDeps struct {
	Store *store.Store
	// Objects reads the posters for the summary; nil sends none.
	Objects Objects
	// Writer writes the summary; nil makes a profile without one.
	Writer *styleprofile.Writer
	Logger *slog.Logger
	// Force writes a new summary even when the profile is up to date, and does not wait for references being measured.
	Force bool
	// Now replaces time.Now (tests).
	Now func() time.Time
}

// Learn aggregates the workspace's measured references (the newest styleprofile.MaxReferences whose asset is not deleted, and the files
// cfctl measured) into its profile and writes the summary, keeping the creator's settings. While a reference is still being measured it
// returns jobs.ErrStyleLearning and changes nothing (the job snoozes), so a pass of "learn again" makes one profile and one summary and
// the editor keeps the previous ones until then. With no reference left it removes the profile; when the ones left all failed it keeps
// it. A profile that is already made from exactly these references, none measured since, and has its summary is left alone (no model
// call). A failed summary call is retried by the job; on the last attempt the profile is stored with the summary it had before (or none).
func Learn(ctx context.Context, d LearnDeps, ws ids.WorkspaceID, lastAttempt bool) (ProfileResult, error) {
	st := d.Store
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	rows, err := st.ListStyleReferences(ctx, ws)
	if err != nil {
		return ProfileResult{}, fmt.Errorf("list references: %w", err)
	}
	assets, err := st.ListStyleReferenceAssets(ctx, ws)
	if err != nil {
		return ProfileResult{}, fmt.Errorf("list reference assets: %w", err)
	}
	live := map[ids.AssetID]bool{}
	for _, a := range assets {
		live[a.ID] = a.Status == store.AssetReady
	}
	var measured []store.StyleReference
	failed := 0 // references left that were not measured (failed, or left queued by a job that gave up)
	for _, r := range rows {
		id, isAsset := store.StyleReferenceAsset(r)
		if isAsset && !live[id] {
			continue
		}
		switch r.Status {
		case store.StyleRefReady:
			measured = append(measured, r)
		case store.StyleRefFailed:
			failed++
		default:
			// Queued or analysing: the profile is made once, when every reference is measured (one summary a pass), and until then
			// the editor keeps the profile and summary it has. A row older than store.StyleInFlightMaxAge belongs to a job that gave
			// up and holds nothing back.
			if !d.Force && r.UpdatedAt.After(now().Add(-store.StyleInFlightMaxAge)) {
				return ProfileResult{}, fmt.Errorf("%w (%s)", jobs.ErrStyleLearning, r.Status)
			}
			failed++ // not measured, and nothing measures it any more
		}
	}
	slices.SortFunc(measured, func(a, b store.StyleReference) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	if len(measured) > styleprofile.MaxReferences {
		measured = measured[len(measured)-styleprofile.MaxReferences:]
	}
	prev, perr := st.GetCreatorStyle(ctx, ws)
	if perr != nil && !errors.Is(perr, store.ErrNotFound) {
		return ProfileResult{}, fmt.Errorf("read the profile: %w", perr)
	}
	if len(measured) == 0 && failed > 0 && perr == nil {
		// Every reference that is left failed to be measured (this pass): the profile the editor has stays until one is measured.
		return ProfileResult{Outcome: "kept: no reference was measured"}, nil
	}
	if len(measured) == 0 {
		if perr == nil && len(prev.ReferenceIds) > 0 {
			if err := st.DeleteCreatorStyle(ctx, ws); err != nil {
				return ProfileResult{}, fmt.Errorf("remove the profile: %w", err)
			}
			return ProfileResult{Outcome: "cleared"}, nil
		}
		return ProfileResult{Outcome: "nothing to learn"}, nil
	}
	set := make([]uuid.UUID, len(measured))
	for i, r := range measured {
		set[i] = r.ID
	}
	var prevSummary *styleprofile.Summary
	if perr == nil {
		prevSummary, _ = styleprofile.ParseSummary(prev.Summary)
		if !d.Force && slices.Equal(sortedIDs(prev.ReferenceIds), sortedIDs(set)) && (prevSummary != nil || d.Writer == nil) &&
			!measuredSince(measured, prev.UpdatedAt) {
			return ProfileResult{Outcome: "up to date", References: len(set)}, nil
		}
	}
	var ms []styleprofile.Measures
	for _, r := range measured {
		var m styleprofile.Measures
		if err := json.Unmarshal(r.Measures, &m); err != nil {
			log.WarnContext(ctx, "creatorstyle: a reference's measures cannot be read; it is left out", "reference", r.ID.String(), "error", err)
			continue
		}
		ms = append(ms, m)
	}
	p := styleprofile.Aggregate(ms)
	if p == nil {
		return ProfileResult{Outcome: "nothing to learn"}, nil
	}
	out := ProfileResult{Outcome: "learnt", References: len(ms)}
	summary := prevSummary
	out.Summary = "none"
	if prevSummary != nil {
		out.Summary = "kept"
	}
	if d.Writer != nil {
		frames := posterFrames(ctx, d, ws, measured)
		res, err := d.Writer.Write(ctx, ws, uuid.New(), p, frames)
		switch {
		case err != nil && !lastAttempt:
			return ProfileResult{}, fmt.Errorf("write the summary: %w", err)
		case err != nil:
			log.WarnContext(ctx, "creatorstyle: the summary could not be written; the profile is stored without a new one", "error", err)
		case res.Summary != nil:
			summary, out.Summary = res.Summary, "written"
		default:
			log.InfoContext(ctx, "creatorstyle: no summary", "reason", res.Reason)
			out.Summary += " (" + res.Reason + ")"
		}
	}
	if _, err := styleprofile.SaveWith(ctx, st, ws, ms, styleprofile.SaveOptions{Summary: summary, ReferenceIDs: set}); err != nil {
		return ProfileResult{}, fmt.Errorf("store the profile: %w", err)
	}
	return out, nil
}

// measuredSince says a reference was measured after t (learnt again after the profile was made).
func measuredSince(refs []store.StyleReference, t time.Time) bool {
	for _, r := range refs {
		if r.AnalysedAt != nil && r.AnalysedAt.After(t) {
			return true
		}
	}
	return false
}

// posterFrames reads the posters of up to styleprofile.SummaryFrames of the references (JPEGs only), for the summary. A poster that
// cannot be read is left out: the summary does without.
func posterFrames(ctx context.Context, d LearnDeps, ws ids.WorkspaceID, refs []store.StyleReference) [][]byte {
	if d.Objects == nil {
		return nil
	}
	var assetIDs []ids.AssetID
	for _, r := range refs {
		if id, ok := store.StyleReferenceAsset(r); ok {
			assetIDs = append(assetIDs, id)
		}
	}
	if len(assetIDs) == 0 {
		return nil
	}
	derivs, err := d.Store.ListAssetDerivatives(ctx, ws, assetIDs)
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, dv := range derivs {
		if len(out) >= styleprofile.SummaryFrames {
			break
		}
		if dv.Kind != store.DerivativePoster || dv.WorkspaceID != ws || !strings.HasPrefix(dv.R2Key, storage.AssetPrefix(ws, dv.AssetID)) {
			continue
		}
		b, err := readSmall(ctx, d.Objects, dv.R2Key, maxPosterBytes)
		if err != nil || !bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func readSmall(ctx context.Context, o Objects, key string, limit int64) ([]byte, error) {
	obj, err := o.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.Size <= 0 || obj.Size > limit {
		return nil, fmt.Errorf("object of %d bytes", obj.Size)
	}
	return io.ReadAll(io.LimitReader(obj.Body, limit+1))
}
