package annotations

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/render"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func main(item, asset string, start, end, srcIn int64, speed float64) render.TimeMapEntry {
	return render.TimeMapEntry{OutStartMs: start, OutEndMs: end, ItemID: item, AssetID: asset, SourceInMs: &srcIn, Speed: &speed}
}

func text(item string, start, end int64) render.TimeMapEntry {
	return render.TimeMapEntry{OutStartMs: start, OutEndMs: end, ItemID: item}
}

func summary(spans []Span) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.ItemID)
	}
	return out
}

func TestResolve(t *testing.T) {
	// c1 plays 0-4 s from 10 s of the clip at normal speed; c2 plays 4-6 s (a 4 s source at double speed) from 20 s; a text over 3.5-4.5 s
	// (over the cut); the last item ends at 6 s.
	tm := []render.TimeMapEntry{
		main("c1", "ast_a", 0, 4000, 10000, 1),
		main("c2", "ast_b", 4000, 6000, 20000, 2),
		text("t1", 3500, 4500),
	}
	cases := []struct {
		name       string
		start, end int64
		want       []string
	}{
		{"a moment inside the first clip", 1000, 1000, []string{"c1"}},
		{"a moment at the start", 0, 0, []string{"c1"}},
		{"a moment on the cut belongs to the next clip, with the text", 4000, 4000, []string{"c2", "t1"}},
		{"a moment under the text", 3600, 3600, []string{"c1", "t1"}},
		{"the end of the last item", 6000, 6000, []string{"c2"}},
		{"past the end", 6001, 6001, nil},
		{"a range over one clip", 1000, 2000, []string{"c1"}},
		{"a range ending on a cut does not reach into the next clip", 1000, 4000, []string{"c1", "t1"}},
		{"a range across the cut", 3000, 5000, []string{"c1", "t1", "c2"}},
		{"a range over everything", 0, 6000, []string{"c1", "t1", "c2"}},
		{"a range past the end", 5000, 9000, []string{"c2"}},
	}
	for _, c := range cases {
		got := summary(Resolve(tm, c.start, c.end))
		if !reflect.DeepEqual(got, c.want) && (len(got) != 0 || len(c.want) != 0) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	// Source time: the part of the clip a range shows, with the speed.
	spans := Resolve(tm, 3000, 5000)
	var c1, c2 Span
	for _, s := range spans {
		switch s.ItemID {
		case "c1":
			c1 = s
		case "c2":
			c2 = s
		}
	}
	if c1.AssetID != "ast_a" || c1.OutStartMs != 3000 || c1.OutEndMs != 4000 || *c1.SourceInMs != 13000 || *c1.SourceOutMs != 14000 {
		t.Errorf("c1: %+v", c1)
	}
	// At double speed, 1 s of output is 2 s of the source.
	if c2.AssetID != "ast_b" || c2.OutStartMs != 4000 || c2.OutEndMs != 5000 || *c2.SourceInMs != 20000 || *c2.SourceOutMs != 22000 {
		t.Errorf("c2: %+v", c2)
	}
	// A text has no source.
	for _, s := range spans {
		if s.ItemID == "t1" && (s.AssetID != "" || s.SourceInMs != nil) {
			t.Errorf("t1: %+v", s)
		}
	}
	// A moment: the source time of that instant.
	if m := Resolve(tm, 4400, 4400); len(m) != 2 || m[0].ItemID != "c2" || *m[0].SourceInMs != 20800 || *m[0].SourceOutMs != 20800 || m[1].ItemID != "t1" {
		t.Errorf("a moment in c2: %+v", m)
	}
	// Overlapping clips of a crossfade are both on screen in the overlap.
	cross := []render.TimeMapEntry{main("a", "ast_a", 0, 4500, 0, 1), main("b", "ast_b", 4000, 8000, 0, 1)}
	if got := summary(Resolve(cross, 4200, 4200)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("crossfade: %v", got)
	}
	if got := Resolve(nil, 0, 100); len(got) != 0 {
		t.Errorf("empty time map: %v", got)
	}
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type oneObject struct {
	key  string
	data string
	gets []string
}

func (o *oneObject) Get(_ context.Context, key string) (storage.Object, error) {
	o.gets = append(o.gets, key)
	if key != o.key {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Body: io.NopCloser(bytes.NewReader([]byte(o.data))), Size: int64(len(o.data))}, nil
}

func TestObjectTimeMapsReadOnlyTheRendersOwnKey(t *testing.T) {
	ws := ids.WorkspaceID{UUID: mustV7(t)}
	prj := ids.ProjectID{UUID: mustV7(t)}
	ren := ids.RenderID{UUID: mustV7(t)}
	key, err := storage.RenderKey(ws, prj, ren, storage.RenderTimeMapName)
	if err != nil {
		t.Fatal(err)
	}
	objs := &oneObject{key: key, data: `[{"out_start_ms":0,"out_end_ms":1000,"item_id":"c1"}]`}
	tm := ObjectTimeMaps{Objects: objs}
	ready := store.Render{ID: ren, WorkspaceID: ws, ProjectID: prj, Status: store.RenderReady, TimeMapKey: &key}

	got, err := tm.TimeMap(context.Background(), ready)
	if err != nil || len(got) != 1 || got[0].ItemID != "c1" {
		t.Fatalf("TimeMap = %+v, %v", got, err)
	}

	// A row whose key is not its own (another workspace's, say) is refused before anything is read.
	otherWS := ids.WorkspaceID{UUID: mustV7(t)}
	foreign, _ := storage.RenderKey(otherWS, prj, ren, storage.RenderTimeMapName)
	bad := ready
	bad.TimeMapKey = &foreign
	objs.gets = nil
	if _, err := tm.TimeMap(context.Background(), bad); err == nil || len(objs.gets) != 0 {
		t.Errorf("a foreign key: err = %v, reads = %v", err, objs.gets)
	}
	notReady := ready
	notReady.Status = store.RenderQueued
	if _, err := tm.TimeMap(context.Background(), notReady); err == nil {
		t.Error("a render that is not ready has a time map")
	}
	objs.data = "nope"
	if _, err := tm.TimeMap(context.Background(), ready); err == nil {
		t.Error("a time map that is not JSON")
	}
	objs.data = strings.Repeat(" ", maxTimeMapBytes+1)
	if _, err := tm.TimeMap(context.Background(), ready); err == nil {
		t.Error("a time map over the limit")
	}
}
