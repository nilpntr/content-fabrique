package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// TestRepository runs the repository suite as the superuser. The role-split test runs the same suite as the app
// role, so every query is proven to work with DML rights only.
func TestRepository(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	migrate(t, srv.SuperDSN(database))
	repositorySuite(t, openStore(t, srv.SuperDSN(database)))
}

// repositorySuite exercises every repository function against a migrated database. It makes its own workspaces,
// so it can run twice against one database.
func repositorySuite(t *testing.T, st *store.Store) {
	ctx := context.Background()
	suffix := uuid.NewString() // unique per run of the suite, so the suite can run twice on one database

	t.Run("workspaces", func(t *testing.T) {
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_" + suffix, Name: "Fit Studio"})
		if err != nil {
			t.Fatal(err)
		}
		if w.ID.IsZero() || w.ID.Version() != 7 || !strings.HasPrefix(w.ID.String(), "wks_") {
			t.Fatalf("id = %v", w.ID)
		}
		if w.Region != store.DefaultRegion || w.BillingMode != string(store.BillingManaged) {
			t.Fatalf("defaults not applied: %+v", w)
		}
		if w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() {
			t.Fatalf("timestamps not set: %+v", w)
		}

		byID, err := st.GetWorkspace(ctx, w.ID)
		if err != nil || byID.ID != w.ID || byID.Name != "Fit Studio" {
			t.Fatalf("GetWorkspace = %+v, %v", byID, err)
		}
		byOrg, err := st.GetWorkspaceByClerkOrgID(ctx, "org_"+suffix)
		if err != nil || byOrg.ID != w.ID {
			t.Fatalf("GetWorkspaceByClerkOrgID = %+v, %v", byOrg, err)
		}

		if _, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_" + suffix, Name: "Again"}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate clerk org: err = %v, want ErrConflict", err)
		}
		if _, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_x" + suffix, Name: "x", BillingMode: "free-for-all"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("bad billing mode: err = %v, want ErrInvalid", err)
		}
		if _, err := st.CreateWorkspace(ctx, store.NewWorkspace{Name: "no org"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("missing org: err = %v, want ErrInvalid", err)
		}
		if _, err := st.GetWorkspaceByClerkOrgID(ctx, "org_missing_"+suffix); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing workspace: err = %v, want ErrNotFound", err)
		}
		byok, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_byok_" + suffix, Name: "B", BillingMode: store.BillingBYOK, Region: "us"})
		if err != nil || byok.BillingMode != "byok" || byok.Region != "us" {
			t.Fatalf("explicit values: %+v, %v", byok, err)
		}
	})

	t.Run("users", func(t *testing.T) {
		first, err := st.UpsertUserByClerkID(ctx, "user_"+suffix, "ada@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if first.ID.IsZero() || !strings.HasPrefix(first.ID.String(), "usr_") {
			t.Fatalf("id = %v", first.ID)
		}
		again, err := st.UpsertUserByClerkID(ctx, "user_"+suffix, "ada@new.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID {
			t.Fatalf("a user must keep its first id: %v then %v", first.ID, again.ID)
		}
		if again.Email != "ada@new.example.com" {
			t.Fatalf("email = %q", again.Email)
		}
		if !again.CreatedAt.Equal(first.CreatedAt) {
			t.Fatalf("created_at changed: %v then %v", first.CreatedAt, again.CreatedAt)
		}
		// The updated_at trigger ran, and it is the trigger's doing: the query never sets the column.
		if !again.UpdatedAt.After(first.UpdatedAt) {
			t.Fatalf("updated_at not bumped: %v then %v", first.UpdatedAt, again.UpdatedAt)
		}
		if _, err := st.UpsertUserByClerkID(ctx, "", "x@example.com"); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("empty clerk id: err = %v", err)
		}
	})

	t.Run("members", func(t *testing.T) {
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_m_" + suffix, Name: "Members"})
		if err != nil {
			t.Fatal(err)
		}
		ada, _ := st.UpsertUserByClerkID(ctx, "user_ada_"+suffix, "ada@example.com")
		bob, _ := st.UpsertUserByClerkID(ctx, "user_bob_"+suffix, "bob@example.com")

		if got, err := st.ListMembers(ctx, w.ID); err != nil || len(got) != 0 {
			t.Fatalf("new workspace members = %v, %v", got, err)
		}
		if _, err := st.AddMember(ctx, w.ID, ada.ID, store.RoleOwner); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddMember(ctx, w.ID, bob.ID, store.RoleViewer); err != nil {
			t.Fatal(err)
		}
		// Adding again changes the role.
		if m, err := st.AddMember(ctx, w.ID, bob.ID, store.RoleEditor); err != nil || m.Role != "editor" {
			t.Fatalf("role change = %+v, %v", m, err)
		}
		got, err := st.ListMembers(ctx, w.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("members = %v, %v", got, err)
		}
		roles := map[string]string{}
		for _, m := range got {
			if m.WorkspaceID != w.ID {
				t.Fatalf("member of another workspace listed: %+v", m)
			}
			roles[m.Email] = m.Role
		}
		if roles["ada@example.com"] != "owner" || roles["bob@example.com"] != "editor" {
			t.Fatalf("roles = %v", roles)
		}

		if _, err := st.AddMember(ctx, w.ID, ada.ID, "admin"); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown role: err = %v, want ErrInvalid", err)
		}
		ghostUser := ghostUserID(t)
		if _, err := st.AddMember(ctx, w.ID, ghostUser, store.RoleViewer); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user: err = %v, want ErrNotFound", err)
		}
		ghostWS := ghostWorkspaceID(t)
		if _, err := st.AddMember(ctx, ghostWS, ada.ID, store.RoleViewer); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown workspace: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("creator profile", func(t *testing.T) {
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_p_" + suffix, Name: "Profile"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetCreatorProfile(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("profile before the first save: err = %v, want ErrNotFound", err)
		}

		// Empty input gets the defaults.
		p, err := st.UpsertCreatorProfile(ctx, w.ID, store.ProfileInput{})
		if err != nil {
			t.Fatal(err)
		}
		if p.WorkspaceID != w.ID || p.Language != "en" || p.Tone != "" || p.AudienceRegions == nil || len(p.AudienceRegions) != 0 ||
			string(p.Niche) != "{}" || string(p.Brand) != "{}" || string(p.Defaults) != "{}" {
			t.Fatalf("defaults: %+v", p)
		}

		in := store.ProfileInput{
			Niche:           json.RawMessage(`{"primary":"fitness","sub":["calisthenics","mobility"]}`),
			AudienceRegions: []string{"NL", "DE"},
			Tone:            "dry, direct",
			Language:        "nl",
			Brand:           json.RawMessage(`{"fonts":["Inter"],"colours":["#111111"]}`),
			Defaults:        json.RawMessage(`{"caption_style":"bold","fps":30}`),
		}
		saved, err := st.UpsertCreatorProfile(ctx, w.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.GetCreatorProfile(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Tone != in.Tone || got.Language != "nl" || !slices.Equal(got.AudienceRegions, in.AudienceRegions) {
			t.Fatalf("profile = %+v", got)
		}
		for name, pair := range map[string][2]json.RawMessage{
			"niche": {got.Niche, in.Niche}, "brand": {got.Brand, in.Brand}, "defaults": {got.Defaults, in.Defaults},
		} {
			if !jsonEqual(t, pair[0], pair[1]) {
				t.Errorf("%s = %s, want %s", name, pair[0], pair[1])
			}
		}
		if !saved.CreatedAt.Equal(p.CreatedAt) || !saved.UpdatedAt.After(p.UpdatedAt) {
			t.Fatalf("timestamps: created %v then %v, updated %v then %v", p.CreatedAt, saved.CreatedAt, p.UpdatedAt, saved.UpdatedAt)
		}

		if _, err := st.UpsertCreatorProfile(ctx, w.ID, store.ProfileInput{Brand: json.RawMessage(`{not json`)}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("invalid JSON: err = %v, want ErrInvalid", err)
		}
		if _, err := st.UpsertCreatorProfile(ctx, ghostWorkspaceID(t), store.ProfileInput{}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("profile for a missing workspace: err = %v, want ErrNotFound", err)
		}
	})

	// Input the database refuses is the caller's problem (ErrInvalid), not an internal error: a NUL byte cannot be
	// stored in text (SQLSTATE 22021) and \u0000 cannot be stored in jsonb (22P05).
	t.Run("data exceptions", func(t *testing.T) {
		if _, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_nul_" + suffix, Name: "a\x00b"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("NUL in text: err = %v, want ErrInvalid", err)
		}
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_u0_" + suffix, Name: "u0"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.UpsertCreatorProfile(ctx, w.ID, store.ProfileInput{Niche: json.RawMessage(`{"a":"\u0000"}`)})
		if !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("\\u0000 in jsonb: err = %v, want ErrInvalid", err)
		}
		if strings.Contains(err.Error(), "u0000") {
			t.Errorf("the error quotes the value: %v", err)
		}
	})

	// Postgres 18 makes the keys (uuidv7()). The first 48 bits are a millisecond timestamp, so the key says when the
	// row was made, and it matches created_at (both are the statement's time).
	t.Run("uuidv7 keys", func(t *testing.T) {
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_v7_" + suffix, Name: "v7"})
		if err != nil {
			t.Fatal(err)
		}
		var extracted time.Time
		if err := st.Pool().QueryRow(ctx, `SELECT uuid_extract_timestamp(id) FROM workspaces WHERE id = $1`, w.ID).Scan(&extracted); err != nil {
			t.Fatal(err)
		}
		if d := extracted.Sub(w.CreatedAt).Abs(); d > 2*time.Second {
			t.Fatalf("uuid_extract_timestamp(id) = %v but created_at = %v", extracted, w.CreatedAt)
		}
		if v := w.ID.Version(); v != 7 {
			t.Fatalf("version = %d", v)
		}
	})

	// The tenancy rule (architecture section 4): a repository function with workspace A's id never returns or changes
	// workspace B's data, even though both are in the same tables.
	t.Run("cross-tenant", func(t *testing.T) {
		a, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_a_" + suffix, Name: "A"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_b_" + suffix, Name: "B"})
		if err != nil {
			t.Fatal(err)
		}
		userB, _ := st.UpsertUserByClerkID(ctx, "user_b_"+suffix, "b@example.com")
		if _, err := st.AddMember(ctx, b.ID, userB.ID, store.RoleOwner); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertCreatorProfile(ctx, b.ID, store.ProfileInput{Tone: "B's secret tone", AudienceRegions: []string{"FR"}}); err != nil {
			t.Fatal(err)
		}

		// A has no profile, and asking with A's id must not fall through to B's.
		if p, err := st.GetCreatorProfile(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace A read a profile: %+v, %v", p, err)
		}
		// A has no members, and B's owner is not among them.
		if ms, err := st.ListMembers(ctx, a.ID); err != nil || len(ms) != 0 {
			t.Fatalf("workspace A lists %d members, %v", len(ms), err)
		}
		// Writing A's profile leaves B's alone.
		if _, err := st.UpsertCreatorProfile(ctx, a.ID, store.ProfileInput{Tone: "A's tone"}); err != nil {
			t.Fatal(err)
		}
		pb, err := st.GetCreatorProfile(ctx, b.ID)
		if err != nil || pb.Tone != "B's secret tone" || !slices.Equal(pb.AudienceRegions, []string{"FR"}) {
			t.Fatalf("workspace B's profile changed or vanished: %+v, %v", pb, err)
		}
		pa, err := st.GetCreatorProfile(ctx, a.ID)
		if err != nil || pa.Tone != "A's tone" || pa.WorkspaceID != a.ID {
			t.Fatalf("workspace A's profile = %+v, %v", pa, err)
		}
		// The zero workspace id matches nothing.
		if _, err := st.GetCreatorProfile(ctx, ids.WorkspaceID{}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("zero workspace id: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("transactions", func(t *testing.T) {
		boom := errors.New("boom")
		err := st.InTx(ctx, func(tx *store.Store) error {
			if _, err := tx.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_rollback_" + suffix, Name: "gone"}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx err = %v", err)
		}
		if _, err := st.GetWorkspaceByClerkOrgID(ctx, "org_rollback_"+suffix); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the rolled-back workspace is visible: %v", err)
		}
		err = st.InTx(ctx, func(tx *store.Store) error {
			_, err := tx.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_commit_" + suffix, Name: "kept"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetWorkspaceByClerkOrgID(ctx, "org_commit_"+suffix); err != nil {
			t.Fatalf("the committed workspace is missing: %v", err)
		}
	})

	t.Run("plans", func(t *testing.T) { plansSuite(t, st, suffix) })
	t.Run("assets", func(t *testing.T) { assetsSuite(t, st, suffix) })
	t.Run("projects", func(t *testing.T) { projectsSuite(t, st, suffix) })
	t.Run("renders", func(t *testing.T) { rendersSuite(t, st, suffix) })
	t.Run("annotations", func(t *testing.T) { annotationsSuite(t, st, suffix) })
	t.Run("usage ledger", func(t *testing.T) { usageSuite(t, st, suffix) })
	t.Run("agent runs", func(t *testing.T) { agentRunsSuite(t, st, suffix) })
}

// ghostUserID and ghostWorkspaceID are well-formed keys that no row has.
func ghostUserID(t *testing.T) ids.UserID {
	t.Helper()
	id, err := ids.ParseUserID(ids.Encode(ids.PrefixUser, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ghostWorkspaceID(t *testing.T) ids.WorkspaceID {
	t.Helper()
	id, err := ids.ParseWorkspaceID(ids.Encode(ids.PrefixWorkspace, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
