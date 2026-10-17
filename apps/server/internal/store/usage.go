package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store/db"
)

// The usage ledger (P4-00a, architecture 11a): one append-only row per metered call. internal/agents/llm writes it
// and reads the day's total for the per-workspace spend cap.

// Who pays for a ledger row (the billed_to column).
const (
	BilledToPlatform     = "platform"
	BilledToWorkspaceKey = "workspace_key"
	BilledToTest         = "test"
)

// UsageEntry is one ledger row. RunID is uuid.Nil for a call outside an agent run. CreatedAt zero means now.
type UsageEntry struct {
	WorkspaceID        ids.WorkspaceID
	RunID              uuid.UUID
	Stage              string
	Kind               string // "llm" when empty
	Model              string
	InputTokens        int64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	CacheReadTokens    int64
	OutputTokens       int64
	ListCostMicros     int64
	PriceTableVersion  string
	BilledTo           string
	RequestID          string
	// Estimated marks a row written for a call whose usage never came back: ListCostMicros is the reserved worst case.
	Estimated bool
	CreatedAt time.Time
}

// UsageRow is a ledger row as read back.
type UsageRow struct {
	ID uuid.UUID
	UsageEntry
}

// InsertUsage appends a ledger row. A row whose RequestID is already in the ledger is ignored (the call is already
// counted), so a retried insert is safe.
func (s *Store) InsertUsage(ctx context.Context, e UsageEntry) error {
	if e.Kind == "" {
		e.Kind = "llm"
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	err := s.q.InsertUsageLedger(ctx, db.InsertUsageLedgerParams{
		WorkspaceID:        e.WorkspaceID,
		RunID:              pgUUID(e.RunID),
		Stage:              e.Stage,
		Kind:               e.Kind,
		Model:              e.Model,
		InputTokens:        e.InputTokens,
		CacheWriteTokens:   e.CacheWriteTokens,
		CacheWrite1hTokens: e.CacheWrite1hTokens,
		CacheReadTokens:    e.CacheReadTokens,
		OutputTokens:       e.OutputTokens,
		ListCostMicros:     e.ListCostMicros,
		PriceTableVersion:  e.PriceTableVersion,
		BilledTo:           e.BilledTo,
		RequestID:          e.RequestID,
		Estimated:          e.Estimated,
		CreatedAt:          e.CreatedAt,
	})
	return mapErr(err)
}

// UsageCostMicros returns what the workspace spent in [from, to) at list price, whoever pays.
func (s *Store) UsageCostMicros(ctx context.Context, ws ids.WorkspaceID, from, to time.Time) (int64, error) {
	n, err := s.q.SumUsageCostMicros(ctx, db.SumUsageCostMicrosParams{WorkspaceID: ws, FromTime: from, ToTime: to})
	return n, mapErr(err)
}

// UsageForRun returns the ledger rows of one run, oldest first.
func (s *Store) UsageForRun(ctx context.Context, ws ids.WorkspaceID, run uuid.UUID) ([]UsageRow, error) {
	rows, err := s.q.ListUsageLedgerForRun(ctx, db.ListUsageLedgerForRunParams{WorkspaceID: ws, RunID: pgUUID(run)})
	if err != nil {
		return nil, mapErr(err)
	}
	return usageRows(rows), nil
}

// RecentUsage returns the newest ledger rows of the workspace, newest first, at most limit (1 to 1000).
func (s *Store) RecentUsage(ctx context.Context, ws ids.WorkspaceID, limit int) ([]UsageRow, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, limit)
	}
	rows, err := s.q.ListRecentUsageLedger(ctx, db.ListRecentUsageLedgerParams{WorkspaceID: ws, RowLimit: int32(limit)})
	if err != nil {
		return nil, mapErr(err)
	}
	return usageRows(rows), nil
}

func usageRows(rows []db.UsageLedger) []UsageRow {
	out := make([]UsageRow, 0, len(rows))
	for _, r := range rows {
		var run uuid.UUID
		if r.RunID.Valid {
			run = r.RunID.Bytes
		}
		out = append(out, UsageRow{ID: r.ID, UsageEntry: UsageEntry{
			WorkspaceID: r.WorkspaceID, RunID: run, Stage: r.Stage, Kind: r.Kind, Model: r.Model,
			InputTokens: r.InputTokens, CacheWriteTokens: r.CacheWriteTokens, CacheWrite1hTokens: r.CacheWrite1hTokens,
			CacheReadTokens: r.CacheReadTokens, OutputTokens: r.OutputTokens, ListCostMicros: r.ListCostMicros,
			PriceTableVersion: r.PriceTableVersion, BilledTo: r.BilledTo, RequestID: r.RequestID, Estimated: r.Estimated, CreatedAt: r.CreatedAt,
		}})
	}
	return out
}

// pgUUID is the nullable column value: uuid.Nil means null.
func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: u != uuid.Nil} }
