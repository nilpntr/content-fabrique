# 05 — Decision Log

> Lightweight ADRs. Each entry gives the context, the decision, the alternatives considered and when to revisit. Status values: **Accepted** (build on it), **Proposed** (default until validated in the noted phase), **Deferred**.
> Evidence links point to the [research report](02-research.md) and primary sources.

| ID | Decision | Status |
|---|---|---|
| D-01 | Postgres from day one; skip D1 for the Go backend | Accepted |
| D-02 | Agents edit a declarative timeline; a deterministic renderer compiles it to ffmpeg | Accepted |
| D-03 | Music: cue-only by default, never bake copyrighted trending audio into exports | Accepted |
| D-04 | Job orchestration with River (Postgres-backed) | Accepted |
| D-05 | Realtime via SSE from Go; Cloudflare K2 deferred | Accepted |
| D-06 | Playback via MP4 proxies, sprites and waveforms on R2; no Cloudflare Stream in v1 | Accepted |
| D-07 | Go-native agent loop on the Claude Messages API; Opus 5.5 edits; cheaper sub-agents for research and bulk work | Accepted (editor on Opus 5.5); sub-agent split tuned by D-17 |
| D-08 | Sandbox: hardened containers for our media jobs; Cloudflare Sandbox for agent code; all behind one interface | Proposed (validate in P1-03 / P4-08) |
| D-09 | Hosting: web on Workers; Go API + workers on EU servers behind Cloudflare Tunnel; burst to Cloudflare Containers | Proposed (validate in P0-10) |
| D-10 | Video understanding: deterministic signals + small-model frame tagging; video-native model optional | Proposed (validate with eval set #1, P2) |
| D-11 | Trend data: curation + web search + creator links + licensed music analytics; no Research API, no scraping without legal sign-off | Accepted for MVP |
| D-12 | Clerk organizations as workspaces | Accepted |
| D-13 | Billing: merchant of record for EU VAT; Clerk for identity only; own usage ledger | Proposed (decide before P7-01) |
| D-14 | OpenAPI 3.1 contract-first API with generated clients | Accepted |
| D-15 | EU data residency where the vendor allows | Accepted |
| D-16 | Only permissively licensed ML components in the product | Accepted |
| D-17 | Model routing is data, tuned by benchmark: effort first, then escalation, then delegation, then cheaper models | Accepted (protocol); results come from P4-10 |
| D-18 | Who pays for tokens: managed credits or bring-your-own Anthropic key, per workspace | Proposed (founder confirms the beta default) |

---

## D-01 Database: Postgres from day one, skip D1 for the Go backend

**Context.** The README plans "D1 temporarily … PlanetScale once really ready". D1 is SQLite built for Workers bindings. Our system of record is accessed by a **Go service outside Workers**, which can reach D1 only through Cloudflare's REST API. That API shares the account-wide limit of **1,200 requests per 5 minutes per user**, and exceeding it blocks all API calls for 5 minutes ([research §5.4](02-research.md#54-database-d1-vs-planetscale)). We also want transactional job enqueueing (D-04), `LISTEN/NOTIFY` (D-05) and later vector search.

**Decision.** Use **Postgres from the first commit**: PlanetScale Postgres in staging and production (local Postgres in Docker for development). Access it with `pgx` + `sqlc`, with migrations in the repo.

**Why.**
- It avoids a later SQLite → Postgres migration of schema, queries and data.
- Postgres gives us River (transactional enqueue), `LISTEN/NOTIFY`, JSONB, full-text search and `pgvector`. PlanetScale Postgres supports pgvector ([PlanetScale docs](https://planetscale.com/docs/postgres/extensions/pgvector)).
- Cost is not a blocker: PlanetScale's single-node Postgres starts at **$5/month** and the smallest HA cluster at **$15/month** ([PlanetScale plans](https://planetscale.com/docs/planetscale-plans)).

**Alternatives.** D1 through the REST API (shares the 1,200-per-5-minutes API budget; SQLite dialect; 10 GB per database). Neon or Supabase (good fallbacks; same Postgres code). Turso/libSQL.

**Revisit** if PlanetScale's EU regions or pricing don't fit. The code stays plain Postgres, so switching vendors is cheap.

## D-02 Edit representation: declarative timeline JSON + deterministic renderer

**Context.** The Editor agent must produce edits that are reliable, reviewable, revisable from comments and cheap to re-render. LLMs writing raw ffmpeg command lines is brittle and hard to validate or diff ([research §3](02-research.md#3-editing--rendering-stack-and-competitors)).

**Decision.** The **only** thing agents and users change is a versioned **timeline JSON** ([architecture §6](03-architecture.md#6-timeline-spec-and-renderer)), edited through validated high-level ops. A deterministic Go compiler renders it with ffmpeg. Raw shell is allowed only through the flagged sandbox escape hatch, and its outputs come back as assets the timeline references.

**Consequences.** We own a timeline schema and a compiler, so golden tests are mandatory. In return we get validation, diffs, an exact comment → clip mapping, reproducibility, and a path to in-browser live preview or alternative renderers.

**Alternatives.** OpenTimelineIO as the core model: richer, but heavier than we need. We could export to OTIO later for pro editors. Remotion compositions as the source of truth would tie us to a React renderer and its licence. Letting the LLM write ffmpeg was rejected.

## D-03 Music: cue-only by default

**Context.** Fitness TikTok runs on trending sounds, but those sounds are licensed for use *inside TikTok* ([research §1.3](02-research.md#13-music-licensing)):
- Personal accounts get the General Sound Library, licensed for non-commercial personal use only.
- Business accounts get only the Commercial Music Library.
- A platform that supplies the music itself is directly liable; the DMCA safe harbour covers only user-directed storage.

A SaaS that bakes a trending song into an exported MP4 makes copies it has no licence for. And the Content Posting API cannot attach a TikTok sound.

**Decision.**
- `trend_sound` items in a timeline default to `export: "cue_only"`.
  - The edit is cut to the sound's beat grid.
  - **Previews play a generated beat-guide track, never the song.**
  - The export ships **without** that audio, plus a cue (sound + start offset), so the creator adds the sound in TikTok, ideally from the inbox draft.
- The renderer *enforces* this policy: a test asserts that `cue_only` audio never reaches an export.
- Baked-in music is allowed only from sources with a licence covering our use (licensed stock library, the creator's own uploads with a rights attestation).

**Revisit** when we have a music licensing partner, or if TikTok offers an API path to attach sounds.

## D-04 Job orchestration: River on Postgres

**Decision.** Use [River](https://riverqueue.com) for all background work: ingest, analysis, renders, agent runs, trend scans and cleanup. Queues are separate per workload class with their own concurrency limits.

**Why.** It is Go-native and uses the same Postgres. Jobs are enqueued **in the same transaction** as the rows that need them (no dual-write bugs). It provides retries with backoff, unique jobs, periodic jobs and a UI.

**Alternatives.** Temporal (powerful, but heavy to operate at this stage), Cloudflare Queues/Workflows (Worker-centric; Go consumers would have to pull over HTTP), Asynq (adds Redis). **Revisit** if we need long-lived multi-step orchestration beyond what River plus our own run state machine can handle.

## D-05 Realtime updates: SSE from Go, K2 deferred

**Context.** The UI needs live upload, analysis, render and agent progress. The founder asked to use Cloudflare K2 only if it is really needed. K2 is a serverless, Kafka-like event-stream service built on R2, in public beta on the Workers Paid plan since October 2026 ([Cloudflare blog](https://blog.cloudflare.com/cloudflare-k2-streams/)).

**Decision.** Use **Server-Sent Events from the Go API**. Events are persisted in Postgres (`agent_events` and status events), fanned out with `LISTEN/NOTIFY` and replayed with `Last-Event-ID`.

**Why not K2 now.** Our event volume is small, our events are already durable in Postgres, and all producers and consumers live in our Go backend. K2 would add a beta dependency and a second source of truth without solving a problem we have.

**Revisit** when we need high-volume event pipelines (product analytics, usage metering into R2/Iceberg) or many independent consumers. That is K2's sweet spot.

## D-06 Video delivery: MP4 proxies on R2, no Cloudflare Stream in v1

**Decision.** Ingest produces a seek-friendly H.264 proxy (≈1 s GOP, faststart), a sprite sheet + VTT and waveform peaks, all stored in R2 and served with short-lived presigned URLs (HTTP range requests). Previews are rendered as MP4 too.

**Why.** We need the original files in R2 anyway for editing. Proxies are cheap to produce in the ingest job we already run, R2 has no egress fees, and a plain `<video>` element with 1 s keyframes scrubs well. Cloudflare Stream would add **$5/month per 1,000 minutes stored + $1 per 1,000 minutes delivered** for a second copy of every clip ([research §5.7](02-research.md#57-video-playback)).

**Revisit** if users need adaptive streaming of long footage on poor networks; then consider HLS proxies or Stream for playback only.

## D-07 Agent runtime: Go-native loop on the Claude Messages API

**Context.** Our agents mostly need **dedicated, typed tools** (search moments, view frames, edit timeline, render preview), not a general-purpose shell. The Claude Agent SDK is TypeScript/Python only. The Go SDK (`anthropic-sdk-go`) supports tool use, strict JSON-schema tools, structured outputs, streaming, prompt caching and a beta tool runner.

**Decision.**
- Agents run in our Go `worker` as a hand-owned loop on the Messages API, with persisted events, budgets and pause/resume ([architecture §7](03-architecture.md#7-agents)).
- **Models** (prices per MTok, Oct 2026). The founder decided the starting design; the benchmark in [D-17](#d-17-model-routing-is-data-tuned-by-benchmark) tunes the rest:
  - **Claude Opus 5.5** (`claude-opus-5-5`, $4 / $20) runs the **editor** end to end. *Decided.* Effort starts at `medium` (Opus 5.5's default, set explicitly) and is tuned by the benchmark.
  - **Research and other bulk reading use cheaper sub-agents directed by an expensive model.** *Decided as the starting design.* An Opus 5.5 lead plans, delegates and synthesises. **Claude Haiku 5.5** (`claude-haiku-5-5`, $0.10 / $0.50) workers search, read and extract. Claude Sonnet 5.5 (`claude-sonnet-5-5`, $2 / $10) workers are used where a task needs more judgement.
  - **Everything else starts on the cheapest model that passes its eval:** Haiku 5.5 for frame tagging, Sonnet 5.5 for ideation, Opus 5.5 for revisions (stepped down only if the benchmark shows parity).
  - Routing lives in versioned config behind an `llm` interface, so a video-native model can be added later ([D-10](#d-10-video-understanding)).
- **The editor stays a single model; there is no orchestrator around it.** It is one dependent chain that fits in one context window. Anthropic's measured runs found that for that shape the coordinator model alone at lower effort beat orchestrated setups, while orchestration paid off only where there was bulk to hand off ([cost guidance](https://platform.claude.com/docs/en/about-claude/models/optimizing-for-cost-and-intelligence)). The benchmark tests exactly one editor delegation variant: a Haiku "inspector" that shortlists candidate clips while Opus verifies the top picks itself.
- **Sub-agents** are nested loops in our Go runtime: a `delegate` tool starts a worker with its own model, tool subset, prompt and budget, and returns a compact structured report. Workers run in parallel. A worker starts a fresh prompt prefix, so it shares no cache with its parent; caches are model-scoped.
- **Claude Managed Agents** is the optional runtime for *free-form* sandbox sessions (the escape hatch), not the core path:
  - Anthropic runs the loop, and tools execute either in Anthropic's cloud container or on our own hosts through a Go environment worker that only makes outbound calls.
  - Sessions support hard dollar budgets. Session runtime is billed at $0.08/hour plus tokens.
  - Its documented "research lead + web researcher" roster (an Opus 5.5 coordinator with Haiku 5.5 workers, run as a scheduled deployment) is a ready-made alternative for the research agent. It is benchmarked against our Go-native sub-agents in P5-07.

**Revisit** after the P4-10 benchmark: if free-form sessions outperform the structured editor, promote Managed Agents to a first-class runtime.

## D-08 Sandbox: pluggable interface, default provider

**Context.** Two very different workloads need isolation ([research §4](02-research.md#4-agent-runtime-and-sandboxes)):
1. **Our own deterministic media jobs** (ingest, analysis, render). They parse untrusted user files and are CPU-heavy, with steady volume.
2. **Free-form agent code** (the `sandbox_exec` escape hatch). It is rare, bursty and the least trusted.

The README asks for a pluggable layer that can be "a hosted one or this one yourself".

**Decision.** One `sandbox.Provider` interface ([architecture §8](03-architecture.md#8-sandbox-layer-d-08)) with three implementations in v1:
- `docker`: local development.
- `hardened`: containers under **gVisor (`runsc`)** on our own worker hosts. No network except presigned R2 URLs; non-root; read-only rootfs; CPU, memory and time limits. **All deterministic media jobs run here.** gVisor needs no KVM, so it runs on ordinary VMs too; measure its I/O overhead in the P1-03 spike.
- `cloudflare`: the **Cloudflare Sandbox SDK** through a thin Worker shim, the **default for the agent escape hatch**. Each job gets its own sandbox with internet disabled. The job's R2 prefix is mounted through `S3Gateway`, so **credentials never enter the sandbox**. Limits: 4 vCPU / 12 GiB / 20 GB disk per instance; ≈ $0.072 per vCPU-hour.

**Alternatives behind the same interface:**
- E2B (Firecracker microVMs, ≈ $0.166/h for 2 vCPU / 4 GiB).
- Claude Managed Agents, when we want Anthropic to run the free-form loop: `cloud` environment, or `self_hosted` on our `hardened` hosts through the Go environment worker.
- Self-hosted Firecracker (E2B OSS, AgentENV) only beyond the cost crossover (≈ 2,000–11,000 sandbox-hours/month, depending on how ops time is valued) or if we need GPUs in sandboxes.

Daytona is not considered: its open-source repo was reported unmaintained or archived in 2026.

**Revisit** when sandbox-hours pass about 2,000 a month, when jobs exceed Cloudflare's per-instance limits, or when we need GPUs.

## D-09 Hosting

**Context.** The README suggests running "the backend on a VM so you can run sandboxes on there". Cloudflare Containers is an alternative ([research §5.2](02-research.md#52-backend-hosting-cloudflare-containers-vs-a-vm)):
- It is zero-ops, but each instance is limited to ≤ 4 vCPU, 12 GiB and 20 GB of ephemeral disk.
- It has no built-in autoscaling yet.
- At full utilisation it costs about 7–15× more per core-hour than a dedicated server, depending on whether you count cores or threads.

**Decision.**
- **Web:** Cloudflare Workers (TanStack Start via `@cloudflare/vite-plugin`).
- **API + workers (MVP → beta):** EU servers, for example one Hetzner dedicated server (AX-class, about 16 cores) for production and a smaller box for staging.
  - They run `api`, `worker` and the `hardened` job runtime as containers (Docker Compose first; Nomad if we outgrow it).
  - They are reachable only through **Cloudflare Tunnel**: no public inbound ports, with TLS, WAF and rate limiting at Cloudflare.
- **Scale-out path:**
  - Everything is stateless (state lives in Postgres and R2), so we can add worker boxes.
  - When queues back up we can **burst render and analysis workers onto Cloudflare Containers** with the same image.
  - The API can later move to Containers or another platform without code changes.

**Revisit** when ops work outweighs the savings, or when Containers gains autoscaling and bigger instances.

## D-10 Video understanding

**Context.** Gemini samples video at 1 fps at ~100–300 tokens/s and only gives **second-level timestamps**. Claude models take images, not video. Practitioners get the best results by having the model *read* structured descriptions and look at frames only on demand ([research §2](02-research.md#2-video-understanding-clip-selection-pose-and-beat-sync)).

**Decision.** A cheap-first hybrid:
- **Deterministic signals** give exact boundaries and quality: shot detection, sharpness, exposure and stability, MediaPipe pose (v1), Beat This! for audio we may process, and transcription for clips with speech.
- **Tagging:** Claude Haiku 5.5 on sampled keyframes with a strict JSON schema based on the fitness taxonomy (≈ $0.003 per footage-minute). Ambiguous shots escalate to Sonnet 5.5.
- **Optional video-native pass:** a Gemini Flash-class model at low media resolution, for whole-clip semantics (exercise sequence, rep counting), kept only if eval set #1 shows a clear gain. Twelve Labs is evaluated for semantic search in the same spike.
- **Model timestamps are hints**; cut points always come from the deterministic tools.

**Revisit** after the P2 evaluation, or when a video model offers frame-accurate grounding.

## D-11 Trend data sources

**Context.** There is no official commercial API for TikTok trends ([research §1.2](02-research.md#12-trend-data-sources)):
- The Research API is limited to non-commercial research.
- Creative Center has no API, and its song chart was unavailable in Sept–Oct 2026.
- Unofficial data APIs and scrapers carry terms-of-service, legal and GDPR risk.

**Decision (MVP).**
1. A **curated concept library**: the 12 seed formats in [product plan §14](01-product-plan.md#14-seed-concept-library-hypotheses-to-validate), maintained by us with manual Creative Center review (hashtags, trending videos).
2. A **Research agent with web search** over public trend roundups and platform blogs, storing evidence links.
3. **Creator-supplied TikTok links**, resolved through TikTok's official oEmbed endpoint.
4. **Licensed music analytics** (Chartmetric from ~$350/month, or Soundcharts) for trending songs once the beta shows creators use the feature.

**Not used:** the TikTok Research API; automated scraping of Creative Center (especially with logged-in cookies). Unofficial providers (EnsembleData and similar) sit behind `trends.Provider` and are enabled only after a legal review.

**Revisit** if TikTok ships a commercial trends API or Creative Center's song chart returns.

## D-12 Auth & tenancy: Clerk organizations as workspaces

**Decision.**
- Clerk handles sign-up, sign-in and sessions in the web app.
- Every workspace is a Clerk organization (a solo creator gets a personal org created at onboarding), so teams and agencies need no migration later.
- The Go API verifies Clerk session JWTs with the Go SDK v2 middleware (`clerkhttp.RequireHeaderAuthorization`, claims via `clerk.SessionClaimsFromContext`) and resolves `workspace_id` from the active org.
- Clerk webhooks (Svix-signed) sync users, orgs and memberships into Postgres idempotently.

**Revisit** if Clerk pricing at scale, or EU data-residency requirements, become a problem.

## D-13 Billing

**Context.** We are an EU-based business selling subscriptions to creators worldwide, so VAT and sales tax compliance is non-trivial ([research §5.5](02-research.md#55-auth-and-billing)):
- Clerk Billing charges 0.7 % on top of Stripe fees, but it has **no tax or VAT support yet**, is **not a merchant of record**, and has no usage metering.
- A third-party guide also reports it is USD-only without 3-D Secure.

**Decision.**
- **Clerk for identity only.**
- **Billing through a merchant of record** that collects and remits VAT and sales tax: Stripe's merchant-of-record product (Managed Payments) if it is available for our entity and product category, otherwise **Paddle** (≈ 5 % + $0.50 per transaction).
- Plans and entitlements are synced to Postgres via webhooks, and our `usage_ledger` enforces quotas and credits whatever the provider.
- There are **two plan families** ([D-18](#d-18-who-pays-for-tokens-managed-credits-or-bring-your-own-key)): managed plans with a monthly credit allowance, and a lower-priced BYOK plan where the creator's own Anthropic key pays for tokens.

**Alternatives.** Stripe Billing + Stripe Tax: lower fees (0.5 % for tax), but we register and file VAT ourselves. Clerk Billing: reconsider once it supports tax, EUR and strong customer authentication.

**Revisit** before P7-01, with real price quotes.

## D-14 API contract: OpenAPI 3.1, contract-first

**Decision.** `packages/schemas/openapi.yaml` is the source of truth. We generate the TS client (`openapi-typescript` + `openapi-fetch`, wrapped in TanStack Query hooks) and Go server interfaces (`oapi-codegen`), and CI fails on drift. The timeline JSON Schema lives next to it and is shared by Go validation and the web app.

## D-15 EU data residency

**Decision.**
- Create R2 buckets with the **EU jurisdiction**. Jurisdiction is fixed at creation, and these buckets must be accessed, including presigning, through the `https://<ACCOUNT_ID>.eu.r2.cloudflarestorage.com` endpoint ([Cloudflare docs](https://developers.cloudflare.com/r2/reference/data-location/)).
- Run the database in an EU region (D-01).
- Prefer EU processing options from model providers where they exist.
- Record every processor in the GDPR records of processing.

**Consequence.** All presigning code must use the jurisdiction endpoint. Signing against the global endpoint fails with 403 for EU buckets.

## D-16 Licensing policy for ML and media components

**Context.** Several popular open-source vision and audio components carry licences that are incompatible with a closed SaaS ([research §2](02-research.md#2-video-understanding-clip-selection-pose-and-beat-sync)):
- Ultralytics YOLO is **AGPL-3.0** unless you buy an Enterprise licence ([Ultralytics](https://pypi.org/project/ultralytics/8.3.80/)).
- Essentia is **AGPL-3.0**.
- madmom's code is BSD, but its **pre-trained models are CC BY-NC-SA 4.0** (non-commercial) ([madmom paper](https://arxiv.org/pdf/1605.07008)).

**Decision.** Only permissively licensed components (MIT, BSD, Apache-2.0, ISC) ship in the product unless a commercial licence is bought. Defaults:
- **Beat tracking**: Beat This! (code and published weights MIT per [its README](https://github.com/CPJKU/beat_this); some training audio is copyrighted, so record it in the inventory) + librosa (ISC).
- **Pose**: MediaPipe Pose Landmarker (Apache-2.0; check the model-card terms of the bundle we ship).
- **Shot detection**: PySceneDetect / ffmpeg `scdet`.

`media-tools` keeps a licence inventory, and CI fails on new copyleft or non-commercial dependencies.

## D-17 Model routing is data, tuned by benchmark

**Context.** The founder's direction: Opus 5.5 for editing, cheaper sub-agents directed by an expensive model for research, and benchmarking to find out the rest. Anthropic's cost guidance ([cost guidance](https://platform.claude.com/docs/en/about-claude/models/optimizing-for-cost-and-intelligence)) sets the order to work in and warns that per-token prices don't predict cost per *completed* task:
- The lever order is caching, then input hygiene, then batch, then effort, with model choice last. Change one lever at a time and keep or revert on measured evidence.
- Sweep effort before dropping a model tier.
- An orchestrator pays only when there is bulk to hand off.

Our own model of an Opus 5.5 editor run ([architecture §13](03-architecture.md#13-cost-model)) says about half the spend is output tokens (thinking and tool calls), about 40 % is cache writes and under 10 % is cache reads. So effort and turn count matter far more than moving image viewing to a cheaper model, which saves only about 5 %.

**Decision.**
1. **Routing profiles are versioned config** (`config/routing/*.yaml`): per stage a model, effort, escalation rule, sub-agents and a batch flag. Code refers to a stage name, never to a model ID.
2. **`cfctl bench` runs scenario × profile × trial** against recorded fixtures (pre-analysed clip libraries; the real renderer; sandbox and posting stubbed) and writes a dated report to `docs/benchmarks/`.
3. **Protocol: one lever at a time, in this order.**
   1. Effort sweep on Opus 5.5 (`low`, `medium`, `high`), each level in separate sessions with otherwise identical requests.
   2. **Escalate on failure:** run at the cheaper effort and re-run at the higher effort only when the deterministic hard-constraint checks fail. We have a cheap failure signal, and in Anthropic's coding runs this held the pass rate for about half the cost.
   3. **Delegation variants:** the editor "inspector" (Haiku 5.5 shortlists, Opus 5.5 verifies the top picks), and the research lead with Haiku 5.5 workers, also against Managed Agents (P5-07).
   4. **Cheaper-model step-down** for revisions and ideation (Sonnet 5.5, then Haiku 5.5), one notch at a time.

   Keep a change only if quality holds within noise and cost per completed task falls. Otherwise revert and record why.
4. **Metrics:** hard-constraint pass rate; LLM-judge rubric score, calibrated against human ratings on ~30 samples; a human "would you post it?" rating on a sample; cost per completed task (mean and p90, because the tail decides the bill); latency; turns; failure rate. The output is a quality-versus-cost Pareto chart.
5. **Rigour:** development sweeps use 20–30 scenarios with at least 3 trials each. The production cutover uses about 50 scenarios with at least 5 trials, the bar Anthropic publishes. A difference of a scenario or two is noise.
6. **Budget** (estimates at Oct 2026 prices, approved once): ≈ $350 per editor development sweep (30 scenarios × 6 profiles × 3 trials), ≈ $1,000 for the cutover measurement, and ≈ $150 for the research and ideation benchmarks. Judge calls run through the Batch API at 50 % off.
7. **Re-run** on every new model release and every major prompt change. Rankings can flip between models and workloads.

**Alternatives.** Choosing by price list or intuition (rejected: it doesn't predict cost per completed task). A router model that picks a model per request (rejected for v1: another failure mode, and it loses cache locality).

**Consequences.** The harness, a consented eval dataset and an approved budget become P4 deliverables ([implementation plan](04-implementation-plan.md)). Until the first sweep finishes, the starting profile in [D-07](#d-07-agent-runtime-go-native-loop-on-the-claude-messages-api) applies.

**Revisit** on every model release.

## D-18 Who pays for tokens: managed credits or bring-your-own key

**Context.** The founder's direction: make people use their own keys, or a subscription where we still take a profit. What the research found:
- **Cost.** With Opus 5.5 on every edit, LLM spend is about $1.15 per exported video, about $1.25 all-in ([architecture §13](03-architecture.md#13-cost-model)). That can't be sold at "unlimited clipping" prices.
- **"Your own key" means an Anthropic Console API key.** Claude.ai Free, Pro and Max subscription logins can't be used inside third-party products: Anthropic's consumer terms bar it, and it was enforced from January 2026 ([The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546), [WinBuzzer](https://winbuzzer.com/2026/02/19/anthropic-bans-claude-subscription-oauth-in-third-party-apps-xcxwbn/)). Both are secondary sources, so confirm on Anthropic's legal pages (P0-12).
- **New keys start on low rate limits.** A third-party report says tiers were consolidated into Start, Build and Scale in June 2026, with Start at about 50 requests per minute and promotion after roughly $40 of spend ([Knightli](https://knightli.com/en/2026/06/29/claude-api-rate-limits-rpm-itpm-otpm-429/); unverified, check the [rate-limits doc](https://platform.claude.com/docs/en/api/rate-limits)). Cache reads don't count toward input-token limits on most models, so prompt caching also helps here.
- **Spend limits exist per workspace** in the Anthropic Console ([Anthropic](https://claude.com/blog/workspaces)), so a BYOK creator can cap their own exposure with a dedicated workspace and key.
- **Prompt caches are isolated per workspace** ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)). In managed mode all tenants share one production workspace, so the static prompt prefix (system prompt and tool definitions) is cached across customers. Each BYOK key has its own cache.

**Decision.** Two ways to pay, chosen per workspace (`workspaces.billing_mode`):
- **Managed (credits).** We call Claude with our key.
  - The subscription includes a monthly credit allowance.
  - Credits are priced **per action**, not per token, and calibrated so that realised COGS stays at or below about 40 % of credit value.
  - There are hard caps per run, per day and per month, plus top-up packs.
- **BYOK.** The creator pastes an Anthropic Console API key (we recommend a dedicated workspace key with a spend limit).
  - A lower platform fee covers compute, storage, transcription and support.
  - Anthropic bills tokens to the creator, and we show an estimated cost per run from our own metering.
  - Plan quotas still cap renders, storage and exports.
- **One code path.** `llm.ClientFactory(workspace)` returns a client for the right key, and every call writes a ledger row with `billed_to` ([architecture §11a](03-architecture.md#11a-model-access-managed-credits-or-byok)).
- **Defaults.** Managed is the default for mainstream creators and for the capped private beta. BYOK is offered from the beta for power users and agencies. Whether BYOK becomes *mandatory* on any plan is the founder's call.
- **Always on our key:** work that serves the whole platform (the shared trend board, research scans) is a platform cost.
- **Not supported:** reusing a creator's Claude Pro or Max login.

**Why not BYOK-only.** The target creator would need an Anthropic account, a payment method, a key, and an understanding of rate-limit tiers, and a fresh key will hit 429s when agents run in parallel. That friction is likely to cut activation. BYOK-only is a sound choice for a developer-savvy or agency segment, and it removes our LLM cost risk entirely.

**Why not managed-only.** At Opus-class costs the margin is thin or negative for heavy users unless prices are high, and power users want control over spend.

**Consequences.** We take on key custody and security work, a rate-limit-aware client, a cost UI, two plan families, and legal checks (Anthropic's terms for customer keys; GDPR roles, because with BYOK the creator's Anthropic account is also a processor of their footage).

**Revisit** after the beta: conversion by billing mode, BYOK support burden, and realised COGS.

## Template for new decisions

```
## D-xx Title
**Context.** Why we need to decide.
**Decision.** What we do.
**Alternatives.** What else we considered and why not.
**Consequences.** What this makes easier or harder.
**Revisit** when …
```
