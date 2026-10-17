#!/bin/bash
# Downloads the caption and title fonts of packages/media-tools/fonts from their official repositories at the versions pinned in
# fonts/manifest.tsv, checks every download and every extracted file against its SHA-256, and writes the fonts and their SIL OFL
# licence texts into fonts/.
#
#   fetch-fonts.sh            download (into a cache directory) and install the files that are missing or differ
#   fetch-fonts.sh --check    only verify the files in fonts/ against the manifest (no network); CI runs this
#
# Fonts are OFL-licensed (D-16): the licence text of every family ships next to its font file, and into the image.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
fonts=$(cd "$here/../fonts" && pwd)
manifest="$fonts/manifest.tsv"
mode=${1:-fetch}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

cache=${CF_FONT_CACHE:-${TMPDIR:-/tmp}/cf-font-cache}
mkdir -p "$cache"
failed=0

while IFS=$'\t' read -r kind family version spdx file want url url_sha member; do
  [ "$kind" = kind ] && continue
  dest="$fonts/$file"
  if [ "$spdx" != "OFL-1.1" ]; then
    echo "FAIL $file: $family is $spdx; only OFL-1.1 fonts are allowed (D-16)" >&2
    failed=1
    continue
  fi
  if [ -f "$dest" ] && [ "$(sha256 "$dest")" = "$want" ]; then
    echo "ok   $file ($family $version)"
    continue
  fi
  if [ "$mode" = "--check" ]; then
    echo "FAIL $file: missing or its SHA-256 differs from the manifest" >&2
    failed=1
    continue
  fi
  download="$cache/$url_sha"
  if [ ! -f "$download" ] || [ "$(sha256 "$download")" != "$url_sha" ]; then
    curl -fsSL --retry 3 -o "$download.part" "$url"
    mv "$download.part" "$download"
  fi
  if [ "$(sha256 "$download")" != "$url_sha" ]; then
    echo "FAIL $file: the download of $url does not match $url_sha" >&2
    failed=1
    continue
  fi
  if [ "$member" = "-" ]; then
    cp "$download" "$dest"
  else
    unzip -p "$download" "$member" >"$dest"
  fi
  if [ "$(sha256 "$dest")" != "$want" ]; then
    echo "FAIL $file: extracted file has SHA-256 $(sha256 "$dest"), the manifest says $want" >&2
    rm -f "$dest"
    failed=1
    continue
  fi
  echo "got  $file ($family $version)"
done <"$manifest"

exit "$failed"
