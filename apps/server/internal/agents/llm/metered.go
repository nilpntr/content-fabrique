package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Ledger is the usage_ledger. *store.Store implements it.
type Ledger interface {
	// InsertUsage appends one row. A row with a request id that is already there is ignored.
	InsertUsage(ctx context.Context, e store.UsageEntry) error
	// UsageCostMicros is what the workspace spent in [from, to) at list price.
	UsageCostMicros(ctx context.Context, ws ids.WorkspaceID, from, to time.Time) (int64, error)
}

var _ Ledger = (*store.Store)(nil)

// ledgerAttempts and ledgerTimeout bound the effort to write a row once the API has answered.
const (
	ledgerAttempts = 3
	ledgerTimeout  = 10 * time.Second
)

// metered is the outermost client. It resolves image references, refuses a call that could break a spend cap, and
// writes the ledger row of the call.
//
// In replay mode nothing was spent, so no ledger row is written and the daily cap is not consulted; the run budget
// is still charged with the cost the recorded usage would have had, so budget behaviour under replay matches live.
type metered struct {
	inner   Client
	ws      ids.WorkspaceID
	stage   string
	billing Billing
	mode    Mode

	prices        *PriceTable
	ledger        Ledger
	images        ImageResolver
	maxRequest    int64
	dailyCap      int64
	allowUnpriced bool

	reserve *reservations

	now func() time.Time
	log *slog.Logger
}

// CountTokens implements Client.
func (m *metered) CountTokens(ctx context.Context, req Request) (int64, error) {
	req, err := m.resolve(ctx, req)
	if err != nil {
		return 0, err
	}
	return m.inner.CountTokens(ctx, req)
}

// Messages implements Client.
func (m *metered) Messages(ctx context.Context, req Request) (Response, error) {
	req, err := m.resolve(ctx, req)
	if err != nil {
		return Response{}, err
	}
	priced := m.prices.Priced(req.Model)
	if !priced {
		if !m.allowUnpriced {
			return Response{}, &UnpricedError{Model: req.Model, TableVersion: m.prices.Version}
		}
		m.log.Warn("llm: calling an unpriced model (LLM_ALLOW_UNPRICED=1): spend caps cannot be checked and the ledger records cost 0", "model", req.Model, "stage", m.stage)
	} else {
		// The reservation is held until the ledger row is written (the defer), so the daily cap never sees a gap.
		release, err := m.checkCaps(ctx, req)
		if err != nil {
			return Response{}, err
		}
		defer release()
	}

	resp, callErr := m.inner.Messages(ctx, req)
	if resp.Usage.IsZero() {
		return resp, callErr
	}

	cost, version := int64(0), UnpricedVersion
	if priced {
		var err error
		if cost, err = m.prices.Cost(req.Model, resp.Usage); err != nil {
			return resp, errors.Join(callErr, err) // unreachable while priced; fail closed if it ever is not
		}
		version = m.prices.Version
	}
	if b := req.Run.Budget; b != nil {
		b.Spend(cost)
	}
	if m.mode == ModeReplay {
		return resp, callErr
	}
	entry := store.UsageEntry{
		WorkspaceID: m.ws, RunID: req.Run.ID, Stage: m.stage, Kind: "llm", Model: req.Model,
		InputTokens: resp.Usage.InputTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheWrite1hTokens: resp.Usage.CacheWrite1hTokens,
		CacheReadTokens: resp.Usage.CacheReadTokens, OutputTokens: resp.Usage.OutputTokens,
		ListCostMicros: cost, PriceTableVersion: version, BilledTo: string(m.billing), RequestID: resp.RequestID, CreatedAt: m.now().UTC(),
	}
	if lerr := m.writeLedger(ctx, entry); lerr != nil {
		return resp, errors.Join(callErr, &LedgerError{Err: lerr})
	}
	return resp, callErr
}

// writeLedger inserts the row, retrying a few times, and detached from the caller's cancellation: the call is already
// paid for, so a cancelled run must still be metered.
func (m *metered) writeLedger(ctx context.Context, e store.UsageEntry) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerTimeout)
	defer cancel()
	var err error
	for attempt := 1; ; attempt++ {
		if err = m.ledger.InsertUsage(ctx, e); err == nil {
			return nil
		}
		if attempt >= ledgerAttempts || sleepCtx(ctx, time.Duration(attempt)*200*time.Millisecond) != nil {
			break
		}
	}
	// The row's content is not secret (no key, no prompt): log it so the ledger can be reconciled by hand.
	m.log.Error("llm: usage ledger write failed", "error", err, "workspace_id", e.WorkspaceID.String(), "run_id", e.RunID.String(),
		"stage", e.Stage, "model", e.Model, "input_tokens", e.InputTokens, "cache_write_tokens", e.CacheWriteTokens,
		"cache_write_1h_tokens", e.CacheWrite1hTokens, "cache_read_tokens", e.CacheReadTokens, "output_tokens", e.OutputTokens,
		"list_cost_micros", e.ListCostMicros, "price_table_version", e.PriceTableVersion, "billed_to", e.BilledTo, "request_id", e.RequestID)
	return err
}

// checkCaps estimates the worst-case cost of the request (counted input tokens at the dearest rate they could be
// billed, plus max_tokens at the output price) and refuses it, before anything is sent, when that is over a cap.
//
// On success it returns the function that releases the daily-cap reservation of this call; call it once the ledger row
// is written.
func (m *metered) checkCaps(ctx context.Context, req Request) (release func(), err error) {
	n, err := m.inner.CountTokens(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm: counting tokens before sending: %w", err)
	}
	est, err := m.prices.EstimateMax(req.Model, n, int64(req.MaxTokens), cacheMarks(req))
	if err != nil {
		return nil, err
	}
	if est > m.maxRequest {
		return nil, &CapError{Kind: CapRequest, EstimateMicros: est, LimitMicros: m.maxRequest}
	}
	if b := req.Run.Budget; b != nil {
		if room := max(b.Remaining(), 0); est > room {
			return nil, &CapError{Kind: CapRun, EstimateMicros: est, LimitMicros: room}
		}
	}
	if m.mode == ModeReplay {
		return func() {}, nil
	}
	now := m.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := m.ledger.UsageCostMicros(ctx, m.ws, day, day.Add(24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("llm: reading today's spend before sending: %w", err) // fail closed
	}
	// Calls of this process that are in flight are not in the ledger yet: count their worst cases too, so a burst of
	// concurrent calls cannot all pass the check on the same stale total. (Other processes' in-flight calls are not
	// seen; the overshoot is bounded by their concurrency and the per-request cap.)
	reserved, ok := m.reserve.tryReserve(m.ws, est, m.dailyCap-spent)
	if !ok {
		return nil, &CapError{Kind: CapDaily, EstimateMicros: est, LimitMicros: max(m.dailyCap-spent-reserved, 0)}
	}
	return func() { m.reserve.release(m.ws, est) }, nil
}

// reservations is the worst-case cost of the calls in flight, per workspace, shared by every client of a factory.
type reservations struct {
	mu sync.Mutex
	m  map[ids.WorkspaceID]int64
}

// tryReserve adds est to the workspace's reservations if it fits in room (the cap minus what the ledger says was
// spent) next to what is already reserved. It returns the amount that was reserved before this call.
func (r *reservations) tryReserve(ws ids.WorkspaceID, est, room int64) (reservedBefore int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[ids.WorkspaceID]int64{}
	}
	reservedBefore = r.m[ws]
	if est > room-reservedBefore {
		return reservedBefore, false
	}
	r.m[ws] = reservedBefore + est
	return reservedBefore, true
}

func (r *reservations) release(ws ids.WorkspaceID, est int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if left := r.m[ws] - est; left > 0 {
		r.m[ws] = left
	} else {
		delete(r.m, ws)
	}
}

func cacheMarks(req Request) CacheMarks {
	var cm CacheMarks
	note := func(c *CacheControl) {
		if c != nil {
			cm.Any = true
			if c.TTL == "1h" {
				cm.OneHour = true
			}
		}
	}
	var walk func(blocks []Block)
	walk = func(blocks []Block) {
		for _, b := range blocks {
			note(b.Cache)
			walk(b.Content)
		}
	}
	walk(req.System)
	for _, m := range req.Messages {
		walk(m.Content)
	}
	for _, t := range req.Tools {
		note(t.Cache)
	}
	return cm
}

// resolve returns the request with every image reference replaced by its bytes. The request passed in is not changed.
func (m *metered) resolve(ctx context.Context, req Request) (Request, error) {
	var err error
	if req.System, err = m.resolveBlocks(ctx, req.System); err != nil {
		return req, err
	}
	msgs := make([]Message, len(req.Messages))
	for i, msg := range req.Messages {
		msgs[i] = msg
		if msgs[i].Content, err = m.resolveBlocks(ctx, msg.Content); err != nil {
			return req, err
		}
	}
	req.Messages = msgs
	return req, nil
}

func (m *metered) resolveBlocks(ctx context.Context, blocks []Block) ([]Block, error) {
	out := make([]Block, len(blocks))
	for i, b := range blocks {
		out[i] = b
		switch {
		case b.Type == BlockImage && b.Image != nil && len(b.Image.Data) == 0 && b.Image.Ref != "":
			if m.images == nil {
				return nil, errors.New("llm: a request carries an image reference but no ImageResolver is configured")
			}
			mediaType, data, err := m.images.ResolveImage(ctx, b.Image.Ref)
			if err != nil {
				return nil, fmt.Errorf("llm: resolving image reference: %w", err)
			}
			if len(data) == 0 || mediaType == "" {
				return nil, errors.New("llm: the image resolver returned no bytes or no media type")
			}
			out[i].Image = &Image{MediaType: mediaType, Data: data, Ref: b.Image.Ref}
		case b.Type == BlockToolResult:
			inner, err := m.resolveBlocks(ctx, b.Content)
			if err != nil {
				return nil, err
			}
			out[i].Content = inner
		}
	}
	return out, nil
}
