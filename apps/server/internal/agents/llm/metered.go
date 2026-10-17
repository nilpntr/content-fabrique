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

// metered is the outermost client. It resolves image references, reserves the worst-case cost of a call against every
// cap before sending it (refusing the call when a cap cannot cover it), and writes the ledger row of the call.
//
// In replay mode nothing was spent, so no ledger row is written and the daily cap is not consulted; the run budget
// is still reserved and settled with the cost the recorded usage would have had, so budget behaviour matches live.
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

// reservation is what one call has set aside: its worst-case cost against the run's budget and against the
// workspace's daily cap.
type reservation struct {
	// est is the worst-case cost; tokens the input tokens it was worked out from (counted, or an upper bound).
	est    int64
	tokens int64

	budget  func(actual int64) // settles the run budget; nil without one
	daily   func()             // releases the in-process daily reservation; nil in replay
	settled bool
}

// settle charges the run's budget with what the call really cost. Only the first call counts.
func (r *reservation) settle(actual int64) {
	if r.settled {
		return
	}
	r.settled = true
	if r.budget != nil {
		r.budget(actual)
	}
}

// done is the deferred clean-up of every path: a call that never settled cost nothing, and the daily reservation goes
// once the ledger row (if any) is written.
func (r *reservation) done() {
	r.settle(0)
	if r.daily != nil {
		r.daily()
		r.daily = nil
	}
}

// Messages implements Client.
func (m *metered) Messages(ctx context.Context, req Request) (Response, error) {
	req, err := m.resolve(ctx, req)
	if err != nil {
		return Response{}, err
	}
	priced := m.prices.Priced(req.Model)
	rsv := &reservation{}
	if !priced {
		if !m.allowUnpriced {
			return Response{}, &UnpricedError{Model: req.Model, TableVersion: m.prices.Version}
		}
		m.log.Warn("llm: calling an unpriced model (LLM_ALLOW_UNPRICED=1): spend caps cannot be checked and the ledger records cost 0", "model", req.Model, "stage", m.stage)
	} else if rsv, err = m.reserveCaps(ctx, req); err != nil {
		return Response{}, err
	}
	// The reservations are held until the ledger row is written: the budget settles with the real cost, and the daily
	// reservation is released only after the row exists, so a concurrent call sees this one either as a reservation or
	// as a row, never as neither.
	defer rsv.done()

	marks := cacheMarks(req)
	resp, callErr := m.inner.Messages(ctx, req)
	if resp.Usage.IsZero() {
		// The request went out and nothing came back (a timeout, a reset, a cut-off body): it may have run and been
		// billed. Charge the worst case, so caps and budgets stay conservative, and say so in the ledger.
		if priced && callErr != nil && errors.Is(callErr, ErrOutcomeUnknown) {
			rsv.settle(rsv.est)
			if m.mode != ModeReplay {
				row := m.row(req, rsv.est, m.prices.Version, true, Usage{InputTokens: rsv.tokens, OutputTokens: int64(req.MaxTokens)}, "")
				if lerr := m.writeLedger(ctx, row); lerr != nil {
					return resp, errors.Join(callErr, &LedgerError{Err: lerr})
				}
			}
		}
		return resp, callErr
	}

	if resp.Usage.CacheWriteSplitMissing && marks.OneHour {
		// The response did not say how long the cache writes live: the request asked for a 1-hour breakpoint, so price
		// them at the 1-hour rate rather than under-count.
		resp.Usage.CacheWrite1hTokens = resp.Usage.CacheWriteTokens
	}
	cost, version := int64(0), UnpricedVersion
	if priced {
		if cost, err = m.prices.Cost(req.Model, resp.Usage); err != nil {
			return resp, errors.Join(callErr, err) // unreachable while priced; fail closed if it ever is not
		}
		version = m.prices.Version
	}
	rsv.settle(cost)
	if m.mode == ModeReplay {
		return resp, callErr
	}
	if lerr := m.writeLedger(ctx, m.row(req, cost, version, false, resp.Usage, resp.RequestID)); lerr != nil {
		return resp, errors.Join(callErr, &LedgerError{Err: lerr})
	}
	return resp, callErr
}

func (m *metered) row(req Request, cost int64, version string, estimated bool, u Usage, requestID string) store.UsageEntry {
	return store.UsageEntry{
		WorkspaceID: m.ws, RunID: req.Run.ID, Stage: m.stage, Kind: "llm", Model: req.Model,
		InputTokens: u.InputTokens, CacheWriteTokens: u.CacheWriteTokens, CacheWrite1hTokens: u.CacheWrite1hTokens,
		CacheReadTokens: u.CacheReadTokens, OutputTokens: u.OutputTokens,
		ListCostMicros: cost, PriceTableVersion: version, BilledTo: string(m.billing), RequestID: requestID,
		Estimated: estimated, CreatedAt: m.now().UTC(),
	}
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
		"list_cost_micros", e.ListCostMicros, "price_table_version", e.PriceTableVersion, "billed_to", e.BilledTo,
		"request_id", e.RequestID, "estimated", e.Estimated)
	return err
}

// Token estimates that need no API call. The upper bound leans very high on purpose: it is only used to decide that a
// request is so far under every cap that counting it exactly is not worth a round trip.
const (
	// upperBytesPerToken: no real text costs more than one token per two bytes of canonical JSON.
	upperBytesPerToken = 2
	// upperImageTokens only matters to EstimateTokens' sibling uses of the bound; a request with an image is always
	// counted (see reserveCaps), because an image can cost several thousand tokens depending on its size.
	upperImageTokens = 5000
	// upperOverheadTokens covers the tokens the API adds for tools and the system.
	upperOverheadTokens = 2000
)

// upperBoundTokens is a cheap, local upper bound of the request's input tokens.
func upperBoundTokens(req Request) (int64, error) {
	canon, images, err := canonicalRequest(req)
	if err != nil {
		return 0, err
	}
	return int64(len(canon))/upperBytesPerToken + int64(images)*upperImageTokens + upperOverheadTokens, nil
}

// reserveCaps works out the request's worst-case cost and sets it aside against every cap, before anything is sent.
// It tries a cheap upper bound of the input tokens first and asks count_tokens only when that does not clear the
// per-request cap, or when the run budget or the daily cap cannot cover the (inflated) bound.
func (m *metered) reserveCaps(ctx context.Context, req Request) (*reservation, error) {
	marks := cacheMarks(req)
	tokens, err := upperBoundTokens(req)
	if err != nil {
		return nil, err
	}
	counted := false
	count := func() error {
		n, err := m.inner.CountTokens(ctx, req)
		if err != nil {
			return fmt.Errorf("llm: counting tokens before sending: %w", err)
		}
		tokens, counted = n, true
		return nil
	}
	estimate := func() (int64, error) { return m.prices.EstimateMax(req.Model, tokens, int64(req.MaxTokens), marks) }

	// The local bound is for text. What an image costs depends on its size (up to about 4,800 tokens at the largest
	// size the models accept, and we send images at full size), so any request with an image is always counted.
	if hasImage(req) {
		if err := count(); err != nil {
			return nil, err
		}
	}
	est, err := estimate()
	if err != nil {
		return nil, err
	}
	if est > m.maxRequest && !counted {
		if err := count(); err != nil {
			return nil, err
		}
		if est, err = estimate(); err != nil {
			return nil, err
		}
	}
	for {
		if est > m.maxRequest {
			return nil, &CapError{Kind: CapRequest, EstimateMicros: est, LimitMicros: m.maxRequest}
		}
		rsv, err := m.take(ctx, req, est, tokens)
		if err == nil {
			return rsv, nil
		}
		var ce *CapError
		if !errors.As(err, &ce) || counted {
			return nil, err
		}
		// The inflated bound did not fit under the budget or the daily cap; the exact count may.
		if err := count(); err != nil {
			return nil, err
		}
		if est, err = estimate(); err != nil {
			return nil, err
		}
	}
}

// take reserves est against the run's budget and the workspace's daily cap, or returns the error of the cap that
// refused. Nothing stays reserved on an error.
func (m *metered) take(ctx context.Context, req Request, est, tokens int64) (*reservation, error) {
	rsv := &reservation{est: est, tokens: tokens}
	if b := req.Run.Budget; b != nil {
		release, err := b.Reserve(est)
		if err != nil {
			var ce *CapError
			if errors.As(err, &ce) {
				return nil, err
			}
			return nil, &CapError{Kind: CapRun, EstimateMicros: est, Cause: err}
		}
		rsv.budget = release
	}
	if m.mode == ModeReplay {
		return rsv, nil
	}
	// Daily cap. Reserve first and remember what was reserved at that moment, then read the ledger, then check. A call
	// that reserved earlier is either still in `before` or already a row in `spent` (its reservation is released only
	// after its row is written), and a call that reserves later counts this one the same way, so no pair of concurrent
	// calls can both miss each other. Counting both for an instant only over-reserves, which is the safe side. Other
	// processes' in-flight calls are not seen; the overshoot is bounded by their concurrency and the per-request cap.
	before := m.reserve.add(m.ws, est)
	now := m.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := m.ledger.UsageCostMicros(ctx, m.ws, day, day.Add(24*time.Hour))
	if err != nil {
		m.reserve.release(m.ws, est)
		rsv.done()
		return nil, fmt.Errorf("llm: reading today's spend before sending: %w", err) // fail closed
	}
	if spent+before+est > m.dailyCap {
		m.reserve.release(m.ws, est)
		rsv.done()
		return nil, &CapError{Kind: CapDaily, EstimateMicros: est, LimitMicros: max(m.dailyCap-spent-before, 0)}
	}
	rsv.daily = func() { m.reserve.release(m.ws, est) }
	return rsv, nil
}

// reservations is the worst-case cost of the calls in flight, per workspace, shared by every client of a factory.
type reservations struct {
	mu sync.Mutex
	m  map[ids.WorkspaceID]int64
}

// add reserves est and returns the total that was reserved before it.
func (r *reservations) add(ws ids.WorkspaceID, est int64) (before int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[ids.WorkspaceID]int64{}
	}
	before = r.m[ws]
	r.m[ws] = before + est
	return before
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

// hasImage reports whether any block of the request, at any depth, is an image.
func hasImage(req Request) bool {
	var walk func(blocks []Block) bool
	walk = func(blocks []Block) bool {
		for _, b := range blocks {
			if b.Type == BlockImage || walk(b.Content) {
				return true
			}
		}
		return false
	}
	if walk(req.System) {
		return true
	}
	for _, m := range req.Messages {
		if walk(m.Content) {
			return true
		}
	}
	return false
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
			mediaType, data, err := m.images.ResolveImage(WithWorkspace(ctx, m.ws), b.Image.Ref)
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
