package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/store"
	svix "github.com/svix/svix-webhooks/go"
)

// A signing secret for the tests only: whsec_ plus base64 of a fixed key, the shape Clerk shows.
var testWebhookSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-signing-key-0123456789abcdef"))

type webhookRig struct {
	t       *testing.T
	st      *store.Store
	dir     *fakeDir
	h       *WebhookHandler
	signer  *svix.Webhook
	logs    *bytes.Buffer
	counter int
}

func newWebhookRig(t *testing.T) *webhookRig {
	t.Helper()
	st, dir := newStore(t), newFakeDir()
	logs := &bytes.Buffer{}
	h, err := NewWebhookHandler(st, dir, testWebhookSecret, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := svix.NewWebhook(testWebhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	return &webhookRig{t: t, st: st, dir: dir, h: h, signer: signer, logs: logs}
}

// delivery is one signed request, so a test can post the very same one twice.
type delivery struct {
	id   string
	body []byte
	ts   time.Time
}

// event builds a signed delivery of a Clerk event with the given payload under "data".
func (r *webhookRig) event(typ string, emitted int64, data map[string]any) delivery {
	r.t.Helper()
	r.counter++
	body, err := json.Marshal(map[string]any{"type": typ, "object": "event", "timestamp": emitted, "data": data})
	if err != nil {
		r.t.Fatal(err)
	}
	return delivery{id: fmt.Sprintf("msg_%d_%s", r.counter, strconv.FormatInt(time.Now().UnixNano(), 36)), body: body, ts: time.Now()}
}

func (r *webhookRig) sign(d delivery) http.Header {
	r.t.Helper()
	sig, err := r.signer.Sign(d.id, d.ts, d.body)
	if err != nil {
		r.t.Fatal(err)
	}
	h := http.Header{}
	h.Set("svix-id", d.id)
	h.Set("svix-timestamp", strconv.FormatInt(d.ts.Unix(), 10))
	h.Set("svix-signature", sig)
	return h
}

func (r *webhookRig) post(d delivery) *httptest.ResponseRecorder {
	return r.postWith(d, r.sign(d))
}

func (r *webhookRig) postWith(d delivery, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/clerk", bytes.NewReader(d.body))
	for k, v := range headers {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func status(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var b struct{ Status string }
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Status
}

func (r *webhookRig) wantStatus(d delivery, want string) {
	r.t.Helper()
	if got := status(r.t, r.post(d)); got != want {
		r.t.Fatalf("%s: status %q, want %q", d.id, got, want)
	}
}

func (r *webhookRig) countEvents() int {
	r.t.Helper()
	var n int
	if err := r.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM webhook_events`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func userPayload(id, email string, updatedAt int64) map[string]any {
	return map[string]any{
		"id": id, "object": "user", "primary_email_address_id": "idn_primary",
		"email_addresses": []map[string]any{{"id": "idn_other", "email_address": "other@example.com"}, {"id": "idn_primary", "email_address": email}},
		"updated_at":      updatedAt,
	}
}

func orgPayload(id, name string, updatedAt int64) map[string]any {
	return map[string]any{"id": id, "object": "organization", "name": name, "updated_at": updatedAt}
}

func membershipPayload(orgID, orgName, userID, role string, updatedAt int64) map[string]any {
	return map[string]any{
		"id": "orgmem_" + userID, "object": "organization_membership", "role": role, "updated_at": updatedAt,
		"organization":     orgPayload(orgID, orgName, updatedAt),
		"public_user_data": map[string]any{"user_id": userID, "identifier": "ignored@example.com"},
	}
}

func TestWebhookRejectsBadSignatures(t *testing.T) {
	r := newWebhookRig(t)
	d := r.event(EventUserCreated, 1000, userPayload("user_ada", "ada@example.com", 1000))
	good := r.sign(d)

	otherSigner, _ := svix.NewWebhook("whsec_" + base64.StdEncoding.EncodeToString([]byte("another-secret-key-0123456789abc")))
	foreignSig, _ := otherSigner.Sign(d.id, d.ts, d.body)
	old := d
	old.ts = time.Now().Add(-10 * time.Minute)
	future := d
	future.ts = time.Now().Add(10 * time.Minute)

	tamper := func(h http.Header, k, v string) http.Header {
		c := h.Clone()
		c.Set(k, v)
		return c
	}
	tests := []struct {
		name string
		d    delivery
		h    http.Header
	}{
		{"no signature headers", d, http.Header{}},
		{"only the id", d, http.Header{"Svix-Id": {d.id}}},
		{"signature of another secret", d, tamper(good, "svix-signature", foreignSig)},
		{"garbage signature", d, tamper(good, "svix-signature", "v1,AAAA")},
		{"unsupported version", d, tamper(good, "svix-signature", "v2,"+strings.TrimPrefix(good.Get("svix-signature"), "v1,"))},
		{"another message id", d, tamper(good, "svix-id", "msg_other")},
		{"another timestamp", d, tamper(good, "svix-timestamp", strconv.FormatInt(d.ts.Unix()+1, 10))},
		{"body changed after signing", delivery{id: d.id, ts: d.ts, body: bytes.Replace(d.body, []byte("ada@"), []byte("eve@"), 1)}, good},
		{"signed more than five minutes ago (replay)", old, r.sign(old)},
		{"signed in the future", future, r.sign(future)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := r.postWith(tc.d, tc.h)
			wantError(t, rec, http.StatusBadRequest, "invalid_signature")
		})
	}
	if _, err := r.st.GetUserByClerkID(context.Background(), "user_ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rejected request changed the database: %v", err)
	}
	if n := r.countEvents(); n != 0 {
		t.Fatalf("%d deliveries recorded for rejected requests", n)
	}
}

func TestWebhookNotConfigured(t *testing.T) {
	st := newStore(t)
	logs := &bytes.Buffer{}
	h, err := NewWebhookHandler(st, nil, "", slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/webhooks/clerk", strings.NewReader(`{"type":"user.created"}`)))
		wantError(t, rec, http.StatusServiceUnavailable, "webhook_not_configured")
	}
	if n := strings.Count(logs.String(), "CLERK_WEBHOOK_SECRET is not set"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, logs)
	}

	if _, err := NewWebhookHandler(st, nil, "whsec_!!!not base64!!!", nil); err == nil {
		t.Fatal("a malformed secret was accepted")
	} else if strings.Contains(err.Error(), "not base64") {
		t.Fatalf("the error repeats the secret: %v", err)
	}
}

func TestWebhookReplayIsIdempotent(t *testing.T) {
	r := newWebhookRig(t)
	ctx := context.Background()
	d := r.event(EventUserCreated, 1000, userPayload("user_ada", "ada@example.com", 1000))
	headers := r.sign(d)

	// The very same signed request, three times: Svix's retries, or a manual replay from the dashboard.
	first := r.postWith(d, headers)
	if got := status(t, first); got != "applied" {
		t.Fatalf("first = %q", got)
	}
	u, err := r.st.GetUserByClerkID(ctx, "user_ada")
	if err != nil || u.Email != "ada@example.com" {
		t.Fatalf("user = %+v, %v (the primary address must win over the first)", u, err)
	}
	for range 2 {
		if got := status(t, r.postWith(d, headers)); got != "duplicate" {
			t.Fatalf("replay = %q, want duplicate", got)
		}
	}
	if n := r.countEvents(); n != 1 {
		t.Fatalf("webhook_events has %d rows, want 1", n)
	}
	again, _ := r.st.GetUserByClerkID(ctx, "user_ada")
	if again != u {
		t.Fatalf("a replay changed the row: %+v -> %+v", u, again)
	}

	// A replay of a delivery does not undo what a later event did.
	update := r.event(EventUserUpdated, 2000, userPayload("user_ada", "ada@new.example.com", 2000))
	r.wantStatus(update, "applied")
	if got := status(t, r.postWith(d, headers)); got != "duplicate" {
		t.Fatalf("late replay = %q", got)
	}
	if now, _ := r.st.GetUserByClerkID(ctx, "user_ada"); now.Email != "ada@new.example.com" {
		t.Fatalf("the replay of the old delivery reverted the user: %+v", now)
	}
}

func TestWebhookConcurrentReplaysApplyOnce(t *testing.T) {
	r := newWebhookRig(t)
	d := r.event(EventOrganizationCreated, 1000, orgPayload("org_fit", "Fit Studio", 1000))
	headers := r.sign(d)

	const n = 8
	results := make(chan string, n)
	for range n {
		go func() { results <- status(t, r.postWith(d, headers)) }()
	}
	applied, duplicate := 0, 0
	for range n {
		switch s := <-results; s {
		case "applied":
			applied++
		case "duplicate":
			duplicate++
		default:
			t.Errorf("status %q", s)
		}
	}
	if applied != 1 || duplicate != n-1 {
		t.Fatalf("applied %d, duplicate %d", applied, duplicate)
	}
	if got := r.countEvents(); got != 1 {
		t.Fatalf("webhook_events has %d rows", got)
	}
}

func TestWebhookOutOfOrderEventsAreIgnored(t *testing.T) {
	r := newWebhookRig(t)
	ctx := context.Background()

	// Users: the update (2000) is delivered before the create (1000).
	r.wantStatus(r.event(EventUserUpdated, 2000, userPayload("user_ada", "new@example.com", 2000)), "applied")
	r.wantStatus(r.event(EventUserCreated, 1000, userPayload("user_ada", "old@example.com", 1000)), "applied") // a new delivery, an old fact
	if u, _ := r.st.GetUserByClerkID(ctx, "user_ada"); u.Email != "new@example.com" || u.ClerkUpdatedAt != 2000 {
		t.Fatalf("user = %+v: the older event overwrote the newer", u)
	}

	// Organizations, the same.
	r.wantStatus(r.event(EventOrganizationUpdated, 5000, orgPayload("org_fit", "Renamed", 5000)), "applied")
	r.wantStatus(r.event(EventOrganizationCreated, 4000, orgPayload("org_fit", "Original", 4000)), "applied")
	if w, _ := r.st.GetWorkspaceByClerkOrgID(ctx, "org_fit"); w.Name != "Renamed" {
		t.Fatalf("workspace = %+v", w)
	}

	// Memberships: a stale role change does not demote.
	r.wantStatus(r.event(EventMembershipUpdated, 7000, membershipPayload("org_fit", "Renamed", "user_ada", "org:admin", 7000)), "applied")
	r.wantStatus(r.event(EventMembershipCreated, 6000, membershipPayload("org_fit", "Renamed", "user_ada", "org:member", 6000)), "applied")
	s, err := r.st.ResolveSession(ctx, "user_ada", "org_fit")
	if err != nil || s.Role == nil || *s.Role != store.RoleOwner {
		t.Fatalf("session = %+v, %v", s, err)
	}
	if n := r.countEvents(); n != 6 {
		t.Fatalf("webhook_events has %d rows, want 6 (each delivery is recorded)", n)
	}
}

func TestWebhookMembershipLifecycle(t *testing.T) {
	r := newWebhookRig(t)
	ctx := context.Background()
	r.wantStatus(r.event(EventUserCreated, 1000, userPayload("user_ada", "ada@example.com", 1000)), "applied")
	// The organization arrives inside the membership event (organization.created has not been delivered yet).
	r.wantStatus(r.event(EventMembershipCreated, 2000, membershipPayload("org_fit", "Fit Studio", "user_ada", "org:admin", 2000)), "applied")

	role := func() *store.Role {
		s, err := r.st.ResolveSession(ctx, "user_ada", "org_fit")
		if err != nil {
			t.Fatal(err)
		}
		return s.Role
	}
	if got := role(); got == nil || *got != store.RoleOwner {
		t.Fatalf("role after create = %v", got)
	}
	if w, _ := r.st.GetWorkspaceByClerkOrgID(ctx, "org_fit"); w.Name != "Fit Studio" {
		t.Fatalf("workspace = %+v", w)
	}

	r.wantStatus(r.event(EventMembershipUpdated, 3000, membershipPayload("org_fit", "Fit Studio", "user_ada", "org:member", 3000)), "applied")
	if got := role(); got == nil || *got != store.RoleEditor {
		t.Fatalf("role after update = %v", got)
	}
	r.wantStatus(r.event(EventMembershipUpdated, 3500, membershipPayload("org_fit", "Fit Studio", "user_ada", "org:custom_role", 3500)), "applied")
	if got := role(); got == nil || *got != store.RoleViewer {
		t.Fatalf("role of a custom Clerk role = %v, want viewer", got)
	}

	// The delete.
	del := membershipPayload("org_fit", "Fit Studio", "user_ada", "org:member", 3500)
	r.wantStatus(r.event(EventMembershipDeleted, 4000, del), "applied")
	if got := role(); got != nil {
		t.Fatalf("role after delete = %v", got)
	}

	// A create or update from before the delete that shows up late must not bring the membership back.
	r.wantStatus(r.event(EventMembershipUpdated, 3600, membershipPayload("org_fit", "Fit Studio", "user_ada", "org:admin", 3600)), "applied")
	if got := role(); got != nil {
		t.Fatalf("a stale update revived the membership: role = %v", *got)
	}

	// Joining again later is a new fact and is applied.
	r.wantStatus(r.event(EventMembershipCreated, 9000, membershipPayload("org_fit", "Fit Studio", "user_ada", "org:member", 9000)), "applied")
	if got := role(); got == nil || *got != store.RoleEditor {
		t.Fatalf("role after rejoining = %v", got)
	}
}

func TestWebhookMembershipFetchesAnUnknownUser(t *testing.T) {
	r := newWebhookRig(t)
	ctx := context.Background()
	d := r.event(EventMembershipCreated, 2000, membershipPayload("org_fit", "Fit Studio", "user_bob", "org:member", 2000))

	// Clerk's Backend API is down: nothing is applied and nothing is recorded, so Svix's retry can still succeed.
	r.dir.err = ErrDirectoryUnavailable
	headers := r.sign(d)
	rec := r.postWith(d, headers)
	wantError(t, rec, http.StatusServiceUnavailable, "identity_unavailable")
	if n := r.countEvents(); n != 0 {
		t.Fatalf("%d deliveries recorded after a failed attempt", n)
	}
	if _, err := r.st.GetWorkspaceByClerkOrgID(ctx, "org_fit"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a failed attempt left a workspace behind: %v", err)
	}

	// The retry, with Clerk back.
	r.dir.err = nil
	r.dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1500}
	if got := status(t, r.postWith(d, headers)); got != "applied" {
		t.Fatalf("retry = %q", got)
	}
	s, err := r.st.ResolveSession(ctx, "user_bob", "org_fit")
	if err != nil || s.Email != "bob@example.com" || s.Role == nil || *s.Role != store.RoleEditor {
		t.Fatalf("session = %+v, %v", s, err)
	}
}

// The Backend API is called before the transaction opens, never inside one.
func TestWebhookDoesNotCallClerkInsideATransaction(t *testing.T) {
	r := newWebhookRig(t)
	r.dir.users["user_bob"] = ClerkUser{ID: "user_bob", Email: "bob@example.com", UpdatedAt: 1500}
	r.dir.orgs["org_fit"] = ClerkOrg{ID: "org_fit", Name: "Fit", UpdatedAt: 1500}
	var open int
	r.dir.onCall = func() {
		if err := r.st.Pool().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`).Scan(&open); err != nil {
			t.Error(err)
		}
	}
	// The payload has no organization name either, so both the user and the organization are fetched.
	payload := membershipPayload("org_fit", "", "user_bob", "org:member", 2000)
	r.wantStatus(r.event(EventMembershipCreated, 2000, payload), "applied")
	if got := r.dir.called(); len(got) != 2 {
		t.Fatalf("directory calls = %v, want the user and the organization", got)
	}
	if open != 0 {
		t.Fatalf("%d transactions were open while Clerk was called", open)
	}
	s, err := r.st.ResolveSession(context.Background(), "user_bob", "org_fit")
	if err != nil || s.Role == nil || s.Email != "bob@example.com" || s.WorkspaceName != "Fit" {
		t.Fatalf("session = %+v, %v", s, err)
	}
}

func TestWebhookDeletes(t *testing.T) {
	r := newWebhookRig(t)
	ctx := context.Background()
	r.wantStatus(r.event(EventUserCreated, 1000, userPayload("user_ada", "ada@example.com", 1000)), "applied")
	r.wantStatus(r.event(EventUserCreated, 1000, userPayload("user_bob", "bob@example.com", 1000)), "applied")
	r.wantStatus(r.event(EventMembershipCreated, 2000, membershipPayload("org_fit", "Fit", "user_ada", "org:admin", 2000)), "applied")
	r.wantStatus(r.event(EventMembershipCreated, 2000, membershipPayload("org_fit", "Fit", "user_bob", "org:member", 2000)), "applied")
	r.wantStatus(r.event(EventMembershipCreated, 2000, membershipPayload("org_gym", "Gym", "user_bob", "org:admin", 2000)), "applied")
	fit := mustWorkspace(t, r.st, "org_fit")

	// user.deleted: soft-deleted, email gone, memberships gone, and the rest of the workspace untouched.
	// Clerk's delete payload is minimal: an id and deleted:true.
	r.wantStatus(r.event(EventUserDeleted, 3000, map[string]any{"id": "user_ada", "object": "user", "deleted": true}), "applied")
	ada, _ := r.st.GetUserByClerkID(ctx, "user_ada")
	if ada.DeletedAt == nil || ada.Email != "" {
		t.Fatalf("user = %+v", ada)
	}
	members, _ := r.st.ListMembers(ctx, fit.ID)
	if len(members) != 1 || members[0].ClerkUserID != "user_bob" {
		t.Fatalf("members = %+v", members)
	}
	if _, err := NewSyncer(r.st, r.dir).Resolve(ctx, identity("user_ada", "org_fit", "org:admin", 4000)); !errors.Is(err, ErrUserDeleted) {
		t.Fatalf("a deleted user can still resolve a session: %v", err)
	}
	// A user.updated/created that arrives after the delete does not bring the user back.
	r.wantStatus(r.event(EventUserUpdated, 3500, userPayload("user_ada", "ada@example.com", 3500)), "applied")
	if ada, _ = r.st.GetUserByClerkID(ctx, "user_ada"); ada.DeletedAt == nil || ada.Email != "" {
		t.Fatalf("a late update revived the user: %+v", ada)
	}
	// Nor a membership.
	r.wantStatus(r.event(EventMembershipCreated, 3600, membershipPayload("org_fit", "Fit", "user_ada", "org:admin", 3600)), "applied")
	if members, _ = r.st.ListMembers(ctx, fit.ID); len(members) != 1 {
		t.Fatalf("a deleted user got a membership: %+v", members)
	}

	// organization.deleted: soft-deleted, memberships gone, and nobody authenticates into it.
	r.wantStatus(r.event(EventOrganizationDeleted, 5000, map[string]any{"id": "org_fit", "object": "organization", "deleted": true}), "applied")
	fit = mustWorkspace(t, r.st, "org_fit")
	if fit.DeletedAt == nil {
		t.Fatalf("workspace = %+v", fit)
	}
	if members, _ = r.st.ListMembers(ctx, fit.ID); len(members) != 0 {
		t.Fatalf("a deleted workspace has members: %+v", members)
	}
	if _, err := NewSyncer(r.st, r.dir).Resolve(ctx, identity("user_bob", "org_fit", "org:member", 6000)); !errors.Is(err, ErrWorkspaceDeleted) {
		t.Fatalf("a deleted workspace can still resolve a session: %v", err)
	}
	r.wantStatus(r.event(EventOrganizationUpdated, 5500, orgPayload("org_fit", "Back", 5500)), "applied")
	if fit = mustWorkspace(t, r.st, "org_fit"); fit.DeletedAt == nil || fit.Name != "Fit" {
		t.Fatalf("a late update revived the workspace: %+v", fit)
	}
	// Bob's other workspace is fine.
	if s, err := r.st.ResolveSession(ctx, "user_bob", "org_gym"); err != nil || s.Role == nil || s.WorkspaceDeleted {
		t.Fatalf("the other workspace: %+v, %v", s, err)
	}

	// Deletes of things we never saw are accepted.
	r.wantStatus(r.event(EventMembershipDeleted, 7000, membershipPayload("org_unknown", "", "user_unknown", "org:member", 7000)), "applied")
	r.wantStatus(r.event(EventUserDeleted, 7000, map[string]any{"id": "user_unknown", "deleted": true}), "applied")
}

func TestWebhookIgnoresOtherEventTypes(t *testing.T) {
	r := newWebhookRig(t)
	d := r.event("session.created", 1000, map[string]any{"id": "sess_1", "user_id": "user_ada"})
	r.wantStatus(d, "ignored")
	if n := r.countEvents(); n != 0 {
		t.Fatalf("%d deliveries recorded for an event we do not handle", n)
	}
}

func TestWebhookRejectsUnusablePayloads(t *testing.T) {
	r := newWebhookRig(t)
	bodies := map[string][]byte{
		"not JSON":                    []byte(`not json`),
		"no type":                     []byte(`{"data":{"id":"user_ada"}}`),
		"user without an id":          []byte(`{"type":"user.created","timestamp":1,"data":{"updated_at":1}}`),
		"data of the wrong type":      []byte(`{"type":"user.created","timestamp":1,"data":"nope"}`),
		"organization without an id":  []byte(`{"type":"organization.updated","timestamp":1,"data":{"name":"x"}}`),
		"membership without a user":   []byte(`{"type":"organizationMembership.created","timestamp":1,"data":{"role":"org:admin","organization":{"id":"org_1"}}}`),
		"update with no version":      []byte(`{"type":"user.updated","data":{"id":"user_ada"}}`),
		"membership role wrong shape": []byte(`{"type":"organizationMembership.created","timestamp":1,"data":{"role":5,"organization":{"id":"org_1"},"public_user_data":{"user_id":"user_1"}}}`),
	}
	for name, body := range bodies {
		t.Run(name, func(_ *testing.T) {
			// Signed but unusable: acknowledged with 2xx, so Svix does not retry it for days or disable the endpoint.
			d := delivery{id: "msg_bad_" + strings.ReplaceAll(name, " ", "_"), body: body, ts: time.Now()}
			r.wantStatus(d, "rejected")
			r.wantStatus(d, "rejected") // and a retry gets the same answer
		})
	}
	// Each was recorded once, as failed, with a reason that has no payload values in it.
	rows, err := r.st.Pool().Query(context.Background(), `SELECT svix_id, status, detail FROM webhook_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, status, detail string
		if err := rows.Scan(&id, &status, &detail); err != nil {
			t.Fatal(err)
		}
		n++
		if status != "failed" || detail == "" || strings.Contains(detail, "nope") || strings.Contains(detail, "user_ada") {
			t.Errorf("%s: status %q, detail %q", id, status, detail)
		}
	}
	if n != len(bodies) {
		t.Fatalf("%d deliveries recorded, want %d", n, len(bodies))
	}
	// Nothing was applied.
	if _, err := r.st.GetUserByClerkID(context.Background(), "user_ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unusable event changed the database: %v", err)
	}
}

// A failed delivery is not a membership tombstone, whatever type it claims.
func TestWebhookFailedDeliveriesAreNotTombstones(t *testing.T) {
	r := newWebhookRig(t)
	body := []byte(`{"type":"organizationMembership.deleted","timestamp":99999,"data":{"role":"org:member","updated_at":99999}}`)
	r.wantStatus(delivery{id: "msg_bad_delete", body: body, ts: time.Now()}, "rejected")
	if gone, err := r.st.MembershipDeletedSince(context.Background(), "/", 1); err != nil || gone {
		t.Fatalf("MembershipDeletedSince = %v, %v", gone, err)
	}
}

func TestWebhookBodyIsLimited(t *testing.T) {
	r := newWebhookRig(t)
	d := delivery{id: "msg_big", body: bytes.Repeat([]byte("x"), MaxWebhookBody+1), ts: time.Now()}
	wantError(t, r.post(d), http.StatusRequestEntityTooLarge, "payload_too_large")
	// Just under the limit is read (and then is not a usable event, which is acknowledged, not a size error).
	d = delivery{id: "msg_edge", body: bytes.Repeat([]byte("x"), MaxWebhookBody), ts: time.Now()}
	r.wantStatus(d, "rejected")
}

func TestWebhookLogsIDsButNoPersonalData(t *testing.T) {
	r := newWebhookRig(t)
	r.wantStatus(r.event(EventUserCreated, 1000, userPayload("user_ada", "ada.lovelace@example.com", 1000)), "applied")
	r.wantStatus(r.event(EventOrganizationCreated, 1000, orgPayload("org_fit", "Ada's Secret Studio", 1000)), "applied")
	r.wantStatus(r.event("session.created", 1000, map[string]any{"id": "sess_1"}), "ignored")
	bad := delivery{id: "msg_leak", body: []byte(`{"type":"user.created","data":{"email_addresses":[{"email_address":"leak@example.com"}]}}`), ts: time.Now()}
	r.wantStatus(bad, "rejected")
	wantError(t, r.postWith(bad, http.Header{}), http.StatusBadRequest, "invalid_signature")

	out := r.logs.String()
	if out == "" {
		t.Fatal("nothing was logged")
	}
	for _, secret := range []string{"ada.lovelace", "example.com", "Secret Studio", testWebhookSecret, "whsec_", "leak@"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log contains %q:\n%s", secret, out)
		}
	}
}

func TestWebhookRouteAcceptsOnlyPost(t *testing.T) {
	st := newStore(t)
	mux := http.NewServeMux()
	if _, err := Mount(mux, deps(st, testWebhookSecret)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/v1/webhooks/clerk", nil))
		// Not the webhook: only POST is, and anything else under /v1/ is the guarded catch-all (no token, so 401).
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d", method, rec.Code)
		}
	}
}

func mustSigner(t *testing.T) *svix.Webhook {
	t.Helper()
	signer, err := svix.NewWebhook(testWebhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
