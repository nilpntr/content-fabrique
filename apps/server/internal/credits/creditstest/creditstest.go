// Package creditstest gives tests the credits an editor run needs: the real price list over the store under test, and a workspace with a
// plan and a balance. Tests that start editor runs use it instead of switching credits off (an editor run is never started without a
// hold, so there is nothing to switch off).
package creditstest

import (
	"context"
	"testing"
	"time"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Service returns the credits service over st with the default price list.
func Service(t testing.TB, st *store.Store) *credits.Service {
	t.Helper()
	list, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	return credits.NewService(st, list)
}

// ServiceWith is Service over a price list that edit has changed first: a test that needs a particular budget (the editor's cassettes were
// recorded with a $5.00 run, and the tool answers carry the budget that is left, so the budget is part of every cassette's key) sets it
// here. The profit invariant is not checked on the changed list; the real list is, by the credits package's tests.
func ServiceWith(t testing.TB, st *store.Store, edit func(*credits.PriceList)) *credits.Service {
	t.Helper()
	list, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	edit(list)
	return credits.NewService(st, list)
}

// Fund makes sure the workspace has an active plan (it grants plan when it has none, by hand, without an end) and adds a top-up of n
// credits that expires in a year. It writes through the same store functions as cfctl credits grant.
func Fund(t testing.TB, st *store.Store, ws ids.WorkspaceID, plan plans.Plan, n int) {
	t.Helper()
	ctx := context.Background()
	if ent, err := st.GetEntitlements(ctx, ws); err != nil {
		t.Fatal(err)
	} else if !ent.Active {
		if _, err := st.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws, Plan: plan, GrantedBy: "creditstest"}); err != nil {
			t.Fatal(err)
		}
	}
	if n <= 0 {
		return
	}
	now := time.Now()
	if _, _, err := st.GrantTopUp(ctx, store.GrantTopUpParams{
		Workspace: ws, Credits: n, ExpiresAt: now.AddDate(1, 0, 0), Actor: "creditstest", Reason: "test funding", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}
