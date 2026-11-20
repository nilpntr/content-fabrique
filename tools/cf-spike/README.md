# cf-spike: one render job per Cloudflare Container

The spike of [docs/design/render-runtime.md](../../docs/design/render-runtime.md) (phase 2). Results and the go/no-go are in
[docs/design/render-runtime-spike.md](../../docs/design/render-runtime-spike.md). Throwaway code: nothing here is used by the
product, and every Cloudflare resource it makes is named `cf-spike-*` so `teardown.sh` can find and delete it.

| Path | What |
|---|---|
| `container/Dockerfile` | The job image: the media-tools image plus a writable `/in` and `/work`, a CA bundle and `cf-job serve` as entrypoint |
| `gateway/` | Worker `cf-spike-gateway` and Durable Object `MediaJob` (EU jurisdiction), container application `cf-spike-media-job` (standard-4, `max_instances` 4) |
| `client.ts` | Submits a case, waits, downloads the result, prints timings, frame count, duration and hashes (one JSON line per job) |
| `manifests/*.json` | The cases, generated from the render compiler with the remote integration test's code path (see below) |
| `push-image.sh`, `remote-push.sh` | Build the image on a Docker host over SSH (linux/amd64) and push it to the account's managed registry |
| `deploy.sh`, `teardown.sh` | Deploy with the pushed image (no Docker needed), and delete everything again |

## The contract

The gateway serves the same `PUT/GET/DELETE /jobs/{id}` as `cf-job serve` (bearer token), so `client.ts` runs the same case
against Cloudflare or against `cf-job serve` in Docker anywhere (`RUNNER_URL`, `RUNNER_TOKEN`). One Durable Object per job id
starts one container, waits for cf-job's `/healthz`, forwards the manifest with a per-job token, keeps the final status,
destroys the container when the job ends, and an alarm destroys it at the deadline.

**Object URLs.** The design wants R2 S3 presigned URLs. Making those needs an R2 S3 access key, which wrangler's OAuth login
cannot create, so the spike signs its own: `/obj/{key}?m=GET|PUT&exp=&sig=` with `sig = HMAC-SHA256(GATEWAY_TOKEN,
"obj\n" + m + "\n" + key + "\n" + exp)`, served by the Worker over an R2 binding on the EU bucket `cf-spike-media`. To cf-job it
is the same thing (a time-limited HTTPS URL for one key and one method). The bytes pass through the Worker, which a real
presigned URL avoids.

## The cases

`manifests/` was written by a throwaway test in `apps/server/internal/render` (not committed) that compiles a timeline exactly
as `TestIntegrationRemoteRender` does (placeholder inputs, `mountInputs`, `useImageFonts`, the subtitles as an input, the
encode, ffprobe and poster steps), with ffprobe given `-count_frames`:

| Case | Timeline | Profile | Sources |
|---|---|---|---|
| `s10-preview` | `v08_transition_crossfade` (8.6 s): the integration test's own manifest | Preview, 540x960 | 640x360 |
| `s10-final` | the same | Final with single-pass loudnorm, 1080x1920 | 1080x1920 |
| `s30-preview` | 11 clips of 3 s with 400 ms crossfades (29 s) | Preview | 1080x1920 |
| `s30-final` | the same | Final with single-pass loudnorm | 1080x1920 |

The Final profile measures loudness in a first pass whose numbers go into the second pass's arguments; a manifest's steps
cannot pass values to each other, so the cases use one pass (see the spike note). `client.ts sources` renders the sources
(16 s testsrc2 with a tone) into the bucket first. The client adds two steps that print the MD5 of the decoded video and audio.

## Running it

```sh
# once: a token file outside the repository
umask 077; printf 'GATEWAY_TOKEN=%s\n' "$(openssl rand -hex 32)" > /somewhere/spike.env

tools/cf-spike/push-image.sh deploy@host v4                 # build on the Docker host, push to registry.cloudflare.com
SPIKE_ENV=/somewhere/spike.env tools/cf-spike/deploy.sh v4  # bucket, Worker, DO, container application
export GATEWAY_URL=https://cf-spike-gateway.<subdomain>.workers.dev GATEWAY_TOKEN=...
bun tools/cf-spike/client.ts sources
bun tools/cf-spike/client.ts run s30-final --parallel 4
tools/cf-spike/teardown.sh deploy@host                      # delete every cf-spike resource and list what is left
```

`THREADS=n`, `X264_ASM=0` and `CPUFLAGS=...` change the encode step, for the determinism checks of the spike note.
`INTERNAL=1` signs the job's object URLs for `http://objects.internal`: the gateway then starts the container with
`enableInternet: false` and hands those requests to its `ObjectProxy` entrypoint (`interceptOutboundHttp`).
