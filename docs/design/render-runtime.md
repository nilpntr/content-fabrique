# Design note: render runtime on Cloudflare Containers, with the box as fallback

> Status: **proposed** · 2026-11-20 · checked against `main` at 62963ab. Design only: nothing is created on Cloudflare and no code changes. Prices and limits were read from Cloudflare's docs on 2026-11-20; **[?]** marks what is unverified or assumed. This note asks to amend D-08 and D-09 (section 9); it does not override them.

**Goal.** Run the heavy media work (ffmpeg renders, chrome-headless-shell motion frames, analysis, whisper) in a container that exists only for the job, on the founder's paid Workers account, with River as the only queue, and keep one image that also runs under docker compose on a Mac or the VPS.

## 1. Options

| Option | Fits | Does not fit |
|---|---|---|
| **(a) Cloudflare Container per job** (primary) | Pay per 10 ms, EU jurisdiction, Firecracker microVM per job, no host to patch, starts in seconds | 4 vCPU / 12 GiB / 20 GB disk per instance, no GPU, about 7 to 15× a dedicated core at full use (D-09) |
| (b) Cloud VM per job (Hetzner Cloud, Fly Machines) | Bigger machines; Fly performance-4x is about $0.18/h **[?]** | Boot plus image pull takes 20 to 60 s **[?]**; an API token that can create servers; orphan VMs bill by the hour; Hetzner raised prices in 2026 and marks plans unavailable **[?]**. Not chosen |
| **(c) Dedicated box**, `hardened` provider (fallback) | Jobs over the limits, long renders, steady volume past the crossover | Fixed monthly cost, ops, one box is one failure domain |
| **(d) Founder's VPS or Mac** (dev and test) | The same image under compose, $0 | Not production: no gVisor, shared host |

## 2. Shape

```
api -> River `render` queue -> Go worker (EU host)            presigns R2 URLs (EU endpoint, D-15)
        sandbox `remote` provider --HTTPS + bearer--> Worker `sandbox-gateway`
              -> Durable Object `MediaJob` (one per attempt, EU) -> Container (media-tools image, `cf-job`)
                    GET inputs / PUT outputs <-> R2 EU bucket, presigned URLs only
```

- **River stays the orchestrator.** No Cloudflare Queues or Workflows: a second queue would mean two sources of truth for retries, credits and progress. The Go worker compiles the plan as today and blocks in `Provider.Run` while it polls the gateway.
- **The Durable Object is the job's supervisor**: it starts the container, stores status, exit code and a bounded output tail in its SQLite storage, and sets an alarm for the deadline.

## 3. Provider decision

A **fourth implementation of the existing `sandbox.Provider`**, named `remote`, not a new interface. `Provider.Run` stays. Two things change shape:

- `Job` gains `Inputs []{URL, Path}` and `Outputs []{Path, URL}` (presigned GET and PUT), exclusive with `Mounts`. This is the `Download`/`Upload` of architecture §8, in one-shot form.
- A render attempt makes two or three `Run`s today (loudness measurement, encode, contact sheet) that share `/work`. A remote job carries them as ordered `Steps` in one manifest, so the footage is downloaded once.

`remote` talks to one HTTP contract, `PUT /jobs/{id}` (manifest, idempotent), `GET /jobs/{id}`, `DELETE /jobs/{id}`, served by two backends: the gateway Worker (staging, production) and `cf-job serve` in compose (dev, VPS). The id is the render id plus the attempt, so a retry never starts a second container for the same attempt. D-08's `cloudflare` provider (Sandbox SDK, interactive `Exec`, agent escape hatch) stays a separate, unbuilt thing; the Sandbox SDK is priced as Containers and adds nothing a one-shot job needs.

**`cf-job`** is a small standard-library Go binary added to the media-tools image: read the manifest, download inputs to `/in`, run each step without a shell under the job's limits, upload outputs, report `{exit_code, stdout, stderr, duration, timed_out}`. Output sizes are capped in the runner (`MaxOutputBytes`), and the worker checks the R2 object before it commits the render row, as it treats outputs today.

## 4. Sizing, cold start, concurrency

| Job | Instance | Why |
|---|---|---|
| Preview (720p proxies) | standard-3: 2 vCPU / 8 GiB / 16 GB | Short; the 10 s preview budget is the risk |
| Final render, whisper.cpp | standard-4: 4 vCPU / 12 GiB / 20 GB | Today's render limit is 4 CPU / 4 GiB (`render.DefaultLimits`) |
| Ingest, analysis, contact sheet | standard-2: 1 vCPU / 6 GiB / 12 GB | Mostly I/O |

- **Cold start**: Cloudflare says "often 1 to 3 s" from a pre-fetched image. Ours is 310 MB, more with a whisper model; assume 3 to 8 s **[?]** and measure in phase 2. If previews miss the budget, the options are a warm container per workspace for a few minutes (changes "one sandbox per job", a D-08 question) or previews on the Go host.
- **Concurrency**: account limits are far above our use (the limits page says 15,000 concurrent vCPU, a changelog says 1,500 **[?]**). The real caps are ours: `max_instances` in wrangler (start at 8) and `RENDER_CONCURRENCY`, which can rise to that number because the worker only waits. Past it, jobs wait in River. A capacity error from the gateway is `sandbox.ErrSandbox`: River retries with backoff, and after two tries the router (section 6) sends the job to the box.

## 5. Cost

Metered rates: $0.000020 per vCPU-second (active CPU only), $0.0000025 per GiB-second, $0.00000007 per GB-second of disk. standard-4 with all cores busy is $0.000111/s, **$0.40/h**; standard-3 is $0.22/h.

| 1080x1920 edit | Wall time assumed **[?]** | Final render | Three previews | Per exported video |
|---|---|---|---|---|
| 30 s | final 60 s, preview 15 s | ≈ $0.007 | ≈ $0.003 | **≈ $0.01** (range $0.007 to $0.02) |
| 90 s | final 150 s, preview 30 s | ≈ $0.017 | ≈ $0.006 | **≈ $0.025** (range $0.017 to $0.05) |

Wall times are assumptions (P1-03 measured a plain 30 s 1080p transcode at 7.4 s on 4 vCPU; a real edit adds filters, overlays and two loudness passes). Phase 2 replaces them with measurements. Architecture §13's "≈ $0.005" counts CPU only.

**What Workers Paid ($5/month) includes**: Containers 375 vCPU-minutes, 25 GiB-hours and 200 GB-hours of disk, which is **about 90 thirty-second finals a month before anything is metered**; 1 TB of container egress in Europe, then $0.025/GB (whether uploads to R2 count is **[?]**); Durable Objects 1 M requests and 400,000 GB-s (a job uses about 20 GB-s); Workers 10 M requests. R2 is billed apart with its own free tier (10 GB, 1 M class A, 10 M class B, no egress fee). Queues (1 M operations) and Workflows are included but not used. Workers AI is metered apart.

**Crossover**: a dedicated 16-core box at about €120/month **[?]** equals about 300 standard-4 hours, about 15,000 short finals a month. Below that Cloudflare is cheaper; near it, move steady volume to the box and keep Cloudflare for bursts (D-09's original shape).

## 6. Fallback and splitting

A `sandbox.Router` (itself a `Provider`) picks per job. It sends a job to `hardened` on the box when: estimated disk (inputs + 2× output + frames) is over 16 GB (`MaxInputBytes` is 8 GiB and the output cap 4 GiB, so the worst case does not fit 20 GB); estimated wall time is over 20 minutes; a GPU is asked for; the gateway failed twice; or the month's Cloudflare spend passed its cap in spend control. With no box configured the render fails with the existing `inputs_too_large` reason, never silently.

**Splitting (phase 4, only for jobs that would otherwise fall back).** Cut the timeline at item boundaries on the main track where no transition, graphic or text item crosses, into segments of 20 to 30 s. Each segment renders video-only in its own container with identical encoder settings and closed GOPs; overlays keep absolute time, which the motion runner's virtual clock allows. Audio (bed, effects, loudness) renders once for the whole timeline in one container. A last container concatenates with stream copy and muxes the audio. Split output is not byte-identical to the single-pass render, so it is its own golden profile. Edits of 30 to 90 s do not need it.

## 7. EU residency, transcription, security

- **EU**: the container config gets `jurisdiction: "eu"` and the Durable Object namespace `jurisdiction("eu")`; R2 stays the EU bucket with EU-endpoint presigning (D-15). The gateway Worker runs at any edge but sees only manifests (URLs and arguments), never media.
- **Whisper (D-27)**: Workers AI `whisper-large-v3-turbo` is $0.000513 per audio minute, but Cloudflare's data-localization table lists Workers AI as **not supported by Regional Services**, so EU-only inference is not guaranteed, and word-level timestamps are not confirmed on the model page **[?]**. D-27's first check is therefore open. Until the founder decides, transcription runs whisper.cpp in a standard-4 container in the EU.
- **No credentials in the container**: only presigned URLs scoped to the job's own keys, expiring with the job timeout. The Worker holds one secret (the gateway token) and no R2 binding.
- **Egress**: start with `enableInternet: false` and intercept outbound HTTPS to the R2 EU host only, forwarding just the URLs in this job's manifest **[?]** (API read from the docs, not tried). This replaces "network is always off" for remote jobs, where a media tool can otherwise reach nothing.
- **Isolation**: one microVM per job, never reused across workspaces. Chrome's own sandbox is off (D-35) and Firecracker confines it instead of gVisor; the director's pending D-35 review must cover that.
- **Gateway auth**: bearer `SANDBOX_GATEWAY_TOKEN` (already in `.env.example`), constant-time compare, and no route that lists other jobs' data.

## 8. Failure, timeout, orphans

- **Timeout**: the Durable Object alarm fires at `Limits.Timeout` plus 30 s, sends SIGTERM, then destroys the container; the worker gets `ErrTimeout`. The inactivity timeout is one minute, so an idle container never bills for long.
- **Container lost** (rollout, host failure): `ErrSandbox`, River retries (5 attempts today). Cloudflare gives a process up to 15 minutes after SIGTERM on a rollout; renders are idempotent, so we just retry.
- **Worker crash**: River rescues the job; the new attempt first sends `DELETE` for the previous attempt's id.
- **Orphans**: the existing `renders_sweep` maintenance job also asks the gateway for running jobs and deletes any whose render is not `rendering`. The alarm is the backstop if the Go side is down. Partial outputs under the render's prefix are removed by the existing purge.

## 9. Contradictions with settled decisions

1. **D-08** says all deterministic media jobs run in `hardened` (gVisor, our hosts) and Cloudflare is for the agent escape hatch. This note makes Cloudflare Containers primary for media jobs. Needs an amendment.
2. **D-09** puts API and workers on EU servers with Containers as the burst. This inverts render placement. The Go `api` and `worker` still need an EU host behind the Tunnel (a small VPS is enough once renders leave it); D-09's cost point holds only at full use.
3. **`sandbox.NetworkNone`** is a code invariant ("the network is always off"). Remote jobs need presigned-URL egress, which §8 already planned.
4. **D-27 against D-15**: see section 7.

## 10. Resources and rollout

**Cloudflare pieces (designed, not created).** One Worker, `apps/sandbox-gateway`, with in `wrangler.jsonc`: `containers: [{ class_name: "MediaJob", image: "../../packages/media-tools/Dockerfile", instance_type: "standard-4", max_instances: 8, constraints: { jurisdiction: "eu" } }]` (exact key names and per-job instance size **[?]**), `durable_objects.bindings: [{ name: "MEDIA_JOB", class_name: "MediaJob" }]`, `migrations: [{ tag: "v1", new_sqlite_classes: ["MediaJob"] }]`, observability on. No Queues, Workflows, KV or R2 binding.

**By hand, the founder**: confirm Workers Paid on the staging and production accounts; create the EU R2 bucket and its S3 token if P0-09 has not; `wrangler secret put GATEWAY_TOKEN`; run `wrangler deploy` with Docker running (it builds and pushes the image); set a billing notification; optionally a route such as `jobs.contentfabrique.app`.

**Dev and VPS path.** `compose.yaml` gets a `runner` profile: service `media-runner` from the media-tools image running `cf-job serve`, read-only root, tmpfs `/work`, no capabilities, `cpus: 4` and `mem_limit: 12g` so the VPS reproduces the Cloudflare limits, network to MinIO only. `MEDIA_SANDBOX_PROVIDER=remote`, `MEDIA_RUNNER_URL=http://localhost:8090` (built as 8090: 8080 is the API's port). Refused when `CF_ENV` is staging or production unless the URL is the gateway. As built in phase 1: Docker publishes no port from an `internal` network, so the runner's network is a bridge it shares with MinIO alone, and cf-job connects only to `CF_JOB_ALLOWED_HOSTS` (dialled as `minio:9000`); the worker's ingest and render still mount files, so the worker refuses `remote` until they build remote jobs.

| Phase | Slice | Cost |
|---|---|---|
| **1 (first PR)** | `cf-job` in the image, `Job.Inputs/Outputs/Steps`, the `remote` provider, the compose `runner` profile, an integration test that renders a fixture through MinIO. No Cloudflare. | $0 |
| 2 | `apps/sandbox-gateway` under `wrangler dev`, then staging; measure cold start and wall time for 30 s and 90 s edits; fill the table in section 5 | Inside the included usage |
| 3 | Router and fallback, orphan sweep, spend cap, amend D-08 and D-09 | $0 |
| 4 | Split rendering; the transcription decision (D-27) | Metered |
