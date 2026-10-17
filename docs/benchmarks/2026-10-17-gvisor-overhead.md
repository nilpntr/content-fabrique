# gVisor (`runsc`) overhead on a 1080p transcode

P1-03 / [D-08](../05-decisions.md#d-08-sandbox-pluggable-interface-default-provider). Question: what does running the `media-tools` jobs under gVisor (the `hardened` provider) cost compared with plain Docker (the `docker` provider)?

**Answer.** On an unthrottled CPU-bound 1080p H.264 transcode there is no measurable difference (-0.8 %, inside the run-to-run noise). A 0.9 GB read-and-write copy through the bind mounts is within 5 % either way. The one cost we measured is start-up: about 30 to 50 ms more per job, +25 to +37 %. For ingest and render jobs that last seconds to minutes that is negligible. We keep `hardened` as the provider for all deterministic media jobs.

## What was run

CI job `gvisor-bench` in `.github/workflows/ci.yml` (manual dispatch, and on pull requests that touch the sandbox, the media wrappers or the image). Run: <https://github.com/nilpntr/content-fabrique/actions/runs/39016125640>, branch `p1-03-media-tools`, 2026-10-17.

| | |
|---|---|
| Runner | GitHub-hosted `ubuntu-latest`: 4 vCPUs, 16 GB RAM, Linux 6.17.0-1022-azure, x86_64 |
| Docker | 28.0.4 |
| gVisor | release 20261012.0, installed from `gvisor.tar.zstd` (SHA-512 checked), registered with `runsc install`; default platform (systrap) |
| Image | `packages/media-tools` (ffmpeg 8.1.3, x264 from Debian trixie), built on the runner |
| Providers | `docker`: `docker run --network none --read-only --cap-drop ALL ...`; `hardened`: the same plus `--runtime runsc` ([internal/sandbox](../../apps/server/internal/sandbox/docker.go)) |
| Tool | `cfctl media bench --providers docker,hardened --runs 3` |

Steps, each three times per provider, timed by the provider itself (`Result.Duration`: from starting `docker run` to its exit, so container creation and teardown are included):

1. **start-up**: `ffprobe -version`. The smallest possible job; measures container and sandbox start.
2. **transcode**: a generated 30 s 1080p30 clip (`testsrc2` + 440 Hz `sine`, H.264 and AAC) re-encoded to H.264 CRF 23 `-preset veryfast` plus AAC, input mounted read-only at `/in/0`, output written to the `/work` bind mount. The output is probed afterwards (1920x1080, H.264, audio, 29 to 31 s), so a fast wrong answer is not a result.
3. **copy**: `ffmpeg -c copy` of 10 s of uncompressed 1080p30 video (0.9 GB) from the read-only mount to `/work`: reading and writing a big file, no encoding.

A provider's three runs of a step run back to back, one provider after the other, on a shared runner, so a noisy neighbour can hit one provider and not the other. The individual runs are shown for that reason.

## Results

Job limits: 2048 MiB memory, PID limit 512, as stated per table for CPUs.

### 4 CPUs per job (all of the runner's vCPUs: nothing is throttled)

| step | provider | runs (s) | median (s) | vs docker |
|---|---|---|---|---|
| start-up (ffprobe -version) | docker | 0.14 / 0.13 / 0.12 | 0.13 | 1.00x |
| start-up (ffprobe -version) | hardened | 0.16 / 0.17 / 0.16 | 0.16 | 1.25x (+24.9 %) |
| transcode 1080p H.264 CRF 23 veryfast | docker | 7.61 / 7.27 / 7.46 | 7.46 | 1.00x |
| transcode 1080p H.264 CRF 23 veryfast | hardened | 7.47 / 7.41 / 7.40 | 7.41 | 0.99x (-0.8 %) |
| copy 0.9 GB (read + write, no encode) | docker | 5.11 / 4.15 / 3.68 | 4.15 | 1.00x |
| copy 0.9 GB (read + write, no encode) | hardened | 4.95 / 3.93 / 3.92 | 3.93 | 0.95x (-5.4 %) |

### 2 CPUs per job (`--cpus 2`, the quota the production defaults use)

| step | provider | runs (s) | median (s) | vs docker |
|---|---|---|---|---|
| start-up (ffprobe -version) | docker | 0.13 / 0.11 / 0.11 | 0.11 | 1.00x |
| start-up (ffprobe -version) | hardened | 0.16 / 0.17 / 0.15 | 0.16 | 1.37x (+37.4 %) |
| transcode 1080p H.264 CRF 23 veryfast | docker | 11.55 / 11.56 / 11.57 | 11.56 | 1.00x |
| transcode 1080p H.264 CRF 23 veryfast | hardened | 9.88 / 10.04 / 10.08 | 10.04 | 0.87x (-13.1 %) |
| copy 0.9 GB (read + write, no encode) | docker | 4.79 / 4.68 / 3.71 | 4.68 | 1.00x |
| copy 0.9 GB (read + write, no encode) | hardened | 4.98 / 4.86 / 3.92 | 4.86 | 1.04x (+3.9 %) |

## How to read it

- **Do not read the 2-CPU transcode as "gVisor is faster".** With `--cpus 2` the docker transcode is bound by the CFS quota: its three runs are within 0.02 s of each other, which is what a fixed amount of CPU time divided by a fixed quota looks like. The hardened run finishes sooner, most likely because the quota is applied to the sandbox's host processes and gVisor's accounting of the guest's CPU time differs from runc's (not investigated further). The honest comparison is the unthrottled 4-CPU table: no difference. Before relying on `--cpus` as a hard CPU limit under `hardened`, check it on the real host with a CPU-bound job.
- **I/O.** Sequential reads and writes of a large file through bind mounts cost nothing visible (within 5 % in both directions, and the runs of one provider vary by more than that: 3.7 to 5.1 s). This is the I/O pattern of our jobs: one input file read once, one output file written once.
- **Not measured.** gVisor's known weak spots are syscall-heavy and small-file-heavy workloads. ffmpeg is neither. Python analysis tools that open thousands of files (P2) should be measured when they exist. A bare-metal worker host (the production target, [D-09](../05-decisions.md#d-09-hosting)) differs from a GitHub VM, and gVisor's platform choice (systrap, KVM) matters there; repeat this benchmark on the first real host.
- **Isolation check.** The same job also ran the sandbox integration tests under runsc (read-only root, read-only input, write only to `/work` and `/tmp`, an odd host path with a comma, quotes and a colon, timeout kills and removes the container, memory limit OOM-kills a 4K encode): all pass under both runtimes.

## Reproduce

```sh
docker build -t media-tools:dev packages/media-tools
cd apps/server
go run ./cmd/cfctl media bench --providers docker,hardened --runs 3 --cpus 4
```

`hardened` needs `runsc` registered with the Docker daemon (`runsc install`, then reload Docker). It does not run on Docker Desktop or OrbStack on a Mac. To rerun in CI: Actions, workflow `ci`, "Run workflow", branch of your choice; the table is in the `gvisor-bench` job summary and the `gvisor-bench` artefact.
