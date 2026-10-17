package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/store"
	svix "github.com/svix/svix-webhooks/go"
)

// MaxWebhookBody caps what the endpoint reads. Clerk's payloads are a few kilobytes.
const MaxWebhookBody = 1 << 20

// The nine Clerk events the endpoint handles (docs/06 section 3.2). The Clerk dashboard subscribes to exactly these.
const (
	EventUserCreated         = "user.created"
	EventUserUpdated         = "user.updated"
	EventUserDeleted         = "user.deleted"
	EventOrganizationCreated = "organization.created"
	EventOrganizationUpdated = "organization.updated"
	EventOrganizationDeleted = "organization.deleted"
	EventMembershipCreated   = "organizationMembership.created"
	EventMembershipUpdated   = "organizationMembership.updated"
	EventMembershipDeleted   = "organizationMembership.deleted"
)

// Error codes and statuses of the webhook endpoint.
const (
	CodeInvalidSignature = "invalid_signature" // 400
	CodeInvalidEvent     = "invalid_event"     // 400
	CodePayloadTooLarge  = "payload_too_large" // 413

	webhookStatusApplied   = "applied"
	webhookStatusDuplicate = "duplicate"
	webhookStatusIgnored   = "ignored"
)

// WebhookHandler is POST /v1/webhooks/clerk: it verifies the Svix signature and mirrors the event into Postgres.
//
// Idempotent. The svix-id of every applied delivery is recorded in webhook_events in the same transaction as the change,
// so a retry or a replay of a delivery changes nothing and is answered 200. Svix also rejects a signature older than five
// minutes, so a captured request cannot be replayed later at all.
//
// Order-safe. Deliveries can arrive out of order, so every write carries the Clerk object's updated_at and applies only
// when it is newer than the row (see package store). A user or workspace deleted in Clerk stays deleted. A membership
// delete is remembered in webhook_events, so a stale create or update of that membership that arrives later is ignored.
//
// It answers 2xx for everything it understood (applied, duplicate, or an event type it does not handle), 400 for a bad
// signature or an unusable payload, and 5xx when Postgres or Clerk's Backend API failed, which makes Svix retry.
// Nothing from a payload is logged but the event type, the delivery id and Clerk's ids.
type WebhookHandler struct {
	store    *store.Store
	dir      Directory
	verifier *svix.Webhook // nil when the endpoint is not configured
	logger   *slog.Logger
	notConf  sync.Once
}

// NewWebhookHandler returns the handler. secret is CLERK_WEBHOOK_SECRET; empty makes the endpoint answer 503
// webhook_not_configured. dir (may be nil) lets a membership event for a user we do not know yet fetch that user.
func NewWebhookHandler(st *store.Store, dir Directory, secret string, logger *slog.Logger) (*WebhookHandler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	h := &WebhookHandler{store: st, dir: dir, logger: logger}
	if secret != "" {
		v, err := svix.NewWebhook(secret)
		if err != nil {
			return nil, errors.New("auth: CLERK_WEBHOOK_SECRET is not a valid Svix signing secret") // never the value
		}
		h.verifier = v
	}
	return h, nil
}

// ServeHTTP implements http.Handler.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.verifier == nil {
		h.notConf.Do(func() {
			h.logger.WarnContext(ctx, "clerk webhook received but CLERK_WEBHOOK_SECRET is not set; answering 503 (logged once)")
		})
		httpapi.WriteError(w, http.StatusServiceUnavailable, CodeWebhookNotConfigured, "the webhook endpoint is not configured on this server")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpapi.WriteError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "the payload is too large")
			return
		}
		httpapi.WriteError(w, http.StatusBadRequest, CodeInvalidEvent, "the body could not be read")
		return
	}

	// The signature covers the exact bytes, so verify before parsing anything.
	if err := h.verifier.Verify(body, r.Header); err != nil {
		h.logger.WarnContext(ctx, "clerk webhook rejected: signature", "reason", err.Error())
		httpapi.WriteError(w, http.StatusBadRequest, CodeInvalidSignature, "the signature is not valid")
		return
	}
	svixID := r.Header.Get("svix-id")
	if svixID == "" {
		svixID = r.Header.Get("webhook-id") // the Standard Webhooks spelling, which Svix's verifier also accepts
	}

	evt, err := parseEvent(body)
	if err != nil {
		h.logger.WarnContext(ctx, "clerk webhook rejected: payload", "svix_id", svixID, "error", err)
		httpapi.WriteError(w, http.StatusBadRequest, CodeInvalidEvent, "the payload is not a Clerk event this server understands")
		return
	}
	if !handledEvent(evt.Type) {
		h.logger.InfoContext(ctx, "clerk webhook ignored", "svix_id", svixID, "type", evt.Type)
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": webhookStatusIgnored})
		return
	}

	applied := false
	err = h.store.InTx(ctx, func(tx *store.Store) error {
		first, err := tx.RecordWebhookEvent(ctx, store.WebhookEvent{SvixID: svixID, Type: evt.Type, Subject: evt.subject(), ClerkEventAt: evt.recordedAt()})
		if err != nil {
			return err
		}
		if !first {
			return nil // a replay: already applied by an earlier delivery
		}
		applied = true
		return h.apply(ctx, tx, evt)
	})
	switch {
	case errors.Is(err, ErrDirectoryUnavailable):
		h.logger.WarnContext(ctx, "clerk webhook not applied: clerk backend api unavailable; Svix will retry", "svix_id", svixID, "type", evt.Type, "error", err)
		httpapi.WriteError(w, http.StatusServiceUnavailable, CodeIdentityProvider, "try again")
	case err != nil:
		h.logger.ErrorContext(ctx, "clerk webhook failed; Svix will retry", "svix_id", svixID, "type", evt.Type, "error", err)
		httpapi.WriteError(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
	case !applied:
		h.logger.InfoContext(ctx, "clerk webhook replay", "svix_id", svixID, "type", evt.Type)
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": webhookStatusDuplicate})
	default:
		h.logger.InfoContext(ctx, "clerk webhook applied", "svix_id", svixID, "type", evt.Type)
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": webhookStatusApplied})
	}
}

func handledEvent(t string) bool {
	switch t {
	case EventUserCreated, EventUserUpdated, EventUserDeleted,
		EventOrganizationCreated, EventOrganizationUpdated, EventOrganizationDeleted,
		EventMembershipCreated, EventMembershipUpdated, EventMembershipDeleted:
		return true
	}
	return false
}

// errInvalidEvent marks a payload that is signed but lacks what the event needs. Wrapped with detail that never holds payload values.
var errInvalidEvent = errors.New("invalid event")

// event is a Clerk event envelope with the data decoded for its type.
type event struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"` // when Clerk emitted the event, Unix milliseconds
	Data      json.RawMessage `json:"data"`

	user       clerkUserData
	org        clerkOrgData
	membership clerkMembershipData
}

type clerkUserData struct {
	ID                    string `json:"id"`
	PrimaryEmailAddressID string `json:"primary_email_address_id"`
	EmailAddresses        []struct {
		ID           string `json:"id"`
		EmailAddress string `json:"email_address"`
	} `json:"email_addresses"`
	UpdatedAt int64 `json:"updated_at"`
}

type clerkOrgData struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt int64  `json:"updated_at"`
}

type clerkMembershipData struct {
	Role         string       `json:"role"`
	UpdatedAt    int64        `json:"updated_at"`
	Organization clerkOrgData `json:"organization"`
	UserData     struct {
		UserID string `json:"user_id"`
	} `json:"public_user_data"`
}

func parseEvent(body []byte) (*event, error) {
	var e event
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("%w: not JSON", errInvalidEvent)
	}
	if e.Type == "" {
		return nil, fmt.Errorf("%w: no type", errInvalidEvent)
	}
	if !handledEvent(e.Type) {
		return &e, nil
	}
	var err error
	switch e.Type {
	case EventUserCreated, EventUserUpdated, EventUserDeleted:
		if err = json.Unmarshal(e.Data, &e.user); err == nil && e.user.ID == "" {
			err = errors.New("no user id")
		}
	case EventOrganizationCreated, EventOrganizationUpdated, EventOrganizationDeleted:
		if err = json.Unmarshal(e.Data, &e.org); err == nil && e.org.ID == "" {
			err = errors.New("no organization id")
		}
	default:
		if err = json.Unmarshal(e.Data, &e.membership); err == nil && (e.membership.Organization.ID == "" || e.membership.UserData.UserID == "") {
			err = errors.New("no organization or user id")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s data: %w", errInvalidEvent, e.Type, err)
	}
	// A create or update without a version cannot be ordered, so it cannot be applied safely.
	if !isDelete(e.Type) && e.version() == 0 {
		return nil, fmt.Errorf("%w: %s has no updated_at or timestamp", errInvalidEvent, e.Type)
	}
	return &e, nil
}

func isDelete(t string) bool {
	return t == EventUserDeleted || t == EventOrganizationDeleted || t == EventMembershipDeleted
}

// version is the event's time for ordering: the object's updated_at, else the envelope timestamp.
func (e *event) version() int64 {
	var v int64
	switch e.Type {
	case EventUserCreated, EventUserUpdated, EventUserDeleted:
		v = e.user.UpdatedAt
	case EventOrganizationCreated, EventOrganizationUpdated, EventOrganizationDeleted:
		v = e.org.UpdatedAt
	default:
		v = e.membership.UpdatedAt
	}
	if v == 0 {
		v = e.Timestamp
	}
	return v
}

// recordedAt is the time stored with the delivery. For a membership delete it is the later of the object's last update
// and the moment Clerk emitted the event, so the tombstone covers every create or update that happened before the delete.
func (e *event) recordedAt() int64 {
	v := e.version()
	if e.Type == EventMembershipDeleted && e.Timestamp > v {
		v = e.Timestamp
	}
	return v
}

// subject is "<clerk org id>/<clerk user id>" for membership events.
func (e *event) subject() string {
	switch e.Type {
	case EventMembershipCreated, EventMembershipUpdated, EventMembershipDeleted:
		return membershipSubject(e.membership.Organization.ID, e.membership.UserData.UserID)
	}
	return ""
}

func membershipSubject(clerkOrgID, clerkUserID string) string { return clerkOrgID + "/" + clerkUserID }

// apply runs inside the transaction that recorded the delivery.
func (h *WebhookHandler) apply(ctx context.Context, tx *store.Store, e *event) error {
	switch e.Type {
	case EventUserCreated, EventUserUpdated:
		var addrs []emailAddress
		for _, a := range e.user.EmailAddresses {
			addrs = append(addrs, emailAddress{ID: a.ID, Address: a.EmailAddress})
		}
		_, _, err := tx.SyncUser(ctx, store.SyncUserInput{ClerkUserID: e.user.ID, Email: primaryEmail(e.user.PrimaryEmailAddressID, addrs), ClerkUpdatedAt: e.version()})
		return err

	case EventUserDeleted:
		_, err := tx.DeleteUser(ctx, e.user.ID)
		return err

	case EventOrganizationCreated, EventOrganizationUpdated:
		_, _, err := tx.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: e.org.ID, Name: e.org.Name, ClerkUpdatedAt: e.version()})
		return err

	case EventOrganizationDeleted:
		_, err := tx.DeleteWorkspace(ctx, e.org.ID)
		return err

	case EventMembershipCreated, EventMembershipUpdated:
		return h.applyMembership(ctx, tx, e)

	case EventMembershipDeleted:
		w, err := tx.GetWorkspaceByClerkOrgID(ctx, e.membership.Organization.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // nothing was ever synced for it, so there is nothing to remove
		}
		if err != nil {
			return err
		}
		u, err := tx.GetUserByClerkID(ctx, e.membership.UserData.UserID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.DeleteMember(ctx, w.ID, u.ID)
	}
	return nil
}

func (h *WebhookHandler) applyMembership(ctx context.Context, tx *store.Store, e *event) error {
	orgID, userID := e.membership.Organization.ID, e.membership.UserData.UserID

	// A membership that was deleted at or after this event's time is gone: this event is the stale one.
	stale, err := tx.MembershipDeletedSince(ctx, e.subject(), e.version())
	if err != nil {
		return err
	}
	if stale {
		return nil
	}

	// The membership event may come before the user's or the organization's own event (Svix does not order across
	// event types). The payload names the organization; the user's email is not in it, so a missing user is fetched.
	var w store.Workspace
	if o := e.membership.Organization; o.Name != "" {
		ov := o.UpdatedAt
		if ov == 0 {
			ov = e.version()
		}
		if w, _, err = tx.SyncWorkspace(ctx, store.SyncWorkspaceInput{ClerkOrgID: orgID, Name: o.Name, ClerkUpdatedAt: ov}); err != nil {
			return err
		}
	} else if w, err = ensureWorkspace(ctx, tx, h.dir, orgID); err != nil {
		return ignoreDeleted(err)
	}
	u, err := ensureUser(ctx, tx, h.dir, userID)
	if err != nil {
		return ignoreDeleted(err)
	}
	if u.DeletedAt != nil || w.DeletedAt != nil {
		return nil // a deleted user or workspace has no memberships
	}
	_, _, err = tx.SyncMember(ctx, w.ID, u.ID, MapRole(e.membership.Role), e.version())
	return err
}

// ignoreDeleted turns "Clerk no longer has it" into success: the event is about something that is gone.
func ignoreDeleted(err error) error {
	if errors.Is(err, ErrUserDeleted) || errors.Is(err, ErrWorkspaceDeleted) {
		return nil
	}
	return err
}
