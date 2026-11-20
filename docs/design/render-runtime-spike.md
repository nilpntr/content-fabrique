# Spike: render jobs on a Cloudflare Container per job

> Status: **spike done** · 2026-11-20 · checked against `main` at e3c17b3. Phase 2 of [render-runtime](render-runtime.md), run for
> real on the founder's Workers Paid account under the amended [D-08](../05-decisions.md#d-08-sandbox-pluggable-interface-default-provider)
> and [D-09](../05-decisions.md#d-09-hosting). Code: [`tools/cf-spike/`](../../tools/cf-spike/README.md). Every resource was named
> `cf-spike-*` and was deleted at the end (section 8). **[?]** marks what was not measured.

## 1. Verdict

**Go: make Cloudflare Containers the primary render runtime**, on the conditions in section 7. The render job ran unchanged
(same image, same `cf-job`, same manifest as the remote integration test). Starts were sub-second, 4 jobs ran in parallel without
slowing one another, the output was correct and the same on every run, and a 30 s final render cost about a quarter of a cent at
list price. What has to change is small and known: three image fixes, real R2 presigned URLs (needs a token from the founder), the
two-pass loudness step, golden hashes per CPU, and the router and remote ingest that the design already planned.

## 2. What ran

```
client.ts (Mac) --HTTPS+bearer--> Worker cf-spike-gateway --> Durable Object MediaJob (EU jurisdiction, one per job id)
                                        |                          --> Container cf-spike-media-job (standard-4, max 4), cf-job serve
                                        +-- /obj/{key} signed URLs <--> R2 cf-spike-media (EU jurisdiction)
```

- **Image**: `packages/media-tools` built for linux/amd64 on the VPS (Docker there; the Mac ran no Docker at all), plus a thin
  layer (`tools/cf-spike/container/Dockerfile`), pushed to `registry.cloudflare.com/<account>/cf-spike-media` with a 30-minute
  push credential from `wrangler containers registries credentials`. `wrangler deploy` then references the pushed image and needs
  no Docker.
- **Gateway**: the job contract of the design (`PUT/GET/DELETE /jobs/{id}`). The Durable Object starts the container, waits for
  cf-job's `/healthz`, forwards the manifest with a per-job token, stores the final status, destroys the container when the job
  ends, and an alarm destroys it at the deadline.
- **Objects**: wrangler's OAuth login can create buckets but not R2 S3 keys, so the spike signed its own URLs
  (HMAC, one key, one method, one hour) served by the Worker over an R2 binding. To cf-job they are presigned URLs.
- **Cases** (`tools/cf-spike/manifests/`, generated from the render compiler with the integration test's code path):
  `s10-preview` is the integration test's own manifest (`v08_transition_crossfade`, 8.6 s, 540x960); `s10-final` is the same
  timeline in the Final profile at 1080x1920; `s30-*` is 11 crossfaded clips (29 s). Sources are synthetic (testsrc2 with a tone,
  16 s; 640x360 for the test case, 1080x1920 for the rest), rendered by a first job on Cloudflare.
- **Baseline**: the same image as `cf-job serve` on the VPS (Docker, 12 GB, 8 vCPU AMD EPYC Rome) through the same client. The
  VPS carries other projects (load average 7 to 14 during the runs), so its times are a lower bound of what a quiet box does.

## 3. Measurements

| What | Result |
|---|---|
| Containers enabled, scopes | Yes. wrangler's OAuth token has `containers (write)` and `cloudchamber (write)`; R2 bucket create and delete work |
| Image | 486 MB unpacked, about 150 MB compressed. Cold build of the media-tools image on the VPS: 431 s (ffmpeg and whisper.cpp from source). First push 13 s; a changed top layer 5 to 6 s |
| Rollout | `wrangler deploy` takes about 19 s. A **new image** then reaches the instances in 1 to 7 minutes (rollout); jobs started meanwhile run the old image |
| First start after the first deploy | Over 90 s (the gateway's health wait timed out once; the retry started in 0.34 s). The image is pulled onto pre-placed instances first |
| Cold start (start to cf-job healthy) | **median 0.34 s**, 0.20 to 0.62 s over 20 jobs, including after 5 and 12 minutes idle. Cloudflare keeps instances placed with the image ready (`healthy: 3`, `instances: 4`) and bills only running time |
| Gateway overhead | 0.27 s median from the client's PUT to the job running (Mac in the Netherlands, workers.dev) |
| Placement | WEUR only: cdg (Paris), dus (Düsseldorf), ams (Amsterdam), dub (Dublin), from the usage analytics. `constraints.jurisdiction: "eu"` was accepted |
| CPU | 4 processors, AMD EPYC with AVX-512 |

**Wall times** (cf-job's `duration_ms`: downloads, all steps, uploads; plus the encode step alone):

| Case | Cloudflare job | Cloudflare encode | VPS job (loaded) | VPS encode |
|---|---|---|---|---|
| s10-preview (8.6 s, 540x960) | 3.1 s | 1.4 s | 6.3 s | 4.0 s |
| s10-final (8.6 s, 1080x1920) | 9.2 s | 4.6 to 4.8 s | 18.5 s | 11.7 s |
| s30-preview (29 s, 540x960) | 9.3 s | 6.7 s | 23.4 s | 19.6 s |
| s30-final (29 s, 1080x1920) | 28.8 s | 18.4 s | 56.8 s | 37.7 s |

The job times include the spike's verification steps (ffprobe `-count_frames` and two MD5 decodes): for s30-final 8.8 s of the
28.8 s. A production final is the encode, a probe and a poster: about 20 s for 29 s of 1080x1920. Client submit to result: 29.7 s.

**Parallel**: 4 × s30-final at once: all 4 succeeded, encodes 15.4 to 18.8 s, batch wall 31.3 s (one alone: 29.7 s), all four
outputs byte-identical. 6 × s10-preview with `max_instances: 4`: the first 4 started in 0.2 to 0.33 s, the other 2 waited
10.5 s for an instance and then ran; nothing failed. Past the cap Cloudflare queues the start, it does not refuse it.

**No internet**: with the object URLs on `http://objects.internal` and `interceptOutboundHttp` handing them to a Worker
entrypoint, the container ran with `enableInternet: false`: s30-final succeeded in the same time and with the same hash, and
a download from `https://www.cloudflare.com` failed (`lookup www.cloudflare.com: i/o timeout`).

## 4. Correctness

| Check | Result |
|---|---|
| Frame count and duration | Every case, both runtimes: 258 frames / 8.600 s and 870 frames / 29.000 s, as the plan says; sizes as the profile says |
| Repeatability on Cloudflare | Identical bytes on every run (s10-final twice, s30-final five times, also under parallel load and without internet) |
| Audio | Decoded audio MD5 identical on Cloudflare and the VPS in every case |
| Video, Cloudflare against the VPS | **Different bytes** (sizes within 1 %). Not the thread count: pinned to `-threads 6` (what x264 picks for 4 CPUs) they still differ, and turning off ffmpeg's own AVX-512 paths changes nothing. With x264's assembly off (`-x264-params asm=0`) and the threads pinned, the outputs are **byte-identical**: the difference is x264's AVX-512 code on Cloudflare's EPYC against AVX2 on the VPS's |

So the render is deterministic per CPU family. Byte-for-byte goldens (golden_render) must be recorded on the runtime they check,
or the encoder pinned to a common instruction set (section 7).

## 5. Cost

**Real usage** (GraphQL `containersUsageAdaptiveGroups`, read with wrangler's OAuth token; it reports CPU seconds, allocated
memory and disk in byte-seconds, and bytes sent). Whole spike, every job including the failed ones and the diagnostics:
**508.5 vCPU-seconds, 3,240 GiB-seconds of memory, 5,400 GB-seconds of disk, 41.5 MB sent** (up to 20:02 UTC; the last
job, a 3 s preview at 20:05, had not reached the analytics yet and adds about $0.0004). At list price that is **$0.019**, and all of it is inside what Workers Paid includes (375 vCPU-minutes,
25 GiB-hours, 200 GB-hours a month), so **the spike billed $0 beyond the $5 plan**. The invoice itself was not checked **[?]**;
Durable Objects, Workers requests and R2 (a few hundred operations, under 100 MB for two hours) are inside their free tiers.

**Per render** at list price ($0.000020 per vCPU-s, $0.0000025 per GiB-s, $0.00000007 per GB-s):

| Render | CPU | Memory (12 GiB) | Disk (20 GB) | Cost |
|---|---|---|---|---|
| s30-final, measured (one job alone in its minute) | 70.5 vCPU-s | 360 GiB-s | 600 GB-s | **$0.0024** |
| s30-final without the verification steps (estimate) | about 55 vCPU-s | about 250 GiB-s | about 420 GB-s | about $0.0018 |
| s30-preview, s10-final (measured CPU 16 to 28 s, about 11 s running) | | | | about $0.0007 |
| s10-preview | 4.4 vCPU-s | 120 GiB-s | 200 GB-s | $0.0004 |

A 30 s exported video (one final, three previews) is about **$0.004**, against the design's estimate of $0.01. Memory is over a
third of a final and most of a short job: a standard-4 bills 12 GiB whether ffmpeg uses it or not, at $0.00003/s, more than one
fully busy vCPU. A custom instance (4 vCPU, 6 GiB, which the limits page allows) would cut a final by about a fifth **[?]**. The analytics count allocation in
10-second steps (every short job shows 120 GiB-s, exactly 12 GiB × 10 s): either the minimum charge or the sampling **[?]**.

At these numbers the plan's included usage covers about 360 thirty-second finals a month (memory, 25 GiB-hours, runs out
first). The crossover with a dedicated box at about €120 a month moves from the design's 15,000 to roughly 70,000 finals, or
about 30,000 exported videos with their previews **[?]**.

## 6. Limits hit and surprises

1. **The media-tools image does not run without compose's mounts.** `/work` is root-owned and 755 (the Dockerfile's
   `COPY --chown ... /rootfs/work /work` sets the contents, not the directory), so every step failed with EACCES until the
   layer re-copied `/in` and `/work` as entries. Compose and the integration test mount a tmpfs there, which hid it.
2. **No CA bundle in the image**: cf-job could not verify any HTTPS URL (`x509: certificate signed by unknown authority`).
   Compose uses plain-http MinIO, which hid it. Real R2 presigned URLs are HTTPS.
3. **No R2 S3 key from OAuth**: wrangler can make buckets but not S3 credentials, so presigned URLs need an API token (section 9).
   The spike's stand-in sends the bytes through the Worker, which caps a request body at 100 MiB; presigned R2 does not.
4. **The intercept path caps request bodies at 100 MiB** too (the docs say streaming does not lift it). A 90 s final at CRF 18
   is near that. Real footage downloads are responses, not request bodies, so only uploads are affected **[?]**.
5. **Two-pass loudness does not fit one manifest**: the Final profile measures in pass 1 and puts the numbers into pass 2's
   arguments, and a manifest's steps cannot pass values. The spike's finals used one pass (the measuring pass is a decode of the
   audio only, a few seconds).
6. **New images roll out in minutes**, not at deploy: an image tag must be immutable and the worker must accept results from
   the previous image during a rollout.
7. **The first start on a new application took over 90 s** (image pull). Deploys should warm the application with one job
   before traffic, or the gateway's health wait must be longer than 90 s.
8. **AVX-512 changes x264's output** (section 4).
9. Nothing hit the per-instance limits: the biggest job used under 30 MB of inputs, a few hundred MB of memory **[?]** and
   2.4 vCPU on average of the 4.

## 7. Changes needed to make it primary

| Change | Where | Why |
|---|---|---|
| Image: writable `/in` and `/work` as directory entries, CA bundle at `/etc/ssl/certs/ca-certificates.crt`, `cf-job serve` as entrypoint (or the gateway passes it) | `packages/media-tools/Dockerfile` | §6.1, §6.2. The thin layer in `tools/cf-spike/container` does exactly this |
| Real presigned R2 URLs on the EU endpoint; the gateway holds no R2 binding | `sandbox` remote provider, worker config | §6.3; needs the founder's R2 token |
| Egress: `enableInternet: false` with `interceptOutboundHttps` for the R2 EU host only, and cf-job trusting the CA Cloudflare injects at `/etc/cloudflare/certs/cloudflare-containers-ca.crt` (`SSL_CERT_FILE` or a copy at start) | gateway, cf-job | The spike proved the plain-HTTP intercept; HTTPS intercept is documented, not tried **[?]** |
| Two-pass loudness: run the measuring pass as its own remote job (the footage downloads twice) or teach cf-job one templated step (`args` reading a previous step's JSON output) | render pipeline or cf-job | §6.5 |
| Goldens per runtime: record golden_render on the Cloudflare runtime, or pin x264 to a common instruction set (an `asm=` mask without AVX-512) in every profile | render tests, profiles | §4 |
| `sandbox.Router` as designed (section 6 of render-runtime): Cloudflare first; the `hardened` box for estimated disk over 16 GB, wall time over 20 minutes, a GPU, two gateway failures, or the month's spend cap | `sandbox` | Fallback |
| **Remote ingest (the "mount provider")**: ingest mounts the original today. The original is already in R2 (the upload lands there), so ingest should build a remote job directly: presigned GET of the original as an input, presigned PUTs for proxy, poster, audio and peaks. A provider that turns `Mounts` into uploads would copy footage from the host to R2 and back, which defeats the point; keep it only as a dev shim | `ingest` pipeline | The worker refuses `remote` until ingest and render build remote jobs (`cmd/worker/media.go`) |
| Gateway hardening: one Durable Object per attempt id (done in the spike), the alarm as the backstop, a `GET /jobs` for the orphan sweep, health wait over 90 s, observability on | `apps/sandbox-gateway` from `tools/cf-spike/gateway` | §6.7 |
| Image pipeline: build linux/amd64 in CI, push with a short-lived registry credential, deploy, wait for the rollout, warm with one job | CI | §6.6, §6.7. The Mac needs no Docker |
| Spend: billing notification on the account; the router's monthly cap reads the usage analytics | spend control | §5 |
| Consider a custom instance (4 vCPU, 6 GiB, 20 GB) for finals | wrangler config | §5 |

## 8. Resources and deletion

Created, all `cf-spike-*`: Worker `cf-spike-gateway` with the Durable Object class `MediaJob` (SQLite, EU jurisdiction),
container application `cf-spike-media-job`, registry images `cf-spike-media:v1` to `v4`, R2 bucket `cf-spike-media` (EU) and its
objects, and on the VPS the images `cf-spike-media-base`, `cf-spike-media`, the container `cf-spike-local` and `~/cf-spike`.
`tools/cf-spike/teardown.sh` deleted them; the listing after it is in section 10.

## 9. What the founder needs to give

- **An R2 API token** (Object Read & Write, scoped to the staging bucket, EU jurisdiction) for real presigned URLs. Its S3
  access key and secret go in `.env` as `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY` (the `.env` holds MinIO's dev values today).
- The decision on this note: amend D-08 and D-09 from "conditional" to settled, and order the changes of section 7.

## 10. Deletion check

After `teardown.sh` (2026-11-20, about 20:08 UTC): the gateway's `DELETE /objects` removed 60 objects and left 0; the four
registry tags were deleted; the Worker and the EU bucket were deleted. Then:

| Listing | Result |
|---|---|
| `wrangler containers list` | No containers found |
| `wrangler containers images list` | empty |
| `wrangler r2 bucket list --jurisdiction eu` | no buckets |
| `wrangler deployments list --name cf-spike-gateway` | "This Worker does not exist on your account" (10007) |
| Durable Object namespaces (API) | none named cf-spike or of class MediaJob |
| VPS: `docker ps -a`, `docker images` matching cf-spike, `~/cf-spike` | 0, 0, gone |
| Mac: `docker ps` | fails (no Docker daemon; OrbStack was never started) |

Left on purpose: Docker's shared build cache on the VPS still holds the media-tools build layers (pruning it would also drop the
other projects' cache); `docker builder prune` there reclaims them if wanted. The usage analytics stay on the account.
