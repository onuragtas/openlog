#!/usr/bin/env bash
# Release artifacts of the openlog PHP agent (docs/contracts/php-agent.md §7) for one architecture:
#
#   <out>/openlog-php-agent_<version>_linux_<arch>.tar.gz   modules for every PHP ABI + installer
#   <out>/openlog-php-agent_<version>_linux_<arch>.deb|.rpm|.apk
#
#   agents/php/packaging/build-artifacts.sh 0.9.1 dist/v0.9.1
#   TARGETS="8.2-nts-glibc 8.3-nts-musl" PACKAGES="deb apk" agents/php/packaging/build-artifacts.sh 0.9.1 /tmp/out
#
# TARGETS: <php minor>-<nts|zts>-<glibc|musl>, default PHP 7.1–8.4 × NTS/ZTS × glibc/musl (36 modules).
#   glibc: compiled on AlmaLinux 8 (glibc 2.28) against headers from the official image's source (Dockerfile.glibc);
#          the module must not need a newer glibc symbol (checked) and is load-tested in the official image
#   musl:  compiled and load-tested in the official Alpine image (php:<v>-cli-alpine / php:<v>-zts-alpine)
# ARCH: the Docker host's architecture, or PLATFORM=linux/<arch> (QEMU; release CI uses native runners).
# PACKAGES (deb rpm apk): nfpm packages from packaging/nfpm/php-agent.yaml. PARALLEL (4) builds at a time.
# Module layout: modules/<ZEND_MODULE_API_NO>-<nts|zts>-<glibc|musl>/openlog.so, index modules.txt
# (key, PHP version, sha256). Containers/images: openlog-php-build*.
set -euo pipefail
[ $# -ge 2 ] || { echo "usage: $0 VERSION OUT_DIR" >&2; exit 2; }
VERSION=$1
mkdir -p "$2"
OUT=$(cd "$2" && pwd)
PKG_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PHP_DIR=$(cd "$PKG_DIR/.." && pwd)
ROOT_DIR=$(cd "$PHP_DIR/../.." && pwd)
PLATFORM=${PLATFORM:-}
PARALLEL=${PARALLEL:-4}
PACKAGES=${PACKAGES-"deb rpm apk"}
MAX_GLIBC=${MAX_GLIBC:-2.28}
NFPM_IMAGE=${NFPM_IMAGE:-goreleaser/nfpm:v2.47.0@sha256:a662cb167d7b6d3a83920c83d76b12d02b8ac5dd2c13e5c62c15270b23f6df0c}
if [ -z "${TARGETS:-}" ]; then
  TARGETS=""
  for v in 7.1 7.2 7.3 7.4 8.0 8.1 8.2 8.3 8.4; do
    TARGETS="$TARGETS $v-nts-glibc $v-zts-glibc $v-nts-musl $v-zts-musl"
  done
fi
if [ -n "$PLATFORM" ]; then ARCH=${PLATFORM#linux/}; else ARCH=$(docker version --format '{{.Server.Arch}}'); fi
case "$ARCH" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
NAME="openlog-php-agent_${VERSION}_linux_$ARCH"
WORK=$(mktemp -d)
STAGE="$WORK/$NAME"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$STAGE/modules" "$STAGE/bin" "$WORK/src" "$WORK/logs"
rsync -a --exclude modules --exclude '*.o' --exclude '*.lo' --exclude .libs --exclude autom4te.cache \
  --exclude tests --exclude fuzz --exclude build "$PHP_DIR/ext/" "$WORK/src/"

build_one() { # build_one <minor>-<nts|zts>-<libc>
  local t=$1 minor zts libc image log="$WORK/logs/$1.log" plat=()
  minor=${t%%-*}; zts=${t#*-}; zts=${zts%%-*}; libc=${t##*-}
  [ -n "$PLATFORM" ] && plat=(--platform "$PLATFORM")
  local out="$WORK/out-$t"
  mkdir -p "$out"
  if [ "$libc" = glibc ]; then
    image="openlog-php-build:$minor-$zts"
    docker build "${plat[@]}" -q -t "$image" --build-arg "PHP_SRC_IMAGE=php:$minor-cli" --build-arg "ZTS=$zts" \
      -f "$PKG_DIR/Dockerfile.glibc" "$PKG_DIR" >"$log" 2>&1 || { echo "$t: build image failed (see $log)"; cat "$log"; return 1; }
  else
    image="php:$minor-$([ "$zts" = zts ] && echo zts || echo cli)-alpine"
  fi
  docker run --rm "${plat[@]}" --name "openlog-php-build-$t-$$" -v "$WORK/src":/src:ro -v "$out":/out "$image" sh -ec '
    command -v apk >/dev/null && apk add --no-cache $PHPIZE_DEPS >/dev/null
    cp -r /src /tmp/b && cd /tmp/b
    phpize >/dev/null && ./configure --enable-openlog CFLAGS="-O2 -g" >/dev/null && make -j"$(nproc)" >/tmp/make.log 2>&1 || { cat /tmp/make.log; exit 1; }
    strip --strip-unneeded modules/openlog.so
    inc=$(php-config --include-dir)
    api=$(sed -n "s/^#define ZEND_MODULE_API_NO \([0-9]*\).*/\1/p" "$inc/Zend/zend_modules.h")
    zts=nts; grep -q "^#define ZTS 1" "$inc/main/php_config.h" && zts=zts
    libc=glibc; [ -f /etc/alpine-release ] && libc=musl
    echo "$api-$zts-$libc $(php-config --version)" > /out/key
    cp modules/openlog.so /out/openlog.so
    if [ "$libc" = glibc ]; then
      objdump -T /out/openlog.so | grep -o "GLIBC_[0-9.]*" | sed "s/GLIBC_//" | sort -uV | tail -1 > /out/glibc
    fi
  ' >>"$log" 2>&1 || { echo "$t: compile failed"; tail -30 "$log"; return 1; }
  read -r key phpver < "$out/key"
  if [ "$libc" = glibc ]; then
    local need; need=$(cat "$out/glibc")
    if [ "$(printf '%s\n%s\n' "$need" "$MAX_GLIBC" | sort -V | tail -1)" != "$MAX_GLIBC" ]; then
      echo "$t: openlog.so needs GLIBC_$need > $MAX_GLIBC"; return 1
    fi
    image="php:$minor-$([ "$zts" = zts ] && echo zts || echo cli)"
  fi
  # load test in the official runtime image
  docker run --rm "${plat[@]}" -v "$out":/m:ro "$image" php -d extension=/m/openlog.so -r '
    if (!extension_loaded("openlog")) { fwrite(STDERR, "not loaded\n"); exit(1); } echo PHP_VERSION, " ", PHP_ZTS ? "zts" : "nts", " ok\n";' >>"$log" 2>&1 \
    || { echo "$t: load test failed in $image"; tail -10 "$log"; return 1; }
  mkdir -p "$STAGE/modules/$key"
  cp "$out/openlog.so" "$STAGE/modules/$key/openlog.so"
  echo "$key $phpver" > "$STAGE/modules/$key/.php-version"
  echo "$t -> modules/$key (PHP $phpver$( [ "$libc" = glibc ] && echo ", needs GLIBC_$(cat "$out/glibc")"))"
}
export -f build_one
export WORK STAGE PLATFORM PKG_DIR MAX_GLIBC

echo "== $NAME: $(echo $TARGETS | wc -w) modules"
# shellcheck disable=SC2086
if ! printf '%s\n' $TARGETS | xargs -P "$PARALLEL" -I{} bash -c 'build_one "$@"' _ {}; then
  echo "some modules failed (logs in $WORK/logs, kept: set KEEP=1 to inspect)"; [ "${KEEP:-0}" = 1 ] && trap - EXIT; exit 1
fi

# stage the rest
cp "$PKG_DIR/openlog-php-install" "$STAGE/bin/openlog-php-install"
chmod 0755 "$STAGE/bin/openlog-php-install"
cp "$PHP_DIR/LICENSE" "$STAGE/LICENSE"
cp "$PHP_DIR/ext/README.md" "$STAGE/README.md"
echo "$VERSION" > "$STAGE/VERSION"
echo "$ARCH" > "$STAGE/ARCH"
# index + reproducible tarball with GNU tools in a container (same result on Linux CI and macOS hosts)
docker run --rm --user "$(id -u):$(id -g)" -v "$WORK":/w -v "$OUT":/out -e NAME="$NAME" -e EPOCH="${SOURCE_DATE_EPOCH:-0}" \
  debian:12 sh -ec '
    cd "/w/$NAME/modules"
    for d in */; do d=${d%/}; read -r _ v < "$d/.php-version"; rm -f "$d/.php-version"
      echo "$d $v $(sha256sum "$d/openlog.so" | cut -d" " -f1)"; done | sort > "/w/$NAME/modules.txt"
    tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$EPOCH" -C /w -czf "/out/$NAME.tar.gz" "$NAME"'
echo "wrote $OUT/$NAME.tar.gz"

if [ -n "$PACKAGES" ]; then
  scripts="$WORK/pkg-scripts"
  mkdir -p "$scripts"
  for s in "$PKG_DIR"/scripts/*.sh; do sed "s/@VERSION@/$VERSION/g" "$s" > "$scripts/${s##*/}"; chmod 0755 "$scripts/${s##*/}"; done
  contents="$WORK/contents.yaml"
  ( cd "$STAGE" && find . -type f | LC_ALL=C sort | while read -r f; do
      f=${f#./}; mode=0644; [ -x "$f" ] && mode=0755
      printf '  - src: /work/%s/%s\n    dst: /opt/openlog/php-agent/versions/%s/%s\n    file_info: {mode: %s, owner: root, group: root}\n' \
        "$NAME" "$f" "$VERSION" "$f" "$mode"
    done ) > "$contents"
  sed -e "s|\${VERSION}|$VERSION|g" -e "s|\${ARCH}|$ARCH|g" -e "s|\${STAGE}|/work/$NAME|g" -e "s|\${SCRIPTS}|/work/pkg-scripts|g" \
    "$ROOT_DIR/packaging/nfpm/php-agent.yaml" | awk -v f="$contents" '
      /^  # @STAGE_CONTENTS@$/ { while ((getline line < f) > 0) print line; next } { print }' > "$WORK/nfpm.yaml"
  for fmt in $PACKAGES; do
    docker run --rm --user "$(id -u):$(id -g)" -v "$WORK":/work -v "$OUT":/out -w /work "$NFPM_IMAGE" \
      package --config /work/nfpm.yaml --packager "$fmt" --target "/out/$NAME.$fmt" >/dev/null
    echo "wrote $OUT/$NAME.$fmt"
  done
fi
