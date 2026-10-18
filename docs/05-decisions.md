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
| D-16 | Only permissively licensed ML components in the product; GPL/LGPL CLI tools (ffmpeg, x264) allowed as separate processes, AGPL and non-commercial banned | Accepted; amended 2026-10-17 (founder to confirm) |
| D-17 | Model routing is data, tuned by benchmark: effort first, then escalation, then delegation, then cheaper models | Accepted (protocol); results come from P4-10 |
| D-18 | Who pays for tokens: managed credits or bring-your-own Anthropic key, per workspace | Proposed (founder confirms the beta default) |
| D-19 | Model gateway: Cloudflare AI Gateway in BYOK mode in front of every model call; other providers through the same gateway, OpenRouter not needed | Accepted (the founder chose it for customer traffic); gated by the conformance test in P4-00 |
| D-20 | Two model lanes: AWS Activate credits fund testing on Amazon Bedrock, direct; customer traffic goes through the gateway on its own funding | Superseded by D-21 (the founder dropped the AWS credits) |
| D-21 | Testing uses the first-party Claude API through its own dev gateway and is self-funded; no AWS credits, no Bedrock lane | Accepted (founder decision) |
| D-22 | Cheap-first testing ladder: free replay, then Haiku, then Sonnet, then Opus, with an exit checklist per rung | Accepted (founder direction) |
| D-23 | Product domain `contentfabrique.app`: the apex serves the landing page and the app, `api.` the API, `staging.` and `api-staging.` for staging | Accepted (the founder bought the domain) |
| D-24 | Public IDs are TypeIDs (a type prefix plus a base32 UUIDv7); Postgres stores the plain `uuid` | Accepted (engineering, 2026-10-16) |
| D-25 | Four output formats in v1: 9:16, 1:1, 4:5 and 16:9, chosen per project; TikTok stays the only publishing target | Accepted (founder decision, 2026-10-17) |
| D-26 | Video effects are curated presets (camera moves, transitions, graphic templates) the agent and the creator pick; bespoke effects only through the flagged sandbox | Accepted (director, 2026-10-17; founder to confirm the first templates) |
| D-27 | Transcription: whisper.cpp in the media-tools sandbox locally; Workers AI `whisper-large-v3-turbo` through Cloudflare on staging and production; one provider interface | Accepted (founder decision, 2026-10-17) |
| D-28 | The editor is content-aware across genres (talking, unboxing/product, workout, vlog, tutorial); fitness stays the launch niche for trends and marketing | Accepted (founder direction, 2026-10-17) |
| D-29 | Content Fabrique runs in its own Dutch operating BV (werkmaatschappij) under the founder's holding; Paddle is the merchant of record | Proposed (founder direction 2026-10-17; confirm with the accountant) |
| D-30 | Semantic search lives in Postgres with pgvector, hybrid with full-text search; no separate vector database | Proposed (director, 2026-10-17; founder asked) |
| D-31 | Spend control: credits charged per action; every action's COGS budget is derived from its price (≤ 40 % of net credit value); caps hold across processes in the database; platform breaker | Accepted (director, 2026-10-17; founder asked) |
| D-32 | Audio: effects are synthesised in ffmpeg and edits are checked with ffmpeg measurements; no audio models, nothing ported from earshot but its numbers and ideas | Accepted (founder decision, 2026-10-17) |
| D-33 | Exports are sound-ready: no music and no trend sound, the clips' own sound as a low bed, effects on top, speech as the programme; amends D-03 | Proposed (director, 2026-10-17; the founder confirms); superseded by D-34 for an edit with a song |
| D-34 | Music from the creator: a song the creator uploads (or the sound of one of their videos) is analysed once and is the export's only sound, -14 LUFS; the creator answers for its rights; supersedes D-33 for an edit with a song | Proposed (director, 2026-10-18; the founder confirms) |

---

## D-01 Database: Postgres from day one, skip D1 for the Go backend

**Context.** The README plans "D1 temporarily … PlanetScale once really ready". D1 is SQLite built for Workers bindings. Our system of record is accessed by a **Go service outside Workers**, which can reach D1 only through Cloudflare's REST API. That API shares the account-wide limit of **1,200 requests per 5 minutes per user**, and exceeding it blocks all API calls for 5 minutes ([research §5.4](02-research.md#54-database-d1-vs-planetscale)). We also want transactional job enqueueing (D-04), `LISTEN/NOTIFY` (D-05) and later vector search.

**Decision.** Use **Postgres from the first commit**: PlanetScale Postgres in staging and production (local Postgres in Docker for development). Access it with `pgx` + `sqlc`, with migrations in the repo.

**Why.**
- It avoids a later SQLite → Postgres migration of schema, queries and data.
- Postgres gives us River (transactional enqueue), `LISTEN/NOTIFY`, JSONB, full-text search and `pgvector`. PlanetScale Postgres supports pgvector ([PlanetScale docs](https://planetscale.com/docs/postgres/extensions/pgvector)).
- Cost is not a blocker: PlanetScale's single-node Postgres starts at **$5/month** and the smallest HA cluster at **$15/month** ([PlanetScale plans](https://planetscale.com/docs/planetscale-plans)).

**Consequences.** Postgres 18 from day one, for the native `uuidv7()` that the id columns default to ([D-24](#d-24-public-ids-are-typeids-over-uuidv7)); the local image, CI and `cfctl doctor` all require it, and the hosted database must offer it.

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

**Amended by [D-33](#d-33-exports-are-sound-ready):** what the export holds besides the cue is now decided (a bed of the clips' own sound and the generated effects); the cue-only rule itself is unchanged.

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

**Validated in P1-03 (2026-10-17).** On a GitHub-hosted 4-vCPU runner with runsc 20261012.0, a 30 s 1080p H.264 transcode takes 7.41 s under `hardened` against 7.46 s under `docker` (-0.8 %, noise), a 0.9 GB read-and-write copy is within 5 %, and the only measured cost is start-up, +30 to 50 ms per job. gVisor stays the `hardened` runtime; repeat the benchmark on the first real worker host. Details: [benchmarks/2026-10-17-gvisor-overhead.md](benchmarks/2026-10-17-gvisor-overhead.md).

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
- **Clerk settings.** Organizations on, and membership required, so every signed-in user has an active workspace and `workspace_id` always resolves. Keep the default Admin and Member roles in v1, and add custom roles only when the Team and Agency plan needs them.
- **Secrets.** The Go service uses `CLERK_SECRET_KEY` with the SDK middleware, which fetches the instance's public signing keys, so there is no separate API secret. The webhook signing secret is the only other one.

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
- Prefer EU processing options from model providers where they exist. As of this research the first-party Claude API offers only `global` or `us` inference ([research §4.5](02-research.md#45-funding-claude-usage-anthropic-credits-aws-activate-and-google)). EU-only inference exists through Bedrock's regional endpoints at a 10 % premium, with the feature gaps listed there. If EU creators need EU-only processing, that is the option to revisit, paid at list price.
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

**Amendment, 2026-10-17** (director, while the founder was away; **founder to confirm**). The rule above was written for ML components. Read literally, it also bans ffmpeg (LGPL, and GPL once x264 is enabled), which the media pipeline and the renderer need ([architecture §6](03-architecture.md#6-timeline-spec-and-renderer)). Clarified:
- **Allowed: GPL or LGPL command-line tools** that run as separate processes inside our own containers. These are ffmpeg and ffprobe built with `--enable-gpl`, x264, and libass.
  - We never link them into our Go or web code.
  - We never ship them to customers. A SaaS doesn't distribute them, so the GPL's copyleft obligations aren't triggered.
  - The image keeps their licence texts and the exact source versions in its inventory.
- **Still banned: AGPL**, because it reaches network use, and **non-commercial** licences on code or model weights. Copyleft libraries linked into our own binaries are also still banned.
- **CI** checks the `media-tools` image against an explicit allowlist. The allowlist names the GPL/LGPL tools above as exceptions. Anything new that is copyleft or non-commercial fails the check.
- **Patents are a separate question.** H.264/AAC patent licensing for encoding at scale is added to the P0-12 legal questions. If the answer requires it, we move to licensed encoders or another codec.
- **Revisit** if we ever distribute an image or binary (on-premise or desktop), or if the P0-12 answer says otherwise.

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
6. **Budget** (estimates at Oct 2026 prices, approved once): ≈ $350 per editor development sweep (30 scenarios × 6 profiles × 3 trials), ≈ $1,000 for the cutover measurement, and ≈ $150 for the research and ideation benchmarks. Judge calls run through the Batch API at 50 % off. A scaled-down development sweep (15 scenarios × 6 profiles × 2 trials) is ≈ $125, which fits one month of Max 20x API credits ($200); the full sweeps need more than a month of plan credits, and credits don't roll over. The founder funds these runs ([D-21](#d-21-testing-on-the-first-party-api-self-funded)).
7. **Re-run** on every new model release and every major prompt change. Rankings can flip between models and workloads.

**Alternatives.** Choosing by price list or intuition (rejected: it doesn't predict cost per completed task). A router model that picks a model per request (rejected for v1: another failure mode, and it loses cache locality).

**Consequences.** The harness, a consented eval dataset and an approved budget become P4 deliverables ([implementation plan](04-implementation-plan.md)). Until the first sweep finishes, the starting profile in [D-07](#d-07-agent-runtime-go-native-loop-on-the-claude-messages-api) applies. Before any sweep, development climbs the cheap-first ladder of [D-22](#d-22-cheap-first-testing-ladder).

**Revisit** on every model release.

## D-18 Who pays for tokens: managed credits or bring-your-own key

**Context.** The founder's direction: make people use their own keys, or a subscription where we still take a profit. What the research found:
- **Cost.** With Opus 5.5 on every edit, LLM spend is about $1.15 per exported video, about $1.25 all-in ([architecture §13](03-architecture.md#13-cost-model)). That can't be sold at "unlimited clipping" prices.
- **"Your own key" means an Anthropic Console API key.** Anthropic's [legal and compliance page](https://code.claude.com/docs/en/legal-and-compliance) points developers building on Claude to API keys, and says third-party developers may not offer Claude.ai login or route requests through Free, Pro or Max credentials on users' behalf. Subscription logins were also blocked in third-party clients from January 2026 ([The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546), [WinBuzzer](https://winbuzzer.com/2026/02/19/anthropic-bans-claude-subscription-oauth-in-third-party-apps-xcxwbn/)). **We found no exception for development or testing.** What exists instead is better: **Max and Team plans include monthly API credits** (Max 5x $100, Max 20x $200, Team pooled up to $500) that pay for API use with a normal key, "to build and run your own applications and agents" ([Anthropic docs](https://platform.claude.com/docs/en/about-claude/api-credits-for-subscribers)). If the founder's plan is eligible, those credits fund our development and benchmark runs (P0-13). Anthropic's pages leave a grey area for its own tools (Claude Code, the Agent SDK, `claude -p`) under a plan login, and we don't build on it.
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
- **One code path.** `llm.ClientFactory(workspace)` returns a client for the right key, every call goes through the AI Gateway ([D-19](#d-19-model-gateway-cloudflare-ai-gateway-in-byok-mode-openrouter-optional)), and every call writes a ledger row with `billed_to` ([architecture §11a](03-architecture.md#11a-model-access-managed-credits-or-byok)).
- **Defaults.** Managed is the default for mainstream creators and for the capped private beta. BYOK is offered from the beta for power users and agencies. Whether BYOK becomes *mandatory* on any plan is the founder's call.
- **Always on our key:** work that serves the whole platform (the shared trend board, research scans) is a platform cost.
- **Not supported:** reusing a creator's Claude Pro or Max login, and using a subscription login for our own runtime or benchmarks.

**Why not BYOK-only.** The target creator would need an Anthropic account, a payment method, a key, and an understanding of rate-limit tiers, and a fresh key will hit 429s when agents run in parallel. That friction is likely to cut activation. BYOK-only is a sound choice for a developer-savvy or agency segment, and it removes our LLM cost risk entirely.

**Why not managed-only.** At Opus-class costs the margin is thin or negative for heavy users unless prices are high, and power users want control over spend.

**Consequences.** We take on key custody and security work, a rate-limit-aware client, a cost UI, two plan families, and legal checks (Anthropic's terms for customer keys; GDPR roles, because with BYOK the creator's Anthropic account is also a processor of their footage).

**Revisit** after the beta: conversion by billing mode, BYOK support burden, and realised COGS.

## D-19 Model gateway: Cloudflare AI Gateway in BYOK mode, OpenRouter optional

**Context.** The founder would rather reach models through a gateway (OpenRouter or Cloudflare) than call providers directly. What the research found ([research §4.6](02-research.md#46-model-gateways-cloudflare-ai-gateway-and-openrouter)):
- **Cloudflare AI Gateway's Anthropic endpoint passes native requests through.** For Bedrock it passes requests, responses and server-sent events through without translation. Core features are free, and Unified Billing adds 5 % on credit purchases.
- **OpenRouter offers a Messages-compatible endpoint but maps Anthropic's fields onto its own `reasoning` parameter.** Its Claude 5.5 migration guide says Opus 5.5 thinking budgets aren't forwarded and mismatched thinking blocks may be dropped, and its native integration is only guaranteed with the first-party Anthropic provider. Its credits cost 5.5 % on purchase.
- **Funding survives a gateway only in BYOK mode.** Anthropic's plan credits and AWS Activate credits are applied by the provider that bills us, so the request must carry our own provider key. Gateway credits (Cloudflare Unified Billing, OpenRouter credits) don't carry them. Testing goes through a development gateway too ([D-21](#d-21-testing-on-the-first-party-api-self-funded)).
- **Workers AI hosts open models, not Claude.** Claude reaches Cloudflare through AI Gateway.

**Decision.**
1. **One Cloudflare AI Gateway per environment** is the front door for every model call, **in BYOK mode**: our Anthropic key for managed workspaces, the workspace's own key for BYOK workspaces, and a development key through `cf-dev` for development, CI and benchmarks ([D-21](#d-21-testing-on-the-first-party-api-self-funded)). An empty `ANTHROPIC_BASE_URL` bypasses it. We don't use Unified Billing on the main paths, and each gateway is set to require provider credentials so a request can't fall back to Cloudflare-managed billing by accident.
2. **The Go client keeps the native Anthropic API.** It uses the Anthropic SDK with the gateway's Anthropic endpoint as the base URL, so adaptive thinking, effort, strict tools, caching and preserved thinking pass through.
3. **A conformance test gates it** (P4-00). The same requests go direct and through the gateway, and we compare:
   - behaviour and the `usage` fields (cache writes and reads);
   - thinking-block round trips and stop reasons;
   - 429 and `retry-after` pass-through;
   - a long streaming Opus run, for timeouts;
   - a request near the 25 MB limit.

   We depend on the gateway only after it passes. If it ever fails, a config flag sends traffic direct.
4. **Privacy.** Payload logging is off by default (`cf-aig-collect-log-payload: false`), so the gateway keeps metadata (tokens, model, cost, duration) and not prompts or frames. Zero-data-retention settings don't control gateway logs, so logging is turned off separately, and we confirm the gateway never stores provider-key headers. Cloudflare is already a processor for R2 and Workers, so this adds no new vendor to the DPA list.
5. **Attribution.** Every request carries `cf-aig-metadata` tags (`workspace_id`, `run_id`, `stage`, `profile`; at most five entries). Gateway spend limits are a coarse backstop only. Our `usage_ledger` budgets stay the source of truth, because gateway cost tracking is a best-effort estimate.
6. **Retries live in our client only,** so a failure is never retried twice.
7. **Other providers go through the same gateway** for benchmark breadth, cheap tagging experiments and the video-model spike. Use each provider's native endpoint, or the REST API's Anthropic-compatible `/ai/v1/messages`, which takes `provider/model` names such as `google/gemini-3-flash` or, for Opus 5.5, `anthropic/claude-opus-5.5`. It authenticates with a Cloudflare API token that has Account → Workers AI → Read (`CF_AI_API_TOKEN`, optional) and bills through Unified Billing, with 5 % on credit purchases ([research §4.6](02-research.md#46-model-gateways-cloudflare-ai-gateway-and-openrouter)). Claude itself always uses the native `/anthropic` endpoint with our own key, so effort, adaptive thinking, strict tools and caching pass through unchanged. That endpoint takes Anthropic's native ID, `claude-opus-5-5`, and `config/routing/models.yaml` holds both names so a profile never depends on the path ([architecture §7.5](03-architecture.md#75-model-routing-sub-agents-and-benchmarking)). Translating endpoints are fine for non-Claude models, where there are no Claude features to lose.
8. **OpenRouter is not needed.** The gateway reaches the same providers. It stays an optional extra only if it offers a model the gateway can't reach.

**Alternatives.** Direct provider calls (the bypass, with the fewest hops). OpenRouter as the main gateway (normalisation risk for Opus 5.5, a new processor, and no free credits unless BYOK). Cloudflare Unified Billing (+5 % and no plan or Activate credits).

**Consequences.** One more hop and one more dependency, mitigated by the bypass flag. In return we get free analytics, one place to attribute cost, and BYOK customer keys that pass through headers we never log.

**Revisit** if the conformance test fails, if the extra latency proves to be a problem, if OpenRouter's Anthropic compatibility passes our own conformance test, or if Cloudflare's REST `/ai/v1/messages` passes it too, which would let testing and non-Claude comparisons run on Cloudflare credits.

## D-20 Two model lanes: credits fund testing, customers fund production

**Status: superseded by [D-21](#d-21-testing-on-the-first-party-api-self-funded).** The founder dropped the AWS credits before applying. This entry stays as the record of what was considered.

**Context.** After the AWS Activate discussion ([research §4.5](02-research.md#45-funding-claude-usage-anthropic-credits-aws-activate-and-google)) the founder decided three things:
- The credits stay in the holding, which will also run other AI apps, and pay for **active testing**.
- Customers pay for their own usage, through managed credits or their own key. The startup credits are never spent on clients.
- Customer traffic goes through the Cloudflare AI Gateway ([D-19](#d-19-model-gateway-cloudflare-ai-gateway-in-byok-mode-openrouter-optional)). Testing goes **direct to Amazon Bedrock**.

That is also the cleanest reading of the Activate terms, which say credits are for the participant's own use and "cannot be passed on to any end customers". What the research found about Bedrock as a test target ([research §4.5](02-research.md#45-funding-claude-usage-anthropic-credits-aws-activate-and-google)):
- **The integration to use is Claude in Amazon Bedrock.** AWS operates it and bills it as a native AWS service, and AWS says Activate credits are redeemable on third-party models on Bedrock, Anthropic included. *Claude Platform on AWS* is Anthropic-operated and billed through AWS Marketplace, and nothing we read says Activate credits apply to it.
- **It lacks features we use.** There is no Batch API, Models API, server-side web search or fetch, Managed Agents, or structured outputs (which includes `strict` tool schemas). Prompt caching, effort, adaptive thinking, tool use and token counting work.
- **Model access isn't automatic.** Anthropic's Bedrock page lists Opus 5.5, Sonnet 5.5 and Haiku 5.5 as "see access", not as open to all customers, so each account must check the console.
- **Quotas are generous.** The default is 2 million input tokens per minute, against a low tier for a new first-party organization.
- **Credits are finite.** Founders credits are $1,000, up to $5,000 for select startups, and they expire.

**Decision.**
1. **Two lanes behind `llm`, with first-party checks as the test lane's exception.**

   | Lane | Used for | Path | Paid with | Data |
   |---|---|---|---|---|
   | **Production** | Customer runs (managed and BYOK), shared research scans | Go client → AI Gateway (BYOK mode) → Anthropic API | Managed: our key, paid for out of customers' subscriptions. BYOK: the creator's key | Real creator data |
   | **Test** | Local development, CI model tests, evals, benchmark sweeps | Go client → Claude in Amazon Bedrock, direct (no gateway) | AWS Activate credits, in the holding's AWS Organization | Synthetic data and consented eval footage only |
   | **First-party checks** | What Bedrock can't do, or what must be proven on the production path: the gateway conformance test, the parity run, Batches and Models API paths, `strict` schemas, research with web search and fetch | Go client → Anthropic API, direct or through a staging gateway | Anthropic plan credits (Max or Team) or prepaid credits, in the development Console organization | Synthetic data and consented eval footage only |

2. **Credits never reach customers.** The lane comes from the environment, not from the workspace. The `production` environment has no AWS credentials and refuses the test lane. Test-lane calls write ledger rows with `billed_to = test` and `provider = bedrock`, so credit burn is visible and never mixes into customer margin.
3. **Provider-neutral code.** Routing profiles name logical models, and `config/routing/models.yaml` maps each to its provider ID (`claude-opus-5-5` on the Anthropic API, `anthropic.claude-opus-5-5` on Bedrock). A capability matrix per provider (`strict` schemas, batch, web tools, Models API) lets the loop degrade. It validates every tool input itself, so a Bedrock run without `strict` still works, and judge calls run synchronously there.
4. **Benchmarks run on the test lane, then get a parity run.** Bedrock differs from production in `strict` schemas, serving stack and the gateway. So the winning profile and its runner-up are re-run on the production path before the cutover: about 15 scenarios × 2 profiles × 3 trials, roughly $75 (an estimate). It is a sanity check, not a significance test: with 45 trials per profile it catches only large gaps. Starting thresholds are a pass rate inside the Bedrock result's confidence interval, and cost per completed task and cache hit rate within about 15 %. If the lanes disagree, the first-party numbers decide.
5. **Account structure.** Test accounts live in the holding's AWS Organization, one per environment (`dev`, `ci`, `bench`), and later one set per AI app. Credits are shared across an Organization's accounts by default, so the holding's own apps use them. Each account has a Budgets alert on credit burn, CI uses GitHub OIDC (no long-lived keys), and an IAM policy allows only `bedrock-mantle:CreateInference` on the three model ARNs. If an app moves to its own company, its accounts leave the Organization and the credits don't follow.
6. **If the credits fall through** (rejected, expired, or Bedrock access to the 5.5 models isn't granted), the test lane's provider flips to the first-party development key in config. Nothing else changes, and development runs on `dev-cheap` and plan credits as in P0-13. Without access to the 5.5 models on Bedrock, loops can use Haiku 4.5 (open to all customers), but sweeps and the cutover need the production models.

**Alternatives.**
- Test through the gateway as well. The founder chose direct: fewer hops, and tests don't depend on the gateway. The conformance test and the parity run cover the gateway path instead.
- Run managed customer traffic on Bedrock to use up the credits. Rejected: Activate credits can't be passed to end customers, Bedrock lacks features we rely on, and the founder wants customers to pay for their own usage.
- Test only on the first-party API with plan credits. This is the fallback in step 6, but $100 to $500 a month does not fund full sweeps.

**Consequences.** Two Claude providers behind `llm`, a capability matrix, a lane guard and a parity run before each cutover. The credits are a fixed pot, so plan the spend:
- At $1,000 they cover about two full development sweeps ($350 each), or the cutover measurement alone ($1,000). Those estimates assume a batch-priced judge, so on Bedrock expect somewhat more.
- At $5,000 they cover the whole protocol with room to spare.
- Run scaled-down sweeps (about $125) until a profile is close to final, and check the expiry date in the Billing console. Sources say one to two years.

Research variants can't run on Bedrock (no web tools), so their ≈ $150 comes from plan credits or cash. Consent for eval footage must name AWS as a processor.

**Revisit** when AWS answers whether a holding that runs several AI startups qualifies, when the credits run out or expire, when Bedrock gains structured outputs or the Batch API, or if a parity run shows the lanes disagree.

## D-21 Testing on the first-party API, self-funded

**Context.** [D-20](#d-20-two-model-lanes-credits-fund-testing-customers-fund-production) planned to spend the holding's AWS Activate credits on testing through Amazon Bedrock. The founder dropped that before applying:
- The credits are small ($1,000, up to $5,000 for select startups), they expire, and they can't be used for customer traffic.
- Claude in Amazon Bedrock lacks features the product relies on: structured outputs (so `strict` tool schemas), the Batch API, the Models API, and web search and fetch ([research §4.5](02-research.md#45-funding-claude-usage-anthropic-credits-aws-activate-and-google)). Results measured there would only be a proxy for production and would need a parity run.

**Decision.**
1. **Testing goes through the gateway too.** Development, CI and benchmarks use a `cf-dev` gateway with a development key in a dedicated Console workspace that has a monthly spend limit (P0-13). Tests then exercise the production path: the headers, caching, thinking blocks and timeouts. An empty `ANTHROPIC_BASE_URL` calls the API directly. That is the bypass for debugging, and how work starts before the gateway exists (P0-09).
2. **Customer traffic is unchanged** ([D-19](#d-19-model-gateway-cloudflare-ai-gateway-in-byok-mode-openrouter-optional)): the production gateway in BYOK mode, on our production key (managed) or the creator's key (BYOK).
3. **The same API, models and path in every environment,** so benchmark results transfer to production without a parity run, and the gateway is exercised all the time, not only by the conformance test (P4-00).
4. **Keys follow the environment.** Production holds the production key and customers' keys. `local`, `ci` and `bench` hold only the development key. Test calls write ledger rows with `billed_to = test`, so test spend never mixes into customer margin.
5. **The founder funds testing,** with Anthropic plan credits first and prepaid credits for the rest. The founder's Max plan gives $200 a month of API credit that doesn't roll over, so paid sweeps should be scheduled to use each month's credit before it resets.

**Alternatives.**
- Apply for AWS Activate anyway ([D-20](#d-20-two-model-lanes-credits-fund-testing-customers-fund-production)). Rejected for the reasons above.
- Test directly against the Anthropic API, as this decision first said, which was a leftover from the Bedrock plan. Rejected: fewer hops isn't worth testing a different path from production. Direct stays as the bypass.
- Pay for development calls from Cloudflare credits (Unified Billing) with a Cloudflare token instead of an Anthropic key. It works on the native `/anthropic` endpoint too: send only `cf-aig-authorization`, no `x-api-key`, and leave **Require provider credentials** off on `cf-dev` ◐. The REST path with `anthropic/claude-opus-5.5` is the other way. Rejected as the default: Cloudflare adds 5 % on every credit top-up, the plan credit above would go unused, the gateway would supply its own key so tests wouldn't run the production path, and the first day needs a Cloudflare account, gateway, token and prepaid credits before the first call. Non-Claude models in benchmarks do use Cloudflare credits (`CF_AI_API_TOKEN`).
- Other startup credits. Google's reportedly don't cover Claude on Vertex, and Anthropic's own startup perks are paused ([research §4.5](02-research.md#45-funding-claude-usage-anthropic-credits-aws-activate-and-google)).

**Consequences.** One provider, no capability matrix, no AWS accounts and no parity run. The benchmark program is now a cash cost. At the [D-17](#d-17-model-routing-is-data-tuned-by-benchmark) estimates it comes to about $1,650 if every sweep is scaled down (four at ≈ $125, the cutover at ≈ $1,000, research and ideation at ≈ $150), and up to about $2,550 if every sweep is a full one (≈ $350 each). The levers are the ones in D-17: `dev-cheap` for everyday loops (≈ $0.02 per run), scaled-down sweeps first, the Batch API for judge calls (50 % off), prompt caching, and the cheap-first ladder of [D-22](#d-22-cheap-first-testing-ladder), which keeps the early work off the expensive models.

**Revisit** if testing spend becomes material and the company has a website that fits the Activate application (applying later is still possible: the Founders grant is one-time per company, and not applying doesn't use it up), if Bedrock gains the missing features, or if EU creators need EU-only processing. Bedrock's EU endpoints are the option for that, at list price ([D-15](#d-15-eu-data-residency)).

## D-22 Cheap-first testing ladder

**Context.** The founder's direction: the first testing runs on cheap models, and only once the flaws are out do we move to expensive ones, because a paid sweep spent on a fixable bug is wasted money. [D-17](#d-17-model-routing-is-data-tuned-by-benchmark) already says to change one lever at a time, and the architecture already has a `dev-cheap` profile. This decision makes the climb explicit: a free rung below the cheapest, a checklist to leave each rung, and a record-and-replay mode so tests stop paying for the same call twice.

Cost of one editor first edit, from [architecture §13](03-architecture.md#13-cost-model): Haiku 5.5 about $0.02, Sonnet 5.5 about $0.33, Opus 5.5 about $0.61.

**Decision.** Five rungs. Leave a rung only when its checklist is green, and record the result in the PR or the benchmark report.

| Rung | Profile | What runs | Cost | Purpose |
|---|---|---|---|---|
| 0. Free | none, `LLM_MODE=replay` | unit tests, golden renders, validators, and agent-loop tests that replay recorded model calls | $0 | Everything deterministic. A flaw found on a higher rung becomes a recorded transcript here first |
| 1. Cheap | `dev-cheap`: Haiku 5.5 for every stage | the loop, tools, validators, budgets, caching, UI flows and the eval harness, on 10 to 20 scenarios | about $0.02 a run, capped by the $50 `dev` workspace limit | Remove the plumbing flaws |
| 2. Mid | `dev-mid`: Sonnet 5.5 for every stage | the same scenarios, about 10 with 2 trials each | about $7 to $10 a pass | Tell model limits apart from prompt and tool flaws |
| 3. Opus | `v1-start` | a smoke test of 5 scenarios, then one scaled-down sweep (15 scenarios × 6 profiles × 2 trials) | about $3, then about $125 | Prove the Opus path and see the shape of the results |
| 4. Sweeps | `v1-start` variants | the full development sweeps and the cutover from D-17 | about $350 each, and about $1,000 | The numbers that decide the routing profile |

**Exit checklists.**
- **Rung 1 to 2.** All of these hold over at least 3 trials of every scenario:
  1. No unhandled errors. Every run ends as succeeded, failed or cancelled, including cancel and resume, and a killed worker resumes without a duplicate side effect.
  2. A rejected tool call never kills a run, and the validator's message lets the model recover.
  3. Budgets stop runs cleanly, and the ledger's total matches the Anthropic Console's usage for the `dev` workspace within about 5 %.
  4. Cache reads appear from the second turn on a stable prefix. The minimum cacheable length differs by model, so check each model once.
  5. The renderer handles every output (the golden tests pass), and the eval harness catches a set of deliberately broken outputs.
  6. Cost per run matches the estimate, about $0.02 to $0.05 on `dev-cheap`.
- **Rung 2 to 3.** Every failure that remains on Sonnet is fixed or explained as a model limit, and the hard-constraint pass rate is at least 80 %, the release-gate number in P4-07.
- **Rung 3 to 4.** The scenario set, validators and judge rubric are frozen (no changes mid-sweep), the judge is calibrated against the human ratings, and the scaled-down sweep gave stable results.

**Rules.**
1. **Never re-run a paid sweep to debug code.** Debug on rung 0 or 1.
2. **Record and replay.** `LLM_MODE=record` stores each request and response under `apps/server/testdata/cassettes/`, keyed by a hash of the canonical request (model, system prompt, messages, tools and parameters). Keys are scrubbed and only synthetic data is recorded. `replay` serves them and fails the test on a miss, so no test silently makes a live call.
3. **Every failure gets a regression test on rung 0** before the next rung starts.
4. **A paid sweep needs the founder's go-ahead each time.** The `dev` workspace limit is raised for it and lowered again afterwards.

**Alternatives.**
- Start on Opus to see the real quality. Rejected: about 30 times the cost of Haiku while the plumbing is still broken.
- Use only Haiku until the cutover. Rejected: Haiku failures can hide prompt flaws, and judging quality needs a stronger model.

**Consequences.** Rungs 0 to 2 cost about $45 to $130 in total (a few hundred Haiku runs and a few Sonnet passes). Rung 3's scaled-down sweep is the first of the scaled-down sweeps already counted in the [D-21](#d-21-testing-on-the-first-party-api-self-funded) budget, plus about $3 for the smoke test. Rung 4 is the rest of that budget, spent only when the flaws are gone. The cost is one more profile (`dev-mid`) and a record and replay mode in the model client.

**Revisit** if a rung's checklist keeps passing but the next rung finds the same class of flaw, because then the checklist is missing a check.

## D-23 Product domain and hostnames: contentfabrique.app

**Context.** The founder bought `contentfabrique.app`. Every environment but localhost needs hostnames: the web app, the landing page with the privacy policy and terms that TikTok review asks for, the Go API behind its tunnel, webhooks (Clerk now, the merchant of record later) and OAuth redirect URIs, plus a staging copy of all of it.

**Decision.**
1. **Production.** `contentfabrique.app` is one TanStack Start Worker that serves the landing page, the privacy policy, the terms and the signed-in app. `www` is routed to the same Worker, which redirects it to the apex. `api.contentfabrique.app` is the Go API behind its Cloudflare Tunnel.
2. **Staging.** `staging.contentfabrique.app` for the web app and `api-staging.contentfabrique.app` for the API.
3. **Names stay one level deep.** Cloudflare's free universal certificate covers the apex and one level of subdomain △, so staging's API is `api-staging`, not `api.staging`.
4. **Callbacks follow the hostnames.** Clerk webhooks go to `https://api[-staging].contentfabrique.app/v1/webhooks/clerk`, and TikTok redirects to `https://[staging.]contentfabrique.app/integrations/tiktok/callback`.
5. **Clerk.** The development instance serves localhost and staging. The production instance is tied to `contentfabrique.app`.

**Alternatives.** Landing page on the apex and the app on `app.contentfabrique.app`: the usual split, at the price of a second Worker and a navigation between two sites. Deeper staging names (`api.staging.…`): they need a paid certificate.

**Consequences.** `.app` is on the browsers' HSTS preload list △, so every hostname is HTTPS-only from the first request. That is fine behind Cloudflare, but a plain-HTTP check fails, and local work stays on `localhost`. The web app and the API are different origins, and the session token travels in an `Authorization: Bearer` header ([architecture §9](03-architecture.md#9-api)), so no cookie crosses between them. The API therefore needs a CORS allowlist, built from `APP_BASE_URL`.

**Revisit** if the landing page needs its own release cadence (then move the app to `app.`), or if the product is renamed.

## D-24 Public IDs are TypeIDs over UUIDv7

**Context.** The database uses UUIDv7 keys ([architecture §4](03-architecture.md#4-data-model)). The API, the timeline spec and the agents' tool calls also need IDs. A bare UUID doesn't say what it points at, so an agent can pass an asset ID where a moment ID belongs, and logs are harder to read. The architecture's examples already write `ast_01j…`.

**Decision.**
- **Outside the database, every ID is a [TypeID](https://github.com/jetify-com/typeid):** a lowercase type prefix, an underscore, and the UUIDv7 in 26-character Crockford base32, for example `ast_01jabcd…`.
- **Postgres stores the plain `uuid`.** The conversion happens at the API and spec boundary in Go (`go.jetify.com/typeid`, Apache-2.0).
- **Prefixes:**

  | Prefix | Object | Prefix | Object |
  |---|---|---|---|
  | `wks` | workspace | `ast` | asset |
  | `usr` | user | `mom` | moment |
  | `shot` | shot | `ann` | annotation |
  | `trd` | trend | `con` | concept |
  | `ide` | idea | `prj` | project |
  | `tl` | timeline | `ren` | render |
  | `run` | agent run | `exp` | export |
  | `lic` | licence | `upl` | upload |
  | `evt` | event | | |

  New tables add their prefix to this list.
- **Clerk's own IDs** (`user_…`, `org_…`) are stored as text next to our keys and never reused as ours.

**Alternatives.**
- Bare UUIDs everywhere: simpler, but untyped.
- ULIDs: same idea without a spec or a typed prefix.
- Prefixed hex UUIDs: 40 characters, and they mix badly with log search.

**Consequences.**
- The OpenAPI spec types each ID field with a `pattern`.
- The timeline JSON Schema checks the prefix per field ([timeline design note](design/timeline-v1.md)).
- A wrong-type ID fails at decode time, before it reaches a query.

**Revisit** if a client library can't handle the format, which is unlikely: it's a plain string.

## D-25 Output formats: 9:16, 1:1, 4:5 and 16:9 per project

**Context.** v1 was scoped to TikTok, so the timeline accepted only 1080×1920. The founder wants a new job to start with a format picker (one card per format, each showing a device in that shape) so creators can also cut square, portrait-feed and landscape versions.

**Decision.**
- **A project picks one output format when it is created:**

  | Format | Size | Typical use | Safe zone |
  |---|---|---|---|
  | `9:16` | 1080×1920 | TikTok, Reels, Shorts | TikTok UI zones ([research §3.4](02-research.md#34-captions-safe-zones-beat-snapping)) |
  | `1:1` | 1080×1080 | Feed posts | 5 % margin on every side |
  | `4:5` | 1080×1350 | Instagram portrait feed | 5 % margin on every side |
  | `16:9` | 1920×1080 | YouTube, landscape | 10 % title-safe margin, plus the bottom 15 % kept clear for player controls |

- **The timeline's `output` takes these four sizes.** The renderer reframes to any of them with the same focus-point logic, and the preview renders at half size.
- **TikTok remains the only publishing target.** A TikTok inbox draft (P6-04) is offered for `9:16` only; the other formats are downloads.
- **Agent prompts and the trend board stay TikTok-tuned in v1.** The editor is told the format and its safe zone.

**Alternatives.**
- 9:16 only, with the other cards marked "coming soon": less work, but the founder wants the other formats to work now.
- Rendering every format from one edit: deferred. One project, one format; a "duplicate as another format" action can come later.

**Consequences.**
- Safe zones become per-format values.
- Golden render tests cover each format.
- The editor eval set (P4-07) gets at least a few non-9:16 scenarios.
- Format is chosen up front, so changing it later means a new project.

**Revisit** when we add a second publishing platform, or when creators ask for one edit in several formats.

## D-26 Video effects are presets

**Context.** Creators want the effects popular on short-form video: digital zoom, zoom-through cuts, checklists, chart grows, magazine covers, phone mockups and green screen. Claude can write such effects as code. But [D-02](#d-02-edit-representation-declarative-timeline-json--deterministic-renderer) keeps edits as validated timeline JSON with a deterministic renderer.

**Decision.**
- **Effects are a curated preset library.** Each preset has an id, typed parameters (text lines, numbers, which clips) and a looping preview for the picker. The editor agent chooses presets through edit ops, and the creator picks them from cards.
- **The library is built in three tiers:**

  | Tier | Presets | How they render | Task |
  |---|---|---|---|
  | Camera moves and transitions | digital zoom (`punch_in`, `push_in`), pull-back reveal (`pull_back`), `shake`, zoom-through cut, flash, whips; vertigo later | ffmpeg in the compiler: a `motion` preset on clips and extra transition types | P3-10 |
  | Graphic templates | First five: **checklist, chart grow, cover story, headline wall, phone mockup**. Later: glass type, ui world, couch wall, paper cut, doodle boil | An HTML template per preset rendered to video frames (HyperFrames, Apache-2.0), composited by the compiler | P4-11 |
  | Needs person segmentation | green screen, frame break, orbit | A permissively licensed matting model ([D-16](#d-16-licensing-policy-for-ml-and-media-components)), after a spike | P2-11 |

- **Bespoke, one-off effects written by Claude** stay behind the flagged sandbox escape hatch (P4-08), and their output comes back as an asset.

**Alternatives.**
- Letting the agent write effect code per video: unvalidated, slow, and impossible to golden-test.
- Licensing a template marketplace: not needed for v1.

**Consequences.**
- Timeline v1 gains additive fields: `motion` on video items, new transition types, and a `graphics` track whose items reference a template.
- Every preset gets golden tests in every format ([D-25](#d-25-output-formats-916-11-45-and-169-per-project)).
- The style registry also covers templates.

**Revisit** when creators ask for presets we don't have, or when a template marketplace becomes worth integrating.

## D-27 Transcription: whisper.cpp locally, Workers AI Whisper when hosted

**Context.** Captions, hooks and quotes need a word-level transcript of the clips that contain speech: P2-05, and the captions renderer in P3-04. The research named ElevenLabs Scribe and the Whisper family. Locally there is no Cloudflare account yet, and creators' voices are personal data ([D-15](#d-15-eu-data-residency)).

**Decision.**
- **One `transcribe.Provider` interface** returns words with start and end times in milliseconds, a confidence and the language. Captions and the editor never see which provider was used.
- **Local, ci and bench run whisper.cpp** (MIT) inside the media-tools image, through the sandbox, on CPU. It uses the `large-v3-turbo` model, quantised, with the model file pinned by checksum. CI uses a tiny model.
- **Staging and production call Workers AI `@cf/openai/whisper-large-v3-turbo`** through the environment's Cloudflare account. It is enabled by config once P0-09 exists.
- **Transcription runs only on clips where speech is detected**, using a cheap voice-activity check first.
- **Two checks come before production uses Workers AI:**
  1. Where inference runs, and whether EU-only processing is available (D-15). If it isn't, production falls back to whisper.cpp on our EU servers, or this decision is revisited. The founder's alternative is OpenRouter; before choosing it, check whether it serves a Whisper-class model with word timestamps, and note that it adds a US processor. Workers AI also offers batch mode for this model, which fits a job that isn't time-critical.
  2. The per-minute price. It goes into the price table, and usage is metered in `usage_ledger` with kind `transcription`.

**As built (P2-05).**
- **Speech check:** whisper.cpp's own voice-activity tool with the Silero VAD v6.2.0 model (MIT, 1 MB), in the same sandbox, about 0.2 s for a 7 s clip. Under 500 ms of speech means `no_speech`: the provider is not called and nothing is metered.
- **Word timing:** the model's attention based timestamps are 200 ms or more off a word's start, so the provider returns an anchor per word (whisper.cpp: the DTW alignment of the model's alignment heads) and `transcribe.Refine` moves each boundary onto the audio (the longest silence, or the quietest 10 ms frame, between two anchors). It runs for every provider, hosted ones included. Measured median start error against known timings: 6 ms on synthesised speech with known gaps, 50 ms on LibriSpeech test-clean against forced alignments (large-v3-turbo q5_0).
- **Speech structure** (D-28): sentence boundaries, filler words (hesitations such as um, uh, eh, äh; phrases such as "you know", "zeg maar"; English, Dutch, German, French and Spanish rules) and pauses over 400 ms are computed from the words, not by the provider, and stored with the transcript.
- **Ledger:** `usage_ledger` kind `transcription` holds the provider and the audio milliseconds metered, one row per request (a hosted clip over ten minutes goes out in chunks), written even when a later chunk fails.
- **Spend control:** Workers AI is never called without a verified price. As built in P2-05 the price was the env var `WORKERS_AI_PRICE_MICROS_PER_MINUTE`, required in staging and production; **P7-09a moved it into the price table** (`transcription.workers_ai.per_minute`, `config/pricing/2026-10b.yaml`), the env var became an optional override, and the refusal is made where the provider is built: a paid provider with no price from either never runs in staging and production. Before each call the worker reserves its worst case (audio length times the price) in the database, with the model client's holds and counters (`internal/spend`; P2-05's in-process `llm.DailyGuard` is gone), against the workspace's `DAILY_SPEND_CAP_MICROS`, the platform breaker and `TRANSCRIBE_DAILY_MINUTES` (default 240 per workspace per UTC day, any provider). A transcription a workspace cap cannot cover fails with `spend_cap` or `minutes_cap` instead of retrying; the breaker makes the job wait. A chunk whose outcome is unknown after it was sent is an `estimated` ledger row.
- **Re-ingest:** the job is unique by asset and ingest generation (`assets.ingested_at`), a save locks the asset and refuses a transcript made from another ingest (`ErrStaleTranscript`, the pattern of the clip analysis), and an ingest that has no audio any more deletes the transcript. `cfctl transcribe run --force` does a transcript again.

**Alternatives.**
- ElevenLabs Scribe: the best quality and audio events, but higher cost and another processor.
- whisper.cpp everywhere: free, but uses CPU on our servers.

**Consequences.**
- The media-tools image grows by the whisper.cpp binary and the model, roughly 0.5–1.5 GB depending on quantisation.
- Transcripts are comparable across environments because the same model family is used everywhere.

**Revisit** if quality on gym audio (music, noise) is poor; if Workers AI can't stay in the EU; or if a cheaper, better hosted model appears.

## D-28 Content-aware editing across genres

**Context.** v1 was scoped to fitness creators. After the first live edits, the founder wants the editor to *understand* whatever people upload (unboxings, talking-to-camera videos, vlogs, tutorials, workouts) and edit each one the way a good editor of that genre would. "It needs to get that feeling that it understands content."

**Decision.**
- **The editor is genre-aware.** Every run starts with an **understand step**: from the quick look, the transcript, the analysis signals and the brief, it decides the content type and the story. It writes a short creative plan (hook, arc, ending) and picks a **genre playbook**. The plan is shown to the creator in the run feed.
- **Genre playbooks** live in the repo as versioned prompt modules, next to the base editor prompt:
  - talking/opinion
  - unboxing/product
  - workout/fitness
  - vlog/lifestyle
  - tutorial/how-to
  - a general fallback
- **Perception is genre-neutral.** It needs:
  - **transcripts with word timings** (P2-05, so its priority rises);
  - **speech structure**: sentence boundaries, filler words and pauses for jump cuts;
  - **faces and subjects**, for 9:16 reframing that keeps the speaker or the product centred;
  - **motion and scene signals** (already in progress);
  - **generic visual tags** from the quick look (already generic, not fitness-only);
  - **audio events** (laughs, impacts) later.
- **Fitness stays the launch niche** for the trend board, marketing and the first beta creators ([product plan](01-product-plan.md)). The editor doesn't assume fitness.

**Alternatives.** A fitness-only editor would be simpler but would feel dumb on the first unboxing. Separate products per genre would split the work.

**Consequences.**
- The eval set (P4-07) gets scenarios per genre.
- Face and subject detection needs a permissively licensed model ([D-16](#d-16-licensing-policy-for-ml-and-media-components)); MediaPipe face detection (Apache-2.0) is the default candidate.
- Transcription moves up the queue.

**Revisit** when a genre's results lag the others in the evals, or when the beta shows which genres creators actually upload.

## D-29 Legal entity: an operating BV under the holding; Paddle as merchant of record

**Context.** Several things need a legal entity: the merchant-of-record account, the TikTok developer app, Anthropic's commercial terms, the data processing agreements and the domain registrant. Content Fabrique also carries risks: creators' personal data (GDPR), copyright and takedown claims, and AI-output disputes. Open question 4 in the plan asked which company operates it.

**Decision** (proposed, to confirm with the accountant).
- **Content Fabrique gets its own operating BV** (werkmaatschappij), owned by the founder's holding BV. Other ventures get their own operating BVs, so each one's liabilities stay in its own company.
- **Everything is contracted in the operating BV's name from day one:** Paddle, the TikTok developer app, the Anthropic organisation for production, the DPAs with Cloudflare, Clerk and the database host, and the domain registrant.
- **Paddle is the merchant of record** ([D-13](#d-13-billing)). It handles EU VAT (OSS), worldwide sales tax, invoices, refunds and chargebacks. It supports both subscriptions and credit top-ups. Stripe's merchant-of-record offering stays the alternative to compare at signup.
- **Keep the holding protected.** Avoid personal or holding guarantees for the operating BV. Decide on a fiscal unity with the accountant, because it makes the holding jointly liable for the group's corporate tax. Respect the distribution test (uitkeringstoets) before moving money up.

**Alternatives.**
- Start in the holding: fast, but it exposes the holding's assets, and moving accounts and customers later is painful.
- One operating BV for all ventures: cheaper, but each venture's risks reach the others.

**Consequences.**
- A notary and KvK step before Paddle onboarding and the TikTok app review.
- The company site on `contentfabrique.app` names the BV in its terms and privacy policy (P0-12).

**Revisit** when an investor or a sale wants a different structure.

## D-30 Semantic search: pgvector in Postgres, hybrid with full-text

**Context.** Universal search (P2-12) and the editor's moment search should work by meaning ("explosive back pull", "him flexing in front of the mirror"), not only by tags. The founder asked whether this needs a vector database.

**Decision** (proposed).
- **Vectors live in the same Postgres** (the `pgvector` extension, already in our `pgvector/pgvector:pg18` image) next to the rows they describe. Every query keeps the `workspace_id` filter, so tenancy has one enforcement point.
- **Hybrid ranking:** full-text (`tsvector`) and vector similarity (HNSW index), merged with reciprocal rank fusion. Exact words still win; meaning fills the gaps.
- **What gets embedded:** per moment or shot, a **text embedding** of its description, tags and transcript chunk, and later an **image embedding** of its keyframe for visual search.
- **Models:**
  - **Text:**
    - hosted environments use a multilingual embedding model on Workers AI (for example `bge-m3`), following [D-27](#d-27-transcription-whispercpp-locally-workers-ai-whisper-when-hosted)'s pattern and checks (EU processing, price in the price table, metered in `usage_ledger`);
    - locally, a small open model runs in the sandbox.
  - **Images:** an open image-text model (SigLIP or CLIP family, permissive licence per [D-16](#d-16-licensing-policy-for-ml-and-media-components)) in media-tools, or a hosted multimodal embedding later.
  - The model name and its dimension are stored with each vector, so a model change triggers a re-embed job instead of mixing spaces.
- **Staging check:** the PlanetScale Postgres database must support `pgvector`. This sits next to the Postgres 18 check in [setup guide §3.4](06-setup-and-credentials.md#34-postgres).

**Alternatives.** A dedicated vector DB (Qdrant, Pinecone, Weaviate) adds a second system to sync, secure and back up, plus a second tenancy boundary, for scale we won't need for a long time.

**Consequences.**
- Embedding jobs after analysis.
- An HNSW index per embedding kind.
- `GET /v1/search` gains a semantic leg.
- Search quality joins the evals.

**Revisit** past tens of millions of vectors per region, or if pgvector latency misses the P2-12 target.

## D-31 Spend control: credits per action, COGS budgets derived from price

**Context.** The founder asked for spend control "so we still have profit in the end", with usage stats in the org, ready for production. Several pieces were missing:
- Only LLM calls were metered.
- The daily cap's reservations lived in one process's memory, so several workers could overspend it together.
- An unpriced model could run whenever `LLM_ALLOW_UNPRICED` was set.
- Paid transcription ran with no cap at all (found in the P2-05 security review).
- There were no credits, no quotas beyond plans, and no usage view.

**Decision** (details in [spend control v1](design/spend-control-v1.md)):
- **Credits** are the customer's currency.
  - **Prices are per action**, from a versioned price list in `config/plans`: a first edit is 6, a revision 2, an export 0. 10 credits make a "video".
  - Each plan's allowance expires at the end of its period. Top-ups last 12 months.
  - A credit **hold** is taken before an action starts, so the last credits can be raced safely.
  - Credits are charged only on success. Failed runs are refunded and recorded as unbilled COGS.
- **Profit by construction.** Each action's COGS budget for each plan is at most 40 % of the net revenue its credits earn: the plan price, minus the merchant-of-record fee, divided by the allowance. That budget is the run's hard budget. A test checks the rule for every plan and action, in every price-list file.
- **Every cost is metered** in `usage_ledger` with a kind (llm, transcription, render, media, storage, egress), a quantity and unit, and attribution to project, member and action.
  - On staging and production, a paid provider with no price refuses to run.
  - Caps are enforced with database holds and counters, so they hold across processes. The cap levels are per run, per workspace per day, per workspace per period, and a **platform-wide daily breaker**.
- **Visibility.**
  - Creators see credits, usage by day, by project and by member, and their activity. BYOK creators also see the estimated cost on their own key. Managed creators never see our COGS or margin.
  - The founder gets `cfctl spend report` (margin per workspace, plan and action, plus unbilled COGS), typed `spend.alert` events, and a daily reconciliation against the provider's usage report.
- **Ledgers are append-only** for the app role. Money is stored as integers.
- **Built in P7-01a** ([spend control v1, "As built (P7-01a)"](design/spend-control-v1.md#as-built-p7-01a)): the price list and plans in `config/plans`, the profit-invariant test, the credit ledger with consumption tracked per grant, the hold-settle-release on editor runs, the allowance periods, `402 credits_exhausted`, `cfctl credits`. Its deviations from the text above are listed there.

**Alternatives.**
- Pass-through token pricing to customers: unpredictable for creators, and it exposes our margin.
- Caps from the ledger only, with no holds: they race across processes.
- A billing SaaS for metering (Orb, Lago, Stripe meters): another processor of usage data, and the merchant of record still has to be wired in. We may revisit at scale.
- Calendar-month allowances: they don't line up with merchant billing periods.

**Consequences.**
- The editor's budget now depends on the plan, so a tight plan can stop a long run early. The runtime's "finish now" warning and the budget backstop handle that.
- Prices and allowances are data, so a new price list is a new YAML file and a test run.
- The numbers above are starting hypotheses. Calibrate them from `cfctl spend report` after the beta.

**Revisit** when real COGS per action is known (the beta), or when checkout (P7-01c) brings real prices and fees.

## D-32 Audio is synthesised and measured with ffmpeg, no audio models

**Context.** The editor cannot hear, and the generated sound effects of P3-10 sounded cheap (single-layer noise). The founder pointed at **earshot** ([github.com/Leonxlnx/earshot](https://github.com/Leonxlnx/earshot), MIT, Copyright (c) 2026 Leon Lin, Python), a tool that lets coding agents hear, judge and synthesise sounds, and asked us to take inspiration from it. It has two halves: a library of knowledge and model-free measurements (the design chain per sound, the numbers of seven measured launch films, loudness and sync checks) and a runtime with models (CLAP and AST classifiers, about 1.4 GB, 20 to 40 s to load per call).

**Decision.**
- We take earshot's **knowledge and measurement ideas, not its runtime.** No Python, no torch, no CLAP or AST models. Everything stays Go plus ffmpeg in the media-tools sandbox, deterministic, with no audio assets and no model calls.
- The sound effects are rebuilt with the professional design chain (layers, bands, saturation, glue, a short dark room, a loudness match) and the editor gets model-free "ears": `internal/audiocheck` measures loudness, silence, onsets, the hook, sync, density and defects from ffmpeg and plain Go, and checks them against targets from earshot's measured launch films and web delivery practice, each with its source and the estimates marked ([audio v1](design/audio-v1.md)).
- **Attribution.** Only numbers and ideas are used; no earshot code is used or ported. Where its values appear in our code (`internal/render/sfx_dsp.go`, `internal/audiocheck/analyse.go`) the comment says so, with the MIT licence and its copyright line, and so do audio v1 and the edit style note. Nothing of earshot ships in the media-tools image, so its licence inventory (`packages/media-tools/LICENSES.md`) is unchanged: a licence entry there is for a component that ships, and a change to that directory would also make every local image stale (the image records a hash of it).

**Alternatives.**
- Run earshot or its models in the sandbox: a 1.4 GB model download, 20 to 40 s of load per call, a D-16 licence review of the model weights, a Python and torch toolchain in the image, and a two-day-old project as a dependency. Its own `docs/LIMITS.md` says the model-free measurements are the trustworthy part and its model verdicts are hints (right about half the time for exposed events, a quarter under music), and a score from a classifier is a target that can be satisfied without the sound being good (Goodhart).
- Licensed sound libraries for the effects: real recordings sound better than synthesis (earshot says so), but they need assets, licences and a store, and they are not deterministic across a library update. Revisit when the effects are the weak point.

**Consequences.**
- The effects are ours (export `licensed`), reproducible and free; the quality ceiling is that of synthesis, which is why there is a gallery to listen to and tests that pin the measured shape and not the taste.
- The editor will get a sound report for the cost of one ffmpeg pass and no tokens; it is advice, with estimates marked, and it cannot judge taste (it says where and how loud, not whether it is good).
- The numbers of seven films are a small sample, and they describe music-led launch films; a talking-head edit breaks some of them, so those checks are estimates and warnings.

**Revisit** when a measured listening test on our own edits contradicts a target (the targets are data in `internal/audiocheck`), or when the effects are shown to be the weak point of an edit (licensed recordings).

## D-33 Exports are sound-ready

> Status: **Proposed** by the director (2026-10-17), built in P3-12 so that it can be listened to; the founder confirms or changes it. **Superseded by [D-34](#d-34-music-from-the-creator) for an edit with a song**; an edit without one is still sound-ready as below. Every part is reversible: the clip sound mode is a field of the timeline, the levels are constants in `internal/render/plan.go` and `internal/audiocheck`, and the editor's behaviour is one prompt version (`editor.v2.1`).

**Context.** D-03 keeps the trend sound out of the export, and until now a clip's own sound was off unless the editor turned it on, so a workout export was silent between the few generated effects: the creator had to add a sound in TikTok before the video felt like anything, and the effects (mixed after the programme normalisation at a fixed level) were tuned for a programme. The first live runs showed the cost: a silent video with an impact in it. The founder's footage has real gym sound in it (plates, a room), which is exactly the ambience a sound-ready cut wants under the creator's trend sound.

**Decision.** An export is *sound-ready*: it has no music and no trend sound (D-03 is unchanged: a trend sound is a cue, and the preview keeps its generated beat guide), and it is not silent.
- **Footage with no speech:** each clip's own sound is a **low ambience bed**, about -24 LUFS short-term, **evened out across clips** (each clip is measured and set to the same level, correcting by at most 18 dB) with a 20 ms fade at the cuts, and the **generated effects sit on top**, lifted so that they peak around -12 to -10 LUFS.
- **Footage with speech** (a clip the editor sets to `full`, or the talking playbook): the speech is the **programme** at the programme level and the programme is **ducked under the effects**, as before. A bed under other clips of the same video is 10 LU under the speech.
- **The bed is an ambience and the trend sound dominates.** The bed is **about -24 LUFS short-term (median)**, evened across clips; at -18 it would be only 4 LU under a trend sound at about -14, which TikTok layers with its original-sound slider at 100 % by default. **Nothing is normalised to a programme target** (not -14, not -17): the bed is set to its own level, the effects to their category's loudness plus 4.5 dB so that their short-term peaks land around **-12 to -10 LUFS** (still within 6 LU of -14, so they cut through the trend sound), and the export's integrated loudness simply results, about **-21 to -19 LUFS** for an edit with few effects (the ears accept -24 to -16: the creator's trend sound brings the programme loudness). A ceiling limiter (-4.5 dBFS sample peak) in the core mix keeps the true peak at or under **-2.5 dBTP** after AAC.
- **The editor sets this with one op,** `set_clip_audio` with `mode` (`mute` the default, `bed`, `full`) and no `id` (every clip). The editor's checks listen to the preview (`audio_level`, `audio_bed`, `audio_effects`) and its prompt teaches the policy (`editor.v2.1`).

**Alternatives.**
- **Keep clips muted** (the old default; D-03 alone): the export is silent between effects, and the creator's trend sound hides the effects. Rejected: the product promise is a video that is nearly ready to post.
- **Put the clips' sound in at the programme level (-14):** a room tone becomes loud and fights the trend sound. Rejected; the mode `full` still exists for speech.
- **Detect speech automatically and pick the mode in the renderer:** needs transcripts for every clip (P2-05) and a threshold nobody has tuned. Rejected for now; the editor chooses from the description, the brief and the playbook, and `full` is one op away. Revisit when transcripts are in the editor's context.
- **A fixed bed level and no evening:** clips from different angles differ by 10 LU or more, so the bed would jump at every cut. Rejected.

**Consequences.**
- The export has sound under every shot, so a video is never silent; a clip with no sound track gives no bed and the check says so (`bed_gaps`), the fix being an effect on it.
- The preview renders the same mix plus the beat guide; the **audio stem** (the mix without the guide) is stored next to the preview so that the editor's check measures the export, not the guide. It has no database column (its key is derived from the render's), so a preview rendered before this change has none and the audio checks say so.
- A speed-ramped clip keeps its bed, at the clip's average speed; its speech is still muted (a tempo cannot follow a ramp).
- Measured on the founder's 26 s back-day edit with a bed on every clip: bed -24.2 LUFS (median short-term), effects at -11.1, -9.6 and -14.5 LUFS, integrated -21.0 LUFS, true peak -2.7 to -2.8 dBTP. (An earlier version of this decision normalised the bed and the effects together to -17 LUFS; that lifted the bed to -18, 4 LU under a trend sound, and was changed before merge.) The levers are `BedTargetLUFS`, `SoundReadyEffectGainDB` and `SoundReadyCeilingDB` in `internal/render/plan.go`.

**Revisit** when the founder has listened to a few real exports, or when transcripts are available to the editor.

## D-34 Music from the creator

> Status: **Proposed** by the director (2026-10-18), built in P6-06 so that it can be watched and listened to; the founder confirms or changes it. **Supersedes [D-33](#d-33-exports-are-sound-ready)** for an edit that has a song; an edit without one is still sound-ready as D-33 says. Every part is reversible: the song is an ordinary `creator_audio` item of the timeline, the mix is a few constants in `internal/render/audio.go`, and the editor's behaviour is one prompt version (`editor.v2.6`).

**Context.** The founder watched the first edits of the gym footage and asked for "no gym sound under the video, just the music": every reference video has loud music (about -12 to -20 LUFS momentary), and a drop 2 to 4.5 s in where the hook ends and the cut or the reveal lands. A sound-ready export (D-33: the gym as a quiet bed, the creator adds a trending sound in TikTok) gives the editor nothing to cut to, and the bed is exactly the sound the founder does not want.

**Decision.** The creator gives the edit a song, and the export carries it.
- **Where the song comes from.** An audio file the creator uploads into the project (the normal upload: the same tenancy, size and type checks as every asset; `audio/*` was already accepted), or the sound of one of their own videos (a reference whose song they want: a `creator_audio` item may name a video asset, and `cfctl music extract` writes that sound as a file for local work). The **creator is responsible for the rights** to music they upload or use from their videos: the asset catalog marks the creator's own uploads `attested`, which is what `creator_audio` needs (a `licensed_track` still needs a licence record). A trend sound added in TikTok stays possible: export without a song, and the edit is sound-ready as before (D-03 is unchanged: a `trend_sound` is a cue and never mixed).
- **Analysed once, without a model.** The analyze job measures an audio asset (and a video whose sound is music) when it is ready (package `music`, table `asset_music`): the tempo and a beat grid with downbeats and phrases, the strong onsets, the drop, the loudness. The editor reads it (a song line in the footage list, the tool `get_music`), cuts to it and `check_edit` holds the cuts to it.
- **The mix is the music.** An edit with a song is music-led: the clips' ambience bed is left out (a clip in `bed` mode is silent under a song), a clip in `full` mode (speech) stays and the song is ducked under it (a sidechain compressor keyed on the speech, about 5 dB), sound effects only when the plan adds them. The programme is normalised to **-14 LUFS integrated with the true peak at or under -1 dBTP** (the renderer's existing two-pass `loudnorm`), and a song that plays to the end fades out over its last 400 ms.

**Alternatives.**
- **Keep exports sound-ready (D-33) and only recommend a sound.** Rejected for the founder's footage: the edit cannot land its hook on a drop it cannot hear, and the gym bed is what the founder asked to remove.
- **Licensed music from a library.** Out of scope for now (no licence deals); the decision does not prevent it (`licensed_track` exists).
- **A rights attestation record per upload.** The schema has `RightsAttested` but no table; for now the upload itself is the attestation and the terms say the creator answers for it. Revisit when uploads get a consent step (P7-02).

**Consequences.**
- The export's sound is the song (-14 LUFS), not a bed at -21; the ears use the programme profile for it.
- The analysis adds one audio pass per audio asset and per video clip with sound (a fraction of a second; the clip's own analysis never fails because of it).
- Measured on the demo of the founder's footage with a synthetic 124 BPM track: -14.0 LUFS integrated, sample peak -2.2 dBFS, the song the only audio input of the graph.

**Revisit** when the founder has listened to edits with real songs, or when uploads get an explicit consent step.

## Template for new decisions

```
## D-xx Title
**Context.** Why we need to decide.
**Decision.** What we do.
**Alternatives.** What else we considered and why not.
**Consequences.** What this makes easier or harder.
**Revisit** when …
```
