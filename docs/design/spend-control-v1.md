# Design note: spend control v1, profitable by construction

> Status: **accepted** · 2026-10-17 · **P7-09a (the foundation, §6 and §10) and P7-01a (credits, §3 to §5) are built; see "As built" at the end of §5 and of §6** · Founder request: "build in spend control so we still have profit in the end, and spend stats in the org; think of everything needed for a prod-ready thing." Implements [D-31](../05-decisions.md#d-31-spend-control-credits-per-action-cogs-budgets-derived-from-price). Builds on [D-13](../05-decisions.md#d-13-billing), [D-18](../05-decisions.md#d-18-who-pays-for-tokens-managed-credits-or-bring-your-own-key), [architecture §13](../03-architecture.md#13-cost-model), the P4-00a ledger (`usage_ledger`, `config/pricing`) and the P0-16 plans. Checkout through the merchant of record ([D-29](../05-decisions.md#d-29-legal-entity-an-operating-bv-under-the-holding-paddle-as-merchant-of-record)) stays a separate, later task. Everything here works with manual grants.

## 1. Goals and non-goals

**Goals**
1. **No action can lose money by surprise.**
   - Every paid action is priced in credits before it starts.
   - Its worst-case cost of goods (COGS) is capped at a fixed share of what those credits earn.
   - A test fails the build if any plan breaks that rule.
2. **Every cost is metered:** LLM, transcription, rendering compute, storage and egress, attributed to a workspace, a project, a member and a run.
3. **Limits hold in production:**
   - they hold across many API and worker processes, under concurrency and retries;
   - an unknown price fails closed;
   - a platform-wide breaker stops a runaway before Anthropic's spend limit does.
4. **Creators see their usage:** credits left, what they were spent on, and per project and per member.
   - Managed creators never see our COGS or margin.
   - BYOK creators see the estimated cost on their own key.
5. **The founder sees margin:** revenue against COGS per workspace, per plan and per action, plus outliers, unbilled COGS and drift against the provider invoices.

**Non-goals for v1**
- Checkout, invoices and tax (merchant of record, a later P7 task).
- Per-member limits inside a team.
- Dunning.
- Email notifications (in-app only; email comes with P7 notifications).

## 2. Two ledgers, two units

| | `usage_ledger` (exists, extended) | `credit_ledger` (new) |
|---|---|---|
| Unit | US dollar micros at list price (our cost) | Integer credits (what the customer pays with) |
| Who sees it | Founder, and BYOK creators for their own key's rows | Creators (members of the workspace) |
| Rows | One per metered cost event (a model call, a transcription chunk, a render, a storage snapshot) | One per grant, charge, refund, expiry or adjustment |
| Mutability | Append-only; corrections are new rows | Append-only; corrections are new rows |

The two are joined by `action_id`: every charged action has one `credit_ledger` charge and zero or more `usage_ledger` rows. That join is what the margin report and the "unbilled COGS" figure are built from.

## 3. Credits and the price list

Credits are the customer's currency. The UI shows them as **videos**: 1 video = 10 credits = a first edit plus two revisions, as in [product plan §10](../01-product-plan.md#10-pricing-and-business-model).

**Price list** (versioned data in `apps/server/config/plans/2026-10.yaml`, the same way as `config/pricing`; a change is a new file):

| Action | Credits | What it covers |
|---|---|---|
| `edit.first` | 6 | An editor run on a project with no finished edit yet, including its previews |
| `edit.revision` | 2 | Any later editor run on the same project (comments handed off, a brief change) |
| `export.final` | 0 | The final render of an edit that was charged |
| `ideation.run` | 1 | One idea run (P5) |
| Ingest, quick look, analysis, transcription, previews | 0 | Covered by the plan and capped by quotas (§6), not credits |

**Allowances:**

| Plan | Billing mode | Allowance | Storage | Concurrent runs | Transcribed minutes per day |
|---|---|---|---|---|---|
| `trial` | managed | 20 credits once (2 videos), never refilled | 2 GB | 1 | 30 |
| `studio` | BYOK | 600 credits per period (60 videos) | 100 GB | 3 | 240 |
| `creator` | managed | 150 credits per period (15 videos) | 50 GB | 2 | 120 |
| `pro` | managed | 400 credits per period (40 videos) | 200 GB | 4 | 360 |

**Rules:**
- The allowance is granted at the start of each period. A period follows the plan's `current_period_start`/`current_period_end`; a manual grant gets monthly periods from the grant date. Unused allowance **expires** at the period end.
- **Top-ups** (later, through the MoR; locally `cfctl credits grant`) last 12 months.
- Allowance is used first, then top-ups, oldest expiry first.
- BYOK workspaces use the same price list. Their credits pay the platform fee, while tokens go to their own key.

## 4. Profit invariant: COGS budgets are derived from price

Every action has a **COGS budget per plan** in the same YAML. It is the hard `budget_micros` of the agent run (or other work) that performs the action. The run cannot spend more: the runtime already stops a run at its budget, and the editor gets a "finish now" warning first.

The invariant, checked by a unit test over every plan and action in every price-list file:

```
cogs_budget(action, plan) ≤ max_cogs_share × net_revenue_per_credit(plan) × credits(action)

net_revenue_per_credit(plan) = (price_eur(plan) × (1 − mor_fee_pct) − mor_fee_fixed_eur) × usd_per_eur / allowance(plan)
max_cogs_share = 0.40          # D-18: COGS ≤ 40 % of credit value; ≥ 60 % gross margin before fixed costs
```

**Worked example, `creator`.** €59, 5 % + $0.50 MoR fee, about 1.08 USD per EUR, 150 credits:
- net revenue is about $0.40 per credit;
- 40 % of that is $0.16 per credit;
- so `edit.first` gets about **$0.96** and `edit.revision` about **$0.32**.

The cost model puts an Opus first edit at about $0.61 (range $0.45–0.95) and a revision at about $0.23. The live runs so far cost $0.22. These budgets fit, and they are the backstop if a run goes long.

**Special cases:**
- **Trial.** It earns nothing, so its budgets come from a fixed trial COGS allowance (about $2.50 per trial, product plan §10).
- **BYOK.** LLM rows are `billed_to = workspace_key` and don't count against our margin. A per-run budget still applies (default $2) to protect the creator's own key.

## 5. Charging an action: hold, run, settle

1. **Hold.** When a billable action is requested, the API (or job) opens a transaction:
   - it locks the workspace's `credit_accounts` row (`FOR UPDATE`);
   - it computes `available = sum(unexpired ledger) − sum(active holds)`;
   - if `available ≥ credits(action)`, it inserts a `credit_holds` row with an expiry, the action id and the plan-list version;
   - otherwise it answers **402 `credits_exhausted`**.

   Two requests racing for the last credits: exactly one wins. The price comes only from the server's price list, never from the client. Whether a run is `edit.first` or `edit.revision` is decided on the server from the project's state.
2. **Run.** The agent run gets `budget_micros = cogs_budget(action, plan)`.
3. **Settle.**
   - **Success** (the run finished with a preview): one transaction inserts the `charge` row and deletes the hold.
   - **Failure or system error:** the hold is released and no credits are charged. The COGS stays in `usage_ledger` as **unbilled COGS**, our loss, which the margin report shows.
   - **Cancelled by the user:** charged if the run had already made a timeline version (the creator got value), released otherwise.
4. **Expiry.** A sweeper releases holds past their expiry: crashed workers, or runs past their wall clock plus a grace period. Settling is idempotent on the action id, so a retried job can't charge twice.

### As built (P7-01a)

Built: the price list and plans as versioned data with the profit-invariant test; `credit_accounts`, `credit_ledger` and `credit_holds`; the balance and the order of use; the allowance periods and their expiry as periodic jobs; hold, settle and release on editor runs; the export rule; `GET /v1/me` credits and `402 credits_exhausted`; `cfctl credits`, `cfctl plan grant` and `cfctl agent run --no-credits`. Not built: the usage endpoints and screens (P7-01b); a hold for `ideation.run` (its price is in the list, but no ideation agent exists yet); checkout and top-up purchase (P7-01c, `cfctl credits grant` stands in); the BYOK key itself (P4-00b).

**The price list** is `apps/server/config/plans/2026-10.yaml`, embedded like `config/pricing` (`configfiles.Plans()`, loaded by `credits.LoadPriceList`). A change is a new file named `<version>.yaml`; the loader refuses a file whose name and version differ, an unknown field, a missing action or plan, and a negative number. It holds the four actions, the MoR fee (5 % and 47 euro cents, which is the $0.50 at 1.08 rounded up), `usd_per_eur`, `max_cogs_share`, the top-up expiry (12 months), the trial's COGS allowance ($2.50) and the four plans. The decimals are parsed exactly (`big.Rat`).

| Plan | Mode | Price | Allowance | Refills | Run budget: first edit / revision / idea | Our non-LLM budget (BYOK only) |
|---|---|---|---|---|---|---|
| `trial` | managed | free | 20 | no | $0.75 / $0.25 / $0.125 | |
| `studio` | byok | €25 | 600 | yes | $2.00 / $2.00 / $0.50 (the creator's own key) | $0.100 / $0.033 / $0.016 |
| `creator` | managed | €59 | 150 | yes | $0.96 / $0.32 / $0.16 | |
| `pro` | managed | €129 | 400 | yes | $0.79 / $0.26 / $0.13 | |

An export has a budget of 0 on every plan: it costs no credits, so the rule gives it nothing.

**The profit invariant** is `credits.(*PriceList).Check()`, run by `TestEveryPriceListFileHoldsTheProfitInvariant` over every file in the directory (a new file is picked up by itself). The test also fails on a deliberately bad fixture (`TestInvariantFailsOnABadFixture`: a creator first edit at $1.50, a trial at $3, a BYOK plan at $0.50 of our own compute), and `TestInvariantBoundaryIsExact` proves the boundary: for every priced action, the largest budget the rule allows passes and one micro-dollar more fails. The three cases are those of §4: a managed plan checks its run budget against what its credits earn; the trial checks it against `trial_cogs_allowance_micros × credits / trial credits`; a BYOK plan checks its `non_llm_cogs_budget_micros` (our compute, storage and transcription, metered but not refused), because its tokens are the creator's.

**The tables** (migration 00018).
- `credit_accounts`: one row per workspace. It is the lock: every transaction that reads the balance in order to change it takes this row `FOR UPDATE` first. It also keeps the plan the current allowance belongs to, the anchor of a manual plan's monthly periods, and when the current allowance period ends. The app role cannot delete from it.
- `credit_ledger`: append-only for the app role (`cf_grant_app_append_only`, tested). Kinds `grant_allowance`, `grant_topup`, `charge`, `refund`, `expire`, `adjust`; signed integer `credits`; `action`, `action_id`, `project_id`, `member_user_id`; `plan` and `plan_list_version`; `expires_at` on a grant; `actor` and `reason` (required by a check for `adjust`, also written on top-ups, refunds and short charges); a unique `idempotency_key`; and **`grant_id`**. Table checks fix the sign of each kind, require an expiry on top-ups and positive adjustments, and require actor and reason on adjustments.
- `credit_holds`: one per action in flight (unique on the action id and on the key). The `credits` may be 0.

**Consumption is tracked per grant, in the ledger itself (`grant_id`), not in an allocation table.** A row that adds credits (an allowance, a top-up, a positive adjustment) is a *source*. Every charge, refund, expiry and negative adjustment names the source it took from or gave back to, so what is left of a source is its own credits plus the sum of the rows that name it, and its expiry takes exactly what is left (an `expire` row of that size). A charge that spans two sources is two rows (`charge:<action id>:<n>`). The balance is a sum with a lateral join on an index on `grant_id`; no counter is kept, so there is nothing to drift. A source never goes below zero: the allocation happens under the account lock, and a test checks the invariant after every race.

**Balance and order of use.** `available = Σ (credits left of each source not past its expiry) − Σ (holds not past their expiry)`, never shown below zero. The allowance is used first, then the other sources, earliest expiry first (`ORDER BY kind <> 'grant_allowance', expires_at NULLS LAST, created_at`). A charge for more than is left (the allowance expired while the run was working) takes what is left and records the rest in `reason`: the creator is never charged below zero, and the shortfall is ours. A free action, or a total shortfall, still writes one zero-credit charge row, because that row is what makes it "a charged edit". A source an `expire` row has taken is gone from the balance (it is not counted in `allowance` or `period_end`, and a refund cannot bring it back).

**Periods.**
- A workspace on a manual plan has monthly periods from the day this account first got the plan (`period_anchor`); the day of the month is kept, clamped to the last day of a short month, and each period is computed from the anchor, so 31 January never drifts down. A plan managed by the merchant of record has the period that ends at `current_period_end` and starts a month earlier.
- The ledger key of an allowance names the workspace, the plan and the period (`allowance:<workspace>:<plan>:until:<period end>`), so any number of workers granting the same period add one grant. A trial's key has no period (`allowance:<workspace>:trial:once`): 20 credits once, never refilled, no expiry, and going back to the trial from a paid plan gives nothing.
- A different plan than the one the account last got **replaces** the allowance: what is left of the old one expires (an `expire` row with the reason) and the new plan's allowance is granted in full, with new monthly periods from that moment. Granting the same plan again (to move `--until`) is not a change. Each change raises the account's `plan_generation`, which is part of the allowance's key from then on, so a plan that comes back within the same period (a downgrade after an upgrade) is granted again; a trial's key never changes. A merchant plan whose period end moves by less than 15 days (a billing-date change, a proration, a pause) is the same billing period and gets no second allowance; a move of a month is a renewal.
- **A run that spans the end of a period (or a plan change) is charged once.** When a source expires while a run holds credits, the expiry leaves the credits that active holds (made before the expiry) rest on in the old source, "frozen" for those holds in the order they were made (`credit_holds.frozen_source_id`, `frozen_credits`, migration 00020), and expires the rest. The holds rest on the sources in the order of use, so a hold rests on the expiring source only as far as the sources before it cannot cover the holds. Available credits count only the unfrozen part of a hold. Settling with a charge takes the frozen part from the old source first and the remainder from the live sources, so the new allowance does not pay again; releasing, replaying or sweeping the hold expires the frozen part exactly then (an `expire` row naming the source and the hold). A hold made after the expiry rests on the new allowance. A run that ends in the minute after a period ends, before the expiry job has run, is settled the same way: the settle first expires its workspace's due sources with the holds kept. A hold rests on one source at most (a hold that outlives two expiries, 7 days of waiting for an answer included, gets the first).
- `store.SyncAllowance` does this under the account lock. `cfctl plan grant` calls it at once and prints the credits granted; the periodic job `credits_periods` (every minute, maintenance queue) calls it for every workspace with an active plan whose allowance is missing or ended, and then writes the `expire` rows (`ExpireDueCredits`) of every source past its expiry that has none. An expire row takes exactly what is left and is dated at the source's expiry. A late job loses nothing: a source past its expiry has stopped counting whether or not its row is written. The hold path does not grant lazily, so at a period boundary a creator can see `402 credits_exhausted` for up to a minute until the job runs.

**Requirements for P7-01c (checkout), found in review and not built here.**
- **Align a merchant grant's expiry with the merchant period.** A merchant period end that moves by less than 15 days (a billing-date change, a proration, a pause) updates `credit_accounts.allowance_until` and grants nothing, but the grant's own `expires_at` stays at the old end. The allowance can then expire up to 15 days before, or outlive, the paid period. Checkout must keep them equal, for instance by granting each period's allowance against the merchant's own period id and writing the new end onto the live grant (an expire row and a replacement grant for the remainder) when the end moves.
- **Prorate or carry over on a plan change.** Each change of plan expires what is left of the old allowance and grants the new plan's in full, which is right for a manual grant and wrong for a paid one: changing plans back and forth within a period would farm a full allowance each time. Checkout must grant the difference (prorated on the time left) or carry the unused credits over, and rate-limit the change, before it calls `SyncAllowance`.
- **Checkout must call `store.SyncAllowance` (through `credits.Service.SyncAllowance`) when it writes a plan or a renewal, instead of leaving the first grant to the `credits_periods` job.** The hold path does not grant lazily, so without it a customer who has just paid sees `402 credits_exhausted` until the job runs (up to a minute).

**Hold, settle, release on editor runs.**
- `runtime.Service.Create` knows one agent that costs credits (`credits.EditorAgent`). It asks `credits.QuoteEdit` under the project's row lock, creates the run with the plan's budget, and takes the hold (`store.HoldCredits`) in the same transaction, with `action_id` = the run id. Without enough credits nothing is created, and the API answers `402 credits_exhausted`. A runtime built without a credits service refuses to start an editor run (`ErrNoCredits`), so a missing wiring is never a free run.
- **First or revision** is decided on the server: the first edit is the run on a project with no *charged edit* (a `charge` row for `edit.first` or `edit.revision` on the project that has not been refunded). A failed run was released, so after a failed first run the next one is the first edit again; a run cancelled after it made a version was charged, so the next one is a revision (a cancel after a preview of earlier work was released, so the next one is a first edit again).
- **The run's budget** is the plan's COGS budget for the action. The caller's budget no longer counts: `budget_usd` in the request is still validated but ignored. The per-request and daily caps of P7-09a are unchanged.
- **Settle** happens in the transaction that ends the run (`EndAgentRun`), and in `RequestAgentRunCancel` for a run that is cancelled while queued or waiting: success charges (a charge row per source, and the hold deleted); failure releases (the hold deleted, no row; the COGS stays in `usage_ledger` as unbilled COGS); **a cancel charges when the run authored a timeline version, and releases when it did not**. (Two earlier versions of this rule were wrong, both found in review. Looking at the preview only: the editor commits a version on every `edit_timeline` and previews afterwards, so a creator could cancel between the two, keep the version, and get a revision for free. Charging for a preview too: `render_preview` can show a version the run did not make, an earlier one, and then the creator got nothing from the run. The version is the work.) The charge row records the newest version the run authored (`credit_ledger.timeline_version`), the work the credits paid for. The run's end and its credits therefore commit together, and a replay charges once (a settle looks for the action's charge rows under the lock, and the keys are unique).
- **The hold follows its run.** A hold counts, and is kept by the sweep, for as long as its run is active (queued, running or waiting for its creator), however long that takes: a long queue or an outage cannot free the credits of a run that will still finish and be charged. Its expiry is the fallback for a hold whose run is over or gone: 2 hours at the start (a busy queue), moved to 30 minutes after each heartbeat (`TouchAgentRun`, best effort) whenever less than 20 are left, 7 days for a run that parks to ask its creator a question. Past its expiry, and when its run is not active, a hold stops counting at once, and the periodic job `credit_holds_sweep` (every minute) deletes it: the credits are released, never charged. A run that has been "active" for 30 days past its hold's expiry is treated as gone, so a run stuck for good cannot keep credits for ever.
- **No-credits runs.** `cfctl agent run --no-credits` (only with `CF_ENV` local or ci; the command is refused on staging and production anyway) takes no plan and no credits and keeps its budget flags, but it still gets a hold of zero credits, so it ends as a zero-credit charge: the project then has a charged edit and can be exported, and the next run is a revision. Without the flag the command charges like the API (it needs a plan and credits; `cfctl plan grant` and `cfctl credits grant` give them), also when the run is `billed_to = test`.

**BYOK plans run only on the creator's own key.** A plan's `billing_mode` decides who pays for the model. The profit invariant leaves a BYOK plan's $2 run budget out of our cost because the creator's key pays for it, and that holds only if a run on a BYOK plan is billed to the workspace's own key. The key resolver of P4-00b does not exist yet and always bills ours, so `runtime.Service.Create` refuses such a run (`credits.RequiredBilling`: the creator's key for BYOK; `runtime.ErrBYOKKeyRequired`, `402 byok_key_required`, "this plan uses your own Anthropic key; add it in settings"), before any run or hold is created. The invariant test encodes the assumption: `PriceList.CheckBilling` with a rule under which our key pays for a BYOK run fails for the studio plan, and `Check` uses the same `RequiredBilling` the runtime enforces. Until P4-00b a studio workspace cannot start editor runs (a free `cfctl agent run --no-credits` run is not affected, being development only); the studio plan is therefore not sellable before P4-00b.

**Runs that end without a charge are capped per day.** A run that is released (not charged) has still spent model money, and a creator could loop "start, cancel before the first version" to burn it. `credits.Service.CheckUncharged` therefore counts, per workspace and UTC day, the editor runs that are **in flight** (queued, running, waiting; they may all end uncharged, and nothing else bounds how many a workspace can start at once, `concurrent_runs` is not enforced yet) plus those that finished that day, **cost money** (`cost_micros > 0`) and have no charge row:
- cancelled before they made a version (a cancel after one is charged, so it is not here);
- succeeded having said there was nothing to change (`no_edit_needed`);
- failed for a reason of the creator's own: `refusal` (the model declined the content), `context_window_exceeded`, `invalid_input`, `context_failed` (`credits.UnchargedFailureReasons`; a runtime test keeps the names in step).

Failures that are ours do not count, so a bad day of the model API or of our services never locks a creator out: `budget` (the run budget comes from the plan's price list, so running out of it is our sizing, not the creator's behaviour, and with budget fitting the editor ends gracefully through `OnLimit` once a preview exists), `model_error`, `model_access`, `model_outcome_unknown`, `max_tokens` (the model could not fit its answer in the limit we set), `event_too_large`, `internal_error`, `ledger_error`, `corrupt_log`, `unknown_agent`, `too_many_resumes`, `unexpected_stop_reason`, `request_cap`, `replay_miss`, and the spend caps (`daily_cap`, `platform_spend_paused`, `spend_cap_busy`, which already stop the spending). A run that cost nothing (cancelled in the queue) does not count once it is over. A run without credits (`--no-credits`, development) is never refused by the cap, but one that ends this way counts toward it for the paid starts of the same workspace.

At the plan's `uncharged_runs_per_day` (5 on every plan in 2026-10; `uncharged_runs_per_day` in the plan YAML) the next start is refused with `429 too_many_uncharged_runs` and a message that names the cap; the next UTC day it starts again. The check runs after the credit hold, in the transaction that creates the run, and counts the new run itself, so the cap is the number of such runs including this one; a workspace that is out of credits (402) or on a BYOK plan without its key (402) is told that first, not to wait for tomorrow. The count is read from `agent_runs` and the ledger, so nothing new is stored, and only runs created in the last eight days are looked at (a range on the UUIDv7 run id, on the existing index; a run created before midnight that is still in flight, or finishes after it, still counts, and the slack also absorbs clock differences between the app and the database). Concurrent starts of one workspace are serialised by the credit account lock the hold takes, which is held to the end of the run-creating transaction, so the cap cannot be overshot by a burst; that is why the check must stay after the hold (a test starts eight runs at once against a cap of three).

**Exports.** A final render is the `export.final` action, and it must point at paid work. `renderjobs.Service.Request` (the only place a final render is made; `cfctl render` is a local ffmpeg tool with no database) refuses it when:
- the project has no charged edit (`422 edit_not_charged`): a project that has only had human edits cannot be exported, which keeps the rule "an export needs at least one charged edit"; or
- the version is not *covered* (`422 version_not_charged`). A version is covered when the newest agent-authored version at or before it belongs to a run that was charged and not refunded, or when there is no agent work before it. A person's edits on top of a charged version are free to export; a version made by a run that failed or was released, and a person's edits made on top of it (they contain the unpaid work), are refused until a later run is charged. A version made by a run that is still working is not covered yet.

Previews of any version are never refused, so an unpaid version can be looked at, not exported. Every new final render that is queued is recorded as a zero-credit charge row (`charge:<render id>:0`) with the project and the member. It costs no credits and is never refused for lack of them. A service wired without the credits service refuses final renders (500). Versions that agent runs made before this migration have no charge and cannot be exported until a later run is charged.

**API.** `GET /v1/me` has `credits {available, allowance, period_end, low, top_ups[{credits, expires_at}]}`: `available` is what a new action can use (sources minus holds), `allowance` the size of this period's allowance (0 without one), `period_end` when it expires (`null` for none or a trial's), `low` is true when `available × 5 ≤ allowance` (at least 80 % used; top-ups count as available). The field is left out when the balance cannot be read. `402 credits_exhausted` is the standard error body with a message that carries no numbers; the web app fetches `/v1/me` for the balance. `POST /v1/projects/{id}/edit` documents it; the 402 response keeps the shared `PlanRequired` reference because every paid operation must reference it. No other usage endpoint exists yet (P7-01b).

**CLI.** `cfctl credits show|grant|adjust --workspace <wks_...|org_...> [--credits N] [--reason ...] [--expires DATE]` prints the plan, the balance, the sources and the newest ledger rows (free text quoted). `grant` adds a top-up (default expiry: the list's 12 months); `adjust` adds credits (positive) or takes them (negative; refused with nothing changed when the workspace has fewer). Both require a reason, write the actor `cfctl:<OS user>`, use the same guard as `cfctl plan` (staging needs `--yes`, production needs `--allow-production` and `--yes`), and never print the database URL.

**Lock order and deadlocks.** A transaction takes at most these locks, in this order: the project row (starting an edit; queuing an export), the run row (ending a run), then the workspace's `credit_accounts` row. The credit hold is taken in the request that creates the run, a spend reservation (`spend_counters`: platform, then workspace) in the worker's model call, and the credit settle in the transaction that ends the run: **no transaction takes a credit account and a spend counter together**, so the two orders never meet. The expiry job, the allowance job and `cfctl` take only the account.

**Tests** (all fake transports and cassettes; no live paid calls): two pools contend for the last credits, 60 rounds of 8 holds for 6 credits with exactly one winner each time; an overbooking race; holds racing settles; settle replayed; failed run released; cancel after a version but before a preview (charged), before any version (released), and after a preview of earlier work only (released); a final export of unpaid agent work refused, of a person's edit on a charged version allowed; an expired hold swept; period rollover granted once by six concurrent callers and the unused allowance expiring exactly; order of use with two top-ups; a trial never refilled; plan change; first versus revision across a failed first run; tenancy; the append-only grants for the app role; the profit invariant and its negative fixture; the API (402, `/v1/me`, budgets); exports; the CLI.

**Deviations from the text above, and why.**
- §5 says success is "the run finished with a preview"; the build charges any run that ends `succeeded` and made a timeline version, with one exception the founder asked for: a run that ends by saying there was nothing to change (the editor's `finish` with `no_edit_needed`, whose output carries `"no_edit_needed": true` and which authored no version) is released, not charged, and the project does not get a charged edit from it. Charging only runs with a preview, or also charging `no_edit_needed`, is a one-line change in `settleRunCredits`.
- A final export is refused without a charged edit (`422 edit_not_charged`, a new error code) and when the version holds unpaid agent work (`422 version_not_charged`, another). Previously any version could be exported. To confirm: a creator who only tweaks a timeline by hand cannot export it until an editor run on the project has been charged, and a run that failed after making a version leaves versions that cannot be exported.
- A change of plan expires what is left of the old allowance and grants the new one in full (an upgrade in mid-period gets the full new allowance and loses the unused old one). This protects the margin; the merchant integration (P7-01c) may want proration.
- The fixed fee of the MoR is stored as 47 euro cents instead of $0.50, because the invariant is written in euros.
- `budget_usd` of `POST /v1/projects/{id}/edit` is ignored (it used to set the run's cap, up to $5). The `runapi.Options` budget settings are gone.
- BYOK plans cap a run at $2 (first edit and revision) on the creator's own key, as §4 says; the invariant checks the platform's non-LLM budget instead.
- The ledger columns `actor` and `reason` are also written on top-ups, refunds, expiries and short charges, not only on adjustments.
- A refund (`store.RefundAction`) gives credits back to their source only if it has not expired or been replaced; there is no CLI for it yet. Every charge row gets a refund row (zero credits for a free charge or an expired source), so a refunded edit stops counting as a charged edit.
- `cfctl credits adjust` with a negative number takes only credits that no run in progress holds.

**Budget-fitted calls (the answer to the revision-budget problem).** The metered client reserves the worst case of every call before it sends it (`max_tokens` at the output price, plus the input). On `v1-start` that is $0.48 plus the input for the editor entry's 24,000 tokens on Opus 5.5, and the invariant allows a revision only $0.32 on creator ($0.26 on pro, $0.25 on a trial), a first edit $0.96 ($0.79, $0.75). The budgets are the largest the 40 % rule allows, so the calls were made to fit them instead:
- **A call is shortened to what the run can still pay for.** In `reserveCaps`, when the worst case at the asked `max_tokens` is more than what the run's budget has left (`llm.RemainingBudget`), the client counts the input exactly first (the local bound leans too high to give output room away for), then sends the largest `max_tokens` whose worst case fits: `fit = floor((remaining - input worst case) / output price)`, found exactly with `PriceTable.FitOutputTokens` (including where Haiku's long-prompt tier starts) and rounded down to a multiple of 16. It reserves that shortened worst case, so the budget stays a hard cap: a call can never cost more than the run has left. The response says so (`Response.Shortened`) and the assistant event records it (`shortened`).
- **A floor, `MinOutputTokens`** (default 2,048, `llm.DefaultMinOutputTokens`; settable per stage as `min_output_tokens` in the routing entry, stored on the run as `agent_runs.min_output_tokens`). Below it the call is refused with the existing run-budget error (`CapRun`), which the runtime takes as the dollar limit and ends through `OnLimit`: gracefully on the newest previewed version for the editor, failed with reason `budget` for an agent with no usable work.
- **Thinking.** A manual thinking budget counts toward `max_tokens` and must stay below it and at least 1,024: a shortened call cuts the thinking budget to half of its `max_tokens` (never below 1,024), the caller's setting is not changed, and a call whose room is too small for that is refused as the run budget. Adaptive thinking has no budget to fit. The editor sets no thinking today.
- **A shortened answer that hits its limit** stops with `max_tokens` and goes through the cut-off recovery (two per run). After them, if the last cut-off was shortened, the run ends as the budget limit (`OnLimit`, or failed `budget`) rather than failed with `max_tokens`. A cut-off that was not shortened still fails with `max_tokens`.
- **Revisions route to the `revision` entry.** The credits decide first edit or revision, so `runtime.Service.Create` resolves a revision's model, effort and limits from the profile's `revision` entry (12,000 tokens on `v1-start`, so one full-size call is $0.24 plus the input and fits a $0.32 budget) and keeps the agent's stage (`editor`) for the ledger and the cassettes. A profile with no `revision` entry keeps the editor's.
- **The editor is told.** Its budget note (the line after each tool answer) adds "Your budget is nearly used: keep this turn short and finish soon." once the money left is less than two calls like the run's earlier ones. A run counts as "low" (no revision pass, finish now) when it has less left than four such calls, a revision pass. "Such a call" is what the run has really spent per tool call so far, plus half for the longer conversation, never under $0.05 (`callCostMicros`); it is not the worst case of a call (`max_tokens` at the output price), which is only a reservation and made every first edit "low" after its first call.
- Tests (fake transport, Opus prices): a $0.96 first edit shrinks its calls (24,000 first, then down to a few thousand), never passes the budget, and ends succeeded on its previewed version at the budget limit; a $0.32 revision runs plan, edit, previews and checks with shortened calls; the floor refuses; the thinking budget stays valid; the ledger total is at most the budget.

## 6. Cost metering and caps (COGS side)

**Metering.** `usage_ledger.kind` is extended beyond `llm`:

| Kind | Unit | Priced from | Written by |
|---|---|---|---|
| `llm` | tokens | `config/pricing` (exists) | `llm` metered client (exists) |
| `transcription` | audio seconds | `config/pricing` `transcription:` section (Workers AI per minute; whisper.cpp local at its compute price) | transcribe pipeline, one row per chunk |
| `render` | vCPU-seconds | `config/pricing` `compute:` section | render jobs |
| `media` | vCPU-seconds | `compute:` | ingest, quick look, analysis |
| `storage` | GB-days | `storage:` (R2 Standard) | a daily snapshot job |
| `egress` | GB | `storage:` (R2 has no egress fees, so price 0; still metered) | delivery, later |

New columns:
- `quantity` and `unit`;
- `project_id`, `member_user_id` and `action_id`, all nullable.

Existing `llm` rows keep their token columns.

**Fail closed.** On `staging` and `production`, a paid external provider (Anthropic, Workers AI) with no price refuses to run. `LLM_ALLOW_UNPRICED` is honoured only in `development` and `ci`. Doctor flags missing prices.

**Caps that hold across processes.** The in-memory reservations in `llm/metered.go` work only inside one process. They are replaced by holds in the database:
- `spend_holds`, plus a `spend_counters` row per (scope, period), locked `FOR UPDATE`;
- the counters are updated in the same transaction as the ledger insert, so a cap check never sums the whole ledger.

Caps, all enforced the same way:

| Cap | Scope | Default | Effect when hit |
|---|---|---|---|
| Per request | one model call | exists | refuse the call |
| Per run | `agent_runs.budget_micros`, from §4 | per action | run finishes or stops |
| Workspace daily | COGS on our key, per UTC day | plan YAML (about 3× a heavy day) | refuse new paid work, 429 `daily_spend_cap` |
| Workspace period | COGS on our key, per billing period | plan YAML (≈ allowance × per-credit budget × 1.5) | refuse, alert founder |
| **Platform daily breaker** | all `billed_to = platform` COGS | `PLATFORM_DAILY_SPEND_CAP_MICROS` | refuse every managed paid call, 503 `platform_spend_paused`, page the founder |
| Provider hard limit | Anthropic Console spend limit on the production org | founder sets it | last line of defence |

**Rule of thumb:** credits stop a customer from overusing what they paid for. The COGS caps stop a bug, a loop or an abuser from costing us money even when credits are fine.

### As built (P7-09a)

Built: the ledger kinds, units and attribution; the holds and counters in the database; the platform breaker; fail-closed pricing; the append-only ledger; the price-table sections; metering of model calls, transcription, renders and media jobs; the two P2-05 review fixes. Not built (P7-09b, as in §11): storage snapshots and egress rows (the kinds, units and the `storage:` prices are ready), the outlier alerts, the reconciliation job, `cfctl spend report`.

**Tables.**
- `spend_holds`: `scope` (workspace or platform), `workspace_id`, `billed_to`, `kind`, `period`, `micros`, `audio_ms`, `expires_at`, `idempotency_key` (unique).
- `spend_counters`: one row per workspace and period and one platform row per period, with `platform_micros`, `test_micros` and `audio_ms`, plus `alerted_80_at` and `tripped_at` on the platform row. The period is `day:YYYY-MM-DD` in UTC (the column also accepts `bill:...`, for billing periods later).
- `usage_ledger` gained `quantity`, `unit`, `project_id`, `member_user_id`, `action_id` and `idempotency_key` (unique; old rows are `legacy:<id>`). `request_id` keeps its own unique index as a second guard.
- The app role has SELECT and INSERT on `usage_ledger` (`cf_grant_app_append_only`), SELECT, INSERT and UPDATE on `spend_counters` (`cf_grant_app_no_delete`) and full DML on `spend_holds`.

**Reserve, settle, release** (`store.ReserveSpend`, `SettleSpend`, `ReleaseSpend`; `internal/spend` adds the caps, the typed errors and the `spend.alert` log lines).
- **Reserve** is one transaction. It makes sure the counter rows exist, locks the platform row `FOR UPDATE` and then the workspace row, sums the live holds, and checks `spent + held + estimate <= cap` for each cap that applies, in this order: the platform breaker, the workspace daily cap, the transcribed minutes. It inserts the hold, or commits with a typed `SpendCapError` naming the cap (committing, so the trip flag stays). A repeated reserve with the same key returns the same hold.
- **Settle** is one transaction. It inserts the ledger rows (`ON CONFLICT DO NOTHING`), adds the cost of the rows that were new to the counters (the platform row first, then the workspace's, through upserts, which take the same locks in the same order), and deletes the hold. A replay inserts nothing, so it counts nothing. A settle for a hold that the sweep already deleted still records the cost.
- **Release** deletes the hold. **The sweep** is the periodic job `spend_holds_sweep` (every minute, maintenance queue); every cap check ignores a hold past its expiry, so a dead caller stops blocking even before the sweep runs.
- **Lock order** is always platform, then workspace. Several workspaces' platform-billed reserves therefore serialise on the platform row for the length of a short transaction; that is the price of an exact breaker.

**What counts against what.**

| `billed_to` | Workspace daily cap | Platform breaker | Recorded in the ledger |
|---|---|---|---|
| `platform` | yes | yes | yes |
| `test` | yes | no | yes |
| `workspace_key` | no | no | yes |

Test traffic keeps the behaviour developers rely on: the workspace daily cap, the per-request cap and the run budget all apply locally exactly as before, because test cost counts toward the workspace cap. Only the platform breaker ignores it. The transcribed minutes count every `billed_to`.

**Caps in the code.**
- `DAILY_SPEND_CAP_MICROS` (workspace), `PLATFORM_DAILY_SPEND_CAP_MICROS` (breaker, default $100 when unset, which `cfctl doctor` rejects in staging and production) and `TRANSCRIBE_DAILY_MINUTES` (as an audio cap, in milliseconds) are enforced in the database. The per-request cap and the run budget stay in process.
- A reservation that counts against a money cap with no positive limit is refused (`ErrSpendCapNotSet`). A database that cannot be asked is an error, never a pass.
- The breaker logs `spend.alert` with `type=platform_breaker` and `threshold_pct=80` (warning) when the settled counter passes 80 % of the cap, and `threshold_pct=100` (error) when it trips (see "Busy is not a trip"), each once per UTC day however many processes see it. A trip is `spend.ErrPlatformPaused`: a run fails with `platform_spend_paused`; a transcription or quick look waits and retries; `spend.HTTPError` maps it to `503 platform_spend_paused` (and a workspace cap to `429 daily_spend_cap`) for any API handler that meets one. No endpoint does yet, because the paid calls run in the worker.

**Hold lifetimes.**
- A hold is short: 5 minutes. While its call is in flight, a heartbeat (`Hold.KeepAlive`, every minute) moves its expiry to now plus 5 minutes (`store.ExtendSpend`, which only moves it forward and never brings back a hold that is gone). No fixed lifetime could cover a model call that queues for its key's concurrency slot, cools down after a 429 and retries up to eight times, so the lifetime follows the call instead.
- A caller that dies stops beating, and its hold stops counting 5 minutes after the last beat; the sweep then deletes it. A call whose ledger row could not be written (the cost is paid and in no counter) stops beating, and its hold is kept to the end of the UTC day it is retained on (`Hold.Retain`) and moved onto that day (a call that reserved before midnight and failed to write after it is counted on the new day, where its cost belongs and where the cap checks read), so the caps stay conservative until the row is reconciled from the log line. A heartbeat that cannot get through for a whole TTL logs an error (the hold has lapsed while its call is in flight), once per outage.
- The model client, the transcription pipeline (provider calls of any length) use the same heartbeat. Renders and media jobs hold nothing.

**Busy is not a trip.**
- The breaker is *tripped* when the committed (settled) platform spend plus a small normal call no longer fits under the cap: `committed + PLATFORM_SPEND_SMALL_CALL_MICROS > cap` (default $0.10, at most a tenth of the cap so that a small cap is not tripped from the first call; `store.BreakerTripped`). "Tripped" therefore means no normal-sized call fits; calls smaller than the small call may still be admitted. Only a trip sets `tripped_at`, raises the 100 % alert and refuses with `platform_spend_paused`. The same rule is checked when a settle adds platform cost, so a crossing is alerted whether or not a call is refused at that moment.
- A reservation that is refused while the rule says "not tripped" (a large request next to the live holds, or a burst of in-flight calls near the cap) is **busy**: `spend_cap_busy` (`store.CapPlatformBusy`, `spend.ErrSpendCapBusy`, `503 spend_cap_busy` through `spend.HTTPError`). It sets no flag and raises no alert, so the real crossing later still alerts once.
- A busy refusal says whether waiting can cure it (`HoldsInTheWay`): the request fits next to the settled spend and only live holds are in the way. A request that is too large for the room left, with nothing held, is busy too but is returned at once, since no settle will make room. A cap that waiting cannot cure (the workspace's daily cap, the minutes) takes precedence over a busy platform refusal, so such a call fails fast.
- Callers: an agent run sets `RunInfo.WaitOnBusy`; once the exact token count has been tried (the local upper bound may be what did not fit), the model client retries the reservation with jittered backoff (2 s doubling to 1 min) while holds are in the way, until the run's wall-clock context ends. A run that is refused with nothing to wait for fails with `spend_cap_busy`. Transcription waits a short 2 minutes on busy and 15 on a trip (snoozed, no attempt used); quick look waits like a call without model access.

**Metering.**

| Kind | Written by | Quantity | Key |
|---|---|---|---|
| `llm` | `llm.metered` | tokens | `llm:<call id>` |
| `transcription` | the transcribe pipeline, one row per request | audio ms (Workers AI) or estimated vCPU ms (whisper.cpp) | `transcribe:<job id>:<attempt>:<chunk>` |
| `render` | the render pipeline, every attempt | `renders.cpu_ms` | `render:<render id>:<job id>:<attempt>` |
| `media` | the ingest, quick look and analysis jobs, every attempt | job wall time times the sandbox's vCPUs | `media:<stage>:<job id>:<attempt>` |

Render and media rows are costs already incurred, so they are recorded, counted toward the breaker and never refused. There is no CPU measurement for them yet: `cpu_ms` of a render is its sandbox runs' wall time times the sandbox CPU limit, and the media jobs use their whole wall time (waiting on storage and the model API included), so both are upper-bound estimates priced from the table's compute estimate. Whisper.cpp's row is estimated the same way, from the audio's length (`wall_seconds_per_audio_second`, 1.0 until measured). A chunk of hosted transcription whose request went out and got no usable answer is written `estimated = true` at the price reserved for its audio.

**Price table 2026-10b** adds `transcription` (Workers AI $0.0005 per audio minute, read from Cloudflare's pricing page on 2026-10-17 and to be confirmed against the first invoice; whisper.cpp as compute), `compute` ($0.000020 per vCPU-second, Cloudflare Containers' price, marked as an estimate) and `storage` (R2 Standard $0.015 per GB-month, egress free). Every ledger row records the version. `WORKERS_AI_PRICE_MICROS_PER_MINUTE` is now an optional override of the table.

**Deviations from the text above, and why.**
- The trip rule and the busy refusal above are not in the first text of this note; they came out of review (one large request must not burn the day's single 100 % alert).
- Units are the smallest whole unit (`audio_ms`, `vcpu_ms`, `byte_days`, `bytes`), not seconds, GB-days and GB, so that quantities and costs stay integers for short chunks; reports divide.
- The counters' minutes are milliseconds (`audio_ms`) for the same reason.
- A counter row has `platform_micros` and `test_micros` instead of one number, because the workspace cap counts both (the local behaviour above) and the breaker counts only the first.
- `LLM_ALLOW_UNPRICED` is honoured in `local` and `ci` only; `bench` refuses it too.
- The `WORKERS_AI_PRICE_MICROS_PER_MINUTE` requirement of P2-05 is gone from `config.Load` and doctor: the price comes from the table, and the refusal sits where the provider is built.
- Transcription chunks are metered when the provider returns, not one by one while it works. A worker that dies in the middle of a hosted transcription leaves its hold to expire and does not record the chunks it had paid for; the retry transcribes the audio again. Per-chunk settling is a later improvement.
- Quick look metering uses the whole job's wall time (a model call included) times the frame sandbox's vCPUs, a deliberate over-count of about two vCPU-seconds.

## 7. Quotas and abuse controls

- **Storage quota** per plan. It is checked when an upload is created, using the declared size plus the current usage, and refused with 402 `storage_quota_exceeded`. Deleted assets stop counting once their objects are purged.
- **Upload limits:**
  - a maximum duration per clip (30 min);
  - a maximum number of uploads per workspace per day (200);
  - the existing `MaxActiveUploads`.
- **Transcribed minutes per day**, per plan (P2-05 adds the counter; this note sets the plan values).
- **Concurrent agent runs**, per plan. Extra runs queue; they are not refused.
- **Re-upload loops cost money, not free retries.** Costs are keyed by workspace, not asset, so deleting and re-uploading still counts against the daily and period caps.
- **Trials:**
  - one trial per Clerk user;
  - trial workspaces can't top up;
  - signup bot protection is on in Clerk (founder setting).

## 8. What creators see

**Settings → Usage** (admins of the Clerk org see everything; members see the balance and their own activity):

- **Plan card:**
  - plan, period end, and credits left out of the allowance, shown as "≈ N videos left";
  - top-ups and their expiry;
  - an upgrade or top-up button ("contact us" until checkout exists).
- **This period:**
  - credits used per day, stacked by action;
  - totals per action;
  - per project (edits, revisions, exports, credits);
  - per member.
- **Activity:** each charge, refund and grant, with the project, member, run link and status.
- **BYOK only:** the estimated Anthropic cost on their key, per run and per period, from our metering.
- **Everywhere:**
  - the header shows a low-credit badge at 80 % used;
  - a banner appears at 100 %;
  - a 402 `credits_exhausted` opens an upgrade dialog;
  - the run feed shows "6 credits" (or "refunded") per run.

**Never shown to managed creators:** dollar COGS, margins or token counts.

API (OpenAPI, paid guard as usual):
- `GET /v1/usage/summary?period=current|YYYY-MM`
- `GET /v1/usage/daily`
- `GET /v1/usage/by-project`
- `GET /v1/usage/by-member`
- `GET /v1/usage/activity` (cursor paging)
- `GET /v1/me` gains `credits {available, allowance, period_end, low, top_ups[]}` (built in P7-01a)

## 9. What the founder sees

- **`cfctl spend report [--period YYYY-MM] [--workspace …] [--json]`:**
  - per workspace and per plan: revenue (plan price, net of the MoR fee), COGS by kind, gross margin;
  - per action: median, p95 and maximum COGS against its budget;
  - unbilled COGS (failed and released runs);
  - `estimated` and `unpriced` rows;
  - the top 10 runs by cost.
- **`cfctl credits show|grant|adjust --workspace … --reason …`:** every adjustment is a ledger row with the actor and reason. `production` needs `--allow-production`.
- **Alerts** are structured log events (`spend.alert`, typed). P0-11 turns them into Axiom monitors:
  - platform breaker at 80 % and 100 %;
  - a workspace daily or period cap hit;
  - a run above its budget (should be impossible), or a run above 3× the action's 7-day p95;
  - any unpriced usage;
  - an estimated row;
  - reconciliation drift.
- **Reconciliation:** a daily job compares our `billed_to = platform` total per day with the provider's own report:
  - Anthropic through the Usage & Cost Admin API, with `ANTHROPIC_ADMIN_KEY`, production only, a founder-provided secret;
  - Cloudflare later.

  Drift over 3 % raises an alert. It is built and tested against a fake server, and does nothing while the key is unset.

## 10. Correctness and security rules

- **Money and types:**
  - money is always an integer: USD micros for cost, integer credits, EUR cents for prices;
  - no floats in storage;
  - every row records the price-list and plan-list versions it used.
- **Append-only:**
  - `usage_ledger`, `credit_ledger` and `spend_counters` history are append-only for the app role (SELECT and INSERT only; counters get UPDATE on their own rows);
  - a corrections path through compensating rows is the only "edit".
- **Idempotency:** every ledger insert has an idempotency key (request id, action id plus kind, or chunk id).
- **Concurrency:** all checks lock one row per workspace (and one platform row) in a fixed order to avoid deadlocks, with tests:
  - last-credit races;
  - two processes against one daily cap;
  - settle replayed twice;
  - a crashed worker whose hold expires.
- **Tenancy:** every query is filtered by workspace, and tenancy tests cover the usage endpoints. The client never sends prices, credits or budgets.
- **Erasure:** deleting a workspace never deletes ledger rows (FK RESTRICT, as today). A GDPR purge anonymises them deliberately.

## 11. Build order

| Task | What | Needs |
|---|---|---|
| **P7-09a** Spend foundation (built) | `usage_ledger` kinds and units and attribution columns; database holds and counters replacing in-memory reservations; platform breaker; fail closed on staging and production for every paid provider; append-only grants; price table sections for transcription, compute and storage; move the llm client and transcription onto it | P2-05 merged |
| **P7-01a** Credits (built) | `config/plans` YAML and loader; `credit_accounts`, `credit_ledger`, `credit_holds`; period grants and expiry job; hold, settle and release wired into editor runs (first or revision decided by the server) and exports; budgets from the YAML; profit-invariant test; 402 `credits_exhausted`; `cfctl credits`; `/v1/me` credits | P7-09a |
| **P7-01b** Usage UI | `/v1/usage/*`; Settings → Usage; header badge, banners, 402 dialog; credits in the run feed; role-based visibility | P7-01a |
| **P7-09b** Guardrails and ops | Storage quota and upload limits; concurrent-run limit; outlier detection and `spend.alert` events; reconciliation job (fake-tested, key-gated); `cfctl spend report`; doctor checks; the runbook in docs/06 | P7-01a (in parallel with P7-01b) |
| P7-01c Checkout | Paddle (D-29): plans, top-ups, webhooks, `source = merchant` | Founder: the operating BV and the Paddle account |

**Acceptance for the program:**
- no billable action runs without a hold;
- the profit-invariant test passes for every plan;
- two workers can't overspend a cap (race test);
- a run never exceeds its budget;
- a creator sees credits, per-project and per-member use, and activity, and never our COGS;
- `cfctl spend report` reproduces a hand-computed margin on seeded data;
- every alert type fires in a test.
