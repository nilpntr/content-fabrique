package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// ledgerAttempts and ledgerTimeout bound the effort to write a row once the API has answered.
const (
	ledgerAttempts = 3
	ledgerTimeout  = 10 * time.Second
)

// metered is the outermost client. It resolves image references, reserves the worst-case cost of a call against every
// cap before sending it (refusing the call when a cap cannot cover it), and writes the ledger row of the call.
//
// The caps that hold across processes (the workspace's daily cap, the platform breaker) are enforced by the database through the
// spend gate: the worst case is held there before the request goes out, and the hold is settled into the ledger row and the
// counters in one transaction afterwards. The per-request cap and the run's budget stay in process.
//
// In replay mode nothing was spent, so no ledger row is written and the database is not consulted; the run budget is still
// reserved and settled with the cost the recorded usage would have had, so budget behaviour matches live.
type metered struct {
	inner   Client
	ws      ids.WorkspaceID
	stage   string
	billing Billing
	mode    Mode

	prices        *PriceTable
	gate          *spend.Gate
	images        ImageResolver
	maxRequest    int64
	allowUnpriced bool

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

// reservation is what one call has set aside: its worst-case cost against the run's budget and, in the database, against the
// workspace's daily cap and the platform breaker.
type reservation struct {
	// key identifies the call in the ledger ("llm:<key>"); the hold's key is derived from it.
	key string
	// est is the worst-case cost; tokens the input tokens it was worked out from (counted, or an upper bound).
	est    int64
	tokens int64

	budget func(actual int64) // settles the run budget; nil without one
	hold   *spend.Hold        // the database hold; nil in replay and for an unpriced call
	// stopBeat ends the hold's heartbeat; nil without a hold.
	stopBeat func()
	// keepHold says the ledger row could not be written: the hold stays until it expires, so the caps stay conservative.
	keepHold bool
	settled  bool
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

// ledgerKey is the idempotency key of the call's ledger row.
func (r *reservation) ledgerKey() string { return "llm:" + r.key }

// Messages implements Client.
func (m *metered) Messages(ctx context.Context, req Request) (Response, error) {
	req, err := m.resolve(ctx, req)
	if err != nil {
		return Response{}, err
	}
	priced := m.prices.Priced(req.Model)
	rsv := &reservation{key: uuid.NewString()}
	if !priced {
		// Fail closed: an unpriced paid call cannot be capped or metered. Only development and ci can allow one (the settings refuse
		// the flag elsewhere); the factory checks it again.
		if !m.allowUnpriced {
			return Response{}, &UnpricedError{Model: req.Model, TableVersion: m.prices.Version}
		}
		m.log.Warn("llm: calling an unpriced model (LLM_ALLOW_UNPRICED=1): spend caps cannot be checked and the ledger records cost 0", "model", req.Model, "stage", m.stage)
	} else if rsv, err = m.reserveCaps(ctx, req, rsv); err != nil {
		return Response{}, err
	}
	// Whatever happens, a reservation that was not settled is given back (the budget with 0, the hold released), unless the ledger
	// row could not be written.
	defer m.done(rsv)

	marks := cacheMarks(req)
	resp, callErr := m.inner.Messages(ctx, req)
	if resp.Usage.IsZero() {
		// The request went out and nothing came back (a timeout, a reset, a cut-off body): it may have run and been billed. Charge the
		// worst case, so caps and budgets stay conservative, and say so in the ledger.
		if priced && callErr != nil && errors.Is(callErr, ErrOutcomeUnknown) {
			rsv.settle(rsv.est)
			if m.mode != ModeReplay {
				row := m.row(req, rsv, rsv.est, m.prices.Version, true, Usage{InputTokens: rsv.tokens, OutputTokens: int64(req.MaxTokens)}, "")
				if lerr := m.writeLedger(ctx, rsv, row); lerr != nil {
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
	if lerr := m.writeLedger(ctx, rsv, m.row(req, rsv, cost, version, false, resp.Usage, resp.RequestID)); lerr != nil {
		return resp, errors.Join(callErr, &LedgerError{Err: lerr})
	}
	return resp, callErr
}

// done is the deferred clean-up of every path: a call that never settled cost nothing; its hold goes back unless the ledger row
// that should have replaced it could not be written.
func (m *metered) done(r *reservation) {
	r.settle(0)
	if r.stopBeat != nil {
		r.stopBeat() // a hold nobody extends expires by itself: this is what frees a hold whose row could not be written
		r.stopBeat = nil
	}
	if r.hold != nil && !r.keepHold {
		ctx, cancel := context.WithTimeout(context.Background(), ledgerTimeout)
		defer cancel()
		if err := r.hold.Release(ctx); err != nil {
			// The hold expires by itself; it only blocks budget until then.
			m.log.Warn("llm: could not release a spend hold; it will expire", "error", err, "workspace_id", m.ws.String())
		}
		r.hold = nil
	}
}

func (m *metered) row(req Request, r *reservation, cost int64, version string, estimated bool, u Usage, requestID string) store.UsageEntry {
	return store.UsageEntry{
		WorkspaceID: m.ws, RunID: req.Run.ID, Stage: m.stage, Kind: store.KindLLM, Model: req.Model,
		InputTokens: u.InputTokens, CacheWriteTokens: u.CacheWriteTokens, CacheWrite1hTokens: u.CacheWrite1hTokens,
		CacheReadTokens: u.CacheReadTokens, OutputTokens: u.OutputTokens,
		ListCostMicros: cost, PriceTableVersion: version, BilledTo: string(m.billing), RequestID: requestID,
		Estimated: estimated, IdempotencyKey: r.ledgerKey(), CreatedAt: m.now().UTC(),
		// The run is the action until real actions exist (P7-01a).
		ProjectID: req.Run.ProjectID, MemberUserID: req.Run.MemberUserID, ActionID: req.Run.ID,
	}
}

// writeLedger settles the call: the row, the counters and the hold in one transaction, retried a few times, and detached from the
// caller's cancellation: the call is already paid for, so a cancelled run must still be metered. The retries are safe: the row's
// key makes a repeated settle a no-op.
func (m *metered) writeLedger(ctx context.Context, r *reservation, e store.UsageEntry) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerTimeout)
	defer cancel()
	var err error
	for attempt := 1; ; attempt++ {
		if r.hold != nil {
			err = r.hold.Settle(ctx, e)
		} else {
			err = m.gate.Record(ctx, m.ws, e)
		}
		if err == nil {
			r.hold = nil // settled: the hold is gone
			return nil
		}
		if attempt >= ledgerAttempts || sleepCtx(ctx, time.Duration(attempt)*200*time.Millisecond) != nil {
			break
		}
	}
	r.keepHold = true
	if r.hold != nil {
		// The cost is paid and not recorded: keep the hold until the end of the UTC day, so the caps stay conservative until the row is
		// reconciled from the log line below. (The heartbeat is stopped by done.)
		if rerr := r.hold.Retain(ctx); rerr != nil {
			m.log.Error("llm: could not retain the spend hold of an unrecorded call", "error", rerr, "workspace_id", e.WorkspaceID.String())
		}
	}
	// The row's content is not secret (no key, no prompt): log it so the ledger can be reconciled by hand.
	m.log.Error("llm: usage ledger write failed", "error", err, "workspace_id", e.WorkspaceID.String(), "run_id", e.RunID.String(),
		"stage", e.Stage, "model", e.Model, "input_tokens", e.InputTokens, "cache_write_tokens", e.CacheWriteTokens,
		"cache_write_1h_tokens", e.CacheWrite1hTokens, "cache_read_tokens", e.CacheReadTokens, "output_tokens", e.OutputTokens,
		"list_cost_micros", e.ListCostMicros, "price_table_version", e.PriceTableVersion, "billed_to", e.BilledTo,
		"request_id", e.RequestID, "estimated", e.Estimated, "idempotency_key", e.IdempotencyKey)
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
// per-request cap, or when the run budget or the database caps cannot cover the (inflated) bound.
func (m *metered) reserveCaps(ctx context.Context, req Request, rsv *reservation) (*reservation, error) {
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
	waits := 0
	for {
		if est > m.maxRequest {
			return nil, &CapError{Kind: CapRequest, EstimateMicros: est, LimitMicros: m.maxRequest}
		}
		got, err := m.take(ctx, req, rsv, est, tokens)
		if err == nil {
			return got, nil
		}
		var ce *CapError
		if !errors.As(err, &ce) || ctx.Err() != nil {
			return nil, err
		}
		if !counted {
			// The inflated bound did not fit under the budget or the daily cap; the exact count may.
			if err := count(); err != nil {
				return nil, err
			}
			if est, err = estimate(); err != nil {
				return nil, err
			}
			continue
		}
		// The exact worst case does not fit next to the live holds near the platform cap, and holds are all that stands in its way: an
		// agent run (WaitOnBusy) waits for them to settle, until its context (the run's wall clock) ends. Anything else, such as a
		// request too large for the room that is left, is refused at once.
		var sce *spend.CapError
		if ce.Kind != CapPlatformBusy || !req.Run.WaitOnBusy || !errors.As(err, &sce) || !sce.HoldsInTheWay {
			return nil, err
		}
		if sleepCtx(ctx, busyBackoff(waits)) != nil {
			return nil, err
		}
		waits++
	}
}

// busyBackoff is how long a call that WaitOnBusy waits before asking the breaker again: 2 s, doubling to a minute, with jitter. A variable
// so tests can shorten it.
var busyBackoff = func(attempt int) time.Duration {
	d := min(2*time.Second<<min(attempt, 5), time.Minute)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// take reserves est against the run's budget and, in the database, the workspace's daily cap and the platform breaker, or returns
// the error of the cap that refused. Nothing stays reserved on an error.
func (m *metered) take(ctx context.Context, req Request, rsv *reservation, est, tokens int64) (*reservation, error) {
	rsv.est, rsv.tokens = est, tokens
	var release func(actual int64)
	if b := req.Run.Budget; b != nil {
		var err error
		if release, err = b.Reserve(est); err != nil {
			var ce *CapError
			if errors.As(err, &ce) {
				return nil, err
			}
			return nil, &CapError{Kind: CapRun, EstimateMicros: est, Cause: err}
		}
	}
	if m.mode == ModeReplay {
		rsv.budget = release
		return rsv, nil
	}
	// The hold is short and a heartbeat extends it for as long as the call is in flight (queued for the key's slot, cooling down after a
	// 429, retrying: no fixed lifetime covers that), so a crashed caller's hold expires within spend.DefaultHoldTTL of its last beat.
	hold, err := m.gate.Reserve(ctx, spend.Request{
		Workspace: m.ws, BilledTo: string(m.billing), Kind: store.KindLLM, Micros: est, Key: "hold:" + rsv.ledgerKey(),
	})
	if err != nil {
		if release != nil {
			release(0) // nothing stays reserved on an error
		}
		var sce *spend.CapError
		if errors.As(err, &sce) {
			kind := CapDaily
			switch sce.Cap {
			case store.CapPlatformDaily:
				kind = CapPlatform
			case store.CapPlatformBusy:
				kind = CapPlatformBusy
			}
			return nil, &CapError{Kind: kind, EstimateMicros: est, LimitMicros: sce.Room, Cause: err}
		}
		return nil, err // the database could not be asked: fail closed
	}
	rsv.budget, rsv.hold = release, hold
	rsv.stopBeat = hold.KeepAlive(ctx)
	return rsv, nil
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
