// Package spendtest is an in-memory spend.Backend for the unit tests of the packages that spend (llm, transcribe, quicklook, the
// render and media metering): the same arithmetic as the database (store.CheckSpendCaps), the same idempotency on the rows' keys and
// request ids, and the same once-per-period alert flags, with knobs to make the "database" fail. The race, lock-order and
// permission tests run against real Postgres (internal/store).
package spendtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

var _ spend.Backend = (*Memory)(nil)

// ErrDown is what the failure knobs return.
var ErrDown = errors.New("spendtest: database down")

// Memory holds ledger rows and holds in memory. The zero value is ready. Counters are derived from the rows, so Seed stands for
// spend that was settled earlier (by this process or another).
type Memory struct {
	mu    sync.Mutex
	rows  []store.UsageEntry
	holds map[string]store.SpendHold

	alerted80 map[string]bool
	tripped   map[string]bool

	// FailReserve, when set, is returned by every ReserveSpend (the database cannot be asked: the call must not go out).
	FailReserve error
	// FailSettle makes the next n SettleSpend calls fail before writing anything.
	FailSettle int
	// FailSettleAfterWrite makes the next n SettleSpend calls write everything and then fail, as a commit whose answer was lost.
	FailSettleAfterWrite int

	// Reserves, Settles and Releases count the calls made, failed ones included.
	Reserves, Settles, Releases int
}

// Seed adds rows as if they had been settled earlier. A row without a key gets one.
func (m *Memory) Seed(rows ...store.UsageEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		if r.IdempotencyKey == "" {
			r.IdempotencyKey = fmt.Sprintf("seed:%d", len(m.rows))
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}
		m.rows = append(m.rows, r)
	}
}

// Rows returns a copy of the ledger rows, oldest first.
func (m *Memory) Rows() []store.UsageEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.rows)
}

// Holds returns the holds that are still in place (expired ones included: the sweep has not run).
func (m *Memory) Holds() []store.SpendHold {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.SpendHold, 0, len(m.holds))
	for _, h := range m.holds {
		out = append(out, h)
	}
	return out
}

// Counter is the counter of the workspace for the period, derived from the rows. A zero workspace gives the platform's.
func (m *Memory) Counter(ws ids.WorkspaceID, period string) store.SpendCounter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counter(ws, period)
}

func (m *Memory) counter(ws ids.WorkspaceID, period string) store.SpendCounter {
	c := store.SpendCounter{Period: period}
	for _, r := range m.rows {
		if store.DayPeriod(r.CreatedAt) != period || (!ws.IsZero() && r.WorkspaceID != ws) {
			continue
		}
		switch r.BilledTo {
		case store.BilledToPlatform:
			c.PlatformMicros += r.ListCostMicros
		case store.BilledToTest:
			if !ws.IsZero() {
				c.TestMicros += r.ListCostMicros
			}
		}
		if r.Kind == store.KindTranscription && !ws.IsZero() {
			c.AudioMs += r.AudioMs
		}
	}
	return c
}

// ReserveSpend implements spend.Backend.
func (m *Memory) ReserveSpend(_ context.Context, p store.ReserveSpendParams) (store.SpendHold, []store.SpendAlert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Reserves++
	if m.FailReserve != nil {
		return store.SpendHold{}, nil, m.FailReserve
	}
	now := p.Now.UTC()
	period := store.DayPeriod(now)
	hold := store.SpendHold{Key: p.Key, Workspace: p.Workspace, BilledTo: p.BilledTo, Kind: p.Kind, Period: period, Micros: p.Micros, AudioMs: p.AudioMs, ExpiresAt: now.Add(p.TTL)}

	platformMicros := p.BilledTo == store.BilledToPlatform && p.Micros > 0
	workspaceMicros := p.BilledTo != store.BilledToWorkspaceKey && p.Micros > 0
	workspaceAudio := p.AudioMs > 0
	if !platformMicros && !workspaceMicros && !workspaceAudio {
		return hold, nil, nil
	}
	if platformMicros && p.Caps.PlatformDailyMicros <= 0 || workspaceMicros && p.Caps.WorkspaceDailyMicros <= 0 {
		return store.SpendHold{}, nil, store.ErrSpendCapNotSet
	}
	if old, ok := m.holds[p.Key]; ok && old.ExpiresAt.After(now) {
		return old, nil, nil
	}

	var st store.SpendState
	var alerts []store.SpendAlert
	if platformMicros {
		c := m.counter(ids.WorkspaceID{}, period)
		st.PlatformSpent = c.PlatformMicros
		for _, h := range m.holds {
			if h.BilledTo == store.BilledToPlatform && h.Period == period && h.ExpiresAt.After(now) {
				st.PlatformHeld += h.Micros
			}
		}
		if m.alerted80 == nil {
			m.alerted80, m.tripped = map[string]bool{}, map[string]bool{}
		}
		if limit := p.Caps.PlatformDailyMicros; c.PlatformMicros*100 >= limit*80 && !m.alerted80[period] {
			m.alerted80[period] = true
			alerts = append(alerts, store.SpendAlert{Type: store.AlertPlatformBreaker, Pct: 80, Period: period, SpentMicros: c.PlatformMicros, CapMicros: limit})
		}
	}
	if workspaceMicros || workspaceAudio {
		c := m.counter(p.Workspace, period)
		st.WorkspaceSpent, st.WorkspaceAudio = c.PlatformMicros+c.TestMicros, c.AudioMs
		for _, h := range m.holds {
			if h.Workspace != p.Workspace || h.Period != period || !h.ExpiresAt.After(now) {
				continue
			}
			if h.BilledTo != store.BilledToWorkspaceKey {
				st.WorkspaceHeld += h.Micros
			}
			st.WorkspaceHeldAudio += h.AudioMs
		}
	}
	if refusal := store.CheckSpendCaps(p, st); refusal != nil {
		if refusal.Cap == store.CapPlatformDaily && !m.tripped[period] {
			m.tripped[period] = true
			alerts = append(alerts, store.SpendAlert{Type: store.AlertPlatformBreaker, Pct: 100, Period: period, SpentMicros: st.PlatformSpent, CapMicros: p.Caps.PlatformDailyMicros})
		}
		return store.SpendHold{}, alerts, refusal
	}
	if m.holds == nil {
		m.holds = map[string]store.SpendHold{}
	}
	hold.ID = newID()
	m.holds[p.Key] = hold
	return hold, alerts, nil
}

// SettleSpend implements spend.Backend.
func (m *Memory) SettleSpend(_ context.Context, p store.SettleSpendParams) (int, []store.SpendAlert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Settles++
	if m.FailSettle > 0 {
		m.FailSettle--
		return 0, nil, ErrDown
	}
	inserted := 0
	for _, r := range p.Rows {
		if r.WorkspaceID.IsZero() {
			r.WorkspaceID = p.Workspace
		}
		if r.IdempotencyKey == "" {
			return 0, nil, errors.Join(store.ErrInvalid, errors.New("a ledger row needs an idempotency key"))
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = p.Now
		}
		dup := false
		for _, old := range m.rows {
			if old.IdempotencyKey == r.IdempotencyKey || (r.RequestID != "" && old.RequestID == r.RequestID) {
				dup = true
				break
			}
		}
		if !dup {
			m.rows = append(m.rows, r)
			inserted++
		}
	}
	if p.HoldKey != "" {
		delete(m.holds, p.HoldKey)
	}
	if m.FailSettleAfterWrite > 0 {
		m.FailSettleAfterWrite--
		return 0, nil, ErrDown
	}
	return inserted, nil, nil
}

// ReleaseSpend implements spend.Backend.
func (m *Memory) ReleaseSpend(_ context.Context, _ ids.WorkspaceID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Releases++
	delete(m.holds, key)
	return nil
}

var idMu sync.Mutex
var idN byte

func newID() [16]byte {
	idMu.Lock()
	defer idMu.Unlock()
	idN++
	var id [16]byte
	id[15], id[0] = idN, 1
	return id
}
