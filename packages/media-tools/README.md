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
- The container user must be able to read the input files: it is the worker's own uid (so a 0600 file the worker staged is
  fine), or nobody (65534) when the worker is root, in which case staged inputs need a readable mode (0644). The provider
  refuses a job whose mounts the user cannot access with `ErrInputUnreadable` instead of letting the tool fail.
- Docker's own failures (daemon down, missing image or runtime: exit 125, 126, 127) come back as `sandbox.ErrSandbox`, never
  as a tool exit code.
- Inputs from users are opened with `-protocol_whitelist file -format_whitelist <media.UntrustedFormats>`; playlists and other
  demuxers that open further files are refused.
- Subtitle text in legacy encodings converts to UTF-8 through glibc's iconv: the image ships the gconv modules for UTF-16/32,
  the Latin, Cyrillic, Greek, Hebrew, Arabic, Turkish and Thai code pages, Shift_JIS, EUC-JP, EUC-KR, GBK, GB18030 and Big5
  (`scripts/smoke-charsets.sh` runs in the Dockerfile and in CI).
- `/usr/share/fonts/cf` is where caption fonts go (P3-04); fontconfig reads only that directory.

## Contents

- `Dockerfile`: multi-stage; builds ffmpeg from a checksummed tarball, then assembles a `FROM scratch` image from the binaries,
  the exact shared libraries they load and the licence texts. Pins: base image digest, Debian snapshot date, ffmpeg checksum.
- `scripts/assemble-rootfs.sh`: the root filesystem assembly and the inventory. `scripts/smoke-charsets.sh`: the charset test.
- `licenses/`: the allowlist (`policy.json`), the check (`check.py`) and its tests. `LICENSES.md` explains the policy and holds the
  generated inventory.

Upgrading ffmpeg or the Debian snapshot: change the ARGs at the top of the Dockerfile together, rebuild, run the licence check,
review any copyright changes it reports, and update `policy.json` and `LICENSES.md`.
