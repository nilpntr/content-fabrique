# syntax=docker/dockerfile:1
#
# The cf-spike job image: the media-tools image (packages/media-tools, built as BASE) with what a Cloudflare Container needs and
# compose gives it from outside: /in writable by the image's user (compose mounts a tmpfs there), and `cf-job serve` as the
# entrypoint (compose passes it as the command). Nothing else changes: same ffmpeg, fonts, chrome and cf-job binaries.
#
#   docker build --platform linux/amd64 -t cf-spike-media-base:v1 packages/media-tools
#   docker build --platform linux/amd64 --build-arg BASE=cf-spike-media-base:v1 -t cf-spike-media:v1 tools/cf-spike/container
#
# Cloudflare Containers run linux/amd64 only.

ARG BASE=cf-spike-media-base:v1
ARG DEBIAN_IMAGE=debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

FROM ${DEBIAN_IMAGE} AS dirs
RUN mkdir -p /d/in /d/work && chmod 1777 /d/in /d/work
# The CA bundle: the media-tools image has none (compose talks plain http to MinIO), so cf-job cannot verify any HTTPS URL, and
# R2 presigned URLs are HTTPS (spike run 4: x509 unknown authority on the first upload).
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && mkdir -p /c/etc/ssl/certs \
    && cp /etc/ssl/certs/ca-certificates.crt /c/etc/ssl/certs/ca-certificates.crt

FROM ${BASE}
# Copying the directory entries themselves (the contents of /d) replaces the metadata of the image's /in and /work: the base
# image's `COPY ... /rootfs/work /work` leaves /work root-owned and 755, which nobody notices under compose (a tmpfs is mounted
# there) but which makes every step fail with EACCES on Cloudflare, where nothing is mounted (spike runs 1 to 3).
COPY --from=dirs --chown=10001:10001 /d/ /
COPY --from=dirs /c/ /
# A standard-4 instance: 4 vCPU and 12 GiB. The job may ask for up to 11 GiB; the rest is the runner, page cache and /tmp.
ENV CF_JOB_CPUS=4 \
    CF_JOB_MEMORY_MB=11264
EXPOSE 8080
ENTRYPOINT ["cf-job", "serve", "-addr", ":8080"]
