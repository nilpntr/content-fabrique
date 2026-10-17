# Design note: spend control v1, profitable by construction

> Status: **accepted** · 2026-10-17 · Founder request: "build in spend control so we still have profit in the end, and spend stats in the org; think of everything needed for a prod-ready thing." Implements [D-31](../05-decisions.md#d-31-spend-control-credits-per-action-cogs-budgets-derived-from-price). Builds on [D-13](../05-decisions.md#d-13-billing), [D-18](../05-decisions.md#d-18-who-pays-for-tokens-managed-credits-or-bring-your-own-key), [architecture §13](../03-architecture.md#13-cost-model), the P4-00a ledger (`usage_ledger`, `config/pricing`) and the P0-16 plans. Checkout through the merchant of record ([D-29](../05-decisions.md#d-29-legal-entity-an-operating-bv-under-the-holding-paddle-as-merchant-of-record)) stays a separate, later task. Everything here works with manual grants.

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
| **P7-09a** Spend foundation | `usage_ledger` kinds and units and attribution columns; database holds and counters replacing in-memory reservations; platform breaker; fail closed on staging and production for every paid provider; append-only grants; price table sections for transcription, compute and storage; move the llm client and transcription onto it | P2-05 merged |
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
