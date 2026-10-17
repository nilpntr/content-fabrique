# syntax=docker/dockerfile:1
#
# media-tools: ffmpeg 8 and ffprobe for the sandbox layer (architecture section 8, D-08, P1-03).
#
# What is in the image: ffmpeg, ffprobe, the exact shared libraries they load, fontconfig's config, the OFL-licensed title and
# caption fonts of fonts/manifest.tsv (P3-04), and the licence texts and inventory of all of it. There is no shell, no coreutils and no package manager, and the user is
# not root. The root filesystem is meant to run read-only: /work (the job's output mount) and /tmp (a tmpfs) are the only
# places a job writes.
#
# Two labels (last lines): org.contentfabrique.fonts=1 says the image holds the fonts (the worker warns about one without it;
# anything that removes the fonts must drop it), and org.contentfabrique.src-hash is the git tree hash of this directory at the
# time of the build (scripts/media-image.sh passes it as SRC_HASH), which `task dev` compares with the tree it has now to decide
# whether the local image is stale. The ARG sits right before the label, so a new hash changes the last layer only.
#
# Reproducibility: the base image is pinned by digest, Debian packages come from a dated snapshot.debian.org archive, and
# the ffmpeg and whisper.cpp sources are checked against pinned SHA-256 values. To bump: change the ARGs below together and rebuild.
#
# Transcription (P2-05, D-27): whisper-cli and whisper-vad-speech-segments of whisper.cpp (MIT), built for CPU, statically
# linked against ggml, so the only shared libraries they add are the C and C++ runtimes the image has anyway. The models are NOT
# in the image: the worker downloads them once into a cache directory, checks their SHA-256 and mounts them read-only.
#
# Licensing (D-16 as amended): ffmpeg is built --enable-gpl (libx264), so the binaries are GPL-2-or-later. They run only as
# separate processes inside our own containers and are never linked into our code or distributed. LICENSES.md and
# licenses/policy.json list every component; CI fails on anything outside that allowlist.

ARG DEBIAN_IMAGE=debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

FROM ${DEBIAN_IMAGE} AS build
# The package archive date (UTC). A newer date means newer library versions: update licenses/policy.json and LICENSES.md with it.
ARG DEBIAN_SNAPSHOT=20261016T000000Z
# ffmpeg 8.x release tarball and its SHA-256 (computed from the download and checked against the detached signature
# https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz.asc on 2026-10-17).
ARG FFMPEG_VERSION=8.1.3
ARG FFMPEG_SHA256=7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3
# whisper.cpp release tag and the SHA-256 of its GitHub source tarball (computed from the download on 2026-10-17).
ARG WHISPER_VERSION=1.9.5
ARG WHISPER_SHA256=ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720

# Both archives are signed (apt checks the archive keyring), so plain http is fine and needs no CA bundle yet.
RUN set -eux; \
    rm -f /etc/apt/sources.list.d/*; \
    printf '%s\n' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}/" \
      'Suites: trixie trixie-updates' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' \
      '' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}/" \
      'Suites: trixie-security' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' \
      > /etc/apt/sources.list.d/debian.sources; \
    printf 'Acquire::Check-Valid-Until "false";\nAPT::Install-Recommends "false";\n' > /etc/apt/apt.conf.d/99snapshot; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      build-essential nasm pkg-config ca-certificates curl xz-utils cmake \
      libx264-dev libass-dev libdav1d-dev libzimg-dev \
      libfreetype-dev libfontconfig-dev libharfbuzz-dev zlib1g-dev

WORKDIR /src
# ffmpeg:
#   --enable-gpl              libx264 (GPL-2+) is the H.264 encoder (D-16 amendment).
#   --disable-autodetect      only the libraries named here are linked, whatever else the build image holds.
#   --disable-network         no network protocols at all, on top of the sandbox having no network.
#   --disable-ffplay/doc/debug  not needed.
#   libass + freetype + fontconfig + harfbuzz   captions and subtitles (P3-04); libzimg   colour conversion and HDR to SDR
#   tone mapping (P1-04); libdav1d   AV1 decoding.
RUN set -eux; \
    curl -fsSL --retry 3 -o ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz"; \
    echo "${FFMPEG_SHA256}  ffmpeg.tar.xz" | sha256sum -c -; \
    mkdir ffmpeg && tar -xJf ffmpeg.tar.xz -C ffmpeg --strip-components=1; \
    cd ffmpeg; \
    ./configure \
      --prefix=/opt/ffmpeg \
      --enable-gpl \
      --disable-debug --disable-doc --disable-ffplay --disable-network --disable-autodetect \
      --enable-zlib --enable-iconv \
      --enable-libx264 --enable-libdav1d --enable-libzimg \
      --enable-libass --enable-libfreetype --enable-libfontconfig --enable-libharfbuzz \
      --extra-cflags='-O2 -fstack-protector-strong -D_FORTIFY_SOURCE=2' \
      --extra-ldflags='-Wl,-z,relro -Wl,-z,now'; \
    make -j"$(nproc)"; \
    make install

# whisper.cpp, CPU only and portable:
#   BUILD_SHARED_LIBS=OFF       ggml and whisper are linked into the two binaries: no libwhisper.so, no plugin directory.
#   GGML_OPENMP=OFF             ggml's own thread pool is used, so libgomp is not needed.
#   GGML_NATIVE=OFF             never tune for the build machine's CPU: the image runs on whatever host the worker has.
#   amd64: AVX2, FMA and F16C   on explicitly (any x86-64 server since 2013 has them); arm64 uses the compiler's baseline NEON.
#   No BLAS, no GPU backends    OpenBLAS would be one more shared library for a small gain on the quantised models.
#   WHISPER_BUILD_TESTS/SERVER  off; only the CLI and the VAD tool are built.
RUN set -eux; \
    curl -fsSL --retry 3 -o whisper.tar.gz "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v${WHISPER_VERSION}.tar.gz"; \
    echo "${WHISPER_SHA256}  whisper.tar.gz" | sha256sum -c -; \
    mkdir whisper && tar -xzf whisper.tar.gz -C whisper --strip-components=1; \
    cd whisper; \
    case "$(dpkg --print-architecture)" in \
      amd64) arch_flags='-DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON -DGGML_AVX512=OFF' ;; \
      *)     arch_flags='' ;; \
    esac; \
    cmake -S . -B build -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/whisper \
      -DBUILD_SHARED_LIBS=OFF -DGGML_OPENMP=OFF -DGGML_NATIVE=OFF -DGGML_BLAS=OFF -DGGML_CUDA=OFF -DGGML_VULKAN=OFF -DGGML_METAL=OFF \
      -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_SDL2=OFF -DWHISPER_FFMPEG=OFF \
      -DCMAKE_C_FLAGS='-O2 -fstack-protector-strong' -DCMAKE_CXX_FLAGS='-O2 -fstack-protector-strong' \
      -DCMAKE_EXE_LINKER_FLAGS='-Wl,-z,relro -Wl,-z,now' $arch_flags; \
    cmake --build build --config Release -j"$(nproc)" --target whisper-cli whisper-vad-speech-segments; \
    mkdir -p /opt/whisper/bin; \
    cp build/bin/whisper-cli build/bin/whisper-vad-speech-segments /opt/whisper/bin/

COPY scripts/assemble-rootfs.sh scripts/smoke-charsets.sh /usr/local/sbin/
COPY fonts.conf /rootfs-src/fonts.conf
COPY fonts/ /rootfs-src/fonts/
RUN set -eux; \
    /usr/local/sbin/assemble-rootfs.sh "${FFMPEG_VERSION}" "${FFMPEG_SHA256}" /src/ffmpeg "${WHISPER_VERSION}" "${WHISPER_SHA256}" /src/whisper; \
    cp /rootfs-src/fonts.conf /rootfs/etc/fonts/fonts.conf; \
    chmod 1777 /rootfs/tmp; \
    # Smoke tests inside the build: the binaries must start with only what is in /rootfs, and subtitle text in legacy
    # encodings must convert (this needs the gconv modules).
    chroot /rootfs /usr/local/bin/ffmpeg -hide_banner -version >/dev/null; \
    chroot /rootfs /usr/local/bin/ffprobe -hide_banner -version >/dev/null; \
    chroot /rootfs /usr/local/bin/whisper-cli --help >/dev/null 2>&1; \
    chroot /rootfs /usr/local/bin/whisper-vad-speech-segments --help >/dev/null 2>&1; \
    /usr/local/sbin/smoke-charsets.sh /rootfs/tmp/smoke /tmp/smoke chroot /rootfs /usr/local/bin/ffmpeg; \
    rm -rf /rootfs/tmp/smoke

FROM scratch
COPY --from=build /rootfs/ /
# The writable mount point. 10001 is the default user; the docker provider overrides --user with the worker's own uid.
COPY --from=build --chown=10001:10001 /rootfs/work /work
ENV PATH=/usr/local/bin \
    HOME=/tmp \
    TMPDIR=/tmp
USER 10001:10001
WORKDIR /work
ARG SRC_HASH=unknown
LABEL org.opencontainers.image.title="media-tools" \
      org.opencontainers.image.description="ffmpeg 8, ffprobe and whisper.cpp for Content Fabrique sandboxes" \
      org.opencontainers.image.licenses="GPL-2.0-or-later" \
      org.opencontainers.image.source="https://github.com/nilpntr/content-fabrique" \
      org.contentfabrique.fonts="1" \
      org.contentfabrique.src-hash="${SRC_HASH}"
