# media-tools: licence inventory

The `media-tools` image runs ffmpeg and ffprobe on untrusted user files inside the sandbox layer. This file says what is in
it and why that is allowed under [D-16](../../docs/05-decisions.md#d-16-licensing-policy-for-ml-and-media-components).

## Policy in one paragraph

D-16 (as amended 2026-10-17): permissive licences ship freely. GPL and LGPL command-line tools are allowed **only as separate
processes inside our own containers**: ffmpeg and ffprobe built with `--enable-gpl`, x264 and libass. We never link them into
our Go or web code and never ship them to customers (a SaaS does not distribute them, so copyleft obligations are not
triggered). AGPL, SSPL, non-commercial and non-free licences are banned, and so is copyleft linked into our own binaries.
If we ever distribute the image or a binary (on-premise, desktop), this inventory must be reviewed again.

## How the check works

`licenses/check.py` runs in CI (job `media-tools-licences`) against the built image. It reads the inventory the build writes
into the image (`/usr/share/media-tools/inventory.tsv`) and each component's copyright file, and compares them with the
allowlist `licenses/policy.json`. It fails when:

- a component is in the image but not in the allowlist;
- a component's copyright file differs from the one a person reviewed (SHA-256), which catches new or changed licences,
  including in packages whose copyright file is prose;
- a licence token is banned (AGPL, SSPL, non-commercial, non-free, ...). Banned licences cannot be allowlisted;
- an entry is inconsistent: "permissive" with a licence off the permissive list, or a copyleft exception without a reason;
- ffmpeg was configured with an `--enable-*` option the allowlist does not name (for example `--enable-nonfree`,
  `--enable-libfdk-aac`, `--enable-libx265`).

The generated tables below are checked for drift in CI. To refresh them after a change to the image:

```sh
docker build -t media-tools:dev packages/media-tools
python3 packages/media-tools/licenses/check.py --image media-tools:dev --write-md packages/media-tools/LICENSES.md
```

To review a version bump, `--print-shas` prints each component's current copyright SHA-256; read the licence diff, then update
`copyrightSha256` in `policy.json` in the same PR.

## Pinned sources

| What | Pinned by |
|---|---|
| Base image | `debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f` |
| Debian packages | snapshot.debian.org archive `20261016T000000Z` (`DEBIAN_SNAPSHOT` in the Dockerfile) |
| ffmpeg | 8.1.3 release tarball, SHA-256 `7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3` |
| whisper.cpp | v1.9.5 source tarball (GitHub), SHA-256 `ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720` (`WHISPER_VERSION`, `WHISPER_SHA256` in the Dockerfile) |

## Notes for reviewers

- The final image is `FROM scratch`: ffmpeg, ffprobe, the shared libraries they load, fontconfig's config and the title and
  caption fonts (below). No shell, no coreutils, no package manager.
- whisper.cpp (P2-05, MIT) provides `whisper-cli` and `whisper-vad-speech-segments`, built for CPU from the checksummed tarball with
  ggml linked statically, so the only shared libraries they add are the C and C++ runtimes already listed. Its licence text is in the
  image (`/usr/share/doc/whisper.cpp/copyright`). The model files are not in the image: the worker downloads them on first use into
  a cache directory and checks a pinned SHA-256 (`apps/server/internal/transcribe/models.go`): Whisper large-v3-turbo quantised
  and tiny.en (OpenAI Whisper weights as converted by the whisper.cpp project, MIT) and the Silero VAD v6.2.0 voice-activity model
  (MIT). A model is added with its licence named here.
- libzimg (WTFPL) is included for HDR to SDR tone mapping in P1-04. WTFPL is permissive but is not one of the four names in
  D-16, so it is listed explicitly.
- Fonts (P3-04): `/usr/share/fonts/cf` holds three OFL-1.1 fonts, Anton, Inter and Montserrat, each with its licence text in the
  image (`/usr/share/doc/font-<family>/copyright`) and a row in the inventory. The fonts are vendored under
  `packages/media-tools/fonts` with their licence texts and `manifest.tsv` (version, source URL and SHA-256 of each file);
  `scripts/fetch-fonts.sh` downloads them from the official repositories at the pinned versions and checks every hash, and
  `fetch-fonts.sh --check` (CI) verifies the committed files. Only OFL-1.1 fonts are allowed (D-16); the spec cannot name a font,
  the style registry (`apps/server/internal/timeline/style`) picks one per style. The fonts are embedded unmodified, so the OFL
  reserved font names stay with their authors. Adding a font means a manifest row, its licence text and an allowlist entry.
- The sandbox, not the licence, is what contains ffmpeg's attack surface: see `apps/server/internal/sandbox`.

## Generated inventory

<!-- BEGIN GENERATED INVENTORY -->
<!-- Generated by licenses/check.py from the built image. Do not edit by hand. -->

### Components in the image

| Component | Version | Licence of what ships | Status | Licence tokens in its copyright file |
|---|---|---|---|---|
| chrome-headless-shell | 155.0.8059.84 | BSD-3-clause AND LGPL-2.1-or-later (bundled components) | named exception | GNU LGPL license., header required., MIT |
| ffmpeg | 8.1.3 | GPL-2.0-or-later | named exception | prose only |
| font-anton | google/fonts@cee74d7ca45d | OFL-1.1 | permissive | prose only |
| font-inter | 4.1 | OFL-1.1 | permissive | prose only |
| font-montserrat | 7.222 | OFL-1.1 | permissive | prose only |
| golang-runtime | 1.27.2 | BSD-3-clause | permissive | prose only |
| libasound2t64 | 1.2.14-1+deb13u1 | LGPL-2.1-or-later | named exception | LGPL-2.1+ |
| libass9 | 1:0.17.3-1+deb13u1 | ISC | permissive | BSD-2-clause, GPL-2+, ISC, NTP, Unlicense |
| libatk-bridge2.0-0t64 | 2.56.2-1+deb13u2 | LGPL-2.1-or-later | named exception | AFL-2.1, GPL-2, GPL-2+, LGPL-2+, LGPL-2.1+, public-domain |
| libatk1.0-0t64 | 2.56.2-1+deb13u2 | LGPL-2.1-or-later | named exception | AFL-2.1, GPL-2, GPL-2+, LGPL-2+, LGPL-2.1+, public-domain |
| libatomic1 | 14.2.0-19 | GPL-3.0-or-later WITH GCC-exception-3.1 | named exception | prose only |
| libatspi2.0-0t64 | 2.56.2-1+deb13u2 | LGPL-2.1-or-later | named exception | AFL-2.1, GPL-2, GPL-2+, LGPL-2+, LGPL-2.1+, public-domain |
| libblkid1 | 2.41.5-0+deb13u1 | LGPL-2.1-or-later | named exception | BSD-2-clause, BSD-3-clause, BSD-4-clause, BSLA, Expat, GPL-2, GPL-2+, GPL-3+, ISC, LGPL, LGPL-2+, LGPL-2.1+, LGPL-3+, public-domain |
| libbrotli1 | 1.1.0-2+b7 | MIT | permissive | MIT |
| libbz2-1.0 | 1.0.8-6 | BSD-variant | permissive | BSD-variant, GPL-2 |
| libc6 | 2.41-12+deb13u4 | LGPL-2.1-or-later | named exception | BSD-2-clause, BSD-3-clause-Berkeley, BSD-3-clause-Carnegie, BSD-3-clause-Oracle, BSD-3-clause-WIDE, BSD-like-Spencer, BSL-1.0, Carnegie, CORE-MATH, DEC, FSFAP, GPL-2, GPL-2+, GPL-2+-with-link-exception, GPL-3+, IBM, Inner-Net, ISC, LGPL-2+, LGPL-2.1+, LGPL-2.1+-with-link-exception, LGPL-3+, MIT-like-Lord, PCRE, public-domain, SunPro, Unicode-DFS-2016, Univ-Coimbra |
| libcap2 | 1:2.75-10+deb13u1+b3 | BSD-3-clause | permissive | BSD-3-clause, GPL-2, GPL-2+ |
| libdav1d7 | 1.5.1-1 | BSD-2-clause | permissive | BSD-2-clause, ISC, public-domain |
| libdbus-1-3 | 1.16.2-2 | AFL-2.1 OR GPL-2.0-or-later | named exception | AFL-2.1, BSD-3-clause, BSD-3-clause-generic, Expat, g10-permissive, GPL-2+, LGPL-2.1+, Tcl-BSDish |
| libdrm2 | 2.4.124-2 | MIT | permissive | prose only |
| libexpat1 | 2.8.3-1~deb13u1 | MIT | permissive | MIT |
| libffi8 | 3.4.8-2 | MIT | permissive | Expat, GPL, GPL-2+, GPL-3+, LGPL-2.1+, MPL-1.1, public-domain, X11 |
| libfontconfig1 | 2.15.0-2.3 | HPND-sell-variant | permissive | prose only |
| libfreetype6 | 2.13.3+dfsg-1+deb13u1 | FTL | permissive | BSD-3-Clause, BSL-1.0, Expat, FSFAP, FTL, GPL-2+, GPL-3+, MIT-Modern-Variant, MIT-SMC, OpenGroup-MIT, Public-Domain, Zlib |
| libfribidi0 | 1.0.16-1 | LGPL-2.1-or-later | named exception | LGPL-2.1+ |
| libgbm1 | 25.0.7-2+deb13u1 | MIT | permissive | Apache-2.0, BSD-2-clause, BSD-3-google, BSL, GPL, GPL-1+, GPL-2, Khronos, MIT, MLAA, SGI |
| libgcc-s1 | 14.2.0-19 | GPL-3.0-or-later WITH GCC-exception-3.1 | named exception | prose only |
| libglib2.0-0t64 | 2.84.4-3~deb13u5 | LGPL-2.1-or-later | named exception | AFL-2.0, Apache-2.0, bzip2-1.0.6, CC-BY-SA-3.0, CC0-1.0, cmph, Expat, FSFULLR, GPL-2+, Iconv-PD, Janik-permissive, Kuchling-PD, LGPL-2+, LGPL-2.1+, LGPL-3+, LLVM exception, Mingw-PD, old-glib-tests, Plumb-PD, Unicode-DFS-2016 |
| libgraphite2-3 | 1.3.14-2+deb13u1 | LGPL-2.1-or-later | named exception | Artistic, custom-sil-open-font-license, GPL-1+, GPL-2+, LGPL-2.1+, MPL-1.1, public-domain |
| libharfbuzz0b | 10.2.0-1+deb13u1 | MIT | permissive | Apache-2.0, AutoConf exception, CC0-1.0, Expat, Font exception, FSFAP, FSFUL, FSFULLR, GPL-2+, GPL-3+, ISC, LGPL-2.1+, LibTool exception, MIT, Monotype, OFL-1.1, UFL-1.0, Unicode |
| libmount1 | 2.41.5-0+deb13u1 | LGPL-2.1-or-later | named exception | BSD-2-clause, BSD-3-clause, BSD-4-clause, BSLA, Expat, GPL-2, GPL-2+, GPL-3+, ISC, LGPL, LGPL-2+, LGPL-2.1+, LGPL-3+, public-domain |
| libnspr4 | 2:4.36-1 | MPL-2.0 | named exception | MPL-2.0 |
| libnss3 | 2:3.110-1+deb13u4 | MPL-2.0 | named exception | BSD-3, MPL-2.0, public-domain, Zlib |
| libpcre2-8-0 | 10.46-1~deb13u3 | BSD-3-clause | permissive | BINARY LIBRARY-LIKE PACKAGES exception, BSD-2-clause, BSD-3-clause, BSD-3-clause-Cambridge, public-domain, X11 |
| libpng16-16t64 | 1.6.48-1+deb13u6 | libpng | permissive | Apache-2.0, BSD-3-clause, BSD-like-with-advertising-clause, expat, GPL-2+, libpng |
| libselinux1 | 3.8.1-1 | public-domain | permissive | GPL-2, public-domain |
| libstdc++6 | 14.2.0-19 | GPL-3.0-or-later WITH GCC-exception-3.1 | named exception | prose only |
| libsystemd0 | 257.13-1~deb13u1 | LGPL-2.1-or-later | named exception | CC0-1.0, Expat, GPL-2, GPL-2+, LGPL-2.1+, Linux-syscall-note exception, public-domain |
| libudev1 | 257.13-1~deb13u1 | LGPL-2.1-or-later | named exception | CC0-1.0, Expat, GPL-2, GPL-2+, LGPL-2.1+, Linux-syscall-note exception, public-domain |
| libunibreak6 | 6.1-3 | Zlib | permissive | unicode-data, Zlib |
| libx11-6 | 2:1.8.12-1 | MIT | permissive | prose only |
| libx264-164 | 2:0.164.3108+git31e19f9-2+b1 | GPL-2.0-or-later | named exception | BSD-3-clause, Expat, GPL-2+, ISC, LGPL-2.1+, other exception, public-domain |
| libxau6 | 1:1.0.11-1 | MIT | permissive | prose only |
| libxcb1 | 1.17.0-2+b1 | MIT | permissive | prose only |
| libxcomposite1 | 1:0.4.6-1 | MIT | permissive | prose only |
| libxdamage1 | 1:1.1.6-1+b2 | MIT | permissive | prose only |
| libxdmcp6 | 1:1.1.5-1 | MIT | permissive | prose only |
| libxext6 | 2:1.3.4-1+b3 | MIT | permissive | prose only |
| libxfixes3 | 1:6.0.0-2+b4 | MIT | permissive | prose only |
| libxi6 | 2:1.8.2-1 | MIT | permissive | prose only |
| libxkbcommon0 | 1.7.0-2 | MIT | permissive | prose only |
| libxrandr2 | 2:1.5.4-1+b3 | MIT | permissive | prose only |
| libxrender1 | 1:0.9.12-1 | MIT | permissive | prose only |
| libzimg2 | 3.0.5+ds1-1+b2 | WTFPL | permissive | AutoConf exception, GPL-3+, WTFPL-2 |
| whisper.cpp | 1.9.5 | MIT | permissive | prose only |
| zlib1g | 1:1.3.dfsg+really1.3.1-1+b1 | Zlib | permissive | Zlib |

### Named copyleft exceptions

- **chrome-headless-shell** (BSD-3-clause AND LGPL-2.1-or-later (bundled components)): chrome-headless-shell from Chrome for Testing: Chromium (BSD-3-Clause) with the third-party components built into it, some of them LGPL (its LICENSE file lists every one). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed. Pinned by version and by the SHA-256 of each architecture's archive.
- **ffmpeg** (GPL-2.0-or-later): Built --enable-gpl because libx264 is GPL. Runs as a separate process in our containers; never linked into our code and never distributed (D-16 amendment). Source: ffmpeg.org release tarball checked against sourceSha256.
- **libasound2t64** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libatk-bridge2.0-0t64** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libatk1.0-0t64** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libatomic1** (GPL-3.0-or-later WITH GCC-exception-3.1): GCC runtime library, linked by the allowed tools. The GCC Runtime Library Exception lets programs link it without becoming GPL. Part of the container's OS layer, not our code.
- **libatspi2.0-0t64** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libblkid1** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libc6** (LGPL-2.1-or-later): glibc, the C library the allowed tools run on. Dynamically linked, never linked into our code.
- **libdbus-1-3** (AFL-2.1 OR GPL-2.0-or-later): D-Bus client library, dual-licensed; used under the Academic Free License 2.1 (permissive, not on the short list). Loaded by chrome-headless-shell. Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libfribidi0** (LGPL-2.1-or-later): Bidi text layout, a dependency of libass. Dynamically linked into the ffmpeg process only.
- **libgcc-s1** (GPL-3.0-or-later WITH GCC-exception-3.1): GCC runtime library, linked by the allowed tools (see libatomic1).
- **libglib2.0-0t64** (LGPL-2.1-or-later): GLib, a dependency of Debian's libharfbuzz. Dynamically linked into the ffmpeg process only.
- **libgraphite2-3** (LGPL-2.1-or-later): Graphite font shaping, a dependency of Debian's libharfbuzz (tri-licensed LGPL-2.1+/GPL-2+/MPL-1.1; used under the LGPL). Dynamically linked into the ffmpeg process only.
- **libmount1** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libnspr4** (MPL-2.0): Mozilla NSPR/NSS, file-level weak copyleft (MPL-2.0), unmodified and dynamically loaded by chrome-headless-shell. Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libnss3** (MPL-2.0): Mozilla NSPR/NSS, file-level weak copyleft (MPL-2.0), unmodified and dynamically loaded by chrome-headless-shell. Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libstdc++6** (GPL-3.0-or-later WITH GCC-exception-3.1): GCC C++ runtime, needed by libzimg and libharfbuzz inside the ffmpeg process (see libatomic1).
- **libsystemd0** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libudev1** (LGPL-2.1-or-later): A shared library chrome-headless-shell loads (directly or through GLib). Chrome and its libraries run as a separate process inside the media sandbox (the motion-graphics renderer, D-35); never linked into our code and never distributed.
- **libx264-164** (GPL-2.0-or-later): x264, the H.264 encoder (D-16 amendment names it). Loaded only by the ffmpeg process in the container.

### ffmpeg configuration

```
--prefix=/opt/ffmpeg --enable-gpl --disable-debug --disable-doc --disable-ffplay --disable-network --disable-autodetect --enable-zlib --enable-iconv --enable-libx264 --enable-libdav1d --enable-libzimg --enable-libass --enable-libfreetype --enable-libfontconfig --enable-libharfbuzz --extra-cflags='-O2 -fstack-protector-strong -D_FORTIFY_SOURCE=2' --extra-ldflags='-Wl,-z,relro -Wl,-z,now'
```
<!-- END GENERATED INVENTORY -->
