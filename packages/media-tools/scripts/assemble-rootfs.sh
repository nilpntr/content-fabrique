#!/bin/sh
# Builds /rootfs, the whole content of the media-tools image, from the freshly built ffmpeg: the two binaries, exactly the
# shared libraries they load (found with ldd), the licence text of every package those libraries come from, and an
# inventory of all of it. Nothing else goes in: no shell, no package manager, no coreutils.
set -eu

FFMPEG_VERSION=$1
FFMPEG_SHA256=$2
SRC=$3 # the unpacked ffmpeg source tree
R=/rootfs

mkdir -p "$R/usr/local/bin" "$R/usr/share/media-tools" "$R/usr/share/doc/ffmpeg" "$R/etc/fonts" \
  "$R/usr/share/fonts/cf" "$R/tmp" "$R/work" "$R/in"
cp /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe "$R/usr/local/bin/"

# Every library the binaries need, plus the ELF interpreter (the dynamic loader), by the name the loader asks for.
# (ldd is run per binary: with two arguments it prints a "path:" header line for each.)
: >/tmp/libs.raw
for bin in ffmpeg ffprobe; do
  out=$(ldd "/opt/ffmpeg/bin/$bin")
  if echo "$out" | grep -q 'not found'; then
    echo "unresolved shared library for $bin" >&2
    exit 1
  fi
  echo "$out" | awk '$2 == "=>" && $3 ~ /^\// {print $3} $1 ~ /^\// {print $1}' >>/tmp/libs.raw
done
sort -u /tmp/libs.raw >/tmp/libs.txt
while read -r lib; do
  cp -L --parents "$lib" "$R"
done </tmp/libs.txt

# Which Debian package owns each library (resolved through symlinks, because of the merged /usr).
: >/tmp/pkgs.txt
while read -r lib; do
  real=$(readlink -f "$lib")
  owner=$(dpkg -S "$real" | head -n1 | cut -d: -f1)
  echo "$owner" >>/tmp/pkgs.txt
done </tmp/libs.txt
sort -u /tmp/pkgs.txt -o /tmp/pkgs.txt

inv="$R/usr/share/media-tools/inventory.tsv"
printf 'kind\tname\tversion\tsource\tsha256\n' >"$inv"
while read -r pkg; do
  version=$(dpkg-query -W -f='${Version}' "$pkg")
  src=$(dpkg-query -W -f='${source:Package}' "$pkg")
  mkdir -p "$R/usr/share/doc/$pkg"
  cp -L "/usr/share/doc/$pkg/copyright" "$R/usr/share/doc/$pkg/copyright"
  printf 'deb\t%s\t%s\t%s\t\n' "$pkg" "$version" "$src" >>"$inv"
done </tmp/pkgs.txt

# ffmpeg itself, built from the checksummed tarball with --enable-gpl, so the combined work is GPL-2-or-later (D-16).
cat "$SRC/COPYING.GPLv2" "$SRC/COPYING.LGPLv2.1" >"$R/usr/share/doc/ffmpeg/copyright"
printf 'source\tffmpeg\t%s\thttps://ffmpeg.org/releases/ffmpeg-%s.tar.xz\t%s\n' \
  "$FFMPEG_VERSION" "$FFMPEG_VERSION" "$FFMPEG_SHA256" >>"$inv"
/opt/ffmpeg/bin/ffmpeg -hide_banner -version | sed -n 's/^configuration: //p' >"$R/usr/share/media-tools/ffmpeg-configuration.txt"
