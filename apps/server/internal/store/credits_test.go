package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nilpntr/content-fabrique/internal/credits"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/plans"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The credits tests (P7-01a) run on the spend rig: a database migrated by the migrate role and used through the app login (so the
// append-only grants are the real ones), with two pools standing for two processes.

var creditDay = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func newUUID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func (r spendRig) topUp(t *testing.T, st *store.Store, ws store.Workspace, n int, expires time.Time, now time.Time) uuid.UUID {
	t.Helper()
	id, created, err := st.GrantTopUp(context.Background(), store.GrantTopUpParams{Workspace: ws.ID, Credits: n, ExpiresAt: expires, Actor: "test", Reason: "test", Now: now})
	if err != nil || !created {
		t.Fatalf("top-up of %d: %v %v", n, created, err)
	}
	return id
}

func hold(ws store.Workspace, action string, id uuid.UUID, n int, now time.Time) store.HoldCreditsParams {
	return store.HoldCreditsParams{Workspace: ws.ID, Action: action, ActionID: id, Credits: n, Plan: "creator", ListVersion: "2026-10", Now: now, TTL: time.Hour}
}

func available(t *testing.T, st *store.Store, ws store.Workspace, now time.Time) int {
	t.Helper()
	b, err := st.CreditBalance(context.Background(), ws.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return b.Available
}

func isInsufficient(err error) bool { return errors.Is(err, store.ErrInsufficientCredits) }

// superConn connects as the container's superuser, to look at the tables the way an operator would.
func (r spendRig) superConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), r.srv.SuperDSN(r.database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// checkLedger asserts the invariants of the credit ledger of a workspace: no source has gone below zero, every row that names a source is
// in the same workspace, and charges, refunds and expiries only name sources.
func (r spendRig) checkLedger(t *testing.T, ws store.Workspace) {
	t.Helper()
	conn := r.superConn(t)
	rows, err := conn.Query(context.Background(), `
SELECT s.id, s.credits + COALESCE(sum(c.credits), 0)
FROM credit_ledger s LEFT JOIN credit_ledger c ON c.grant_id = s.id
WHERE s.workspace_id = $1 AND s.credits > 0 AND s.kind IN ('grant_allowance', 'grant_topup', 'adjust')
GROUP BY s.id, s.credits`, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var left int
		if err := rows.Scan(&id, &left); err != nil {
			t.Fatal(err)
		}
		if left < 0 {
			t.Errorf("source %s has %d credits left", id, left)
		}
	}
	var cross int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM credit_ledger c JOIN credit_ledger s ON s.id = c.grant_id WHERE s.workspace_id <> c.workspace_id`).Scan(&cross); err != nil || cross != 0 {
		t.Errorf("%d rows name a source of another workspace (%v)", cross, err)
	}
}

// Two pools contend for the last credits: exactly one hold wins, round after round.
func TestCreditsRaceForTheLastCredits(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	const rounds = 60
	for round := range rounds {
		ws := rig.workspace(t, fmt.Sprintf("race%d", round))
		rig.topUp(t, rig.a, ws, 6, creditDay.AddDate(0, 1, 0), creditDay)

		var wins, refused atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, st := range []*store.Store{rig.a, rig.b} {
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := st.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 6, creditDay))
					switch {
					case err == nil:
						wins.Add(1)
					case isInsufficient(err):
						refused.Add(1)
					default:
						t.Errorf("round %d: %v", round, err)
					}
				}()
			}
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 || refused.Load() != 7 {
			t.Fatalf("round %d: %d holds won and %d were refused, want exactly 1 and 7", round, wins.Load(), refused.Load())
		}
		if got := available(t, rig.b, ws, creditDay); got != 0 {
			t.Fatalf("round %d: %d credits available next to the winner's hold", round, got)
		}
	}
}

// With room for several holds, no more than fit are granted however the two pools interleave.
func TestCreditsRaceNeverOverbooks(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	for round := range 30 {
		ws := rig.workspace(t, fmt.Sprintf("book%d", round))
		rig.topUp(t, rig.a, ws, 13, creditDay.AddDate(0, 1, 0), creditDay)
		var wins atomic.Int64
		var wg sync.WaitGroup
		for _, st := range []*store.Store{rig.a, rig.b} {
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := st.HoldCredits(ctx, hold(ws, store.ActionEditRevision, newUUID(), 2, creditDay)); err == nil {
						wins.Add(1)
					} else if !isInsufficient(err) {
						t.Errorf("round %d: %v", round, err)
					}
				}()
			}
		}
		wg.Wait()
		if wins.Load() != 6 { // 13 credits hold six holds of 2
			t.Fatalf("round %d: %d holds won, want 6", round, wins.Load())
		}
		if got := available(t, rig.a, ws, creditDay); got != 1 {
			t.Fatalf("round %d: %d available, want 1", round, got)
		}
	}
}

// Settles and holds racing for the same credits: nothing goes below zero and what is charged is exactly what the settles took.
func TestCreditsRaceHoldsAndSettles(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	for round := range 20 {
		ws := rig.workspace(t, fmt.Sprintf("mix%d", round))
		rig.topUp(t, rig.a, ws, 12, creditDay.AddDate(0, 1, 0), creditDay)
		first := make([]uuid.UUID, 4)
		for i := range first {
			first[i] = newUUID()
			if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, first[i], 3, creditDay)); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		var newHolds atomic.Int64
		for i, id := range first {
			st := []*store.Store{rig.a, rig.b}[i%2]
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, err := st.SettleCredits(ctx, ws.ID, id, i < 2, creditDay); err != nil { // two charged, two released
					t.Errorf("settle: %v", err)
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := st.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 3, creditDay)); err == nil {
					newHolds.Add(1)
				} else if !isInsufficient(err) {
					t.Errorf("hold: %v", err)
				}
			}()
		}
		wg.Wait()
		// 12 credits: 6 charged, 6 free once the two released holds are gone. The new holds may only have used what was free, so at most
		// 2 won at the end (and at least 0, if they all ran before the releases).
		if n := newHolds.Load(); n > 2 {
			t.Fatalf("round %d: %d new holds won, at most 2 fit", round, n)
		}
		if got := available(t, rig.a, ws, creditDay); got != 12-6-3*int(newHolds.Load()) {
			t.Fatalf("round %d: %d available, %d new holds", round, got, newHolds.Load())
		}
		rig.checkLedger(t, ws)
	}
}

func TestCreditsHoldSettleAndReplay(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "settle")
	rig.topUp(t, rig.a, ws, 10, creditDay.AddDate(0, 1, 0), creditDay)
	id := newUUID()
	project := ids.ProjectID{UUID: newUUID()}
	member := ids.UserID{UUID: newUUID()}
	p := hold(ws, store.ActionEditFirst, id, 6, creditDay)
	p.Project, p.Member = project, member
	h, err := rig.a.HoldCredits(ctx, p)
	if err != nil || h.Credits != 6 || h.Project != project || h.Member != member || h.Plan != "creator" {
		t.Fatalf("hold = %+v, %v", h, err)
	}
	// The same action asks again: it finds its hold and sets nothing more aside.
	if again, err := rig.b.HoldCredits(ctx, p); err != nil || again.ID != h.ID {
		t.Fatalf("a repeated hold = %+v, %v", again, err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 4 {
		t.Fatalf("available with one hold = %d, want 4", got)
	}
	// A second action cannot have more than is left.
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 5, creditDay)); !isInsufficient(err) {
		t.Fatalf("5 of 4: %v", err)
	}
	var ins *store.InsufficientCreditsError
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 5, creditDay)); !errors.As(err, &ins) || ins.Needed != 5 || ins.Available != 4 {
		t.Fatalf("the error says %+v", ins)
	}

	// Settling twice charges once.
	for i := range 2 {
		res, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (!res.Held || res.Charged != 6) || i == 1 && (res.Held || res.Charged != 0) {
			t.Fatalf("settle %d = %+v", i, res)
		}
	}
	if got := available(t, rig.a, ws, creditDay); got != 4 {
		t.Fatalf("available after the charge = %d, want 4", got)
	}
	rows, err := rig.a.CreditLedger(ctx, ws.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var charges []store.CreditLedgerRow
	for _, r := range rows {
		if r.Kind == store.CreditCharge {
			charges = append(charges, r)
		}
	}
	if len(charges) != 1 || charges[0].Credits != -6 || *charges[0].Action != store.ActionEditFirst || charges[0].ActionID.Bytes != id ||
		charges[0].ProjectID.Bytes != project.UUID || charges[0].MemberUserID.Bytes != member.UUID || *charges[0].Plan != "creator" || *charges[0].PlanListVersion != "2026-10" {
		t.Fatalf("charges = %+v", charges)
	}
	// A hold made after the settle for the same action is a new question, but the charge stays: settling again charges nothing.
	if ok, err := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); err != nil || !ok {
		t.Fatalf("charged edit = %v, %v", ok, err)
	}
	rig.checkLedger(t, ws)
}

func TestCreditsReleaseKeepsTheCredits(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "release")
	rig.topUp(t, rig.a, ws, 6, creditDay.AddDate(0, 1, 0), creditDay)
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, id, 6, creditDay)); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.ReleaseCredits(ctx, ws.ID, id, creditDay)
	if err != nil || !res.Released || res.Charged != 0 {
		t.Fatalf("release = %+v, %v", res, err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 6 {
		t.Fatalf("available after the release = %d, want 6", got)
	}
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	for _, r := range rows {
		if r.Kind == store.CreditCharge {
			t.Fatalf("a release wrote a charge: %+v", r)
		}
	}
	// A release of a hold that is gone is fine.
	if res, err := rig.a.ReleaseCredits(ctx, ws.ID, id, creditDay); err != nil || res.Held {
		t.Fatalf("release again = %+v, %v", res, err)
	}
}

// A hold whose run has gone stops counting when it expires, and the sweep deletes it.
func TestCreditsExpiredHoldIsSwept(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "sweep")
	rig.topUp(t, rig.a, ws, 6, creditDay.AddDate(0, 1, 0), creditDay)
	crashed, live := newUUID(), newUUID()
	if _, err := rig.a.HoldCredits(ctx, store.HoldCreditsParams{Workspace: ws.ID, Action: store.ActionEditFirst, ActionID: crashed, Credits: 4, Now: creditDay, TTL: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, creditDay.Add(5*time.Minute)); got != 2 {
		t.Fatalf("available during the hold = %d, want 2", got)
	}
	later := creditDay.Add(time.Hour)
	// Past its expiry the hold no longer counts, before any sweep has run.
	if got := available(t, rig.a, ws, later); got != 6 {
		t.Fatalf("available after the hold expired = %d, want 6", got)
	}
	if _, err := rig.b.HoldCredits(ctx, store.HoldCreditsParams{Workspace: ws.ID, Action: store.ActionEditRevision, ActionID: live, Credits: 2, Now: later, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SweepExpiredCreditHolds(ctx, later)
	if err != nil || res.Holds != 1 || res.Credits != 4 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if _, err := rig.a.GetCreditHold(ctx, ws.ID, crashed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the swept hold: %v", err)
	}
	if _, err := rig.a.GetCreditHold(ctx, ws.ID, live); err != nil {
		t.Fatalf("the live hold: %v", err)
	}
	// Nothing was charged for the crashed run.
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	for _, r := range rows {
		if r.Kind == store.CreditCharge {
			t.Fatalf("a swept hold was charged: %+v", r)
		}
	}
	if res, err := rig.a.SweepExpiredCreditHolds(ctx, later); err != nil || res.Holds != 0 {
		t.Fatalf("a second sweep = %+v, %v", res, err)
	}
}

func TestCreditsRenewHoldFollowsTheHeartbeat(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "renew")
	rig.topUp(t, rig.a, ws, 6, creditDay.AddDate(0, 1, 0), creditDay)
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, store.HoldCreditsParams{Workspace: ws.ID, Action: store.ActionEditFirst, ActionID: id, Credits: 6, Now: creditDay, TTL: store.CreditHoldQueuedTTL}); err != nil {
		t.Fatal(err)
	}
	expiry := func() time.Time {
		h, err := rig.a.GetCreditHold(ctx, ws.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return h.ExpiresAt
	}
	if got := expiry(); !got.Equal(creditDay.Add(2 * time.Hour)) {
		t.Fatalf("expiry %v", got)
	}
	// A beat with plenty left changes nothing; a beat near the end moves it forward; it never moves back.
	if err := rig.a.RenewCreditHold(ctx, ws.ID, id, creditDay.Add(time.Minute)); err != nil || !expiry().Equal(creditDay.Add(2*time.Hour)) {
		t.Fatalf("early beat: %v %v", expiry(), err)
	}
	late := creditDay.Add(110 * time.Minute)
	if err := rig.a.RenewCreditHold(ctx, ws.ID, id, late); err != nil || !expiry().Equal(late.Add(store.CreditHoldRunTTL)) {
		t.Fatalf("late beat: %v %v", expiry(), err)
	}
	if err := rig.a.RenewCreditHold(ctx, ws.ID, id, creditDay); err != nil || !expiry().Equal(late.Add(store.CreditHoldRunTTL)) {
		t.Fatalf("a beat from the past moved the expiry back: %v %v", expiry(), err)
	}
	// A hold that is gone is not brought back.
	if _, err := rig.a.ReleaseCredits(ctx, ws.ID, id, late); err != nil {
		t.Fatal(err)
	}
	if err := rig.a.RenewCreditHold(ctx, ws.ID, id, late); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.GetCreditHold(ctx, ws.ID, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the hold came back: %v", err)
	}
}

// The allowance is used first, then the top-ups, earliest expiry first, and each source's expiry takes exactly what is left of it.
func TestCreditsOrderOfUseAndExactExpiry(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	ws := rig.workspace(t, "order")
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SyncAllowance(ctx, ws.ID, list.AllowanceSpecs(), creditDay)
	if err != nil || res.Granted != 150 {
		t.Fatalf("allowance = %+v, %v", res, err)
	}
	late := rig.topUp(t, rig.a, ws, 10, creditDay.AddDate(0, 0, 10), creditDay) // expires later, granted first
	soon := rig.topUp(t, rig.a, ws, 10, creditDay.AddDate(0, 0, 5), creditDay)

	// 155 credits: the 150 of the allowance, then 5 of the top-up that expires first.
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, id, 155, creditDay)); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay); err != nil {
		t.Fatal(err)
	}
	b, err := rig.a.CreditBalance(ctx, ws.ID, creditDay)
	if err != nil || b.Available != 15 || b.AllowanceRemaining != 0 || b.Allowance != 150 {
		t.Fatalf("balance = %+v, %v", b, err)
	}
	if len(b.TopUps) != 2 || b.TopUps[0].Credits != 5 || b.TopUps[1].Credits != 10 {
		t.Fatalf("top-ups = %+v, want 5 (expires first) then 10", b.TopUps)
	}
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	took := map[uuid.UUID]int32{}
	for _, r := range rows {
		if r.Kind == store.CreditCharge {
			took[r.GrantID.Bytes] += r.Credits
		}
	}
	if took[soon] != -5 || took[late] != 0 || len(took) != 2 {
		t.Fatalf("the charge took %v; the soon-expiring top-up is %s", took, soon)
	}

	// Six days on, the top-up that expired takes exactly the 5 that were left, and the later one is untouched.
	n, left, err := rig.a.ExpireDueCredits(ctx, creditDay.AddDate(0, 0, 6), 100)
	if err != nil || n != 1 || left != 5 {
		t.Fatalf("expiry = %d sources %d credits, %v; want 1 and 5", n, left, err)
	}
	if got := available(t, rig.a, ws, creditDay.AddDate(0, 0, 6)); got != 10 {
		t.Fatalf("available after the expiry = %d, want 10", got)
	}
	// Again: nothing more to expire.
	if n, _, err := rig.a.ExpireDueCredits(ctx, creditDay.AddDate(0, 0, 6), 100); err != nil || n != 0 {
		t.Fatalf("second expiry = %d, %v", n, err)
	}
	rig.checkLedger(t, ws)
}

// The period rolls over once however many workers notice, and unused allowance expires exactly.
func TestCreditsPeriodRolloverGrantsOnceAndExpiresExactly(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, err := credits.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "rollover")
	other := rig.workspace(t, "rollover2")
	for _, w := range []store.Workspace{ws, other} {
		if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := rig.b.SyncAllowance(ctx, other.ID, specs, creditDay); err != nil || res.Granted != 150 {
		t.Fatalf("the other workspace: %+v, %v", res, err)
	}
	// The first grant, from six callers on two pools at once: one allowance.
	var wg sync.WaitGroup
	var granted atomic.Int64
	for _, st := range []*store.Store{rig.a, rig.b} {
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := st.SyncAllowance(ctx, ws.ID, specs, creditDay)
				if err != nil {
					t.Errorf("sync: %v", err)
				}
				granted.Add(int64(res.Granted))
			}()
		}
	}
	wg.Wait()
	if granted.Load() != 150 {
		t.Fatalf("%d credits granted by six concurrent syncs, want 150 once", granted.Load())
	}
	start, end := creditDay, store.AddMonths(creditDay, 1)
	res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.Add(time.Hour))
	if err != nil || res.Granted != 0 || !res.PeriodStart.Equal(start) || !res.PeriodEnd.Equal(end) {
		t.Fatalf("a second sync = %+v, %v", res, err)
	}

	// 6 credits used in the period.
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, id, 6, creditDay.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, end.Add(-time.Minute)); got != 144 {
		t.Fatalf("available at the end of the period = %d, want 144", got)
	}

	// The period ends. The allowance is gone at once (it no longer counts), the job writes the expiry of exactly what was unused.
	after := end.Add(5 * time.Minute)
	if got := available(t, rig.a, ws, after); got != 0 {
		t.Fatalf("available after the period = %d, want 0", got)
	}
	if n, left, err := rig.b.ExpireDueCredits(ctx, after, 100); err != nil || n != 2 || left != 144+150 {
		// Two workspaces, two allowances: 144 left of the first, all 150 of the second (its owner used none).
		t.Fatalf("expiry = %d sources %d credits, %v", n, left, err)
	}
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	var expire *store.CreditLedgerRow
	for i, r := range rows {
		if r.Kind == store.CreditExpire {
			expire = &rows[i]
		}
	}
	if expire == nil || expire.Credits != -144 || !expire.CreatedAt.Equal(end) {
		t.Fatalf("expire row = %+v, want -144 dated at the end of the period", expire)
	}

	// The next period's allowance, once however many workers.
	granted.Store(0)
	for _, st := range []*store.Store{rig.a, rig.b} {
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := st.SyncAllowance(ctx, ws.ID, specs, after)
				if err != nil {
					t.Errorf("sync: %v", err)
				}
				granted.Add(int64(res.Granted))
			}()
		}
	}
	wg.Wait()
	if granted.Load() != 150 {
		t.Fatalf("%d credits granted for the second period, want 150", granted.Load())
	}
	b, err := rig.a.CreditBalance(ctx, ws.ID, after)
	if err != nil || b.Available != 150 || b.Allowance != 150 || b.PeriodEnd == nil || !b.PeriodEnd.Equal(store.AddMonths(creditDay, 2)) {
		t.Fatalf("balance = %+v, %v", b, err)
	}
	rig.checkLedger(t, ws)
	rig.checkLedger(t, other)
}

func TestCreditsAllowanceFollowsTheMerchantPeriod(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "merchant")
	periodEnd := creditDay.AddDate(0, 0, 20)
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, PeriodEnd: &periodEnd, Source: store.SourceMerchant, GrantedBy: "paddle"}); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay)
	if err != nil || res.Granted != 400 || !res.PeriodEnd.Equal(periodEnd) || !res.PeriodStart.Equal(store.AddMonths(periodEnd, -1)) {
		t.Fatalf("first period = %+v, %v", res, err)
	}
	// The renewal moves the period end by a month; the job sees a workspace that needs a grant and grants the new period.
	next := store.AddMonths(periodEnd, 1)
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, PeriodEnd: &next, Source: store.SourceMerchant, GrantedBy: "paddle", Force: true}); err != nil {
		t.Fatal(err)
	}
	due, err := rig.a.WorkspacesNeedingAllowance(ctx, creditDay.AddDate(0, 0, 19), ids.WorkspaceID{}, 10)
	if err != nil || len(due) != 1 || due[0] != ws.ID {
		t.Fatalf("workspaces needing an allowance = %v, %v", due, err)
	}
	res, err = rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.AddDate(0, 0, 19))
	if err != nil || res.Granted != 400 || !res.PeriodEnd.Equal(next) {
		t.Fatalf("renewal = %+v, %v", res, err)
	}
	if due, _ := rig.a.WorkspacesNeedingAllowance(ctx, creditDay.AddDate(0, 0, 19), ids.WorkspaceID{}, 10); len(due) != 0 {
		t.Fatalf("still needing an allowance: %v", due)
	}
}

func TestCreditsTrialIsGrantedOnceAndNeverRefilled(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "trial")
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Trial, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay)
	if err != nil || res.Granted != 20 {
		t.Fatalf("trial = %+v, %v", res, err)
	}
	// Use all of it, then wait for months: nothing is granted again, by the sync or by the job's list.
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, id, 6, creditDay)); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay); err != nil {
		t.Fatal(err)
	}
	for _, months := range []int{1, 2, 7} {
		at := store.AddMonths(creditDay, months)
		if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, at); err != nil || res.Granted != 0 {
			t.Fatalf("after %d months: %+v, %v", months, res, err)
		}
		if due, err := rig.a.WorkspacesNeedingAllowance(ctx, at, ids.WorkspaceID{}, 10); err != nil || len(due) != 0 {
			t.Fatalf("after %d months the job would look at %v (%v)", months, due, err)
		}
		if got := available(t, rig.a, ws, at); got != 14 {
			t.Fatalf("after %d months %d credits, want the 14 that were left", months, got)
		}
	}
	// Going to a paid plan replaces the trial's leftover, and going back to the trial does not give another 20.
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	at := store.AddMonths(creditDay, 8)
	res, err = rig.a.SyncAllowance(ctx, ws.ID, specs, at)
	if err != nil || res.Granted != 150 || res.Replaced != 14 {
		t.Fatalf("trial to creator = %+v, %v", res, err)
	}
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Trial, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	res, err = rig.a.SyncAllowance(ctx, ws.ID, specs, at.Add(time.Hour))
	if err != nil || res.Granted != 0 || res.Replaced != 150 {
		t.Fatalf("creator back to trial = %+v, %v", res, err)
	}
	if got := available(t, rig.a, ws, at.Add(time.Hour)); got != 0 {
		t.Fatalf("a second trial gave %d credits", got)
	}
	rig.checkLedger(t, ws)
}

func TestCreditsNoPlanNoAllowance(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	ws := rig.workspace(t, "noplan")
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, list.AllowanceSpecs(), creditDay); err != nil || res.Granted != 0 || res.Plan != "" {
		t.Fatalf("a workspace without a plan: %+v, %v", res, err)
	}
	// A plan that has already ended gives nothing either.
	end := creditDay.Add(-time.Hour)
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, PeriodEnd: &end, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, list.AllowanceSpecs(), creditDay); err != nil || res.Granted != 0 {
		t.Fatalf("an expired plan: %+v, %v", res, err)
	}
	if _, err := rig.a.RevokePlan(ctx, store.RevokeInput{WorkspaceID: ws.ID, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, list.AllowanceSpecs(), creditDay); err != nil || res.Granted != 0 {
		t.Fatalf("a revoked plan: %+v, %v", res, err)
	}
}

func TestCreditsPlanChangeReplacesTheAllowance(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "change")
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.AddDate(0, 0, 3))
	if err != nil || res.Granted != 400 || res.Replaced != 150 {
		t.Fatalf("creator to pro = %+v, %v", res, err)
	}
	b, _ := rig.a.CreditBalance(ctx, ws.ID, creditDay.AddDate(0, 0, 3))
	if b.Available != 400 || b.Plan != "pro" {
		t.Fatalf("balance = %+v", b)
	}
	// The monthly periods of the new plan start from the change.
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.AddDate(0, 0, 4)); err != nil || res.Granted != 0 || !res.PeriodStart.Equal(creditDay.AddDate(0, 0, 3)) {
		t.Fatalf("the pro period = %+v, %v", res, err)
	}
	// Re-granting the same plan (to move its end date, say) is not a change.
	end := creditDay.AddDate(1, 0, 0)
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, PeriodEnd: &end, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.AddDate(0, 0, 5)); err != nil || res.Granted != 0 || res.Replaced != 0 {
		t.Fatalf("the same plan again = %+v, %v", res, err)
	}
}

func TestCreditsMonthMath(t *testing.T) {
	utc := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		name   string
		anchor time.Time
		n      int
		want   time.Time
	}{
		{"plain", utc(2026, 10, 17, 12), 1, utc(2026, 11, 17, 12)},
		{"31 January plus one month", utc(2026, 1, 31, 8), 1, utc(2026, 2, 28, 8)},
		{"31 January plus two months does not drift", utc(2026, 1, 31, 8), 2, utc(2026, 3, 31, 8)},
		{"leap year", utc(2028, 1, 31, 0), 1, utc(2028, 2, 29, 0)},
		{"over the year", utc(2026, 12, 15, 0), 2, utc(2027, 2, 15, 0)},
		{"back a month", utc(2026, 3, 31, 0), -1, utc(2026, 2, 28, 0)},
	} {
		if got := store.AddMonths(tc.anchor, tc.n); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	anchor := utc(2026, 1, 31, 8)
	for _, tc := range []struct{ now, start, end time.Time }{
		{anchor, anchor, utc(2026, 2, 28, 8)},
		{utc(2026, 2, 28, 7), anchor, utc(2026, 2, 28, 8)},
		{utc(2026, 2, 28, 8), utc(2026, 2, 28, 8), utc(2026, 3, 31, 8)},
		{utc(2026, 3, 30, 0), utc(2026, 2, 28, 8), utc(2026, 3, 31, 8)},
		{utc(2026, 12, 31, 9), utc(2026, 12, 31, 8), utc(2027, 1, 31, 8)},
		{utc(2025, 1, 1, 0), anchor, utc(2026, 2, 28, 8)}, // before the anchor: the first period
	} {
		if s, e := store.ManualPeriod(anchor, tc.now); !s.Equal(tc.start) || !e.Equal(tc.end) {
			t.Errorf("ManualPeriod(%v) = %v to %v, want %v to %v", tc.now, s, e, tc.start, tc.end)
		}
	}
}

// A charge for more than is left takes what is left and says so: an allowance that expired while the run worked is not charged below zero.
func TestCreditsShortChargeIsAbsorbed(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "short")
	rig.topUp(t, rig.a, ws, 6, creditDay.Add(time.Hour), creditDay)
	id := newUUID()
	project := ids.ProjectID{UUID: newUUID()}
	p := hold(ws, store.ActionEditFirst, id, 6, creditDay)
	p.Project = project
	if _, err := rig.a.HoldCredits(ctx, p); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay.Add(2*time.Hour)) // the credits expired at +1h
	if err != nil || res.Charged != 0 || res.Short != 6 {
		t.Fatalf("settle = %+v, %v", res, err)
	}
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	var charge *store.CreditLedgerRow
	for i, r := range rows {
		if r.Kind == store.CreditCharge {
			charge = &rows[i]
		}
	}
	if charge == nil || charge.Credits != 0 || charge.Reason == nil {
		t.Fatalf("charge row = %+v", charge)
	}
	// It still counts as a charged edit: the creator got the edit.
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); !ok {
		t.Fatal("a short charge did not record the edit")
	}
	rig.checkLedger(t, ws)

	// Partly short: 4 of 6 left.
	ws2 := rig.workspace(t, "short2")
	rig.topUp(t, rig.a, ws2, 6, creditDay.Add(time.Hour), creditDay)
	rig.topUp(t, rig.a, ws2, 4, creditDay.AddDate(0, 1, 0), creditDay)
	id2 := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws2, store.ActionEditFirst, id2, 6, creditDay)); err != nil {
		t.Fatal(err)
	}
	if res, err := rig.a.SettleCredits(ctx, ws2.ID, id2, true, creditDay.Add(2*time.Hour)); err != nil || res.Charged != 4 || res.Short != 2 {
		t.Fatalf("partly short = %+v, %v", res, err)
	}
	if got := available(t, rig.a, ws2, creditDay.Add(2*time.Hour)); got != 0 {
		t.Fatalf("available = %d", got)
	}
	rig.checkLedger(t, ws2)
}

func TestCreditsTenancy(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	a, b := rig.workspace(t, "tenant-a"), rig.workspace(t, "tenant-b")
	rig.topUp(t, rig.a, a, 6, creditDay.AddDate(0, 1, 0), creditDay)
	if got := available(t, rig.a, b, creditDay); got != 0 {
		t.Fatalf("B sees %d of A's credits", got)
	}
	// B cannot spend A's credits.
	if _, err := rig.b.HoldCredits(ctx, hold(b, store.ActionEditFirst, newUUID(), 1, creditDay)); !isInsufficient(err) {
		t.Fatalf("B's hold: %v", err)
	}
	idA := newUUID()
	projectA := ids.ProjectID{UUID: newUUID()}
	pa := hold(a, store.ActionEditFirst, idA, 6, creditDay)
	pa.Project = projectA
	if _, err := rig.a.HoldCredits(ctx, pa); err != nil {
		t.Fatal(err)
	}
	// B cannot settle, release, read, extend or refund A's action, and the hold stays.
	if res, err := rig.b.SettleCredits(ctx, b.ID, idA, true, creditDay); err != nil || res.Held {
		t.Fatalf("B settled A's action: %+v, %v", res, err)
	}
	if res, err := rig.b.ReleaseCredits(ctx, b.ID, idA, creditDay); err != nil || res.Held {
		t.Fatalf("B released A's action: %+v, %v", res, err)
	}
	if _, err := rig.b.GetCreditHold(ctx, b.ID, idA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B read A's hold: %v", err)
	}
	if err := rig.b.ExtendCreditHold(ctx, b.ID, idA, creditDay.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if h, err := rig.a.GetCreditHold(ctx, a.ID, idA); err != nil || h.ExpiresAt.After(creditDay.AddDate(0, 0, 1)) {
		t.Fatalf("A's hold: %+v, %v", h, err)
	}
	if _, err := rig.a.SettleCredits(ctx, a.ID, idA, true, creditDay); err != nil {
		t.Fatal(err)
	}
	if ref, lost, err := rig.b.RefundAction(ctx, b.ID, idA, "test", "test", creditDay); err != nil || ref != 0 || lost != 0 {
		t.Fatalf("B refunded A's charge: %d %d %v", ref, lost, err)
	}
	if ok, err := rig.b.ProjectHasChargedEdit(ctx, b.ID, projectA); err != nil || ok {
		t.Fatalf("B sees A's charged edit: %v %v", ok, err)
	}
	if rows, err := rig.b.CreditLedger(ctx, b.ID, 50); err != nil || len(rows) != 0 {
		t.Fatalf("B's ledger has %d rows: %v", len(rows), err)
	}
	// B cannot take A's credits with an adjustment either.
	if _, err := rig.b.AdjustCredits(ctx, store.AdjustCreditsParams{Workspace: b.ID, Credits: -1, Actor: "test", Reason: "test", Now: creditDay}); !isInsufficient(err) {
		t.Fatalf("B adjusted A's credits: %v", err)
	}
	rig.checkLedger(t, a)
}

// The ledger is append-only for the app role; holds are plain rows.
func TestCreditsAppRoleCannotChangeTheLedger(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "append-credits")
	rig.topUp(t, rig.a, ws, 6, creditDay.AddDate(0, 1, 0), creditDay)
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 2, creditDay)); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, rig.srv.DSN(rig.database, appLogin, rolePass))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, stmt := range []string{
		`UPDATE credit_ledger SET credits = 600`,
		`UPDATE credit_ledger SET reason = 'x' WHERE false`,
		`DELETE FROM credit_ledger`,
		`DELETE FROM credit_ledger WHERE false`,
		`TRUNCATE credit_ledger`,
		`DELETE FROM credit_accounts`,
		`TRUNCATE credit_accounts`,
		`TRUNCATE credit_holds`,
	} {
		_, err := conn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("%q: error = %v, want SQLSTATE %s", stmt, err, permissionDenied)
		}
	}
	for _, stmt := range []string{
		`SELECT count(*) FROM credit_ledger`,
		`SELECT count(*) FROM credit_accounts`,
		`UPDATE credit_accounts SET plan = plan WHERE false`,
		`DELETE FROM credit_holds WHERE false`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Errorf("%q should be allowed: %v", stmt, err)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM credit_ledger`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger rows = %d, %v", n, err)
	}
	// The table's own rules: an adjustment needs an actor and a reason; a charge cannot add credits.
	for _, stmt := range []string{
		fmt.Sprintf(`INSERT INTO credit_ledger (workspace_id, kind, credits, grant_id, idempotency_key) SELECT workspace_id, 'adjust', -1, id, 'raw:adjust' FROM credit_ledger WHERE workspace_id = '%s' LIMIT 1`, ws.ID.UUID),
		fmt.Sprintf(`INSERT INTO credit_ledger (workspace_id, kind, credits, action, action_id, idempotency_key) VALUES ('%s', 'charge', 5, 'edit.first', '%s', 'raw:charge')`, ws.ID.UUID, newUUID()),
		fmt.Sprintf(`INSERT INTO credit_ledger (workspace_id, kind, credits, expires_at, idempotency_key) VALUES ('%s', 'grant_topup', 5, NULL, 'raw:topup')`, ws.ID.UUID),
	} {
		_, err := conn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%q: error = %v, want a check violation", stmt, err)
		}
	}
}

func TestCreditsAdjustments(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "adjust")
	rig.topUp(t, rig.a, ws, 5, creditDay.AddDate(0, 0, 5), creditDay)
	rig.topUp(t, rig.a, ws, 5, creditDay.AddDate(0, 0, 9), creditDay)
	adj := func(n int, key string) store.AdjustCreditsParams {
		return store.AdjustCreditsParams{Workspace: ws.ID, Credits: n, ExpiresAt: creditDay.AddDate(0, 1, 0), Actor: "cfctl:sam", Reason: "goodwill", Key: key, Now: creditDay}
	}
	// An adjustment says who and why.
	for _, bad := range []store.AdjustCreditsParams{
		{Workspace: ws.ID, Credits: 3, ExpiresAt: creditDay.AddDate(0, 1, 0), Reason: "x", Now: creditDay},
		{Workspace: ws.ID, Credits: 3, ExpiresAt: creditDay.AddDate(0, 1, 0), Actor: "x", Now: creditDay},
		{Workspace: ws.ID, Credits: 0, Actor: "x", Reason: "x", Now: creditDay},
		{Workspace: ws.ID, Credits: 3, Actor: "x", Reason: "x", Now: creditDay}, // credits added need an expiry
	} {
		if _, err := rig.a.AdjustCredits(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// Taking away: from the top-up that expires first, then the next; and only what exists.
	if _, err := rig.a.AdjustCredits(ctx, adj(-7, "adj-1")); err != nil {
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 3 {
		t.Fatalf("available = %d, want 3", got)
	}
	if _, err := rig.a.AdjustCredits(ctx, adj(-7, "adj-1")); err != nil { // the same key again: replayed, nothing more taken
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 3 {
		t.Fatalf("a replayed adjustment took more: %d", got)
	}
	if _, err := rig.a.AdjustCredits(ctx, adj(-4, "adj-2")); !isInsufficient(err) {
		t.Fatalf("taking 4 of 3: %v", err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 3 {
		t.Fatalf("a refused adjustment changed the balance: %d", got)
	}
	// Adding.
	if _, err := rig.a.AdjustCredits(ctx, adj(10, "adj-3")); err != nil {
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 13 {
		t.Fatalf("available = %d, want 13", got)
	}
	if _, err := rig.a.AdjustCredits(ctx, adj(10, "adj-3")); err != nil {
		t.Fatal(err)
	}
	if got := available(t, rig.a, ws, creditDay); got != 13 {
		t.Fatalf("a replayed addition added again: %d", got)
	}
	// Every row says who and why.
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 50)
	n := 0
	for _, r := range rows {
		if r.Kind == store.CreditAdjust {
			n++
			if r.Actor == nil || *r.Actor != "cfctl:sam" || r.Reason == nil || *r.Reason != "goodwill" {
				t.Errorf("adjust row without actor and reason: %+v", r)
			}
		}
	}
	if n != 3 { // -5 and -2 (two sources), +10
		t.Fatalf("%d adjust rows, want 3", n)
	}
	rig.checkLedger(t, ws)
}

func TestCreditsRefundAction(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "refund")
	rig.topUp(t, rig.a, ws, 10, creditDay.AddDate(0, 0, 5), creditDay)
	project := ids.ProjectID{UUID: newUUID()}
	id := newUUID()
	p := hold(ws, store.ActionEditFirst, id, 6, creditDay)
	p.Project = project
	if _, err := rig.a.HoldCredits(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); !ok {
		t.Fatal("no charged edit")
	}
	got, lost, err := rig.a.RefundAction(ctx, ws.ID, id, "cfctl:sam", "the render was broken", creditDay.Add(time.Hour))
	if err != nil || got != 6 || lost != 0 {
		t.Fatalf("refund = %d %d %v", got, lost, err)
	}
	if again, _, err := rig.a.RefundAction(ctx, ws.ID, id, "cfctl:sam", "the render was broken", creditDay.Add(time.Hour)); err != nil || again != 0 {
		t.Fatalf("a second refund gave %d (%v)", again, err)
	}
	if got := available(t, rig.a, ws, creditDay.Add(time.Hour)); got != 10 {
		t.Fatalf("available after the refund = %d, want 10", got)
	}
	// A refunded edit is not a charged edit: the next run is a first edit again.
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); ok {
		t.Fatal("a refunded edit still counts as charged")
	}
	// Credits whose source has expired cannot come back.
	id2 := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditRevision, id2, 2, creditDay)); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id2, true, creditDay); err != nil {
		t.Fatal(err)
	}
	got, lost, err = rig.a.RefundAction(ctx, ws.ID, id2, "cfctl:sam", "late", creditDay.AddDate(0, 0, 6))
	if err != nil || got != 0 || lost != 2 {
		t.Fatalf("refund of expired credits = %d %d %v", got, lost, err)
	}
	rig.checkLedger(t, ws)
}

// ---- The end of a run -------------------------------------------------------------------------------------------

type runRig struct {
	t   *testing.T
	st  *store.Store
	rig spendRig
	ws  store.Workspace
}

func newRunRig(t *testing.T, name string, credits int) runRig {
	t.Helper()
	rig := newSpendRig(t)
	ws := rig.workspace(t, name)
	now := time.Now()
	rig.topUp(t, rig.a, ws, credits, now.AddDate(0, 1, 0), now)
	return runRig{t: t, st: rig.a, rig: rig, ws: ws}
}

// start creates a queued editor-like run with a hold of n credits.
func (r runRig) start(n int, action string, project ids.ProjectID) store.AgentRun {
	r.t.Helper()
	ctx := context.Background()
	run, err := r.st.CreateAgentRun(ctx, r.ws.ID, store.NewAgentRun{
		Agent: "editor", Stage: "editor", Input: json.RawMessage(`{}`), RoutingProfile: "dev-cheap", Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 1000,
		BudgetMicros: 100_000, BudgetTokens: 10_000, BudgetToolCalls: 5, BudgetWallClockMs: 60_000, BilledTo: store.BilledToTest,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	p := hold(r.ws, action, run.ID, n, time.Now())
	p.Project = project
	if _, err := r.st.HoldCredits(ctx, p); err != nil {
		r.t.Fatal(err)
	}
	return run
}

func (r runRig) end(run store.AgentRun, status, reason string) store.AgentRun {
	r.t.Helper()
	ctx := context.Background()
	c, err := r.st.ClaimAgentRun(ctx, r.ws.ID, run.ID, time.Minute)
	if err != nil {
		r.t.Fatal(err)
	}
	out, err := r.st.EndAgentRun(ctx, r.ws.ID, run.ID, c.Epoch, store.RunEnd{Status: status, FailureReason: reason, Event: json.RawMessage(`{"state":"` + status + `"}`)})
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r runRig) charged(run store.AgentRun) int {
	r.t.Helper()
	rows, err := r.st.CreditLedger(context.Background(), r.ws.ID, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Kind == store.CreditCharge && row.ActionID.Bytes == run.ID {
			n += int(-row.Credits)
		}
	}
	return n
}

func (r runRig) held(run store.AgentRun) bool {
	r.t.Helper()
	_, err := r.st.GetCreditHold(context.Background(), r.ws.ID, run.ID)
	return err == nil
}

func TestCreditsRunEndsSettleTheHold(t *testing.T) {
	r := newRunRig(t, "ends", 30)
	ctx := context.Background()
	project := ids.ProjectID{UUID: newUUID()}

	// A run that fails releases its hold and charges nothing; the project has no charged edit, so the next run is a first edit again.
	failed := r.start(6, store.ActionEditFirst, project)
	r.end(failed, store.AgentFailed, "budget")
	if r.held(failed) || r.charged(failed) != 0 {
		t.Fatalf("a failed run: held %v charged %d", r.held(failed), r.charged(failed))
	}
	if ok, _ := r.st.ProjectHasChargedEdit(ctx, r.ws.ID, project); ok {
		t.Fatal("a failed first run counts as a charged edit")
	}
	if got := available(t, r.st, r.ws, time.Now()); got != 30 {
		t.Fatalf("available after a failed run = %d, want 30", got)
	}

	// A run that succeeds is charged, and the project has a charged edit.
	ok := r.start(6, store.ActionEditFirst, project)
	if got := available(t, r.st, r.ws, time.Now()); got != 24 {
		t.Fatalf("available while the run works = %d, want 24", got)
	}
	r.end(ok, store.AgentSucceeded, "")
	if r.held(ok) || r.charged(ok) != 6 {
		t.Fatalf("a successful run: held %v charged %d", r.held(ok), r.charged(ok))
	}
	if has, _ := r.st.ProjectHasChargedEdit(ctx, r.ws.ID, project); !has {
		t.Fatal("a successful run is not a charged edit")
	}
	if got := available(t, r.st, r.ws, time.Now()); got != 24 {
		t.Fatalf("available after the charge = %d, want 24", got)
	}

	// A run that is not an editor run (no hold) is left alone: nothing is locked and nothing is charged.
	plain, err := r.st.CreateAgentRun(ctx, r.ws.ID, store.NewAgentRun{
		Agent: "echo", Stage: "ideation", Input: json.RawMessage(`{}`), RoutingProfile: "dev-cheap", Model: "claude-haiku-5-5", Effort: "medium", MaxTokens: 1000,
		BudgetMicros: 100_000, BudgetTokens: 10_000, BudgetToolCalls: 5, BudgetWallClockMs: 60_000, BilledTo: store.BilledToTest,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.end(plain, store.AgentSucceeded, "")
	if got := available(t, r.st, r.ws, time.Now()); got != 24 {
		t.Fatalf("an echo run changed the balance: %d", got)
	}
	r.rig.checkLedger(t, r.ws)
}

// A creator who cancels is charged if a preview of the run was rendered (they got something), and not otherwise.
func TestCreditsCancelChargesOnlyAfterAPreview(t *testing.T) {
	r := newRunRig(t, "cancel", 30)
	ctx := context.Background()

	// Cancelled while queued: no preview, released.
	queued := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	if _, err := r.st.RequestAgentRunCancel(ctx, r.ws.ID, queued.ID, json.RawMessage(`{}`), json.RawMessage(`{"state":"cancelled"}`)); err != nil {
		t.Fatal(err)
	}
	if r.held(queued) || r.charged(queued) != 0 {
		t.Fatalf("cancelled in the queue: held %v charged %d", r.held(queued), r.charged(queued))
	}

	// Cancelled while running, after the loop saw the request, with no preview: released.
	noPreview := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	c, err := r.st.ClaimAgentRun(ctx, r.ws.ID, noPreview.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.RequestAgentRunCancel(ctx, r.ws.ID, noPreview.ID, json.RawMessage(`{}`), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !r.held(noPreview) {
		t.Fatal("a cancel request on a running run settled it before the loop ended it")
	}
	if _, err := r.st.EndAgentRun(ctx, r.ws.ID, noPreview.ID, c.Epoch, store.RunEnd{Status: store.AgentCancelled, Event: json.RawMessage(`{"state":"cancelled"}`)}); err != nil {
		t.Fatal(err)
	}
	if r.held(noPreview) || r.charged(noPreview) != 0 {
		t.Fatalf("cancelled without a preview: held %v charged %d", r.held(noPreview), r.charged(noPreview))
	}

	// Cancelled after a preview was rendered: charged.
	project := ids.ProjectID{UUID: newUUID()}
	previewed := r.start(6, store.ActionEditFirst, project)
	c, err = r.st.ClaimAgentRun(ctx, r.ws.ID, previewed.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AppendAgentEvent(ctx, r.ws.ID, previewed.ID, c.Epoch, store.EventArtifact, json.RawMessage(`{"kind":"preview","data":{"version":1,"duration_ms":9000}}`), store.EventDelta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.EndAgentRun(ctx, r.ws.ID, previewed.ID, c.Epoch, store.RunEnd{Status: store.AgentCancelled, Event: json.RawMessage(`{"state":"cancelled"}`)}); err != nil {
		t.Fatal(err)
	}
	if r.held(previewed) || r.charged(previewed) != 6 {
		t.Fatalf("cancelled after a preview: held %v charged %d", r.held(previewed), r.charged(previewed))
	}
	if has, _ := r.st.ProjectHasChargedEdit(ctx, r.ws.ID, project); !has {
		t.Fatal("a charged cancel is not a charged edit")
	}

	// A different artifact is not a preview.
	other := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	c, err = r.st.ClaimAgentRun(ctx, r.ws.ID, other.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AppendAgentEvent(ctx, r.ws.ID, other.ID, c.Epoch, store.EventArtifact, json.RawMessage(`{"kind":"creative_plan","data":{}}`), store.EventDelta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.EndAgentRun(ctx, r.ws.ID, other.ID, c.Epoch, store.RunEnd{Status: store.AgentCancelled, Event: json.RawMessage(`{"state":"cancelled"}`)}); err != nil {
		t.Fatal(err)
	}
	if r.charged(other) != 0 {
		t.Fatalf("a plan is not a preview, but %d were charged", r.charged(other))
	}
	r.rig.checkLedger(t, r.ws)
}

// A run that asks the creator a question keeps its hold for days, and a cancel while it waits is settled like any other.
func TestCreditsWaitingRunKeepsItsHold(t *testing.T) {
	r := newRunRig(t, "waiting", 30)
	ctx := context.Background()
	run := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	c, err := r.st.ClaimAgentRun(ctx, r.ws.ID, run.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.EndAgentRun(ctx, r.ws.ID, run.ID, c.Epoch, store.RunEnd{Status: store.AgentWaitingInput, Event: json.RawMessage(`{"state":"waiting_input"}`)}); err != nil {
		t.Fatal(err)
	}
	h, err := r.st.GetCreditHold(ctx, r.ws.ID, run.ID)
	if err != nil || h.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("hold of a waiting run: %+v, %v", h, err)
	}
	if res, err := r.st.SweepExpiredCreditHolds(ctx, time.Now().Add(time.Hour)); err != nil || res.Holds != 0 {
		t.Fatalf("the sweep took a waiting run's hold: %+v, %v", res, err)
	}
	if _, err := r.st.RequestAgentRunCancel(ctx, r.ws.ID, run.ID, json.RawMessage(`{}`), json.RawMessage(`{"state":"cancelled"}`)); err != nil {
		t.Fatal(err)
	}
	if r.held(run) || r.charged(run) != 0 {
		t.Fatalf("cancelled while waiting: held %v charged %d", r.held(run), r.charged(run))
	}
}

// Two ends of one run (a retried end, a worker that lost the race) charge once.
func TestCreditsEndReplayChargesOnce(t *testing.T) {
	r := newRunRig(t, "replay", 30)
	ctx := context.Background()
	run := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	r.end(run, store.AgentSucceeded, "")
	// The same settle again, as a replayed end or a second worker would do.
	for range 3 {
		if _, err := r.st.SettleCredits(ctx, r.ws.ID, run.ID, true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if r.charged(run) != 6 {
		t.Fatalf("charged %d, want 6", r.charged(run))
	}
	// Even if the hold were left behind (a crash between the two statements cannot happen in one transaction, but a hold made again for
	// the same action must not charge twice).
	if _, err := r.st.HoldCredits(ctx, hold(r.ws, store.ActionEditFirst, run.ID, 6, time.Now())); err != nil {
		t.Fatal(err)
	}
	if res, err := r.st.SettleCredits(ctx, r.ws.ID, run.ID, true, time.Now()); err != nil || !res.Replayed || res.Charged != 0 {
		t.Fatalf("settle over an old charge = %+v, %v", res, err)
	}
	if r.charged(run) != 6 || r.held(run) {
		t.Fatalf("charged %d held %v", r.charged(run), r.held(run))
	}
	r.rig.checkLedger(t, r.ws)
}

// An editor run without credits still has a hold (of zero credits) and ends up recorded as an edit of the project.
func TestCreditsZeroCreditHoldRecordsTheEdit(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "free")
	project := ids.ProjectID{UUID: newUUID()}
	id := newUUID()
	p := hold(ws, store.ActionEditFirst, id, 0, creditDay)
	p.Project = project
	if _, err := rig.a.HoldCredits(ctx, p); err != nil { // no credits at all, and still fine
		t.Fatal(err)
	}
	res, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay)
	if err != nil || res.Charged != 0 || !res.Held {
		t.Fatalf("settle = %+v, %v", res, err)
	}
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); !ok {
		t.Fatal("a zero-credit edit is not recorded")
	}
}

func TestCreditsFreeActionIsRecordedOnce(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "export")
	render := newUUID()
	p := store.RecordCreditActionParams{Workspace: ws.ID, Action: store.ActionExportFinal, ActionID: render, Project: ids.ProjectID{UUID: newUUID()}, Plan: "creator", ListVersion: "2026-10", Now: creditDay}
	for range 3 {
		if err := rig.a.RecordFreeCreditAction(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := rig.a.CreditLedger(ctx, ws.ID, 10)
	if len(rows) != 1 || rows[0].Kind != store.CreditCharge || rows[0].Credits != 0 || *rows[0].Action != store.ActionExportFinal {
		t.Fatalf("rows = %+v", rows)
	}
	// An export is not an edit: it does not make the project a charged edit.
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, p.Project); ok {
		t.Fatal("an export counted as a charged edit")
	}
}

func TestCreditsLedgerOrderIsStable(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	ws := rig.workspace(t, "stable")
	for i := range 5 {
		rig.topUp(t, rig.a, ws, i+1, creditDay.AddDate(0, 1, 0), creditDay.Add(time.Duration(i)*time.Minute))
	}
	rows, err := rig.a.CreditLedger(ctx, ws.ID, 3)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	got := []int32{rows[0].Credits, rows[1].Credits, rows[2].Credits}
	if !slices.Equal(got, []int32{5, 4, 3}) {
		t.Fatalf("newest first: %v", got)
	}
	if _, err := rig.a.CreditLedger(ctx, ws.ID, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("limit 0: %v", err)
	}
}

// The heartbeat of the run that owns a hold keeps the hold alive: a run that works for hours is not swept, and a run whose worker has
// gone stops beating and is.
func TestCreditsRunHeartbeatRenewsItsHold(t *testing.T) {
	r := newRunRig(t, "beat", 30)
	ctx := context.Background()
	run := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	c, err := r.st.ClaimAgentRun(ctx, r.ws.ID, run.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Make the hold short, as if the run had been working for a long time.
	conn := r.rig.superConn(t)
	if _, err := conn.Exec(ctx, `UPDATE credit_holds SET expires_at = now() + interval '5 minutes' WHERE action_id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.TouchAgentRun(ctx, r.ws.ID, run.ID, c.Epoch, 1000); err != nil {
		t.Fatal(err)
	}
	h, err := r.st.GetCreditHold(ctx, r.ws.ID, run.ID)
	if err != nil || h.ExpiresAt.Before(time.Now().Add(25*time.Minute)) {
		t.Fatalf("hold after a beat: %+v, %v", h, err)
	}
	// A fenced beat (the run was taken over) changes nothing.
	if err := r.st.TouchAgentRun(ctx, r.ws.ID, run.ID, c.Epoch+7, 0); !errors.Is(err, store.ErrRunFenced) {
		t.Fatalf("fenced beat: %v", err)
	}
}

// ---- found by review ---------------------------------------------------------------------------------------------

// The allowance the balance shows is the live one: a plan change that expired the old allowance does not leave its size behind.
func TestCreditsBalanceAfterAPlanChangeShowsOnlyTheLiveAllowance(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "shows")
	for _, plan := range []plans.Plan{plans.Trial, plans.Creator, plans.Pro} {
		if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plan, GrantedBy: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay); err != nil {
			t.Fatal(err)
		}
	}
	b, err := rig.a.CreditBalance(ctx, ws.ID, creditDay)
	if err != nil || b.Allowance != 400 || b.Available != 400 || b.PeriodEnd == nil || !b.PeriodEnd.Equal(store.AddMonths(creditDay, 1)) || b.Plan != "pro" {
		t.Fatalf("balance = %+v, %v; want only pro's 400", b, err)
	}
}

// A plan that comes back within the same period gets its allowance again: the first one was expired when the plan went away.
func TestCreditsPlanComingBackInTheSamePeriodIsGrantedAgain(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "flip")
	end := creditDay.AddDate(0, 0, 20)
	grant := func(plan plans.Plan) {
		t.Helper()
		in := store.GrantInput{WorkspaceID: ws.ID, Plan: plan, PeriodEnd: &end, Source: store.SourceMerchant, GrantedBy: "paddle", Force: true}
		if _, err := rig.a.GrantPlan(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	for i, plan := range []plans.Plan{plans.Creator, plans.Pro, plans.Creator} {
		grant(plan)
		res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.Add(time.Duration(i)*time.Hour))
		if err != nil || res.Granted != specs[plan].Credits {
			t.Fatalf("step %d (%s): %+v, %v", i, plan, res, err)
		}
	}
	if got := available(t, rig.a, ws, creditDay.Add(3*time.Hour)); got != 150 {
		t.Fatalf("available = %d, want the 150 of creator", got)
	}
	// Syncing again changes nothing.
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.Add(4*time.Hour)); err != nil || res.Granted != 0 || res.Replaced != 0 {
		t.Fatalf("again: %+v, %v", res, err)
	}
	rig.checkLedger(t, ws)
}

// The replay of a taking-away is detected whichever source it started from, and it cannot take credits that a run has held.
func TestCreditsAdjustReplayWithAnEmptyFirstSourceAndHolds(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	ws := rig.workspace(t, "adjust2")
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SyncAllowance(ctx, ws.ID, list.AllowanceSpecs(), creditDay); err != nil {
		t.Fatal(err)
	}
	rig.topUp(t, rig.a, ws, 30, creditDay.AddDate(0, 1, 0), creditDay)
	// Use the whole allowance, so the first source in the order of use is empty.
	id := newUUID()
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, id, 150, creditDay)); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay); err != nil {
		t.Fatal(err)
	}
	adj := func(n int) error {
		_, err := rig.a.AdjustCredits(ctx, store.AdjustCreditsParams{Workspace: ws.ID, Credits: -n, Actor: "t", Reason: "t", Key: "same-key", Now: creditDay})
		return err
	}
	if err := adj(10); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := adj(10); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if got := available(t, rig.a, ws, creditDay); got != 20 {
		t.Fatalf("available = %d, want 20: the replays took credits", got)
	}
	// A run holds 15 of the 20: only 5 are free to take away.
	if _, err := rig.a.HoldCredits(ctx, hold(ws, store.ActionEditFirst, newUUID(), 15, creditDay)); err != nil {
		t.Fatal(err)
	}
	var ins *store.InsufficientCreditsError
	if _, err := rig.a.AdjustCredits(ctx, store.AdjustCreditsParams{Workspace: ws.ID, Credits: -6, Actor: "t", Reason: "t", Now: creditDay}); !errors.As(err, &ins) || ins.Available != 5 {
		t.Fatalf("taking 6 of 5 free credits: %v", err)
	}
	rig.checkLedger(t, ws)
}

// A refund gives back what can be given back, and in every case the action stops counting as a charged edit.
func TestCreditsRefundOfFreeAndExpiredCharges(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "refund2")

	// A free (zero-credit) edit.
	free := ids.ProjectID{UUID: newUUID()}
	fid := newUUID()
	p := hold(ws, store.ActionEditFirst, fid, 0, creditDay)
	p.Project = free
	if _, err := rig.a.HoldCredits(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, fid, true, creditDay); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, free); !ok {
		t.Fatal("a free edit is not recorded")
	}
	if got, lost, err := rig.a.RefundAction(ctx, ws.ID, fid, "t", "t", creditDay); err != nil || got != 0 || lost != 0 {
		t.Fatalf("refund of a free edit: %d %d %v", got, lost, err)
	}
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, free); ok {
		t.Fatal("a refunded free edit still counts")
	}

	// An allowance that a plan change replaced: the credits cannot come back, and the edit stops counting.
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay); err != nil {
		t.Fatal(err)
	}
	project := ids.ProjectID{UUID: newUUID()}
	id := newUUID()
	q := hold(ws, store.ActionEditFirst, id, 6, creditDay)
	q.Project = project
	if _, err := rig.a.HoldCredits(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SettleCredits(ctx, ws.ID, id, true, creditDay); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Pro, GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := available(t, rig.a, ws, creditDay.Add(2*time.Hour))
	got, lost, err := rig.a.RefundAction(ctx, ws.ID, id, "t", "t", creditDay.Add(2*time.Hour))
	if err != nil || got != 0 || lost != 6 {
		t.Fatalf("refund into a replaced allowance: %d %d %v", got, lost, err)
	}
	if after := available(t, rig.a, ws, creditDay.Add(2*time.Hour)); after != before {
		t.Fatalf("a refund resurrected a replaced allowance: %d to %d", before, after)
	}
	if ok, _ := rig.a.ProjectHasChargedEdit(ctx, ws.ID, project); ok {
		t.Fatal("a refunded edit still counts")
	}
	if got, lost, err := rig.a.RefundAction(ctx, ws.ID, id, "t", "t", creditDay.Add(2*time.Hour)); err != nil || got != 0 || lost != 0 {
		t.Fatalf("a second refund: %d %d %v", got, lost, err)
	}
	rig.checkLedger(t, ws)
}

// A hold lives as long as its run is active, however long the queue or the outage: it keeps counting and the sweep leaves it; once the
// run is over it goes with its expiry.
func TestCreditsHoldOfAnActiveRunOutlivesItsExpiry(t *testing.T) {
	r := newRunRig(t, "active", 30)
	ctx := context.Background()
	run := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	conn := r.rig.superConn(t)
	if _, err := conn.Exec(ctx, `UPDATE credit_holds SET expires_at = now() - interval '5 hours' WHERE action_id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := available(t, r.st, r.ws, time.Now()); got != 24 {
		t.Fatalf("available = %d: the expired hold of a queued run stopped counting", got)
	}
	if res, err := r.st.SweepExpiredCreditHolds(ctx, time.Now()); err != nil || res.Holds != 0 {
		t.Fatalf("the sweep took the hold of a queued run: %+v, %v", res, err)
	}
	// The run finishes: its hold is settled as always.
	r.end(run, store.AgentSucceeded, "")
	if r.charged(run) != 6 {
		t.Fatalf("charged %d, want 6", r.charged(run))
	}
	// A stuck run is not kept for ever: 30 days past the expiry the hold goes.
	stuck := r.start(6, store.ActionEditFirst, ids.ProjectID{UUID: newUUID()})
	if _, err := conn.Exec(ctx, `UPDATE credit_holds SET expires_at = now() - interval '31 days' WHERE action_id = $1`, stuck.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := r.st.SweepExpiredCreditHolds(ctx, time.Now()); err != nil || res.Holds != 1 {
		t.Fatalf("the sweep of a run stuck for a month: %+v, %v", res, err)
	}
}

// The list of workspaces that need an allowance is paged by id, so one that stays on it cannot hide the others.
func TestCreditsWorkspacesNeedingAllowanceArePaged(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	var all []ids.WorkspaceID
	for i := range 5 {
		w := rig.workspace(t, fmt.Sprintf("page%d", i))
		if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: w.ID, Plan: plans.Creator, GrantedBy: "test"}); err != nil {
			t.Fatal(err)
		}
		all = append(all, w.ID)
	}
	slices.SortFunc(all, func(a, b ids.WorkspaceID) int { return slices.Compare(a.UUID[:], b.UUID[:]) })
	var got []ids.WorkspaceID
	var after ids.WorkspaceID
	for {
		page, err := rig.a.WorkspacesNeedingAllowance(ctx, time.Now(), after, 2)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1]
	}
	if !slices.Equal(got, all) {
		t.Fatalf("paged list = %v, want %v", got, all)
	}
}

// A merchant plan whose period end moves by less than a renewal (a billing-date change, a proration, a pause) is the same billing period:
// it gets no second allowance. A real renewal does.
func TestCreditsMerchantPeriodEndShiftIsNotARenewal(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	list, _ := credits.LoadDefault()
	specs := list.AllowanceSpecs()
	ws := rig.workspace(t, "shift")
	end := creditDay.AddDate(0, 0, 20)
	setEnd := func(e time.Time) {
		t.Helper()
		if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws.ID, Plan: plans.Creator, PeriodEnd: &e, Source: store.SourceMerchant, GrantedBy: "paddle", Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	setEnd(end)
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay); err != nil || res.Granted != 150 {
		t.Fatalf("first period: %+v, %v", res, err)
	}
	for _, shifted := range []time.Time{end.AddDate(0, 0, 3), end.AddDate(0, 0, -4), end.AddDate(0, 0, 9)} {
		setEnd(shifted)
		now := creditDay.Add(time.Hour)
		if due, _ := rig.a.WorkspacesNeedingAllowance(ctx, now, ids.WorkspaceID{}, 10); len(due) != 1 {
			t.Fatalf("a shifted end should list the workspace once: %v", due)
		}
		if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, now); err != nil || res.Granted != 0 {
			t.Fatalf("end moved to %v: %+v, %v", shifted, res, err)
		}
		if due, _ := rig.a.WorkspacesNeedingAllowance(ctx, now, ids.WorkspaceID{}, 10); len(due) != 0 {
			t.Fatalf("the shifted end is recorded, but the workspace is still listed: %v", due)
		}
		end = shifted
	}
	if got := available(t, rig.a, ws, creditDay.Add(time.Hour)); got != 150 {
		t.Fatalf("available = %d, want one allowance of 150", got)
	}
	// The renewal, a month on, is a new period.
	setEnd(store.AddMonths(end, 1))
	if res, err := rig.a.SyncAllowance(ctx, ws.ID, specs, creditDay.AddDate(0, 0, 25)); err != nil || res.Granted != 150 {
		t.Fatalf("renewal: %+v, %v", res, err)
	}
	// A merchant plan with no period end is not listed again and again.
	ws2 := rig.workspace(t, "noend")
	if _, err := rig.a.GrantPlan(ctx, store.GrantInput{WorkspaceID: ws2.ID, Plan: plans.Creator, Source: store.SourceMerchant, GrantedBy: "paddle", Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.a.SyncAllowance(ctx, ws2.ID, specs, creditDay); err != nil {
		t.Fatal(err)
	}
	due, _ := rig.a.WorkspacesNeedingAllowance(ctx, creditDay.Add(time.Hour), ids.WorkspaceID{}, 10)
	if slices.Contains(due, ws2.ID) {
		t.Fatalf("a merchant plan without an end is listed again: %v", due)
	}
	// Long texts are refused with ErrInvalid, not with a database error.
	long := string(make([]byte, 1001))
	if _, _, err := rig.a.GrantTopUp(ctx, store.GrantTopUpParams{Workspace: ws.ID, Credits: 1, ExpiresAt: creditDay.AddDate(1, 0, 0), Actor: "t", Reason: long, Now: creditDay}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("a reason of 1001 bytes: %v", err)
	}
}
