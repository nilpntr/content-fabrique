package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// creatorStyleSuite is part of repositorySuite (superuser, then the app role), so the creator style queries are proven to work with
// DML rights only.
func creatorStyleSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) store.Workspace {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_style_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	t.Run("none, save, replace, delete", func(t *testing.T) {
		w := newWorkspace("a")
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a workspace with no profile: %v", err)
		}
		row, err := st.SaveCreatorStyle(ctx, w.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1,"references":2}`), Measures: json.RawMessage(`[{"name":"a"},{"name":"b"}]`), ReferencesCount: 2})
		if err != nil || row.ReferencesCount != 2 || row.Version != 1 {
			t.Fatalf("save: %+v %v", row, err)
		}
		got, err := st.GetCreatorStyle(ctx, w.ID)
		if err != nil || !strings.Contains(string(got.Profile), `"references": 2`) && !strings.Contains(string(got.Profile), `"references":2`) {
			t.Fatalf("get: %s %v", got.Profile, err)
		}
		// A second save replaces the first (one profile per workspace).
		if _, err = st.SaveCreatorStyle(ctx, w.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1,"references":5}`), ReferencesCount: 5}); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetCreatorStyle(ctx, w.ID)
		if err != nil || got.ReferencesCount != 5 || string(got.Measures) != "[]" {
			t.Fatalf("replace: %+v %v", got, err)
		}
		if err := st.DeleteCreatorStyle(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
		// Deleting what is not there is fine.
		if err := st.DeleteCreatorStyle(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("one workspace never sees another's profile", func(t *testing.T) {
		a, b := newWorkspace("iso-a"), newWorkspace("iso-b")
		if _, err := st.SaveCreatorStyle(ctx, a.ID, store.NewCreatorStyle{Version: 1, Profile: json.RawMessage(`{"version":1}`), ReferencesCount: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, b.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace b sees workspace a's profile: %v", err)
		}
		if err := st.DeleteCreatorStyle(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorStyle(ctx, a.ID); err != nil {
			t.Fatalf("b's delete removed a's profile: %v", err)
		}
	})

	t.Run("what is not a profile is refused", func(t *testing.T) {
		w := newWorkspace("bad")
		for name, in := range map[string]store.NewCreatorStyle{
			"no version":      {Profile: json.RawMessage(`{}`)},
			"no profile":      {Version: 1},
			"not json":        {Version: 1, Profile: json.RawMessage(`{nope`)},
			"not an object":   {Version: 1, Profile: json.RawMessage(`[1]`)},
			"measures not []": {Version: 1, Profile: json.RawMessage(`{}`), Measures: json.RawMessage(`{"a":1}`)},
			"too big":         {Version: 1, Profile: json.RawMessage(`{"x":"` + strings.Repeat("a", 300_000) + `"}`)},
		} {
			if _, err := st.SaveCreatorStyle(ctx, w.ID, in); err == nil {
				t.Errorf("%s was stored", name)
			}
		}
		if _, err := st.GetCreatorStyle(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a refused save left a row: %v", err)
		}
	})
}
