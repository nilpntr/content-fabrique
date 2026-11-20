package clipfeedback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// fakeStore is the store of one workspace with one project: a version, the project's clips and the feedback rows.
type fakeStore struct {
	ws      ids.WorkspaceID
	project ids.ProjectID
	spec    []byte
	cat     []store.ProjectCatalogRow
	rows    []store.ClipFeedback
	saved   []store.NewClipFeedback
}

func (f *fakeStore) GetProject(_ context.Context, ws ids.WorkspaceID, id ids.ProjectID) (store.Project, error) {
	if ws != f.ws || id != f.project {
		return store.Project{}, store.ErrNotFound
	}
	return store.Project{ID: id, WorkspaceID: ws}, nil
}

func (f *fakeStore) GetTimelineVersion(_ context.Context, ws ids.WorkspaceID, id ids.ProjectID, v int) (store.TimelineVersion, error) {
	if ws != f.ws || id != f.project || v != 1 {
		return store.TimelineVersion{}, store.ErrNotFound
	}
	return store.TimelineVersion{Version: 1, Spec: f.spec}, nil
}

func (f *fakeStore) ListProjectCatalogAssets(context.Context, ids.WorkspaceID, ids.ProjectID) ([]store.ProjectCatalogRow, error) {
	return f.cat, nil
}

func (f *fakeStore) SaveClipFeedback(_ context.Context, ws ids.WorkspaceID, in store.NewClipFeedback) (store.ClipFeedback, error) {
	f.saved = append(f.saved, in)
	row := store.ClipFeedback{
		ID: ids.ClipFeedbackID{UUID: uuid.Must(uuid.NewV7())}, WorkspaceID: ws, AssetID: in.Asset, Kind: in.Kind, StartMs: in.StartMs, EndMs: in.EndMs,
		ProjectID: in.Project, TimelineVersion: int32(in.Version), ShotNumber: int32(in.ShotNumber), ItemID: in.ItemID, Note: in.Note, //nolint:gosec // small
		CreatedByKind: in.CreatedByKind, CreatedByID: in.CreatedByID, CreatedAt: time.Now(),
	}
	f.rows = append(f.rows, row)
	return row, nil
}

func (f *fakeStore) ListClipFeedback(_ context.Context, ws ids.WorkspaceID, assets []ids.AssetID, limit int) ([]store.ClipFeedback, error) {
	var out []store.ClipFeedback
	for _, r := range f.rows {
		if r.WorkspaceID != ws {
			continue
		}
		for _, a := range assets {
			if a == r.AssetID {
				out = append(out, r)
			}
		}
		if len(assets) == 0 {
			out = append(out, r)
		}
	}
	return out[:min(len(out), limit)], nil
}

func (f *fakeStore) ListAllClipFeedback(ctx context.Context, ws ids.WorkspaceID, assets []ids.AssetID) ([]store.ClipFeedback, error) {
	return f.ListClipFeedback(ctx, ws, assets, 1<<30)
}

func (f *fakeStore) DeleteClipFeedback(_ context.Context, ws ids.WorkspaceID, id ids.ClipFeedbackID) error {
	for i, r := range f.rows {
		if r.ID == id && r.WorkspaceID == ws {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func newID[T any](t *testing.T, parse func(string) (T, error), prefix string) T {
	t.Helper()
	id, err := parse(ids.Encode(prefix, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newFake(t *testing.T) (*fakeStore, ids.AssetID, ids.AssetID) {
	t.Helper()
	a := newID(t, ids.ParseAssetID, ids.PrefixAsset)
	b := newID(t, ids.ParseAssetID, ids.PrefixAsset)
	spec := timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{&timeline.VideoTrack{ID: timeline.MainTrackID, Items: []timeline.VideoItem{
			{ID: "c1", AssetID: a.String(), AtMs: 0, Source: timeline.Source{InMs: 2_000, OutMs: 5_000}},
			{ID: "c2", AssetID: b.String(), AtMs: 3_000, Source: timeline.Source{InMs: 12_000, OutMs: 15_500}},
		}}},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	clipMs := int64(20_000)
	return &fakeStore{
		ws: newID(t, ids.ParseWorkspaceID, ids.PrefixWorkspace), project: newID(t, ids.ParseProjectID, ids.PrefixProject), spec: raw,
		cat: []store.ProjectCatalogRow{{AssetID: a, Kind: "video", Title: "Squat", DurationMs: &clipMs}, {AssetID: b, Kind: "video", Title: "Row", DurationMs: &clipMs}},
	}, a, b
}

func TestFeedbackOnAShotIsStoredByClipAndRange(t *testing.T) {
	f, _, b := newFake(t)
	svc, err := NewService(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	who := Giver{Workspace: f.ws, User: newID(t, ids.ParseUserID, ids.PrefixUser)}

	row, e := svc.Give(ctx, who, Give{Project: f.project, Version: 1, ShotNumber: 2, Kind: NeverUse, Note: "  too\nblurry  "})
	if e != nil {
		t.Fatal(e)
	}
	if row.AssetID != b || row.StartMs != 12_000 || row.EndMs != 15_500 || row.ShotNumber != 2 || row.ItemID != "c2" || row.Note != "too blurry" {
		t.Errorf("stored %+v", row)
	}
	if f.saved[0].CreatedByKind != store.AuthorUser || f.saved[0].CreatedByID != who.User.String() {
		t.Errorf("created by %q %q", f.saved[0].CreatedByKind, f.saved[0].CreatedByID)
	}
	later, e := svc.Give(ctx, who, Give{Project: f.project, Version: 1, ShotNumber: 1, Kind: StartLater, OffsetMs: 2_000})
	if e != nil || later.StartMs != 2_000 || later.EndMs != 4_000 {
		t.Fatalf("start later: %+v, %v", later, e)
	}

	// The shot list shows each shot's feedback.
	list, e := svc.ShotList(ctx, f.ws, f.project, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Shots) != 2 || list.Shots[0].Title != "Squat" || len(list.Shots[0].Feedback) != 1 || len(list.Shots[1].Feedback) != 1 || list.Shots[1].Feedback[0].ID != row.ID {
		t.Errorf("shot list %+v", list.Shots)
	}

	// What is refused.
	for name, c := range map[string]struct {
		in     Give
		status int
	}{
		"no such shot":            {Give{Project: f.project, Version: 1, ShotNumber: 3, Kind: NeverUse}, 404},
		"no such version":         {Give{Project: f.project, Version: 2, ShotNumber: 1, Kind: NeverUse}, 404},
		"another project":         {Give{Project: newID(t, ids.ParseProjectID, ids.PrefixProject), Version: 1, ShotNumber: 1, Kind: NeverUse}, 404},
		"an unknown kind":         {Give{Project: f.project, Version: 1, ShotNumber: 1, Kind: "maybe"}, 400},
		"an offset too small":     {Give{Project: f.project, Version: 1, ShotNumber: 1, Kind: StartLater, OffsetMs: 10}, 400},
		"a note that is too long": {Give{Project: f.project, Version: 1, ShotNumber: 1, Kind: NeverUse, Note: strings.Repeat("a", 501)}, 400},
	} {
		if _, e := svc.Give(ctx, who, c.in); e == nil || e.Status != c.status {
			t.Errorf("%s: %v, want %d", name, e, c.status)
		}
	}
	other := Giver{Workspace: newID(t, ids.ParseWorkspaceID, ids.PrefixWorkspace), User: who.User}
	if _, e := svc.Give(ctx, other, Give{Project: f.project, Version: 1, ShotNumber: 1, Kind: NeverUse}); e == nil || e.Status != 404 {
		t.Errorf("another workspace: %v", e)
	}

	// Taking it back.
	if e := svc.Delete(ctx, f.ws, row.ID); e != nil {
		t.Fatal(e)
	}
	if e := svc.Delete(ctx, f.ws, row.ID); e == nil || e.Status != 404 {
		t.Errorf("deleting twice: %v", e)
	}
	rules, err := LoadRules(ctx, f, f.ws, []ids.AssetID{b})
	if err != nil || len(rules) != 0 {
		t.Errorf("rules after the delete: %+v, %v", rules, err)
	}
	if rows, e := svc.List(ctx, f.ws, nil, 0); e != nil || len(rows) != 1 {
		t.Errorf("list: %d, %v", len(rows), e)
	}
	if _, e := svc.List(ctx, f.ws, nil, MaxPageSize+1); e == nil || e.Status != 400 {
		t.Errorf("a limit too large: %v", e)
	}
	if _, err := NewService(nil, nil); err == nil || errors.Is(err, ErrInvalid) {
		t.Error("a service without a store")
	}
}
