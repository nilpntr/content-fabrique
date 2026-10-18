# media-tools

The container image for everything that parses untrusted media: ffmpeg 8 and ffprobe (P1-03), whisper.cpp for transcription
(P2-05), later the Python analysis tools (scene detection, pose, beats). It runs under the sandbox providers in `apps/server/internal/sandbox`.

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
- `/usr/share/fonts/cf` holds the title and caption fonts (P3-04: Anton, Inter, Montserrat, all SIL OFL 1.1, from
  `fonts/manifest.tsv`); fontconfig reads only that directory. A render stages the same files next to its `.ass` file and points
  the `ass` filter at them, so the output does not depend on which fonts a provider has installed.

## Transcription (P2-05, D-27)

`whisper-cli` and `whisper-vad-speech-segments` (whisper.cpp v1.9.5, MIT, CPU only, ggml linked statically; amd64 builds with
AVX2/FMA/F16C, arm64 with the compiler's NEON baseline) run in the same sandbox as ffmpeg. The image holds no models. They sit
read-only at `/in/0` (and the audio at `/in/1`), downloaded by the worker into `WHISPER_MODELS_DIR` and checked against a pinned
SHA-256 first: `large-v3-turbo-q5_0` (574 MB, the default), `tiny.en` (78 MB, CI) and the Silero voice-activity model (1 MB).
`cfctl transcribe models` fetches them ahead of time; `cfctl transcribe file clip.wav` runs the pipeline's steps on a file;
`scripts/transcribe-eval` measures the word timing error.

## Motion graphics (D-35)

`cf-motion` (`motion-runner/`, Go standard library only, built statically in the `runner` stage) renders the motion-graphics overlays
of a render with `chrome-headless-shell` (Chrome for Testing 155.0.8059.84, `/opt/chrome`, pinned by version and by the SHA-256 of each
architecture's archive; locales other than en-US, hyphenation data and SwiftShader are left out). The worker mounts the template
bundle read-only at `/in/0` and writes the job into the work directory:

```sh
cf-motion --bundle /in/0 --job /work/motion-job.json --out /work/motion   # Chrome and fonts default to the image's own
```

The runner speaks the DevTools protocol over a pipe (no port), refuses any request outside the bundle, loads the fonts of
`/usr/share/fonts/cf` as bytes, and writes transparent PNGs and a `manifest.json` (every frame's SHA-256). The design and the security
model are in [motion graphics v1](../../docs/design/motion-graphics-v1.md). The image carries the label `org.contentfabrique.motion=1`.

## Contents

- `Dockerfile`: multi-stage; builds ffmpeg and whisper.cpp from checksummed tarballs, then assembles a `FROM scratch` image from the binaries,
  the exact shared libraries they load and the licence texts. Pins: base image digest, Debian snapshot date, ffmpeg and whisper.cpp checksums.
- `scripts/assemble-rootfs.sh`: the root filesystem assembly and the inventory. `scripts/smoke-charsets.sh`: the charset test.
- `licenses/`: the allowlist (`policy.json`), the check (`check.py`) and its tests. `LICENSES.md` explains the policy and holds the
  generated inventory.

Upgrading ffmpeg or the Debian snapshot: change the ARGs at the top of the Dockerfile together, rebuild, run the licence check,
review any copyright changes it reports, and update `policy.json` and `LICENSES.md`.
