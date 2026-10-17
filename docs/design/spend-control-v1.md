# Design note: spend control v1, profitable by construction

> Status: **accepted** · 2026-10-17 · **P7-09a (the foundation, §6 and §10) is built; see "As built" at the end of §6** · Founder request: "build in spend control so we still have profit in the end, and spend stats in the org; think of everything needed for a prod-ready thing." Implements [D-31](../05-decisions.md#d-31-spend-control-credits-per-action-cogs-budgets-derived-from-price). Builds on [D-13](../05-decisions.md#d-13-billing), [D-18](../05-decisions.md#d-18-who-pays-for-tokens-managed-credits-or-bring-your-own-key), [architecture §13](../03-architecture.md#13-cost-model), the P4-00a ledger (`usage_ledger`, `config/pricing`) and the P0-16 plans. Checkout through the merchant of record ([D-29](../05-decisions.md#d-29-legal-entity-an-operating-bv-under-the-holding-paddle-as-merchant-of-record)) stays a separate, later task. Everything here works with manual grants.

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
   - **Cancelled by the user:** charged if a preview had already rendered (the creator got value), released otherwise.
4. **Expiry.** A sweeper releases holds past their expiry: crashed workers, or runs past their wall clock plus a grace period. Settling is idempotent on the action id, so a retried job can't charge twice.

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
- A caller that dies stops beating, and its hold stops counting 5 minutes after the last beat; the sweep then deletes it. A call whose ledger row could not be written stops beating too (its hold is kept, not released, and lapses on its own).
- The model client, the transcription pipeline (provider calls of any length) use the same heartbeat. Renders and media jobs hold nothing.

**Busy is not a trip.**
- The breaker is *tripped* when the committed (settled) platform spend plus a small normal call no longer fits under the cap: `committed + PLATFORM_SPEND_SMALL_CALL_MICROS > cap` (default $0.10, `store.BreakerTripped`). Only a trip sets `tripped_at`, raises the 100 % alert and refuses with `platform_spend_paused`. The same rule is checked when a settle adds platform cost, so a crossing is alerted whether or not a call is refused at that moment.
- A reservation that is refused while the rule says "not tripped" (a large request next to the live holds, or a burst of in-flight calls near the cap) is **busy**: `spend_cap_busy` (`store.CapPlatformBusy`, `spend.ErrSpendCapBusy`, `503 spend_cap_busy` through `spend.HTTPError`). It sets no flag and raises no alert, so the real crossing later still alerts once.
- Callers wait on busy and fail only on a trip: an agent run sets `RunInfo.WaitOnBusy` and the model client retries the reservation with jittered backoff (2 s doubling to 1 min) until the run's wall-clock context ends, so a busy moment is never a failed run; transcription and quick look treat busy like a trip and wait (snooze) without using an attempt.

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
- `GET /v1/me` gains `credits {available, allowance, period_end, low}`

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
| **P7-01a** Credits | `config/plans` YAML and loader; `credit_accounts`, `credit_ledger`, `credit_holds`; period grants and expiry job; hold, settle and release wired into editor runs (first or revision decided by the server) and exports; budgets from the YAML; profit-invariant test; 402 `credits_exhausted`; `cfctl credits`; `/v1/me` credits | P7-09a |
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
