#!/bin/bash
# Subtitle character-set smoke test: converts tiny .srt files from legacy encodings to UTF-8 with `ffmpeg -sub_charenc`, which
# goes through glibc's iconv and so needs the gconv modules shipped in the image. Exits non-zero if any conversion fails.
#
#   smoke-charsets.sh HOST_DIR CONTAINER_DIR ffmpeg-command...
#
# HOST_DIR is a writable scratch directory on this machine; CONTAINER_DIR is the same directory as the ffmpeg command sees
# it (the build chroots into /rootfs, CI mounts it at /work). Needs `iconv` on the machine running the script to make the inputs.
set -euo pipefail
host=$1
cdir=$2
shift 2
ff=("$@")
mkdir -p "$host"

failed=0
check() { # name ffmpeg-charenc iconv-charset text
  local name=$1 enc=$2 charset=$3 text=$4
  printf '1\n00:00:00,000 --> 00:00:01,000\n%s\n' "$text" | iconv -f UTF-8 -t "$charset" >"$host/$name.srt"
  local charenc=()
  if [ -n "$enc" ]; then charenc=(-sub_charenc "$enc"); fi
  "${ff[@]}" -nostdin -v error -y "${charenc[@]}" -f srt -i "$cdir/$name.srt" -f srt "$cdir/$name.out.srt"
  if grep -q -- "$text" "$host/$name.out.srt"; then
    echo "ok   $name (${enc:-BOM})"
  else
    echo "FAIL $name ($enc): output is not '$text'; it is:" >&2
    od -c "$host/$name.out.srt" | head -8 >&2
    failed=1
  fi
}

check cp1252 CP1252 CP1252 'Café ñ €'
# UTF-16 with a byte-order mark is detected by ffmpeg itself (iconv's UTF-16 writes the BOM); no -sub_charenc.
check utf16bom '' UTF-16 'Café 日本語'
check latin2 ISO-8859-2 ISO-8859-2 'Zażółć gęślą jaźń'
check cyrillic CP1251 CP1251 'Привет мир'
check sjis SHIFT_JIS SHIFT_JIS 'こんにちは世界'
check euckr EUC-KR EUC-KR '안녕하세요'
check gbk GBK GBK '你好世界'
check big5 BIG5 BIG5 '你好世界'
exit "$failed"
