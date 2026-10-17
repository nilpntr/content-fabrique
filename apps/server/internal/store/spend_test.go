package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/store/storetest"
)

// spendRig is a database migrated by the migrate role and used through the app login, as on staging: the holds and counters are
// proven to work with the rights the app really has, and two stores over it are two processes (two pools).
type spendRig struct {
	srv      *storetest.Server
	database string
	a, b     *store.Store // two separate pools: two processes
}

func newSpendRig(t *testing.T) spendRig {
	t.Helper()
	srv := storetest.Start(t)
	database := setupRoles(t, srv, roleSetup{preCreateGroup: true})
	migrate(t, srv.DSN(database, migrateRole, rolePass))
	dsn := srv.DSN(database, appLogin, rolePass)
	return spendRig{srv: srv, database: database, a: openStore(t, dsn), b: openStore(t, dsn)}
}

func (r spendRig) workspace(t *testing.T, name string) store.Workspace {
	t.Helper()
	w, err := r.a.CreateWorkspace(context.Background(), store.NewWorkspace{ClerkOrgID: "org_spend_" + name + "_" + uuid.NewString(), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

var spendDay = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func reserve(key string, w store.Workspace, billed string, micros int64, caps store.SpendCaps, now time.Time) store.ReserveSpendParams {
	return store.ReserveSpendParams{
		Key: key, Workspace: w.ID, BilledTo: billed, Kind: store.KindLLM, Micros: micros, Caps: caps, Now: now, TTL: 5 * time.Minute,
	}
}

func llmRow(w store.Workspace, key, billed string, cost int64, at time.Time) store.UsageEntry {
	return store.UsageEntry{
		WorkspaceID: w.ID, Stage: "editor", Model: "claude-haiku-5-5", InputTokens: 10, OutputTokens: 5, ListCostMicros: cost,
		PriceTableVersion: "2026-10b", BilledTo: billed, IdempotencyKey: key, CreatedAt: at,
	}
}

// Two goroutines on two separate pools reserve against one workspace cap and one platform cap. However the calls interleave, what
// is held and settled never passes either cap. Run many times, each time on a fresh day (a fresh platform counter) and workspace.
func TestSpendRaceTwoProcessesNeverPassTheCaps(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	const (
		est      = int64(1_000)
		perProc  = 12
		rounds   = 40
		wsCap    = int64(7_000) // 7 calls
		platCap  = int64(12_000)
		procs    = 2
		maxAsked = perProc * procs
	)
	for round := range rounds {
		now := spendDay.AddDate(0, 0, round)
		ws1, ws2 := rig.workspace(t, fmt.Sprintf("r%da", round)), rig.workspace(t, fmt.Sprintf("r%db", round))
		caps := store.SpendCaps{WorkspaceDailyMicros: wsCap, PlatformDailyMicros: platCap}

		var granted atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for proc, st := range []*store.Store{rig.a, rig.b} {
			for _, ws := range []store.Workspace{ws1, ws2} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := range perProc / 2 {
						key := fmt.Sprintf("race:%d:%d:%s:%d", round, proc, ws.ID, i)
						_, _, err := st.ReserveSpend(ctx, reserve(key, ws, store.BilledToPlatform, est, caps, now))
						switch {
						case err == nil:
							granted.Add(1)
						case errors.Is(err, store.ErrSpendCapExceeded):
						default:
							t.Errorf("round %d: reserve: %v", round, err)
						}
					}
				}()
			}
		}
		close(start)
		wg.Wait()

		// The platform cap (12 calls) is below what two workspaces may hold (2 x 7), so it is the binding one: exactly 12 granted.
		if got := granted.Load() * est; got > platCap {
			t.Fatalf("round %d: %d micros held, platform cap %d", round, got, platCap)
		}
		if got := granted.Load(); got != platCap/est {
			t.Fatalf("round %d: %d calls granted, want exactly %d (demand %d exceeds the cap)", round, got, platCap/est, maxAsked)
		}
		// Each workspace's share is within its own cap too: count the live holds per workspace through a probe reserve of the rest.
		for _, ws := range []store.Workspace{ws1, ws2} {
			_, _, err := rig.b.ReserveSpend(ctx, reserve("probe:"+ws.ID.String(), ws, store.BilledToTest, wsCap, caps, now))
			if !errors.Is(err, store.ErrSpendCapExceeded) {
				t.Fatalf("round %d: a probe for the whole cap succeeded next to live holds: %v", round, err)
			}
		}
	}
}

// The same on a workspace cap alone, with test-billed calls (no platform row), and settles interleaved with reserves: spent plus held
// never passes the cap, and every settle moves a hold into a counter without a gap.
func TestSpendRaceWithSettlesInBetween(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	const (
		est   = int64(1_000)
		cost  = int64(600) // each call turns out cheaper than its reservation
		wsCap = int64(9_000)
	)
	for round := range 25 {
		now := spendDay.AddDate(0, 1, round)
		ws := rig.workspace(t, fmt.Sprintf("s%d", round))
		caps := store.SpendCaps{WorkspaceDailyMicros: wsCap, PlatformDailyMicros: 1 << 40}
		var settled, refused atomic.Int64
		var wg sync.WaitGroup
		for proc, st := range []*store.Store{rig.a, rig.b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 20 {
					key := fmt.Sprintf("s:%d:%d:%d", round, proc, i)
					h, _, err := st.ReserveSpend(ctx, reserve(key, ws, store.BilledToTest, est, caps, now))
					if err != nil {
						if !errors.Is(err, store.ErrSpendCapExceeded) {
							t.Errorf("reserve: %v", err)
						}
						refused.Add(1)
						continue
					}
					n, _, err := st.SettleSpend(ctx, store.SettleSpendParams{
						HoldKey: h.Key, Workspace: ws.ID, Rows: []store.UsageEntry{llmRow(ws, "llm:"+key, store.BilledToTest, cost, now)}, Now: now,
					})
					if err != nil || n != 1 {
						t.Errorf("settle: %d, %v", n, err)
					}
					settled.Add(1)
				}
			}()
		}
		wg.Wait()
		c, err := rig.a.WorkspaceSpend(ctx, ws.ID, store.DayPeriod(now))
		if err != nil {
			t.Fatal(err)
		}
		if c.TestMicros != settled.Load()*cost || c.TestMicros > wsCap {
			t.Fatalf("round %d: counter %d, settled %d x %d, cap %d", round, c.TestMicros, settled.Load(), cost, wsCap)
		}
		// Every settle removed its hold: nothing is held any more, so the rest of the cap is free (the cap check sees no phantoms).
		if _, _, err := rig.b.ReserveSpend(ctx, reserve(fmt.Sprintf("rest:%d", round), ws, store.BilledToTest, wsCap-c.TestMicros, caps, now)); err != nil {
			t.Fatalf("round %d: the rest of the cap (%d) was refused: %v", round, wsCap-c.TestMicros, err)
		}
	}
}

func TestSpendReserveRefusesNamingTheCap(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "caps")
	caps := store.SpendCaps{WorkspaceDailyMicros: 5_000, PlatformDailyMicros: 8_000, WorkspaceDailyAudioMs: 60_000}

	var capErr *store.SpendCapError
	// The workspace cap.
	_, _, err := rig.a.ReserveSpend(ctx, reserve("w1", w, store.BilledToPlatform, 5_001, caps, spendDay))
	if !errors.As(err, &capErr) || capErr.Cap != store.CapWorkspaceDaily || capErr.Room != 5_000 || !errors.Is(err, store.ErrSpendCapExceeded) {
		t.Fatalf("over the workspace cap: %v", err)
	}
	// The platform cap, with a workspace that has room.
	other := rig.workspace(t, "caps2")
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("p1", other, store.BilledToPlatform, 4_000, store.SpendCaps{WorkspaceDailyMicros: 50_000, PlatformDailyMicros: 8_000}, spendDay)); err != nil {
		t.Fatal(err)
	}
	_, _, err = rig.b.ReserveSpend(ctx, reserve("p2", w, store.BilledToPlatform, 4_001, store.SpendCaps{WorkspaceDailyMicros: 50_000, PlatformDailyMicros: 8_000}, spendDay))
	if !errors.As(err, &capErr) || capErr.Cap != store.CapPlatformDaily || capErr.Room != 4_000 {
		t.Fatalf("over the platform cap: %v", err)
	}
	// Audio.
	au := reserve("a1", w, store.BilledToTest, 0, caps, spendDay)
	au.Kind, au.AudioMs = store.KindTranscription, 61_000
	if _, _, err = rig.a.ReserveSpend(ctx, au); !errors.As(err, &capErr) || capErr.Cap != store.CapWorkspaceAudio {
		t.Fatalf("over the audio cap: %v", err)
	}
	au.AudioMs = 60_000
	if _, _, err = rig.a.ReserveSpend(ctx, au); err != nil {
		t.Fatalf("exactly the audio cap: %v", err)
	}
	// A cap that is not set refuses instead of letting the call through.
	if _, _, err = rig.a.ReserveSpend(ctx, reserve("n1", w, store.BilledToPlatform, 10, store.SpendCaps{WorkspaceDailyMicros: 5_000}, spendDay)); !errors.Is(err, store.ErrSpendCapNotSet) {
		t.Fatalf("no platform cap: %v", err)
	}
	// A call that costs nothing, and a call on the creator's own key, set nothing aside and never hit a cap.
	for _, b := range []string{store.BilledToWorkspaceKey} {
		h, _, err := rig.a.ReserveSpend(ctx, reserve("k-"+b, w, b, 1_000_000_000, store.SpendCaps{WorkspaceDailyMicros: 1, PlatformDailyMicros: 1}, spendDay))
		if err != nil || h.ID != uuid.Nil {
			t.Fatalf("%s: %+v, %v", b, h, err)
		}
	}
	if h, _, err := rig.a.ReserveSpend(ctx, reserve("free", w, store.BilledToPlatform, 0, store.SpendCaps{}, spendDay)); err != nil || h.ID != uuid.Nil {
		t.Fatalf("free call: %+v, %v", h, err)
	}
}

// Test-billed spend counts against the workspace's daily cap (as it always did, so a developer's local cap works) and never against
// the platform breaker; workspace_key spend is recorded and counts against neither.
func TestSpendBilledToDecidesWhichCapsCount(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "billed")
	caps := store.SpendCaps{WorkspaceDailyMicros: 3_000, PlatformDailyMicros: 3_000}
	settle := func(key, billed string, cost int64) {
		t.Helper()
		if _, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: w.ID, Rows: []store.UsageEntry{llmRow(w, key, billed, cost, spendDay)}, PlatformCapMicros: caps.PlatformDailyMicros, Now: spendDay}); err != nil {
			t.Fatal(err)
		}
	}
	settle("b1", store.BilledToTest, 2_500)
	settle("b2", store.BilledToWorkspaceKey, 9_999_999)
	plat, _ := rig.a.PlatformSpend(ctx, store.DayPeriod(spendDay))
	if plat.PlatformMicros != 0 {
		t.Fatalf("test and workspace_key spend reached the platform counter: %+v", plat)
	}
	wc, _ := rig.a.WorkspaceSpend(ctx, w.ID, store.DayPeriod(spendDay))
	if wc.TestMicros != 2_500 || wc.PlatformMicros != 0 {
		t.Fatalf("workspace counter = %+v", wc)
	}
	// 2_500 of test spend leaves 500 under the workspace cap, for a test-billed call too.
	var capErr *store.SpendCapError
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("t1", w, store.BilledToTest, 501, caps, spendDay)); !errors.As(err, &capErr) || capErr.Cap != store.CapWorkspaceDaily {
		t.Fatalf("test spend did not count against the workspace cap: %v", err)
	}
	// But a platform-billed call of another workspace is not blocked by it, and the breaker is untouched by test traffic.
	w2 := rig.workspace(t, "billed2")
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("t2", w2, store.BilledToPlatform, 3_000, caps, spendDay)); err != nil {
		t.Fatalf("another workspace's platform call: %v", err)
	}
	// The ledger has all three rows, workspace_key included.
	rows, err := rig.a.RecentUsage(ctx, w.ID, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ledger rows = %d, %v", len(rows), err)
	}
}

func TestSpendSettleIsIdempotent(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "idem")
	caps := store.SpendCaps{WorkspaceDailyMicros: 100_000, PlatformDailyMicros: 100_000}
	h, _, err := rig.a.ReserveSpend(ctx, reserve("idem-1", w, store.BilledToPlatform, 2_000, caps, spendDay))
	if err != nil || h.ID == uuid.Nil {
		t.Fatalf("reserve: %+v, %v", h, err)
	}
	p := store.SettleSpendParams{HoldKey: h.Key, Workspace: w.ID, Rows: []store.UsageEntry{llmRow(w, "llm:idem-1", store.BilledToPlatform, 1_500, spendDay)}, PlatformCapMicros: caps.PlatformDailyMicros, Now: spendDay}
	for i := range 3 {
		n, _, err := rig.a.SettleSpend(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 1, false: 0}[i == 0]; n != want {
			t.Fatalf("settle %d inserted %d rows, want %d", i, n, want)
		}
		// A replay from the other process, too.
		if _, _, err := rig.b.SettleSpend(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	plat, _ := rig.a.PlatformSpend(ctx, store.DayPeriod(spendDay))
	wc, _ := rig.a.WorkspaceSpend(ctx, w.ID, store.DayPeriod(spendDay))
	if plat.PlatformMicros != 1_500 || wc.PlatformMicros != 1_500 {
		t.Fatalf("a replayed settle counted again: platform %d, workspace %d", plat.PlatformMicros, wc.PlatformMicros)
	}
	if rows, _ := rig.a.RecentUsage(ctx, w.ID, 10); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// The hold is gone: the whole cap but the spent 1,500 is free.
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("idem-2", w, store.BilledToPlatform, 98_500, caps, spendDay)); err != nil {
		t.Fatalf("the settled hold still counts: %v", err)
	}
	// A replay of a reserve with the same key returns the hold and holds nothing more.
	h2, _, err := rig.b.ReserveSpend(ctx, reserve("idem-2", w, store.BilledToPlatform, 98_500, caps, spendDay))
	if err != nil || h2.Key != "idem-2" {
		t.Fatalf("replayed reserve: %+v, %v", h2, err)
	}
	// A request_id that is already in the ledger under another key is skipped as well.
	dup := llmRow(w, "llm:other-key", store.BilledToPlatform, 400, spendDay)
	first := llmRow(w, "llm:req-a", store.BilledToPlatform, 400, spendDay)
	first.RequestID, dup.RequestID = "req_same", "req_same"
	if n, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: w.ID, Rows: []store.UsageEntry{first, dup}, Now: spendDay}); err != nil || n != 1 {
		t.Fatalf("same request id twice: inserted %d, %v", n, err)
	}
}

func TestSpendReleaseGivesTheReservationBack(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "release")
	caps := store.SpendCaps{WorkspaceDailyMicros: 5_000, PlatformDailyMicros: 5_000}
	h, _, err := rig.a.ReserveSpend(ctx, reserve("rel-1", w, store.BilledToPlatform, 5_000, caps, spendDay))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("rel-2", w, store.BilledToPlatform, 1, caps, spendDay)); !errors.Is(err, store.ErrSpendCapExceeded) {
		t.Fatalf("a full cap let a call through: %v", err)
	}
	for range 2 { // releasing twice is fine
		if err := rig.b.ReleaseSpend(ctx, w.ID, h.Key); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("rel-3", w, store.BilledToPlatform, 5_000, caps, spendDay)); err != nil {
		t.Fatalf("after a release: %v", err)
	}
	if rows, _ := rig.a.RecentUsage(ctx, w.ID, 10); len(rows) != 0 {
		t.Fatalf("a release wrote %d ledger rows", len(rows))
	}
}

// A caller that crashes between reserve and settle leaves a hold. It stops counting at its expiry (without the sweep running), and
// the sweep then deletes it.
func TestSpendExpiredHoldStopsBlockingAndIsSwept(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "expiry")
	caps := store.SpendCaps{WorkspaceDailyMicros: 5_000, PlatformDailyMicros: 5_000}
	p := reserve("crash-1", w, store.BilledToPlatform, 5_000, caps, spendDay)
	p.TTL = 10 * time.Minute
	if _, _, err := rig.a.ReserveSpend(ctx, p); err != nil {
		t.Fatal(err)
	}
	// The "crashed" caller never settles. Before the expiry the cap is held; after it, it is not.
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("crash-2", w, store.BilledToPlatform, 1, caps, spendDay.Add(9*time.Minute))); !errors.Is(err, store.ErrSpendCapExceeded) {
		t.Fatalf("before expiry: %v", err)
	}
	later := spendDay.Add(11 * time.Minute)
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("crash-3", w, store.BilledToPlatform, 5_000, caps, later)); err != nil {
		t.Fatalf("an expired hold still blocks: %v", err)
	}
	// The sweep removes the expired one (crash-1) and leaves the live one (crash-3).
	res, err := rig.a.SweepExpiredSpendHolds(ctx, later)
	if err != nil || res.Holds != 1 || res.Micros != 5_000 {
		t.Fatalf("sweep = %+v, %v", res, err)
	}
	if res, err = rig.a.SweepExpiredSpendHolds(ctx, later); err != nil || res.Holds != 0 {
		t.Fatalf("second sweep = %+v, %v", res, err)
	}
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("crash-4", w, store.BilledToPlatform, 1, caps, later)); !errors.Is(err, store.ErrSpendCapExceeded) {
		t.Fatalf("the live hold was swept: %v", err)
	}
	// Settling a call whose hold the sweep took still records the cost.
	n, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{HoldKey: "crash-1", Workspace: w.ID, Rows: []store.UsageEntry{llmRow(w, "llm:crash-1", store.BilledToPlatform, 700, later)}, Now: later})
	if err != nil || n != 1 {
		t.Fatalf("late settle: %d, %v", n, err)
	}
}

type alertLog struct {
	mu   sync.Mutex
	pct  map[int]int
	list []store.SpendAlert
}

func (l *alertLog) add(as []store.SpendAlert) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pct == nil {
		l.pct = map[int]int{}
	}
	for _, a := range as {
		l.pct[a.Pct]++
		l.list = append(l.list, a)
	}
}

// The breaker trips and refuses; its 80 % and 100 % alerts each come back once per period, however many callers on however many
// pools cross them.
func TestSpendBreakerTripsAndAlertsOncePerCrossing(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	const platCap = int64(10_000)
	caps := store.SpendCaps{WorkspaceDailyMicros: 1 << 40, PlatformDailyMicros: platCap}
	var alerts alertLog
	var refused, settled atomic.Int64
	var wg sync.WaitGroup
	for proc, st := range []*store.Store{rig.a, rig.b} {
		for k := range 3 {
			ws := rig.workspace(t, fmt.Sprintf("br%d%d", proc, k))
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 12 {
					key := fmt.Sprintf("br:%d:%d:%d", proc, k, i)
					h, as, err := st.ReserveSpend(ctx, reserve(key, ws, store.BilledToPlatform, 700, caps, spendDay))
					alerts.add(as)
					if err != nil {
						if !errors.Is(err, store.ErrSpendCapExceeded) {
							t.Errorf("reserve: %v", err)
						}
						refused.Add(1)
						continue
					}
					_, as, err = st.SettleSpend(ctx, store.SettleSpendParams{
						HoldKey: h.Key, Workspace: ws.ID, Rows: []store.UsageEntry{llmRow(ws, "llm:"+key, store.BilledToPlatform, 700, spendDay)},
						PlatformCapMicros: platCap, Now: spendDay,
					})
					if err != nil {
						t.Errorf("settle: %v", err)
					}
					alerts.add(as)
					settled.Add(1)
				}
			}()
		}
	}
	wg.Wait()
	if refused.Load() == 0 {
		t.Fatal("the breaker never refused a call")
	}
	plat, _ := rig.a.PlatformSpend(ctx, store.DayPeriod(spendDay))
	if plat.PlatformMicros != settled.Load()*700 || plat.PlatformMicros > platCap {
		t.Fatalf("platform counter %d, settled %d x 700, cap %d", plat.PlatformMicros, settled.Load(), platCap)
	}
	if plat.Alerted80At == nil || plat.TrippedAt == nil {
		t.Fatalf("flags not set: %+v", plat)
	}
	alerts.mu.Lock()
	defer alerts.mu.Unlock()
	if alerts.pct[80] != 1 || alerts.pct[100] != 1 {
		t.Fatalf("alerts = %v, want one at 80 and one at 100: %+v", alerts.pct, alerts.list)
	}
	// Another day starts clean: its own counter, its own alerts.
	next := spendDay.Add(24 * time.Hour)
	ws := rig.workspace(t, "brnext")
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("br-next", ws, store.BilledToPlatform, 700, caps, next)); err != nil {
		t.Fatalf("the next day is still tripped: %v", err)
	}
}

// Holds and counters of one workspace never reach another.
func TestSpendHoldsAndCountersNeverCrossWorkspaces(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	a, b := rig.workspace(t, "ta"), rig.workspace(t, "tb")
	caps := store.SpendCaps{WorkspaceDailyMicros: 2_000, PlatformDailyMicros: 1 << 40, WorkspaceDailyAudioMs: 10_000}
	ha, _, err := rig.a.ReserveSpend(ctx, reserve("tenant-a", a, store.BilledToPlatform, 2_000, caps, spendDay))
	if err != nil {
		t.Fatal(err)
	}
	// A's full cap leaves B's untouched.
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("tenant-b", b, store.BilledToPlatform, 2_000, caps, spendDay)); err != nil {
		t.Fatalf("workspace B was blocked by A: %v", err)
	}
	// B cannot release or settle A's hold, nor read its key through a reserve.
	if err := rig.b.ReleaseSpend(ctx, b.ID, ha.Key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rig.b.SettleSpend(ctx, store.SettleSpendParams{HoldKey: ha.Key, Workspace: b.ID, Rows: []store.UsageEntry{llmRow(b, "llm:tenant-b", store.BilledToPlatform, 10, spendDay)}, Now: spendDay}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rig.a.ReserveSpend(ctx, reserve("tenant-a2", a, store.BilledToPlatform, 1, caps, spendDay)); !errors.Is(err, store.ErrSpendCapExceeded) {
		t.Fatalf("A's hold was removed by B: %v", err)
	}
	if _, _, err := rig.b.ReserveSpend(ctx, reserve("tenant-a", b, store.BilledToPlatform, 1, caps, spendDay)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("B reserved with A's key: %v", err)
	}
	// B's settle counted for B only.
	ca, _ := rig.a.WorkspaceSpend(ctx, a.ID, store.DayPeriod(spendDay))
	cb, _ := rig.a.WorkspaceSpend(ctx, b.ID, store.DayPeriod(spendDay))
	if ca.PlatformMicros != 0 || cb.PlatformMicros != 10 {
		t.Fatalf("counters: a=%+v b=%+v", ca, cb)
	}
	// A row for another workspace than the settle's is refused outright.
	if _, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: a.ID, Rows: []store.UsageEntry{llmRow(b, "llm:cross", store.BilledToPlatform, 1, spendDay)}, Now: spendDay}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("a cross-workspace row: %v", err)
	}
	// The audio minutes are per workspace as well.
	au := reserve("aud-a", a, store.BilledToTest, 0, caps, spendDay)
	au.Kind, au.AudioMs = store.KindTranscription, 10_000
	if _, _, err := rig.a.ReserveSpend(ctx, au); err != nil {
		t.Fatal(err)
	}
	au2 := reserve("aud-b", b, store.BilledToTest, 0, caps, spendDay)
	au2.Kind, au2.AudioMs = store.KindTranscription, 10_000
	if _, _, err := rig.b.ReserveSpend(ctx, au2); err != nil {
		t.Fatalf("B's audio blocked by A: %v", err)
	}
}

// The usage ledger is append-only for the app role: it can read and insert and nothing else. Counters cannot be deleted either.
func TestSpendAppRoleCannotUpdateOrDeleteTheLedger(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "append")
	if err := rig.a.InsertUsage(ctx, llmRow(w, "llm:append-1", store.BilledToTest, 5, spendDay)); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, rig.srv.DSN(rig.database, appLogin, rolePass))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, stmt := range []string{
		`UPDATE usage_ledger SET list_cost_micros = 0`,
		`UPDATE usage_ledger SET estimated = true WHERE false`,
		`DELETE FROM usage_ledger`,
		`DELETE FROM usage_ledger WHERE false`,
		`TRUNCATE usage_ledger`,
		`DELETE FROM spend_counters`,
		`TRUNCATE spend_counters`,
		`TRUNCATE spend_holds`,
	} {
		_, err := conn.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != permissionDenied {
			t.Errorf("%q: error = %v, want SQLSTATE %s", stmt, err, permissionDenied)
		}
	}
	for _, stmt := range []string{
		`SELECT count(*) FROM usage_ledger`,
		`SELECT count(*) FROM spend_counters`,
		`UPDATE spend_counters SET audio_ms = audio_ms WHERE false`,
		`DELETE FROM spend_holds WHERE false`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Errorf("%q should be allowed: %v", stmt, err)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM usage_ledger`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger rows = %d, %v", n, err)
	}
}

// Attribution columns reach the ledger and come back.
func TestSpendLedgerKeepsAttributionAndUnits(t *testing.T) {
	rig := newSpendRig(t)
	ctx := context.Background()
	w := rig.workspace(t, "attr")
	project := ids.ProjectID{UUID: uuid.Must(uuid.NewV7())}
	member := ids.UserID{UUID: uuid.Must(uuid.NewV7())}
	action := uuid.Must(uuid.NewV7())
	rows := []store.UsageEntry{
		{WorkspaceID: w.ID, Stage: "render", Kind: store.KindRender, Model: "render-vcpu", Quantity: 8_000, Unit: store.UnitVCPUMs,
			ListCostMicros: 160, PriceTableVersion: "2026-10b", BilledTo: store.BilledToTest, IdempotencyKey: "render:1", ProjectID: project, MemberUserID: member, ActionID: action, CreatedAt: spendDay},
		llmRow(w, "llm:attr", store.BilledToTest, 20, spendDay),
	}
	if _, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: w.ID, Rows: rows, Now: spendDay}); err != nil {
		t.Fatal(err)
	}
	got, err := rig.a.RecentUsage(ctx, w.ID, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("rows = %d, %v", len(got), err)
	}
	for _, r := range got {
		switch r.Kind {
		case store.KindRender:
			if r.Quantity != 8_000 || r.Unit != store.UnitVCPUMs || r.ProjectID != project || r.MemberUserID != member || r.ActionID != action || r.IdempotencyKey != "render:1" {
				t.Errorf("render row = %+v", r)
			}
		case store.KindLLM:
			if r.Quantity != 15 || r.Unit != store.UnitTokens || !r.ProjectID.IsZero() {
				t.Errorf("llm row = %+v", r)
			}
		}
	}
	// A row without a key, or with a unit the table does not know, is refused before it reaches the database.
	bad := llmRow(w, "", store.BilledToTest, 1, spendDay)
	if _, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: w.ID, Rows: []store.UsageEntry{bad}, Now: spendDay}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("no key: %v", err)
	}
	bad = llmRow(w, "llm:badunit", store.BilledToTest, 1, spendDay)
	bad.Kind, bad.Unit = store.KindMedia, "furlongs"
	if _, _, err := rig.a.SettleSpend(ctx, store.SettleSpendParams{Workspace: w.ID, Rows: []store.UsageEntry{bad}, Now: spendDay}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("bad unit: %v", err)
	}
}

// Rows from before the migration get 'legacy:<id>' keys, their quantity and unit, and the ledger works after.
func TestSpendMigrationBackfillsLegacyRows(t *testing.T) {
	srv := storetest.Start(t)
	database := srv.NewDatabase(t, "")
	dsn := srv.SuperDSN(database)
	ctx := context.Background()
	m, err := store.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	srv.Exec(t, database,
		`INSERT INTO workspaces (id, clerk_org_id, name) VALUES ('0192f3a0-0000-7000-8000-000000000001', 'org_legacy', 'legacy')`,
		`INSERT INTO usage_ledger (workspace_id, stage, kind, model, input_tokens, output_tokens, list_cost_micros, price_table_version, billed_to, request_id)
		 VALUES ('0192f3a0-0000-7000-8000-000000000001', 'editor', 'llm', 'm', 100, 20, 5, '2026-10', 'test', 'req_legacy')`,
		`INSERT INTO usage_ledger (workspace_id, stage, kind, model, input_tokens, output_tokens, list_cost_micros, price_table_version, billed_to, provider, audio_ms)
		 VALUES ('0192f3a0-0000-7000-8000-000000000001', 'transcription', 'transcription', 'w', 0, 0, 7, 'x', 'test', 'workersai', 90000)`,
		// Platform-billed today, a creator-key call today, and a platform call on an older day.
		`INSERT INTO usage_ledger (workspace_id, stage, kind, model, input_tokens, output_tokens, list_cost_micros, price_table_version, billed_to)
		 VALUES ('0192f3a0-0000-7000-8000-000000000001', 'editor', 'llm', 'm', 1, 1, 11, '2026-10', 'platform')`,
		`INSERT INTO usage_ledger (workspace_id, stage, kind, model, input_tokens, output_tokens, list_cost_micros, price_table_version, billed_to)
		 VALUES ('0192f3a0-0000-7000-8000-000000000001', 'editor', 'llm', 'm', 1, 1, 1000, '2026-10', 'workspace_key')`,
		`INSERT INTO usage_ledger (workspace_id, stage, kind, model, input_tokens, output_tokens, list_cost_micros, price_table_version, billed_to, created_at)
		 VALUES ('0192f3a0-0000-7000-8000-000000000001', 'editor', 'llm', 'm', 1, 1, 13, '2026-10', 'platform', '2026-01-02 23:30:00+00')`,
	)
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dsn)
	rows, err := st.RecentUsage(ctx, ids.WorkspaceID{UUID: uuid.MustParse("0192f3a0-0000-7000-8000-000000000001")}, 10)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	// The counters start from the ledger, so the day's caps do not reset when the migration deploys.
	wsID := ids.WorkspaceID{UUID: uuid.MustParse("0192f3a0-0000-7000-8000-000000000001")}
	today := store.DayPeriod(time.Now())
	if c, err := st.WorkspaceSpend(ctx, wsID, today); err != nil || c.PlatformMicros != 11 || c.TestMicros != 12 || c.AudioMs != 90000 {
		t.Errorf("today's workspace counter = %+v, %v (want platform 11, test 12, audio 90000; the creator-key row counts for nothing)", c, err)
	}
	if c, err := st.PlatformSpend(ctx, today); err != nil || c.PlatformMicros != 11 {
		t.Errorf("today's platform counter = %+v, %v", c, err)
	}
	if c, err := st.PlatformSpend(ctx, "day:2026-01-02"); err != nil || c.PlatformMicros != 13 {
		t.Errorf("the older day's platform counter = %+v, %v", c, err)
	}
	for _, r := range rows {
		if r.IdempotencyKey != "legacy:"+r.ID.String() {
			t.Errorf("key = %q", r.IdempotencyKey)
		}
		switch r.Kind {
		case store.KindLLM:
			if r.Quantity != r.InputTokens+r.OutputTokens || r.Unit != store.UnitTokens {
				t.Errorf("llm row = %+v", r)
			}
		case store.KindTranscription:
			if r.Quantity != 90000 || r.Unit != store.UnitAudioMs {
				t.Errorf("transcription row = %+v", r)
			}
		}
	}
}
