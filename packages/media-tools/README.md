# media-tools

The container image for everything that parses untrusted media: ffmpeg 8 and ffprobe (P1-03), later the Python analysis tools
(scene detection, pose, beats, transcription). It runs under the sandbox providers in `apps/server/internal/sandbox`.

```sh
docker build -t media-tools:dev packages/media-tools
cd apps/server
go run ./cmd/cfctl media probe ~/clip.mov                       # docker provider
go run ./cmd/cfctl media probe ~/clip.mov --provider hardened   # docker under gVisor (runsc must be registered)
go run ./cmd/cfctl media probe ~/clip.mov --provider local      # ffprobe on this machine, no isolation, development only
go run ./cmd/cfctl media bench                                   # docker vs hardened timings on a generated 1080p clip
```

## Contract with the sandbox

- No entrypoint; the job's argument list starts with the program (`ffprobe`, `ffmpeg`), run without a shell.
- Runs as a non-root user (default 10001; the docker provider passes the worker's own uid so output files belong to it).
- The root filesystem is read-only. Inputs appear read-only under `/in/<name>`. The only writable places are `/work`, the
  job's output mount, and `/tmp`, a tmpfs. Nothing else exists to write to.
- No network (`--network none`), no capabilities, no credentials: the environment is `HOME` and `TMPDIR` only.
- `/usr/share/fonts/cf` is where caption fonts go (P3-04); fontconfig reads only that directory.

## Contents

- `Dockerfile`: multi-stage; builds ffmpeg from a checksummed tarball, then assembles a `FROM scratch` image from the binaries,
  the exact shared libraries they load and the licence texts. Pins: base image digest, Debian snapshot date, ffmpeg checksum.
- `scripts/assemble-rootfs.sh`: the root filesystem assembly and the inventory.
- `licenses/`: the allowlist (`policy.json`), the check (`check.py`) and its tests. `LICENSES.md` explains the policy and holds the
  generated inventory.

Upgrading ffmpeg or the Debian snapshot: change the ARGs at the top of the Dockerfile together, rebuild, run the licence check,
review any copyright changes it reports, and update `policy.json` and `LICENSES.md`.
