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
  - Claude models via a Go-native agent loop.
  - A pluggable sandbox layer: gVisor-hardened containers for our media jobs, Cloudflare Sandbox for free-form agent code.
  - Cloudflare K2 is deliberately **not** needed in v1.
- **Biggest risks and answers:**
  - **Music licensing:** never reproduce trending songs; cut to the beat, preview with a beat-guide track, and let the creator add the sound in TikTok via an inbox draft.
  - **Trend data:** TikTok's Research API is non-commercial only, and Creative Center's song chart is unreliable. Use curation, web research, creator links and licensed music analytics instead.
  - **Edit quality:** eval suites gate every prompt or model change.
  - **Cost:** model tokens dominate. Claude Opus 5.5 on every edit is ≈ $1.10 per video, so where cheaper models are good enough is a decision for after the editor evals.
- **Decisions waiting on you:**
  - model routing vs price per video (after P4);
  - the legal questions in P0-12;
  - your budget, target regions and beta creators ([implementation plan, open questions](04-implementation-plan.md#open-questions-for-the-founder)).
- **First action:** `apps/web` was committed as a broken git link, so the UI isn't in the repo yet. See [implementation plan §0](04-implementation-plan.md#0-current-state-2026-10-16).

## Documents

| # | Document | What's in it |
|---|---|---|
| 01 | [Product plan](01-product-plan.md) | Problem, users and personas, principles, journeys, MVP scope, agents, music strategy, pricing hypothesis, metrics, risks, seed concept library, roadmap |
| 02 | [Research report](02-research.md) | TikTok APIs and trend data, music licensing, video understanding, editing and rendering stack, competitors, agent runtime and sandboxes, Cloudflare/Clerk/Go platform, costs. Sourced and confidence-marked |
| 03 | [Architecture](03-architecture.md) | Components, data model, media pipeline, timeline spec and renderer, agents and evals, sandbox interface, API, realtime, auth/billing, deployment, cost model, security |
| 04 | [Implementation plan](04-implementation-plan.md) | Phases P0–P7 with PR-sized tasks and acceptance criteria, cross-cutting practices, definition of done, open questions |
| 05 | [Decision log](05-decisions.md) | Lightweight ADRs (D-01 … D-16) with context, alternatives and when to revisit |

Suggested reading order: TL;DR → 01 → 03 → 04, with 02 and 05 as reference.

## Keeping these docs alive

- Change a decision? Update its entry in [05](05-decisions.md) (don't delete history; mark it superseded) and the affected sections.
- Each implementation phase should leave the docs truer than it found them: schemas in `packages/schemas`, runbooks next to the services.
- Re-check vendor prices and limits in [02](02-research.md) before committing money; they move fast.
