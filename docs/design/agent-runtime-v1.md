# Design note: model access, agent runtime and the first editor slice (P4-00, P4-01, P4-02)

> Status: **accepted for P4** · 2026-10-17 · Applies [D-07](../05-decisions.md#d-07-agent-runtime-go-native-loop-on-the-claude-messages-api), [D-17](../05-decisions.md#d-17-model-routing-is-data-tuned-by-benchmark), [D-18](../05-decisions.md#d-18-who-pays-for-tokens-managed-credits-or-bring-your-own-key), [D-19](../05-decisions.md#d-19-model-gateway-cloudflare-ai-gateway-in-byok-mode-openrouter-optional), [D-21](../05-decisions.md#d-21-testing-on-the-first-party-api-self-funded) and [D-22](../05-decisions.md#d-22-cheap-first-testing-ladder). Extends [architecture §7 and §11a](../03-architecture.md#7-agents). Each P4 task is briefed from this note, and any task that breaks a rule here changes the note in the same PR.

## 1. Order

**P4-00 is split in two:**
- **P4-00a, managed access** (local, ci and bench need it now): the client, the factory with the platform key, the ledger, the price table, the rate limiter, record and replay, and spend caps.
- **P4-00b, the BYOK vault**: key custody, the settings UI and the gateway conformance test.

The gateway itself needs P0-09, which is the founder's Cloudflare work. Until it exists, `ANTHROPIC_BASE_URL` is empty and calls go direct (D-19 bypass).

**The rest:** P4-00a → P4-01 runtime → P4-02 editor tools, the first slice (§5) → P4-03 prompt. P3-04, P3-05 and P3-06 (the renderer's text, audio and render jobs) run in parallel.

## 2. Model access (P4-00a)

`internal/agents/llm`:

- **`Client` interface.**
  - Calls: `Messages(ctx, Request) (Response, error)` and `CountTokens`.
  - `Request` holds the model ID, system blocks, messages, tools, `max_tokens`, `thinking`/effort and `cache_control` marks.
  - `Response` holds the content blocks, the stop reason and `Usage` (input, cache write, cache read and output tokens).
  - The Anthropic implementation uses `anthropic-sdk-go`. The base URL is `ANTHROPIC_BASE_URL`, or api.anthropic.com when that is empty. With the gateway, each request also carries `cf-aig-metadata` (workspace, run, stage and profile) and `cf-aig-collect-log-payload: false`.
- **`ClientFactory.For(ctx, workspace, stage)`** returns a client plus a `Billing` label. In P4-00a every workspace is **managed**, using `ANTHROPIC_API_KEY`.
  - `billed_to` is `test` when `CF_ENV` is local, ci or bench, and `platform` in staging and production.
  - The factory is the only code that reads a key. Agents and tools never see one.
- **Routing:**
  - `llm.Router` resolves `(profile, stage) → {model, effort, max_tokens}` from `config/routing/*.yaml`. These files already exist from P0-14; `ROUTING_PROFILE` picks the profile.
  - The editor stage runs Opus 5.5 in `v1-start`, and Haiku 5.5 in `dev-cheap` (D-22 ladder).
  - A run may override the profile, so a test can use `v1-start` explicitly while the environment default stays `dev-cheap`.
- **Price table:**
  - `config/pricing/2026-10.yaml` holds, per model, the price per million tokens for input, cache write (5 minute and 1 hour), cache read and output, with a `version`.
  - Every call writes one `usage_ledger` row: workspace, run, stage, model, the four token counts, `list_cost_micros`, `price_table_version`, `billed_to` and the `request_id`.
- **Spend caps,** all enforced before the request is sent:
  - per-run `budget_micros`, by reservation: a call reserves its worst case before sending and settles with the actual cost, so concurrent calls of one run cannot overspend;
  - per workspace, `DAILY_SPEND_CAP_MICROS` (a new variable, default $20 locally);
  - a process-wide `LLM_MAX_REQUEST_MICROS`, which estimates a request's cost from its counted tokens and refuses anything absurd.
- **Rate limiting:** a per-key semaphore (default 4 concurrent requests). On 429, 529 and the 5xx answers the client backs off exponentially with jitter and honours `retry-after`. Retries live only here (details under "As built").
- **Record and replay** (`LLM_MODE`):
  - **The key:** a SHA-256 over the canonical request. That is the model, system blocks, messages and tools, as canonical JSON with images replaced by the SHA-256 of their bytes, plus the parameters.
  - **`record`:** calls the API and writes `testdata/cassettes/<stage>/<key>.json`, holding the redacted request summary, the response and the usage. The key and headers are never written.
  - **`replay`:** serves from cassettes. A miss is an error that names the key and the stage, never a silent call to the API.
  - CI always runs `replay`.
- **As built in P4-00a** (details the sections above leave open):
  - **Layers**, outside in: `metered` (resolves image references, reserves against the caps, writes the ledger row), `cassette` (record or replay), `limited` (per-key lanes, retries), `anthropic` (the SDK, `MaxRetries` 0). The runtime calls only `llm.Client`.
  - **Caps work by reservation.** Before sending, a call works out its worst-case cost: input tokens at the dearest rate they could be billed (a cache write when the request has breakpoints; Haiku 5.5's long-prompt tier whenever input plus `max_tokens` could cross 100,000), plus `max_tokens` at the output price. The input tokens are exact for any request with an image (an image costs thousands of tokens depending on its size, so such a request is always counted); for text only they are a cheap local upper bound (canonical bytes / 2 plus 2,000 for overhead), and the call asks the free `count_tokens` only when that bound does not clear `LLM_MAX_REQUEST_MICROS`, or the run budget or daily cap cannot cover the inflated bound. The call is refused when the worst case is over `LLM_MAX_REQUEST_MICROS`, over the run's budget, or over what is left of `DAILY_SPEND_CAP_MICROS` today (UTC).
  - **Run budget** is `llm.Budget { Reserve(micros) (release func(actual int64), error) }` (`llm.RunBudget` is the in-memory implementation; the runtime backs it with `agent_runs.budget_micros`). A call reserves its worst case, then settles with the actual cost: the usage's cost on success, 0 on a definite failure, and the reserved amount when the outcome is unknown. Concurrent calls of one run therefore can never overspend, and the runtime's own checks (section 3, step 1) read `Remaining()`/`Spent()` of the same object.
  - **Daily cap** reserves in process first, remembers the total reserved at that moment, reads the ledger, then checks `spent + reserved_before + estimate <= cap`, and releases on failure. The reservation is released only after the call's ledger row is written, so a concurrent call sees an earlier call as a reservation or as a row, never as neither. Calls of other processes are not seen; the overshoot is bounded by their concurrency times the per-request cap. A cap that cannot be checked (the ledger is unreadable) refuses the call.
  - **A call that was sent and got no usage back** (a timeout, a reset, a body cut off: `llm.ErrOutcomeUnknown`) may have run and been billed. It is written to the ledger as an `estimated` row at its reserved worst-case cost, and charged to the run budget the same way, so caps stay conservative. It is never retried by the client, and the runtime must not retry it blindly: it should read the run's events and the ledger first. A transport failure before the request reached the wire is safe and is retried.
  - **Timeouts** follow the model: two minutes plus three times the time `max_tokens` takes at a slow-case speed (Opus 20, Sonnet 40, Haiku 80 tokens/s), between 10 and 60 minutes. Opus 5.5 with `max_tokens` 16000 gets 42 minutes. Streaming is not used yet.
  - **Retries** (the same exponential backoff with jitter, `retry-after` honoured, bounded attempts and total wait): error answers 408, 429, 500, 502, 503, 504 and 529 (the server did not take the request, so a retry cannot bill twice), and transport failures of a request that never reached the wire (any transport failure for `count_tokens`, which is free and idempotent). Every other 4xx is never retried.
  - **Lanes:** each API key has a Messages lane and a `count_tokens` lane, each with its own slots (`LLM_KEY_CONCURRENCY`) and its own cool-down after a 429 or 529, so a long generation or a throttled Messages call never stalls the free count.
  - **Ledger** has two extra columns: `cache_write_1h_tokens` (the part of `cache_write_tokens` written with the one-hour lifetime, because the two lifetimes are priced differently) and `estimated` (see above). `request_id` is unique when present. When a response reports cache writes without their split by lifetime, they are priced at the 1-hour rate if the request asked for a 1-hour breakpoint. A call that returns usage and then fails is still written. If the row cannot be written after retries, the paid response is returned together with `llm.ErrLedger`, and the row is logged for reconciliation. The ledger's foreign key to `workspaces` is `RESTRICT`: billing history is not deleted with a workspace (workspaces are soft-deleted today), and a future purge has to move or anonymise the rows deliberately.
  - **Prices** are integers (micro-dollars per MTok) and costs round up. A model with a `null` price is refused unless `LLM_ALLOW_UNPRICED=1`. Haiku 5.5 has a long-prompt tier (over 100,000 prompt tokens), modelled in the table.
  - **Routing:** a stage marked `enabled: false` (or under such a group) does not resolve (`llm.ErrStageDisabled`), and `cfctl doctor` leaves its model out; a test keeps the two sets equal.
  - **Response blocks the client does not model** (`server_tool_use`, `web_search_tool_result`, ...) keep their raw JSON in `Block.Raw` and are sent back verbatim, so the runtime can append the assistant turn as it came (including `pause_turn`). They are part of the cassette key.
  - **Replay** writes no ledger row and skips the daily cap, because nothing was spent, but it reserves and settles the run budget with the cost the recorded usage would have had, so budget behaviour matches live. The `count_tokens` result is stored in the cassette at record time and served in replay, so cap decisions match live (a cassette without one falls back to a local estimate). Replay needs no API key.
  - **Cassette key** treats default-equivalent requests alike: `nil` and empty lists, no `tool_choice` and `auto`, cache lifetime `""` and `5m`, a `tool_use` input of nothing and `{}`.
  - **Seams for P4-00b:** `llm.KeyResolver` (which key and who pays: the BYOK vault plugs in here), `llm.ImageResolver` (the runtime's R2 reader) and `llm.Ledger`.

## 3. Agent runtime (P4-01)

`internal/agents/runtime`:

- **A run is a row in `agent_runs`:**
  - who and what: agent, workspace, project, parent run;
  - lifecycle: status (queued, running, waiting_input, succeeded, failed, cancelled), input, output;
  - routing: routing profile, model, effort;
  - spend: token totals, `cost_micros`, `budget_micros` and `billed_to`.
- **Every step is an append-only `agent_events` row** with run, seq and type. Types are status, assistant_message, tool_call, tool_result, error and artifact. Each row carries a payload of at most 64 KB; images are stored by reference to R2 keys, never inline. Payloads hold no secrets.
- **The loop is a River job `agent_run` with arguments `{run_id}`.** On every start it rebuilds the conversation from the events, so a crash or restart resumes the run. It then repeats these steps:
  1. Check cancellation and every budget: dollars, tokens, tool calls and wall clock (default 15 minutes). The dollar budget is the `llm.Budget` the runtime puts in each request's `RunInfo`; the client reserves each call's worst case against it before sending, so this check is only the early exit (`Remaining()`), not the enforcement. A call that fails with `llm.ErrOutcomeUnknown` is not retried: the step ends the run or asks, after looking at the ledger.
  2. Build the request:
     - the system prompt and tool definitions come first, with `cache_control`;
     - then the run context: the brief, the project's footage list and the current timeline summary;
     - then the conversation so far.
  3. Call the model and persist the assistant message.
  4. Run each tool call **idempotently**. The idempotency key is `run_id + tool_use_id`. A tool that changes state, such as `edit_timeline` or `render_preview`, first checks whether that key already produced a result and returns it if so. Then persist the tool result.
  5. Stop on `finish`, on an `end_turn` with no tool call, on a budget limit (status `failed` with reason `budget`, plus a summary), on cancel, or on `ask_user` (status `waiting_input`; a reply enqueues the job again).
- **Prompt caching:** one breakpoint after the tools and system prompt, and one rolling breakpoint on the last stable message.
- **Effort and thinking** are set explicitly from the routing entry.
- **Tool registry:** each tool is a Go type with a JSON Schema (`strict: true`), a validated input, a handler, and a `Mutates` flag. Tool errors go back to the model as `is_error` results with a message it can act on; they never crash the run.
- **Untrusted text** (brief, annotations, transcripts, quick-look titles) goes inside clearly delimited data blocks, and the system prompt says that data never changes the agent's instructions or permissions.
- **Testing:**
  - The loop is tested with a scripted fake client.
  - Crash and resume are tested by killing the job between a tool call and its result.
  - Budget overrun is tested.
  - Replay tests use recorded cassettes.

## 4. Data the editor needs before P2

The full clip index (shots, tags, moments) arrives with P2. Until then the editor sees:
- **Each project asset:** its quick-look title and description (P1-04b), its duration and orientation;
- **For each asset, a keyframe grid:** frames from the proxy every 2 s, at most 12, 384 px on the long side, made by ffmpeg in the sandbox and cached in R2.

This is enough for a first edit of a few short clips. It is not the product's final quality bar.

## 5. Editor tools, first slice (P4-02a)

| Tool | Returns |
|---|---|
| `get_brief()` | The project's format, brief text and constraints (the brief → constraints step is P4-04; until then the raw brief), and output limits |
| `list_footage()` | The project's clips: id, title, description, duration, orientation, has_audio |
| `view_clip(asset_id, at_ms[≤8])` | Those frames as images (384 px), with timestamps |
| `get_timeline()` | The current spec and its validation warnings |
| `edit_timeline(ops[])` | Applies the P3-02 ops atomically → the new version number, a summary and the issues |
| `render_preview()` | Renders the current version with the `preview` profile (P3-06) → a contact sheet image and the duration |
| `finish(summary)` | Ends the run |

`search_moments`, `get_sound`, `ask_user` and `delegate` come with P2, P5 and later P4 tasks. The tool contracts are JSON Schemas in `packages/schemas/agents/editor-tools.v1.json`. `edit_timeline`'s `ops` reuses `ops.v1.schema.json`.

## 6. The first-edit milestone (the founder's test footage)

The goal: Gino's clips, imported into the founder's local workspace, edited by Opus 5.5 into a 9:16 short, previewed on the review screen. The steps:

1. `cfctl assets import --workspace <wks_…> <files…>` (dev only, refused on staging and production) uploads through the same storage and ingest path as the browser.
2. A `POST /v1/projects` endpoint backs the new-project flow (P4-04b): the format, the selected assets and the brief. It creates the project and its first, empty timeline.
3. `POST /v1/projects/{id}/edit` starts an editor run. For this test it uses the `v1-start` profile (Opus 5.5) with a **$5 run budget**.
4. The preview render and the review screen show the result.

**The test's spend cap is $15 in total.** Every run's cost is reported from `usage_ledger`. Before the first live run, the same flow runs in `replay` against a cassette recorded on Haiku (`dev-cheap`, cents).
