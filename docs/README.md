# Content Fabrique: Plan & Research

> Status: **draft v0.1** · 2026-10-16 · Scope agreed with the founder: **multi-tenant SaaS, fitness creators first, TikTok only for v1**.

## TL;DR

- **What:** creators upload raw clips. A **Research agent** tracks what works on fitness TikTok right now. An **Editor agent** cuts the creator's own footage into a trend-aware short. The creator steers with an optional brief and with comments pinned to time ranges or spots in the video, or lets the agents run free.
- **Core technical bet:** agents never write raw ffmpeg. They edit a **declarative timeline (JSON)** through validated operations, and a deterministic Go renderer turns it into video. This makes edits reviewable, revisable from comments, testable and cheap to re-render.
- **Stack:**
  - TanStack Start on Cloudflare Workers.
  - A Go API + River job workers on EU servers behind Cloudflare Tunnel, bursting to Cloudflare Containers when queues back up.
  - Postgres from day one, not D1, because a Go service outside Workers can only reach D1 through Cloudflare's rate-limited REST API.
  - R2 in the EU jurisdiction.
  - Clerk for auth; billing through a merchant of record (handles EU VAT), not Clerk Billing.
  - Every model call goes through Cloudflare AI Gateway in BYOK mode: our Anthropic key or the creator's for customers, and a development key, funded by you (no AWS credits), for testing. Other providers' models use the same gateway for benchmarks. A direct call is only a bypass for debugging.
  - Claude Opus 5.5 edits, via a Go-native agent loop. Cheaper models (Haiku 5.5) run as sub-agents directed by an Opus 5.5 lead for research and other bulk reading. Everything else is tuned by a benchmark harness.
  - A pluggable sandbox layer: gVisor-hardened containers for our media jobs, Cloudflare Sandbox for free-form agent code.
  - Cloudflare K2 is deliberately **not** needed in v1.
- **Biggest risks and answers:**
  - **Music licensing:** never reproduce trending songs; cut to the beat, preview with a beat-guide track, and let the creator add the sound in TikTok via an inbox draft.
  - **Trend data:** TikTok's Research API is non-commercial only, and Creative Center's song chart is unreliable. Use curation, web research, creator links and licensed music analytics instead.
  - **Edit quality:** eval suites gate every prompt or model change.
  - **Cost:** model tokens dominate. With Opus 5.5 editing, an exported video costs ≈ $1.25 all-in, about half of it output tokens. So the benchmark tunes effort and escalate-on-failure first, and cheaper models second. Testing climbs a cheap-first ladder (free replay, then Haiku, then Sonnet, then Opus), so paid sweeps aren't spent on fixable flaws.
  - **Who pays for tokens:** two plan families per workspace. *Managed* credits (we call Claude, priced for ≥ 60 % margin) or *BYOK* (the creator's Anthropic API key, lower platform fee). Claude Pro/Max logins can't be used in third-party products.
- **Decisions waiting on you:**
  - the billing mode for the private beta (I recommend managed credits with a hard cap per creator, BYOK opt-in);
  - which company operates Content Fabrique (the TikTok developer app, data processing agreements and merchant of record);
  - the company site: a one-page site on `contentfabrique.app` (the domain is bought) with a privacy policy and terms. TikTok review needs it, and Clerk production and the DNS records need the domain;
  - plan prices and the margin target;
  - the benchmark budget you'd fund yourself (≈ $350 per development sweep, ≈ $1,000 for the cutover, about $1,650 to $2,550 in total, partly covered by your Max plan's $200 a month of API credit, which doesn't roll over), and whether the Console organization holding that credit is development-only;
  - the legal questions in P0-12, including Anthropic's terms for customer keys;
  - your budget, target regions and beta creators ([implementation plan, open questions](04-implementation-plan.md#open-questions-for-the-founder)).
- **First action:** `apps/web` was committed as a broken git link, so the UI isn't in the repo yet. See [implementation plan §0](04-implementation-plan.md#0-current-state-2026-10-16). Then copy `.env.example` to `.env` and fill the three `[YOU-now]` values ([setup guide](06-setup-and-credentials.md)). Then start the director session with the prompt in [setup guide §7](06-setup-and-credentials.md#7-prompt-for-the-implementing-agent).

## Documents

| # | Document | What's in it |
|---|---|---|
| 01 | [Product plan](01-product-plan.md) | Problem, users and personas, principles, journeys, MVP scope, agents, music strategy, pricing and business model (managed + BYOK), metrics, risks, seed concept library, roadmap |
| 02 | [Research report](02-research.md) | TikTok APIs and trend data, music licensing, video understanding, editing and rendering stack, competitors, agent runtime and sandboxes, Cloudflare/Clerk/Go platform, costs. Sourced and confidence-marked |
| 03 | [Architecture](03-architecture.md) | Components, data model, media pipeline, timeline spec and renderer, agents and evals, model routing and sub-agents, benchmark harness, sandbox interface, API, realtime, auth/billing, managed vs BYOK model access, deployment, cost model, security |
| 04 | [Implementation plan](04-implementation-plan.md) | Phases P0–P7 with PR-sized tasks and acceptance criteria, cross-cutting practices, definition of done, open questions |
| 05 | [Decision log](05-decisions.md) | Lightweight ADRs (D-01 … D-23) with context, alternatives and when to revisit |
| 06 | [Setup and credentials](06-setup-and-credentials.md) | Every account, key and `.env` variable, who creates it and when; the scopes to grant each token; the hostnames on `contentfabrique.app`; `cfctl doctor` and bootstrap tooling; a prompt for the implementing agent |

Suggested reading order: TL;DR → 01 → 03 → 04, with 02 and 05 as reference, and 06 before you start building.

## Keeping these docs alive

- Change a decision? Update its entry in [05](05-decisions.md) (don't delete history; mark it superseded) and the affected sections.
- Each implementation phase should leave the docs truer than it found them: schemas in `packages/schemas`, runbooks next to the services.
- Re-check vendor prices and limits in [02](02-research.md) before committing money; they move fast.
