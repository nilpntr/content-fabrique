#!/bin/bash
# Builds /rootfs, the whole content of the media-tools image, from the freshly built ffmpeg: the two binaries, exactly the
# shared libraries they load (found with ldd), the licence text of every package those libraries come from, and an
# inventory of all of it. Nothing else goes in: no shell, no package manager, no coreutils.
set -euo pipefail

FFMPEG_VERSION=$1
FFMPEG_SHA256=$2
SRC=$3 # the unpacked ffmpeg source tree
WHISPER_VERSION=$4
WHISPER_SHA256=$5
WSRC=$6 # the unpacked whisper.cpp source tree
R=/rootfs

mkdir -p "$R/usr/local/bin" "$R/usr/share/media-tools" "$R/usr/share/doc/ffmpeg" "$R/usr/share/doc/whisper.cpp" "$R/etc/fonts" \
  "$R/usr/share/fonts/cf" "$R/tmp" "$R/work" "$R/in"
cp /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe "$R/usr/local/bin/"
cp /opt/whisper/bin/whisper-cli /opt/whisper/bin/whisper-vad-speech-segments "$R/usr/local/bin/"

# Every library the binaries need, plus the ELF interpreter (the dynamic loader), by the name the loader asks for.
# (ldd is run per binary: with two arguments it prints a "path:" header line for each.)
: >/tmp/libs.raw
for bin in /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /opt/whisper/bin/whisper-cli /opt/whisper/bin/whisper-vad-speech-segments; do
  out=$(ldd "$bin")
  if echo "$out" | grep -q 'not found'; then
    echo "unresolved shared library for $bin" >&2
    exit 1
  fi
  echo "$out" | awk '$2 == "=>" && $3 ~ /^\// {print $3} $1 ~ /^\// {print $1}' >>/tmp/libs.raw
done
sort -u /tmp/libs.raw >/tmp/libs.txt
if [ ! -s /tmp/libs.txt ]; then
  echo "no shared libraries found for ffmpeg: ldd output not understood" >&2
  exit 1
fi
while read -r lib; do
  cp -L --parents "$lib" "$R"
done </tmp/libs.txt

# glibc's iconv loads one shared module per character set at run time (dlopen), so ldd does not see them. ffmpeg and libass
# convert subtitle text from other encodings to UTF-8 through iconv (-sub_charenc). Ship the sets creators' subtitle files are
# likely to use: Unicode (UTF-16/32), Western and Central European and Cyrillic code pages, Greek, Hebrew, Arabic, Turkish,
# Thai, Japanese (Shift_JIS, EUC-JP), Korean (EUC-KR), Chinese (GBK, GB18030, Big5), with the table libraries the CJK modules
# load (libJIS and friends). Their package, libc6, is in the inventory
# already. ISO-8859-1, ASCII and UTF-8 are built into glibc.
gconv=$(dpkg-architecture -qDEB_HOST_MULTIARCH)
gdir=/usr/lib/$gconv/gconv
for m in UTF-16 UTF-32 UNICODE UTF-7 'ISO8859-*' 'CP125*' IBM437 IBM850 IBM852 IBM866 TIS-620 KOI8-R KOI8-U MACINTOSH \
         SJIS CP932 EUC-JP EUC-KR UHC JOHAB GBK GB18030 EUC-CN BIG5 \
         libCNS libGB libISOIR165 libJIS libJISX0213 libKSC; do
  found=0
  for f in "$gdir"/$m.so; do
    [ -e "$f" ] || continue
    found=1
    cp -L --parents "$f" "$R"
  done
  if [ "$found" = 0 ]; then
    echo "gconv module $m not found in $gdir" >&2
    exit 1
  fi
done
cp -a --parents "$gdir/gconv-modules" "$R"
if [ -d "$gdir/gconv-modules.d" ]; then cp -a --parents "$gdir/gconv-modules.d" "$R"; fi

# Which Debian package owns each library (resolved through symlinks, because of the merged /usr).
: >/tmp/pkgs.txt
while read -r lib; do
  real=$(readlink -f "$lib")
  owner=$(dpkg -S "$real" | cut -d: -f1 | sort -u)
  if [ -z "$owner" ] || [ "$(echo "$owner" | wc -l)" -ne 1 ]; then
    echo "no single Debian package owns $lib ($real): '$owner'" >&2
    exit 1
  fi
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

# whisper.cpp (MIT), built from the checksummed tarball; ggml is linked statically, so its licence is the one text.
cp "$WSRC/LICENSE" "$R/usr/share/doc/whisper.cpp/copyright"
printf 'source\twhisper.cpp\t%s\thttps://github.com/ggml-org/whisper.cpp/archive/refs/tags/v%s.tar.gz\t%s\n' \
  "$WHISPER_VERSION" "$WHISPER_VERSION" "$WHISPER_SHA256" >>"$inv"

# ffmpeg itself, built from the checksummed tarball with --enable-gpl, so the combined work is GPL-2-or-later (D-16).
cat "$SRC/COPYING.GPLv2" "$SRC/COPYING.LGPLv2.1" >"$R/usr/share/doc/ffmpeg/copyright"
printf 'source\tffmpeg\t%s\thttps://ffmpeg.org/releases/ffmpeg-%s.tar.xz\t%s\n' \
  "$FFMPEG_VERSION" "$FFMPEG_VERSION" "$FFMPEG_SHA256" >>"$inv"
/opt/ffmpeg/bin/ffmpeg -hide_banner -version | sed -n 's/^configuration: //p' >"$R/usr/share/media-tools/ffmpeg-configuration.txt"

# The fonts (P3-04): the OFL-licensed families of fonts/manifest.tsv go to /usr/share/fonts/cf, which is the only directory
# fontconfig reads, and each family's licence text goes to /usr/share/doc/font-<family>/copyright with a row in the inventory.
# The build fails if a file differs from the SHA-256 in the manifest.
fonts_src=/rootfs-src/fonts
while IFS=$'\t' read -r kind family version spdx file want _; do
  [ "$kind" = kind ] && continue
  got=$(sha256sum "$fonts_src/$file" | cut -d' ' -f1)
  if [ "$got" != "$want" ]; then
    echo "$file: SHA-256 $got is not the $want of fonts/manifest.tsv" >&2
    exit 1
  fi
  name="font-$(echo "$family" | tr 'A-Z ' 'a-z-')"
  case "$kind" in
    font)
      cp "$fonts_src/$file" "$R/usr/share/fonts/cf/$file"
      printf 'font\t%s\t%s\t%s\t%s\n' "$name" "$version" "$family ($spdx)" "$want" >>"$inv"
      ;;
    licence)
      mkdir -p "$R/usr/share/doc/$name"
      cp "$fonts_src/$file" "$R/usr/share/doc/$name/copyright"
      ;;
  esac
done <"$fonts_src/manifest.tsv"
