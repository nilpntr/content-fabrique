package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// plansSuite is part of repositorySuite, which TestRepository runs as the superuser and TestRoleSplit runs again as
// the app role: so every plan query is proven to work with DML rights only, and no grant needs DDL.
func plansSuite(t *testing.T, st *store.Store, suffix string) {
	ctx := context.Background()
	newWorkspace := func(name string) store.Workspace {
		t.Helper()
		w, err := st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_plan_" + name + "_" + suffix, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	ent := func(w store.Workspace) store.Entitlements {
		t.Helper()
		e, err := st.GetEntitlements(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)

	t.Run("a new workspace has no plan", func(t *testing.T) {
		w := newWorkspace("fresh")
		e := ent(w)
		if e.Active || e.Plan != plans.None || e.Status != store.StatusNone || e.CurrentPeriodEnd != nil || e.WorkspaceID != w.ID {
			t.Fatalf("entitlements = %+v", e)
		}
	})

	t.Run("grant, replace and revoke", func(t *testing.T) {
		w := newWorkspace("granted")
		e, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, GrantedBy: "cfctl:test"})
		if err != nil {
			t.Fatal(err)
		}
		if !e.Active || e.Plan != plans.Creator || e.Status != store.StatusActive || e.Source != store.SourceManual ||
			e.CurrentPeriodEnd != nil || e.GrantedBy != "cfctl:test" {
			t.Fatalf("after grant = %+v", e)
		}
		// A second grant replaces the first, with an end date.
		e, err = st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Pro, PeriodEnd: &future})
		if err != nil || !e.Active || e.Plan != plans.Pro || e.CurrentPeriodEnd == nil || !e.CurrentPeriodEnd.Equal(future.Truncate(time.Microsecond)) {
			t.Fatalf("after second grant = %+v, %v", e, err)
		}
		e, err = st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: w.ID, GrantedBy: "cfctl:test"})
		if err != nil || e.Active || e.Plan != plans.None || e.Status != store.StatusCanceled || e.CurrentPeriodEnd != nil {
			t.Fatalf("after revoke = %+v, %v", e, err)
		}
		// Revoking again is fine, and so is revoking a workspace that never had a plan.
		if e, err = st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: w.ID, GrantedBy: ""}); err != nil || e.Active {
			t.Fatalf("second revoke = %+v, %v", e, err)
		}
		if e, err = st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: newWorkspace("never").ID, GrantedBy: ""}); err != nil || e.Active {
			t.Fatalf("revoke without a plan = %+v, %v", e, err)
		}
		// And a grant after a revoke brings it back.
		if e, err = st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Studio}); err != nil || !e.Active || e.Plan != plans.Studio {
			t.Fatalf("grant after revoke = %+v, %v", e, err)
		}
	})

	t.Run("a period that ended is not active", func(t *testing.T) {
		w := newWorkspace("expired")
		e, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, PeriodEnd: &past})
		if err != nil {
			t.Fatal(err)
		}
		// The record says what was granted; "active" is what the paywall looks at.
		if e.Active || e.Plan != plans.Creator || e.Status != store.StatusActive || e.CurrentPeriodEnd == nil {
			t.Fatalf("expired grant = %+v", e)
		}
		// The same row becomes active when it is extended, with no other change.
		if e, err = st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, PeriodEnd: &future}); err != nil || !e.Active {
			t.Fatalf("extended grant = %+v, %v", e, err)
		}
		// A period that ends in a moment is active until it does: the database clock decides, at read time.
		soon := time.Now().Add(800 * time.Millisecond)
		if e, err = st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, PeriodEnd: &soon}); err != nil || !e.Active {
			t.Fatalf("grant ending soon = %+v, %v", e, err)
		}
		time.Sleep(time.Second)
		if e = ent(w); e.Active {
			t.Fatalf("the grant is still active after its end: %+v", e)
		}
	})

	t.Run("only a granted plan with an active status counts", func(t *testing.T) {
		w := newWorkspace("status")
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Trial}); err != nil {
			t.Fatal(err)
		}
		// past_due and canceled are written by the merchant of record's webhooks later; a rejected status never reaches here.
		for _, tc := range []struct {
			status string
			active bool
		}{{"past_due", false}, {"canceled", false}, {"active", true}} {
			setPlanStatus(t, st, w, tc.status)
			if got := ent(w); got.Active != tc.active || got.Status != store.PlanStatus(tc.status) {
				t.Fatalf("status %s: %+v", tc.status, got)
			}
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		w := newWorkspace("invalid")
		for name, in := range map[string]store.GrantInput{
			"none":         {WorkspaceID: w.ID, Plan: plans.None},
			"unknown plan": {WorkspaceID: w.ID, Plan: "platinum"},
			"empty plan":   {WorkspaceID: w.ID},
			"unknown src":  {WorkspaceID: w.ID, Plan: plans.Pro, Source: "barter"},
			"no workspace": {Plan: plans.Pro},
			"ghost":        {WorkspaceID: ghostWorkspaceID(t), Plan: plans.Pro},
		} {
			_, err := st.GrantPlan(ctx, in)
			if !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrInvalid or ErrNotFound", name, err)
			}
		}
		if e := ent(w); e.Active {
			t.Fatalf("a refused grant activated the workspace: %+v", e)
		}
		if _, err := st.GetEntitlements(ctx, ghostWorkspaceID(t)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("entitlements of a missing workspace: err = %v", err)
		}
	})

	// The tenancy test: a grant is keyed by the workspace, so one workspace's grant never unlocks another, also for a
	// person who is a member of both.
	t.Run("a grant never unlocks another workspace", func(t *testing.T) {
		a, b := newWorkspace("tenant-a"), newWorkspace("tenant-b")
		ada, err := st.UpsertUserByClerkID(ctx, "user_both_"+suffix, "both-"+suffix+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		bob, err := st.UpsertUserByClerkID(ctx, "user_bob_plan_"+suffix, "bob-plan-"+suffix+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []struct {
			w store.Workspace
			u store.User
		}{{a, ada}, {b, ada}, {b, bob}} {
			if _, err := st.AddMember(ctx, m.w.ID, m.u.ID, store.RoleOwner); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: a.ID, Plan: plans.Pro}); err != nil {
			t.Fatal(err)
		}
		if !ent(a).Active {
			t.Fatal("the granted workspace is not active")
		}
		if e := ent(b); e.Active || e.Plan != plans.None {
			t.Fatalf("workspace B was unlocked by A's grant: %+v", e)
		}
		// Revoking B (which has nothing) leaves A alone, and revoking A leaves B alone.
		if _, err := st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: b.ID, GrantedBy: ""}); err != nil {
			t.Fatal(err)
		}
		if !ent(a).Active {
			t.Fatal("revoking B took A's plan away")
		}
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: b.ID, Plan: plans.Studio}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: a.ID, GrantedBy: ""}); err != nil {
			t.Fatal(err)
		}
		if ent(a).Active || !ent(b).Active || ent(b).Plan != plans.Studio {
			t.Fatalf("after revoking A: A = %+v, B = %+v", ent(a), ent(b))
		}

		// The person who is in both workspaces is found in both; a person in only one is found in that one.
		refs := workspacesOf(t, st, "BOTH-"+suffix+"@EXAMPLE.com") // any case
		if len(refs) != 2 {
			t.Fatalf("workspaces of ada = %+v", refs)
		}
		got := []string{refs[0].ID.String(), refs[1].ID.String()}
		if !slices.Contains(got, a.ID.String()) || !slices.Contains(got, b.ID.String()) {
			t.Fatalf("ids = %v", got)
		}
		refs = workspacesOf(t, st, "user_bob_plan_"+suffix) // by Clerk user id
		if len(refs) != 1 || refs[0].ID != b.ID || refs[0].Name != "tenant-b" || refs[0].ClerkOrgID != "org_plan_tenant-b_"+suffix ||
			refs[0].Role != store.RoleOwner || refs[0].Members != 2 || refs[0].CreatedAt.IsZero() {
			t.Fatalf("workspaces of bob = %+v", refs)
		}
	})

	t.Run("finding a person", func(t *testing.T) {
		if people, err := st.FindPeople(ctx, "nobody-"+suffix+"@example.com"); err != nil || len(people) != 0 {
			t.Fatalf("unknown person = %+v, %v", people, err)
		}
		if _, err := st.UpsertUserByClerkID(ctx, "user_lonely_"+suffix, "lonely-"+suffix+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if refs := workspacesOf(t, st, "lonely-"+suffix+"@example.com"); len(refs) != 0 {
			t.Fatalf("person in no workspace = %+v", refs)
		}
		if _, err := st.FindPeople(ctx, "  "); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("blank person: err = %v", err)
		}
		// A deleted workspace is not offered.
		w := newWorkspace("deleted")
		u, _ := st.UpsertUserByClerkID(ctx, "user_gone_"+suffix, "gone-"+suffix+"@example.com")
		if _, err := st.AddMember(ctx, w.ID, u.ID, store.RoleOwner); err != nil {
			t.Fatal(err)
		}
		if refs := workspacesOf(t, st, u.Email); len(refs) != 1 {
			t.Fatalf("before delete: %+v", refs)
		}
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Pro}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DeleteWorkspace(ctx, w.ClerkOrgID); err != nil {
			t.Fatal(err)
		}
		if refs := workspacesOf(t, st, u.Email); len(refs) != 0 {
			t.Fatalf("after delete: %+v", refs)
		}
		// A deleted workspace has no entitlements to read, whatever it was granted before.
		if _, err := st.GetEntitlements(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("entitlements of a deleted workspace: err = %v", err)
		}
	})

	// An e-mail address is not unique: two accounts can share it, and then nothing may pick one of them for you.
	t.Run("an e-mail address shared by several users", func(t *testing.T) {
		shared := "shared-" + suffix + "@example.com"
		one, err := st.UpsertUserByClerkID(ctx, "user_shared_one_"+suffix, shared)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertUserByClerkID(ctx, "user_shared_two_"+suffix, strings.ToUpper(shared)); err != nil {
			t.Fatal(err)
		}
		people, err := st.FindPeople(ctx, shared)
		if err != nil || len(people) != 2 {
			t.Fatalf("people = %+v, %v", people, err)
		}
		// The Clerk user id picks exactly one.
		people, err = st.FindPeople(ctx, one.ClerkUserID)
		if err != nil || len(people) != 1 || people[0].ID != one.ID || people[0].ClerkUserID != one.ClerkUserID {
			t.Fatalf("by Clerk user id = %+v, %v", people, err)
		}
	})

	t.Run("a plan the merchant of record manages is not overwritten by hand", func(t *testing.T) {
		w := newWorkspace("merchant")
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Pro, Source: store.SourceMerchant}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Trial}); !errors.Is(err, store.ErrPlanSourceConflict) {
			t.Fatalf("grant over a merchant plan: err = %v", err)
		}
		if _, err := st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: w.ID}); !errors.Is(err, store.ErrPlanSourceConflict) {
			t.Fatalf("revoke of a merchant plan: err = %v", err)
		}
		if e := ent(w); !e.Active || e.Plan != plans.Pro || e.Source != store.SourceMerchant {
			t.Fatalf("the refused commands changed the plan: %+v", e)
		}
		// Forced, the hand edit wins and the record is manual from then on.
		e, err := st.RevokePlan(ctx, store.RevokeInput{WorkspaceID: w.ID, Force: true})
		if err != nil || e.Active || e.Source != store.SourceManual {
			t.Fatalf("forced revoke = %+v, %v", e, err)
		}
		// A manual record is replaced without --force, and so is a workspace with no record.
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Studio}); err != nil {
			t.Fatalf("grant over a manual plan: %v", err)
		}
	})

	t.Run("a workspace with no record reports source none", func(t *testing.T) {
		if e := ent(newWorkspace("nosource")); e.Source != store.SourceNone {
			t.Fatalf("source = %q", e.Source)
		}
	})
}

func workspacesOf(t *testing.T, st *store.Store, person string) []store.WorkspaceRef {
	t.Helper()
	people, err := st.FindPeople(context.Background(), person)
	if err != nil || len(people) != 1 {
		t.Fatalf("FindPeople(%q) = %+v, %v; want exactly one", person, people, err)
	}
	refs, err := st.WorkspacesOfUser(context.Background(), people[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

// setPlanStatus writes a status the way the merchant of record's webhooks will (P7-01), through the same pool.
func setPlanStatus(t *testing.T, st *store.Store, w store.Workspace, status string) {
	t.Helper()
	if _, err := st.Pool().Exec(context.Background(), `UPDATE workspace_plans SET status = $2 WHERE workspace_id = $1`, w.ID, status); err != nil {
		t.Fatal(err)
	}
}
