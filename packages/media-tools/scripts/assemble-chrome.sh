#!/bin/bash
# Builds /rootfs-chrome, the motion-graphics part of the media-tools image (D-35): chrome-headless-shell from Chrome for Testing
# (unpacked and pruned in /opt/chrome by the Dockerfile), exactly the shared libraries it loads (found with ldd, plus the NSS modules
# it opens at run time), the licence text of every package they come from, and an inventory of it all in the format of
# assemble-rootfs.sh (merged into the image's inventory by the Dockerfile).
set -euo pipefail

CHROME_VERSION=$1
CHROME_SHA256=$2
CHROME_URL=$3
R=/rootfs-chrome
mkdir -p "$R/opt" "$R/usr/share/media-tools" "$R/usr/share/doc/chrome-headless-shell"
cp -a /opt/chrome "$R/opt/chrome"

: >/tmp/chrome-libs.raw
for bin in /opt/chrome/chrome-headless-shell /opt/chrome/libEGL.so /opt/chrome/libGLESv2.so; do
  out=$(ldd "$bin")
  if echo "$out" | grep -q 'not found'; then
    echo "unresolved shared library for $bin:" >&2
    echo "$out" | grep 'not found' >&2
    exit 1
  fi
  # Libraries Chrome ships itself (in /opt/chrome) are already copied.
  echo "$out" | awk '$2 == "=>" && $3 ~ /^\// {print $3} $1 ~ /^\// {print $1}' | grep -v '^/opt/chrome/' >>/tmp/chrome-libs.raw || true
done
# NSS loads its softoken and freebl with dlopen, so ldd does not list them.
multi=$(dpkg-architecture -qDEB_HOST_MULTIARCH)
for f in /usr/lib/"$multi"/nss/*.so /usr/lib/"$multi"/nss/*.chk; do
  [ -e "$f" ] && echo "$f" >>/tmp/chrome-libs.raw
done
sort -u /tmp/chrome-libs.raw >/tmp/chrome-libs.txt
while read -r lib; do
  cp -L --parents "$lib" "$R"
done </tmp/chrome-libs.txt

: >/tmp/chrome-pkgs.txt
while read -r lib; do
  real=$(readlink -f "$lib")
  owner=$(dpkg -S "$real" | cut -d: -f1 | sort -u)
  if [ -z "$owner" ] || [ "$(echo "$owner" | wc -l)" -ne 1 ]; then
    echo "no single Debian package owns $lib ($real): '$owner'" >&2
    exit 1
  fi
  echo "$owner" >>/tmp/chrome-pkgs.txt
done </tmp/chrome-libs.txt
sort -u /tmp/chrome-pkgs.txt -o /tmp/chrome-pkgs.txt

inv="$R/usr/share/media-tools/inventory-chrome.tsv"
: >"$inv"
while read -r pkg; do
  version=$(dpkg-query -W -f='${Version}' "$pkg")
  src=$(dpkg-query -W -f='${source:Package}' "$pkg")
  mkdir -p "$R/usr/share/doc/$pkg"
  cp -L "/usr/share/doc/$pkg/copyright" "$R/usr/share/doc/$pkg/copyright"
  printf 'deb\t%s\t%s\t%s\t\n' "$pkg" "$version" "$src" >>"$inv"
done </tmp/chrome-pkgs.txt

# Chrome for Testing's own licence file: Chromium's licence and the notices of every third-party component built into the binary.
cp /opt/chrome/LICENSE.headless_shell "$R/usr/share/doc/chrome-headless-shell/copyright"
rm -f "$R/opt/chrome/LICENSE.headless_shell" "$R/opt/chrome/ABOUT" "$R/opt/chrome/deb.deps" "$R/opt/chrome/rpm.deps"
printf 'binary\tchrome-headless-shell\t%s\t%s\t%s\n' "$CHROME_VERSION" "$CHROME_URL" "$CHROME_SHA256" >>"$inv"
