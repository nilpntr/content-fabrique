package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

func TestClerkSync(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	migrate(t, srv.SuperDSN(database))
	clerkSyncSuite(t, openStore(t, srv.SuperDSN(database)))
}

// clerkSyncSuite exercises the Clerk sync functions. It uses fresh Clerk ids, so it can run twice on one database
// (the role-split test runs it as the app role).
func clerkSyncSuite(t *testing.T, st *store.Store) {
	ctx := context.Background()
	suffix := uuid.NewString()

	newUser := func(t *testing.T, name string, at int64) store.User {
		t.Helper()
		u, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: "user_" + name + suffix, Email: name + "@example.com", ClerkUpdatedAt: at})
		if err != nil || !applied {
			t.Fatalf("SyncUser(%s) = %+v, %v, %v", name, u, applied, err)
		}
		return u
	}
	newWorkspace := func(t *testing.T, name string, at int64) store.Workspace {
		t.Helper()
		w, applied, err := st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_" + name + suffix, Name: name, ClerkUpdatedAt: at})
		if err != nil || !applied {
			t.Fatalf("SyncWorkspace(%s) = %+v, %v, %v", name, w, applied, err)
		}
		return w
	}

	t.Run("users are synced in order", func(t *testing.T) {
		u := newUser(t, "ada", 1000)
		if u.ID.IsZero() || u.ID.Version() != 7 || u.ClerkUpdatedAt != 1000 || u.DeletedAt != nil {
			t.Fatalf("user = %+v", u)
		}
		id := "user_ada" + suffix

		// The same version again (a replay) and an older one change nothing.
		for _, at := range []int64{1000, 999, 0} {
			got, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: id, Email: "stale@example.com", ClerkUpdatedAt: at})
			if err != nil || applied || got.Email != "ada@example.com" || got.ID != u.ID {
				t.Fatalf("version %d: %+v, applied=%v, %v", at, got, applied, err)
			}
		}
		// A newer one updates the email and keeps the id.
		got, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: id, Email: "ada@new.example.com", ClerkUpdatedAt: 2000})
		if err != nil || !applied || got.Email != "ada@new.example.com" || got.ID != u.ID || got.ClerkUpdatedAt != 2000 {
			t.Fatalf("newer: %+v, applied=%v, %v", got, applied, err)
		}
		if g, err := st.GetUserByClerkID(ctx, id); err != nil || g.ID != u.ID {
			t.Fatalf("GetUserByClerkID = %+v, %v", g, err)
		}
		if _, err := st.GetUserByClerkID(ctx, "user_nobody"+suffix); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user: err = %v", err)
		}
		// A user without an email address is still a user.
		if _, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: "user_noemail" + suffix, ClerkUpdatedAt: 5}); err != nil || !applied {
			t.Fatalf("empty email: %v %v", applied, err)
		}
		if _, _, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: " ", ClerkUpdatedAt: 5}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("empty id: err = %v", err)
		}
	})

	t.Run("workspaces are synced in order", func(t *testing.T) {
		w := newWorkspace(t, "studio", 1000)
		if w.ID.IsZero() || w.Region != store.DefaultRegion || w.BillingMode != string(store.BillingManaged) || w.DeletedAt != nil {
			t.Fatalf("workspace = %+v", w)
		}
		id := "org_studio" + suffix
		got, applied, err := st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: id, Name: "Old name", ClerkUpdatedAt: 900})
		if err != nil || applied || got.Name != "studio" {
			t.Fatalf("older: %+v, applied=%v, %v", got, applied, err)
		}
		got, applied, err = st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: id, Name: "Renamed", ClerkUpdatedAt: 2000})
		if err != nil || !applied || got.Name != "Renamed" || got.ID != w.ID {
			t.Fatalf("newer: %+v, applied=%v, %v", got, applied, err)
		}
	})

	t.Run("memberships are synced in order", func(t *testing.T) {
		w, u := newWorkspace(t, "m-ws", 1), newUser(t, "m-user", 1)
		m, applied, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleEditor, 100)
		if err != nil || !applied || m.Role != "editor" {
			t.Fatalf("create: %+v, %v, %v", m, applied, err)
		}
		// An older update (a reordered webhook, a stale token) does not change the role; a newer one does.
		if m, applied, err = st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, 99); err != nil || applied || m.Role != "editor" {
			t.Fatalf("older: %+v, %v, %v", m, applied, err)
		}
		if m, applied, err = st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, 100); err != nil || applied || m.Role != "editor" {
			t.Fatalf("same version: %+v, %v, %v", m, applied, err)
		}
		if m, applied, err = st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, 101); err != nil || !applied || m.Role != "owner" {
			t.Fatalf("newer: %+v, %v, %v", m, applied, err)
		}
		if _, _, err = st.SyncMember(ctx, w.ID, u.ID, "admin", 200); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("unknown role: err = %v", err)
		}
		ghost := ids.UserID{UUID: uuid.Must(uuid.NewV7())}
		if _, _, err = st.SyncMember(ctx, w.ID, ghost, store.RoleViewer, 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user: err = %v", err)
		}
		if err := st.DeleteMember(ctx, w.ID, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteMember(ctx, w.ID, u.ID); err != nil { // removing it twice is fine
			t.Fatal(err)
		}
		if members, err := st.ListMembers(ctx, w.ID); err != nil || len(members) != 0 {
			t.Fatalf("members after delete = %v, %v", members, err)
		}
	})

	t.Run("ResolveSession", func(t *testing.T) {
		w, u := newWorkspace(t, "r-ws", 1), newUser(t, "r-user", 1)
		clerkUser, clerkOrg := "user_r-user"+suffix, "org_r-ws"+suffix

		if _, err := st.ResolveSession(ctx, clerkUser, "org_unknown"+suffix); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown workspace: err = %v", err)
		}
		if _, err := st.ResolveSession(ctx, "user_unknown"+suffix, clerkOrg); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user: err = %v", err)
		}
		// Both exist, no membership yet.
		s, err := st.ResolveSession(ctx, clerkUser, clerkOrg)
		if err != nil || s.Role != nil || s.UserID != u.ID || s.WorkspaceID != w.ID || s.Email != "r-user@example.com" || s.WorkspaceName != "r-ws" || s.UserDeleted || s.WorkspaceDeleted {
			t.Fatalf("session = %+v, %v", s, err)
		}
		if _, _, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, 50); err != nil {
			t.Fatal(err)
		}
		s, err = st.ResolveSession(ctx, clerkUser, clerkOrg)
		if err != nil || s.Role == nil || *s.Role != store.RoleOwner || s.MemberClerkUpdatedAt != 50 {
			t.Fatalf("session = %+v, %v", s, err)
		}
		// Another workspace's membership is not this one's.
		other := newWorkspace(t, "r-other", 1)
		s, err = st.ResolveSession(ctx, clerkUser, "org_r-other"+suffix)
		if err != nil || s.Role != nil || s.WorkspaceID != other.ID {
			t.Fatalf("session in another workspace = %+v, %v", s, err)
		}
	})

	t.Run("deleting a user", func(t *testing.T) {
		w, u := newWorkspace(t, "du-ws", 1), newUser(t, "du-user", 1)
		if _, _, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleEditor, 5); err != nil {
			t.Fatal(err)
		}
		id := "user_du-user" + suffix
		d, err := st.DeleteUser(ctx, id)
		if err != nil || d.ID != u.ID || d.DeletedAt == nil || d.Email != "" {
			t.Fatalf("DeleteUser = %+v, %v", d, err)
		}
		if members, _ := st.ListMembers(ctx, w.ID); len(members) != 0 {
			t.Fatalf("memberships survived the user: %v", members)
		}
		// Deleting twice keeps the first time. A later sync, even a newer one, does not bring the user back.
		again, err := st.DeleteUser(ctx, id)
		if err != nil || again.DeletedAt == nil || !again.DeletedAt.Equal(*d.DeletedAt) {
			t.Fatalf("second delete = %+v, %v", again, err)
		}
		got, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: id, Email: "back@example.com", ClerkUpdatedAt: 999999})
		if err != nil || applied || got.DeletedAt == nil || got.Email != "" {
			t.Fatalf("sync after delete = %+v, applied=%v, %v", got, applied, err)
		}
		s, err := st.ResolveSession(ctx, id, "org_du-ws"+suffix)
		if err != nil || !s.UserDeleted || s.Role != nil {
			t.Fatalf("session = %+v, %v", s, err)
		}
	})

	t.Run("deleting a user that was never synced leaves a tombstone", func(t *testing.T) {
		id := "user_unseen" + suffix
		d, err := st.DeleteUser(ctx, id)
		if err != nil || d.DeletedAt == nil {
			t.Fatalf("DeleteUser = %+v, %v", d, err)
		}
		// The user.created that was overtaken by the delete arrives late.
		got, applied, err := st.SyncUser(ctx, store.SyncUserInput{ClerkUserID: id, Email: "late@example.com", ClerkUpdatedAt: 10})
		if err != nil || applied || got.DeletedAt == nil {
			t.Fatalf("late create = %+v, applied=%v, %v", got, applied, err)
		}
	})

	t.Run("deleting a workspace", func(t *testing.T) {
		w, u := newWorkspace(t, "dw-ws", 1), newUser(t, "dw-user", 1)
		if _, _, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertCreatorProfile(ctx, w.ID, store.ProfileInput{Tone: "kept"}); err != nil {
			t.Fatal(err)
		}
		id := "org_dw-ws" + suffix
		d, err := st.DeleteWorkspace(ctx, id)
		if err != nil || d.ID != w.ID || d.DeletedAt == nil {
			t.Fatalf("DeleteWorkspace = %+v, %v", d, err)
		}
		if members, _ := st.ListMembers(ctx, w.ID); len(members) != 0 {
			t.Fatalf("memberships survived the workspace: %v", members)
		}
		// The workspace's own data is not dropped by a webhook.
		if p, err := st.GetCreatorProfile(ctx, w.ID); err != nil || p.Tone != "kept" {
			t.Fatalf("profile = %+v, %v", p, err)
		}
		got, applied, err := st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: id, Name: "Back", ClerkUpdatedAt: 999999})
		if err != nil || applied || got.DeletedAt == nil || got.Name != "dw-ws" {
			t.Fatalf("sync after delete = %+v, applied=%v, %v", got, applied, err)
		}
		s, err := st.ResolveSession(ctx, "user_dw-user"+suffix, id)
		if err != nil || !s.WorkspaceDeleted {
			t.Fatalf("session = %+v, %v", s, err)
		}
		// An unseen workspace gets a tombstone as well.
		unseen, err := st.DeleteWorkspace(ctx, "org_unseen"+suffix)
		if err != nil || unseen.DeletedAt == nil {
			t.Fatalf("DeleteWorkspace of an unseen org = %+v, %v", unseen, err)
		}
		if _, applied, _ := st.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: "org_unseen" + suffix, Name: "x", ClerkUpdatedAt: 1}); applied {
			t.Fatal("a tombstoned workspace was revived")
		}
	})

	t.Run("webhook deliveries are recorded once", func(t *testing.T) {
		evt := store.WebhookEvent{SvixID: "msg_" + suffix, Type: "user.created", ClerkEventAt: 10}
		if first, err := st.RecordWebhookEvent(ctx, evt); err != nil || !first {
			t.Fatalf("first = %v, %v", first, err)
		}
		if first, err := st.RecordWebhookEvent(ctx, evt); err != nil || first {
			t.Fatalf("replay = %v, %v", first, err)
		}
		if _, err := st.RecordWebhookEvent(ctx, store.WebhookEvent{Type: "user.created"}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("no id: err = %v", err)
		}

		// A rolled-back transaction records nothing, so Svix's retry is applied.
		boom := errors.New("boom")
		retry := store.WebhookEvent{SvixID: "msg_retry" + suffix, Type: "user.created", ClerkEventAt: 10}
		err := st.InTx(ctx, func(tx *store.Store) error {
			if first, err := tx.RecordWebhookEvent(ctx, retry); err != nil || !first {
				t.Fatalf("in tx: %v, %v", first, err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx = %v", err)
		}
		if first, err := st.RecordWebhookEvent(ctx, retry); err != nil || !first {
			t.Fatalf("after rollback = %v, %v", first, err)
		}
	})

	t.Run("membership tombstones", func(t *testing.T) {
		subject := "org_t" + suffix + "/user_t" + suffix
		if gone, err := st.MembershipDeletedSince(ctx, subject, 1); err != nil || gone {
			t.Fatalf("before any delete: %v, %v", gone, err)
		}
		if _, err := st.RecordWebhookEvent(ctx, store.WebhookEvent{SvixID: "msg_del" + suffix, Type: "organizationMembership.deleted", Subject: subject, ClerkEventAt: 500}); err != nil {
			t.Fatal(err)
		}
		// An event at or before the delete is stale; one after it is a new membership.
		for at, want := range map[int64]bool{1: true, 499: true, 500: true, 501: false} {
			if gone, err := st.MembershipDeletedSince(ctx, subject, at); err != nil || gone != want {
				t.Errorf("MembershipDeletedSince(%d) = %v, %v; want %v", at, gone, err, want)
			}
		}
		// Only deletes count, and only for this pair.
		if _, err := st.RecordWebhookEvent(ctx, store.WebhookEvent{SvixID: "msg_upd" + suffix, Type: "organizationMembership.updated", Subject: subject + "x", ClerkEventAt: 900}); err != nil {
			t.Fatal(err)
		}
		if gone, _ := st.MembershipDeletedSince(ctx, subject+"x", 1); gone {
			t.Error("an update counted as a delete")
		}
		if gone, _ := st.MembershipDeletedSince(ctx, "org_other/user_t"+suffix, 1); gone {
			t.Error("another organization's delete counted")
		}
	})

	t.Run("SyncMember refuses what a membership delete already removed", func(t *testing.T) {
		w, u := newWorkspace(t, "tomb-ws", 1), newUser(t, "tomb-user", 1)
		subject := "org_tomb-ws" + suffix + "/user_tomb-user" + suffix
		if _, _, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleEditor, 100); err != nil {
			t.Fatal(err)
		}
		// The delete webhook, at 500.
		if err := st.DeleteMember(ctx, w.ID, u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RecordWebhookEvent(ctx, store.WebhookEvent{SvixID: "msg_tomb" + suffix, Type: "organizationMembership.deleted", Subject: subject, ClerkEventAt: 500}); err != nil {
			t.Fatal(err)
		}
		// A write from a token issued at 400, or at the very moment, is stale; one issued at 501 is a new membership.
		for _, at := range []int64{400, 500} {
			if _, applied, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleOwner, at); !errors.Is(err, store.ErrMembershipRemoved) || applied {
				t.Fatalf("at %d: applied=%v, err=%v, want ErrMembershipRemoved", at, applied, err)
			}
		}
		if members, _ := st.ListMembers(ctx, w.ID); len(members) != 0 {
			t.Fatalf("a refused write left a row: %v", members)
		}
		if m, applied, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleViewer, 501); err != nil || !applied || m.Role != "viewer" {
			t.Fatalf("at 501: %+v, %v, %v", m, applied, err)
		}
		// A delete that failed to apply (status failed) is no tombstone.
		other := newUser(t, "tomb-other", 1)
		if _, err := st.RecordWebhookEvent(ctx, store.WebhookEvent{SvixID: "msg_tomb_failed" + suffix, Type: "organizationMembership.deleted", Subject: "org_tomb-ws" + suffix + "/user_tomb-other" + suffix, ClerkEventAt: 900, Failed: true, Detail: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, applied, err := st.SyncMember(ctx, w.ID, other.ID, store.RoleViewer, 100); err != nil || !applied {
			t.Fatalf("after a failed delete record: %v, %v", applied, err)
		}
	})
}
