package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func identity(userID, orgID, role string, issuedAtMillis int64) Identity {
	return Identity{ClerkUserID: userID, ClerkOrgID: orgID, OrgRole: role, IssuedAtMillis: issuedAtMillis}
}

func TestSyncerCreatesWhatIsMissingFromClerk(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1000}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit Studio", UpdatedAt: 1000}
	s := NewSyncer(st, dir)

	p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 5000))
	if err != nil {
		t.Fatal(err)
	}
	if p.Email != "ada@example.com" || p.WorkspaceName != "Fit Studio" || p.Role != store.RoleOwner || p.UserID.IsZero() || p.WorkspaceID.IsZero() {
		t.Fatalf("principal = %+v", p)
	}
	if got := dir.called(); !slices.Equal(got, []string{"user:user_ada", "org:org_fit"}) {
		t.Fatalf("directory calls = %v", got)
	}

	// The rows are in Postgres, and the workspace id is the one we made, not anything Clerk or the caller sent.
	w, err := st.GetWorkspaceByClerkOrgID(ctx, "org_fit")
	if err != nil || w.ID != p.WorkspaceID || w.Name != "Fit Studio" {
		t.Fatalf("workspace = %+v, %v", w, err)
	}
	members, err := st.ListMembers(ctx, w.ID)
	if err != nil || len(members) != 1 || members[0].UserID != p.UserID || members[0].Role != "owner" {
		t.Fatalf("members = %+v, %v", members, err)
	}

	// The next request is one query and no call to Clerk.
	dir.reset()
	again, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 6000))
	if err != nil || again != p {
		t.Fatalf("second resolve = %+v, %v", again, err)
	}
	if got := dir.called(); len(got) != 0 {
		t.Fatalf("the second request called Clerk: %v", got)
	}
}

func TestSyncerFetchesOnlyWhatIsMissing(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit Studio", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	first, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 10))
	if err != nil {
		t.Fatal(err)
	}
	// A teammate joins the existing workspace: the organization is known, so only the user is fetched.
	dir.reset()
	second, err := s.Resolve(ctx, identity("user_bob", "org_fit", "org:member", 20))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.called(); !slices.Equal(got, []string{"user:user_bob"}) {
		t.Fatalf("directory calls = %v", got)
	}
	if second.WorkspaceID != first.WorkspaceID || second.UserID == first.UserID || second.Role != store.RoleEditor {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
}

func TestSyncerMapsRolesFromTheToken(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1}
	s := NewSyncer(st, dir)
	for i, tc := range []struct {
		clerkRole string
		want      store.Role
	}{{"org:admin", store.RoleOwner}, {"org:member", store.RoleEditor}, {"org:billing_manager", store.RoleViewer}, {"", store.RoleViewer}} {
		id := "user_" + string(rune('a'+i))
		dir.users[id] = ClerkUser{ID: id, Email: id + "@example.com", UpdatedAt: 1}
		p, err := s.Resolve(ctx, identity(id, "org_fit", tc.clerkRole, 10))
		if err != nil || p.Role != tc.want {
			t.Errorf("role %q: %+v, %v; want %s", tc.clerkRole, p, err, tc.want)
		}
	}
}

func TestSyncerFollowsRoleChangesInOrder(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	if p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:member", 1000)); err != nil || p.Role != store.RoleEditor {
		t.Fatalf("joined as member: %+v, %v", p, err)
	}
	// Promoted in Clerk: a token issued later says admin, and the next request follows it.
	if p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 2000)); err != nil || p.Role != store.RoleOwner {
		t.Fatalf("promoted: %+v, %v", p, err)
	}
	// A token issued before the promotion (still within its minute) must not undo it.
	if p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:member", 1500)); err != nil || p.Role != store.RoleOwner {
		t.Fatalf("stale token: %+v, %v; the older token must not demote", p, err)
	}
	// Demoted: a later token says member.
	if p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:member", 3000)); err != nil || p.Role != store.RoleEditor {
		t.Fatalf("demoted: %+v, %v", p, err)
	}

	// A webhook that arrived after the token was issued wins over the token.
	w, _ := st.GetWorkspaceByClerkOrgID(ctx, "org_fit")
	u, _ := st.GetUserByClerkID(ctx, "user_ada")
	if _, applied, err := st.SyncMember(ctx, w.ID, u.ID, store.RoleViewer, 9000); err != nil || !applied {
		t.Fatalf("webhook: %v %v", applied, err)
	}
	if p, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 8000)); err != nil || p.Role != store.RoleViewer {
		t.Fatalf("token older than the webhook: %+v, %v", p, err)
	}
}

func TestSyncerRefusesDeletedUsersAndWorkspaces(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1}
	dir.orgs["org_gym"] = ClerkOrg{ID: "org_gym", Name: "Gym", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	for _, id := range []Identity{
		identity("user_ada", "org_fit", "org:admin", 10),
		identity("user_bob", "org_gym", "org:admin", 10),
	} {
		if _, err := s.Resolve(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	// A valid token of a user deleted in Clerk no longer gets in, in either path.
	if _, err := st.DeleteUser(ctx, "user_ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 20)); !errors.Is(err, ErrUserDeleted) {
		t.Fatalf("deleted user: err = %v", err)
	}
	// The same for a deleted workspace, even for a member who is still fine.
	if _, err := st.DeleteWorkspace(ctx, "org_gym"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, identity("user_bob", "org_gym", "org:admin", 20)); !errors.Is(err, ErrWorkspaceDeleted) {
		t.Fatalf("deleted workspace: err = %v", err)
	}
	// And in the slow path: a user whose row is a tombstone and who has no membership yet.
	if _, err := s.Resolve(ctx, identity("user_ada", "org_gym", "org:admin", 20)); !errors.Is(err, ErrUserDeleted) && !errors.Is(err, ErrWorkspaceDeleted) {
		t.Fatalf("deleted user in a deleted workspace: err = %v", err)
	}
	// Nothing was resurrected.
	if u, _ := st.GetUserByClerkID(ctx, "user_ada"); u.DeletedAt == nil {
		t.Fatal("the deleted user was revived")
	}
	if w, _ := st.GetWorkspaceByClerkOrgID(ctx, "org_gym"); w.DeletedAt == nil {
		t.Fatal("the deleted workspace was revived")
	}
	if members, _ := st.ListMembers(ctx, mustWorkspace(t, st, "org_gym").ID); len(members) != 0 {
		t.Fatalf("a deleted workspace has members: %v", members)
	}
}

func mustWorkspace(t *testing.T, st *store.Store, clerkOrgID string) store.Workspace {
	t.Helper()
	w, err := st.GetWorkspaceByClerkOrgID(context.Background(), clerkOrgID)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSyncerWhenClerkNoLongerKnowsThem(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	// The organization was deleted: Clerk answers 404.
	if _, err := s.Resolve(ctx, identity("user_ada", "org_gone", "org:admin", 10)); !errors.Is(err, ErrWorkspaceDeleted) {
		t.Fatalf("org gone: err = %v", err)
	}
	// The user was deleted.
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1}
	if _, err := s.Resolve(ctx, identity("user_gone", "org_fit", "org:admin", 10)); !errors.Is(err, ErrUserDeleted) {
		t.Fatalf("user gone: err = %v", err)
	}
}

func TestSyncerWhenClerkIsDown(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.err = ErrDirectoryUnavailable
	s := NewSyncer(st, dir)
	if _, err := s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 10)); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// No Backend client at all (no secret key) is the same failure, never an open door.
	if _, err := NewSyncer(st, nil).Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 10)); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("no directory: err = %v", err)
	}
	if _, err := st.GetUserByClerkID(ctx, "user_ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a user was stored although Clerk could not be asked: %v", err)
	}
}

func TestSyncerConcurrentFirstRequests(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t), newFakeDir()
	dir.users["user_ada"] = ClerkUser{ID: "user_ada", Email: "ada@example.com", UpdatedAt: 1}
	dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1}
	s := NewSyncer(st, dir)

	var wg sync.WaitGroup
	results := make([]Principal, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 10))
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatalf("request %d: %+v, %v (first %+v)", i, results[i], errs[i], results[0])
		}
	}
}

// The whole API path with the real Syncer, the real Postgres and a locally signed token: GET /v1/me.
func TestMountedMeEndpoint(t *testing.T) {
	st, dir, f := newStore(t), newFakeDir(), newFixture(t)
	dir.users["user_2abc"] = ClerkUser{ID: "user_2abc", Email: "ada@example.com", UpdatedAt: 1}
	dir.orgs["org_2abc"] = ClerkOrg{ID: "org_2abc", Name: "Fit Studio", UpdatedAt: 1}

	mux := http.NewServeMux()
	if _, err := Mount(mux, Deps{
		Config:     config.Config{AppOrigin: testOrigin, ClerkSecretKey: "sk_test_unused"},
		Store:      st,
		Directory:  dir,
		JWKSClient: &jwks.Client{Backend: f.backend},
	}); err != nil {
		t.Fatal(err)
	}

	do := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	wantError(t, do(""), http.StatusUnauthorized, "unauthenticated")
	wantError(t, do(f.token(t, map[string]any{"azp": "http://evil.example"})), http.StatusUnauthorized, "invalid_token")
	wantError(t, do(f.token(t, map[string]any{"o": nil})), http.StatusForbidden, "no_active_workspace")

	rec := do(f.token(t, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"email":"ada@example.com"`, `"name":"Fit Studio"`, `"role":"owner"`, `"id":"usr_`, `"id":"wks_`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
	if strings.Contains(body, "user_2abc") || strings.Contains(body, "org_2abc") {
		t.Errorf("body leaks Clerk ids: %s", body)
	}

	// Deleted in Clerk: the very same valid token is refused.
	if _, err := st.DeleteWorkspace(context.Background(), "org_2abc"); err != nil {
		t.Fatal(err)
	}
	wantError(t, do(f.token(t, nil)), http.StatusForbidden, "workspace_deleted")
}

func TestMountWithoutSecretsFailsClosed(t *testing.T) {
	st := newStore(t)
	mux := http.NewServeMux()
	if _, err := Mount(mux, Deps{Config: config.Config{AppOrigin: testOrigin}, Store: st}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	wantError(t, rec, http.StatusServiceUnavailable, "auth_not_configured")

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/webhooks/clerk", strings.NewReader("{}")))
	wantError(t, rec, http.StatusServiceUnavailable, "webhook_not_configured")
}
