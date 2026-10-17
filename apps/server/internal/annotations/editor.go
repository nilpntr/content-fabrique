package annotations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The revision loop, the server side (architecture section 7.2): the open comments on a project's renders reach the next editor run,
// each resolved through the time map of the render it was made on to the timeline items that play in its range.

// Span is one timeline item that plays during a comment's range, from the time map. A main item carries its asset and the part of
// the asset the range shows; a text item carries its id and the output times only.
type Span struct {
	ItemID string
	// AssetID is the clip a main item shows; empty for a text item.
	AssetID string
	// OutStartMs and OutEndMs are the part of the comment's range this item plays, in output time.
	OutStartMs, OutEndMs int64
	// SourceInMs and SourceOutMs are the part of the asset shown over that range; nil for a text item.
	SourceInMs, SourceOutMs *int64
}

// Resolve finds the items of a time map that play in [startMs, endMs] of the output. A moment (end = start) finds the item that is
// on screen at that time (the last frame of the video belongs to the last item); a range finds every item it overlaps, with the part
// of each that falls inside. Items come in time order, a clip before the text over it.
func Resolve(entries []render.TimeMapEntry, startMs, endMs int64) []Span {
	var out []Span
	for _, e := range entries {
		var lo, hi int64
		if endMs == startMs {
			// A moment: on screen when start <= t < end; the final instant belongs to the item that ends there.
			if !(startMs >= e.OutStartMs && (startMs < e.OutEndMs || (startMs == e.OutEndMs && e.OutEndMs == lastEnd(entries)))) {
				continue
			}
			lo, hi = startMs, startMs
		} else {
			lo, hi = max(startMs, e.OutStartMs), min(endMs, e.OutEndMs)
			if lo >= hi {
				continue
			}
		}
		s := Span{ItemID: e.ItemID, AssetID: e.AssetID, OutStartMs: lo, OutEndMs: hi}
		if e.SourceInMs != nil && e.Speed != nil {
			in := *e.SourceInMs + int64(math.Round(float64(lo-e.OutStartMs)**e.Speed))
			outT := *e.SourceInMs + int64(math.Round(float64(hi-e.OutStartMs)**e.Speed))
			s.SourceInMs, s.SourceOutMs = &in, &outT
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OutStartMs != out[j].OutStartMs {
			return out[i].OutStartMs < out[j].OutStartMs
		}
		return out[i].AssetID != "" && out[j].AssetID == ""
	})
	return out
}

func lastEnd(entries []render.TimeMapEntry) int64 {
	var m int64
	for _, e := range entries {
		m = max(m, e.OutEndMs)
	}
	return m
}

// Comment is an open comment as the editor is handed it.
type Comment struct {
	Annotation Annotation
	Pin        *Pin
	// Items are the timeline items that play in the comment's range on the version it was made on. Empty with Mapped false when
	// that version has no ready render (its time map does not exist), and empty with Mapped true when nothing plays there.
	Items  []Span
	Mapped bool
}

// TimeMaps reads the time map of a ready render.
type TimeMaps interface {
	TimeMap(ctx context.Context, r store.Render) ([]render.TimeMapEntry, error)
}

// ObjectGetter is the part of object storage ObjectTimeMaps reads with. *storage.Client is the real one.
type ObjectGetter interface {
	Get(ctx context.Context, key string) (storage.Object, error)
}

// maxTimeMapBytes bounds a time map read back: a timeline has at most a few hundred items, a time map is a few KB.
const maxTimeMapBytes = 4 << 20

// ObjectTimeMaps reads time maps through the server's own storage credentials, from the key the render row holds after checking it
// is the render's own.
type ObjectTimeMaps struct{ Objects ObjectGetter }

// TimeMap implements TimeMaps.
func (o ObjectTimeMaps) TimeMap(ctx context.Context, r store.Render) ([]render.TimeMapEntry, error) {
	if r.Status != store.RenderReady || r.TimeMapKey == nil {
		return nil, errors.New("annotations: the render has no time map yet")
	}
	want, err := storage.RenderKey(r.WorkspaceID, r.ProjectID, r.ID, storage.RenderTimeMapName)
	if err != nil || *r.TimeMapKey != want {
		return nil, errors.New("annotations: the time map key is not the render's own")
	}
	obj, err := o.Objects.Get(ctx, want)
	if err != nil {
		return nil, fmt.Errorf("get time map: %w", err)
	}
	defer func() { _ = obj.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(obj.Body, maxTimeMapBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read time map: %w", err)
	}
	if len(b) > maxTimeMapBytes {
		return nil, errors.New("annotations: the stored time map is too large")
	}
	var entries []render.TimeMapEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("decode time map: %w", err)
	}
	return entries, nil
}

// EditorStore is what Source reads and writes. *store.Store is the real one.
type EditorStore interface {
	ListOpenAnnotationsForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, limit int) ([]store.Annotation, error)
	GetReadyRenderOfVersion(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) (store.Render, error)
	ApplyAnnotations(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, run uuid.UUID, version *int, reply string) ([]store.Annotation, error)
}

// MaxEditorComments is how many open comments one run is handed. The oldest come first; the rest wait for the next run.
const MaxEditorComments = 50

// Source is what the editor agent reads comments from and reports back to (editor.CommentSource).
type Source struct {
	Store EditorStore
	// TimeMaps reads time maps; nil means comments are listed with their times only (Mapped false).
	TimeMaps TimeMaps
	Logger   *slog.Logger
}

// OpenForProject returns the open comments on the project's renders and versions, oldest first, each resolved through the time map of
// its version's newest ready render. A version without a ready render, or a time map that cannot be read, leaves the comment unmapped
// (it is still handed to the editor, with its times): the failure is logged, not fatal.
func (s *Source) OpenForProject(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID) ([]Comment, error) {
	rows, err := s.Store.ListOpenAnnotationsForProject(ctx, ws, project, MaxEditorComments)
	if err != nil {
		return nil, fmt.Errorf("list open comments: %w", err)
	}
	maps := map[int32]*[]render.TimeMapEntry{} // by version; nil entry: no time map
	out := make([]Comment, 0, len(rows))
	for _, a := range rows {
		pin, perr := ParsePin(a.Pin)
		if perr != nil {
			return nil, perr
		}
		c := Comment{Annotation: a, Pin: pin}
		if a.TimelineVersion != nil {
			tm, seen := maps[*a.TimelineVersion]
			if !seen {
				tm = s.timeMap(ctx, ws, project, int(*a.TimelineVersion))
				maps[*a.TimelineVersion] = tm
			}
			if tm != nil {
				c.Mapped = true
				c.Items = Resolve(*tm, a.StartMs, a.EndMs)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Source) timeMap(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, version int) *[]render.TimeMapEntry {
	if s.TimeMaps == nil {
		return nil
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	r, err := s.Store.GetReadyRenderOfVersion(ctx, ws, project, version)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.WarnContext(ctx, "annotations: looking for a render of a commented version failed", "version", version, "error", err)
		}
		return nil
	}
	entries, err := s.TimeMaps.TimeMap(ctx, r)
	if err != nil {
		log.WarnContext(ctx, "annotations: reading a time map failed; its comments stay unmapped", "version", version, "error", err)
		return nil
	}
	return &entries
}

// MarkAddressed is the end of a successful editor run: the comments and instructions that were open when the run started are applied
// by it, in the version it made (nil when it made none), and each gets the editor's short answer. It returns how many it marked.
func (s *Source) MarkAddressed(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, run uuid.UUID, version *int) (int, error) {
	reply := "Looked at this; no change was needed."
	if version != nil {
		reply = fmt.Sprintf("Addressed in v%d.", *version)
	}
	rows, err := s.Store.ApplyAnnotations(ctx, ws, project, run, version, reply)
	if err != nil {
		return 0, fmt.Errorf("mark comments applied: %w", err)
	}
	return len(rows), nil
}
