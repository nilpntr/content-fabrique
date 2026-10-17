# syntax=docker/dockerfile:1
#
# media-tools: ffmpeg 8 and ffprobe for the sandbox layer (architecture section 8, D-08, P1-03).
#
# What is in the image: ffmpeg, ffprobe, the exact shared libraries they load, fontconfig's config, the OFL-licensed title and
# caption fonts of fonts/manifest.tsv (P3-04), and the licence texts and inventory of all of it. There is no shell, no coreutils and no package manager, and the user is
# not root. The root filesystem is meant to run read-only: /work (the job's output mount) and /tmp (a tmpfs) are the only
# places a job writes.
#
# The label org.contentfabrique.fonts=1 (last line) says the image holds the fonts: `task dev` rebuilds an image without it, and the
# worker warns about one. Anything that removes the fonts must drop the label.
#
# Reproducibility: the base image is pinned by digest, Debian packages come from a dated snapshot.debian.org archive, and
# the ffmpeg source is checked against a pinned SHA-256. To bump: change the three ARGs below together and rebuild.
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
      build-essential nasm pkg-config ca-certificates curl xz-utils \
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

COPY scripts/assemble-rootfs.sh scripts/smoke-charsets.sh /usr/local/sbin/
COPY fonts.conf /rootfs-src/fonts.conf
COPY fonts/ /rootfs-src/fonts/
RUN set -eux; \
    /usr/local/sbin/assemble-rootfs.sh "${FFMPEG_VERSION}" "${FFMPEG_SHA256}" /src/ffmpeg; \
    cp /rootfs-src/fonts.conf /rootfs/etc/fonts/fonts.conf; \
    chmod 1777 /rootfs/tmp; \
    # Smoke tests inside the build: the binaries must start with only what is in /rootfs, and subtitle text in legacy
    # encodings must convert (this needs the gconv modules).
    chroot /rootfs /usr/local/bin/ffmpeg -hide_banner -version >/dev/null; \
    chroot /rootfs /usr/local/bin/ffprobe -hide_banner -version >/dev/null; \
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
LABEL org.opencontainers.image.title="media-tools" \
      org.opencontainers.image.description="ffmpeg 8 and ffprobe for Content Fabrique sandboxes" \
      org.opencontainers.image.licenses="GPL-2.0-or-later" \
      org.opencontainers.image.source="https://github.com/nilpntr/content-fabrique" \
      org.contentfabrique.fonts="1"
