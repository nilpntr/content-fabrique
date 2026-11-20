package store

import (
	"context"
	"errors"
	"time"
)

// Test spend (the cost guard): what development, ci and bench have spent, across every workspace of the database, from the usage ledger
// (billed_to = test, D-21). cfctl spend summary prints it against the founder's test cap, and the eval commands refuse a run whose
// estimate would pass the cap. The queries are read-only and hand-written: they read the whole ledger, which the per-workspace sqlc
// queries never do.

// SpendSlice is the test spend of one day, run type or model.
type SpendSlice struct {
	Key    string
	Micros int64
	// Calls is the number of ledger rows (a model call, a transcription chunk, a render, ...).
	Calls int64
}

// TestSpendSummary is the test spend of the whole database.
type TestSpendSummary struct {
	// TotalMicros is the ledger's test spend at list price; Calls its rows, Estimated the rows whose usage never came back (their cost is
	// the reserved worst case).
	TotalMicros, Calls, Estimated int64
	// HeldMicros is what test calls in flight have reserved and not settled yet (live holds).
	HeldMicros int64
	// First and Last are the oldest and newest row (nil when there is none).
	First, Last *time.Time
	// ByDay is newest first (UTC dates, YYYY-MM-DD); ByRunType (the ledger's stage, with the kind in front for a cost that is not a model
	// call) and ByModel are largest first.
	ByDay, ByRunType, ByModel []SpendSlice
}

// ErrNeedsPool is returned by the whole-ledger reads inside a transaction.
var ErrNeedsPool = errors.New("store: this read needs the pool store")

// TestSpendMicros is the test spend so far: the ledger's billed_to = test rows plus the live test holds, so a call in flight counts.
func (s *Store) TestSpendMicros(ctx context.Context, now time.Time) (int64, error) {
	if s.pool == nil {
		return 0, ErrNeedsPool
	}
	var micros int64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT COALESCE(sum(list_cost_micros), 0) FROM usage_ledger WHERE billed_to = 'test')::bigint +
		(SELECT COALESCE(sum(micros), 0) FROM spend_holds WHERE billed_to = 'test' AND scope = 'workspace' AND expires_at > $1)::bigint`, now).Scan(&micros)
	return micros, mapErr(err)
}

// TestSpend summarises the ledger's test spend: the total, and by day, by run type and by model. days limits ByDay to the newest days
// (0 means all).
func (s *Store) TestSpend(ctx context.Context, now time.Time, days int) (TestSpendSummary, error) {
	if s.pool == nil {
		return TestSpendSummary{}, ErrNeedsPool
	}
	var sum TestSpendSummary
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(list_cost_micros), 0)::bigint, count(*), count(*) FILTER (WHERE estimated), min(created_at), max(created_at)
		FROM usage_ledger WHERE billed_to = 'test'`).Scan(&sum.TotalMicros, &sum.Calls, &sum.Estimated, &sum.First, &sum.Last)
	if err != nil {
		return TestSpendSummary{}, mapErr(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(micros), 0)::bigint FROM spend_holds WHERE billed_to = 'test' AND scope = 'workspace' AND expires_at > $1`,
		now).Scan(&sum.HeldMicros); err != nil {
		return TestSpendSummary{}, mapErr(err)
	}
	limit := any(nil)
	if days > 0 {
		limit = days
	}
	if sum.ByDay, err = s.spendSlices(ctx, `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS k, sum(list_cost_micros)::bigint, count(*)
		FROM usage_ledger WHERE billed_to = 'test' GROUP BY k ORDER BY k DESC LIMIT $1`, limit); err != nil {
		return TestSpendSummary{}, err
	}
	if sum.ByRunType, err = s.spendSlices(ctx, `SELECT CASE WHEN kind = 'llm' THEN stage ELSE kind || ':' || stage END AS k, sum(list_cost_micros)::bigint, count(*)
		FROM usage_ledger WHERE billed_to = 'test' GROUP BY k ORDER BY 2 DESC, k LIMIT $1`, nil); err != nil {
		return TestSpendSummary{}, err
	}
	if sum.ByModel, err = s.spendSlices(ctx, `SELECT model AS k, sum(list_cost_micros)::bigint, count(*)
		FROM usage_ledger WHERE billed_to = 'test' GROUP BY k ORDER BY 2 DESC, k LIMIT $1`, nil); err != nil {
		return TestSpendSummary{}, err
	}
	return sum, nil
}

func (s *Store) spendSlices(ctx context.Context, query string, limit any) ([]SpendSlice, error) {
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []SpendSlice
	for rows.Next() {
		var sl SpendSlice
		if err := rows.Scan(&sl.Key, &sl.Micros, &sl.Calls); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, sl)
	}
	return out, mapErr(rows.Err())
}
