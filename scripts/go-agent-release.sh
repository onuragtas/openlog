#!/usr/bin/env bash
# Go agent module release tooling (docs/operations/releasing.md "Go agent modules").
#
#   go-agent-release.sh prepare X.Y.Z   bump agents/go/version.go and every in-repo module require to vX.Y.Z
#   go-agent-release.sh check   X.Y.Z   fail unless the tree is prepared for X.Y.Z (release.yml runs this)
#   go-agent-release.sh verify [X.Y.Z]  build every module that requires in-repo modules WITHOUT replace
#                                       directives against a local file proxy of this tree
#                                       (default version: the one in the requires)
#   go-agent-release.sh modules         module directories that get a Go module tag
#   go-agent-release.sh tags    X.Y.Z   the Go module tags of release X.Y.Z
#
# Replace directives stay in every go.mod: they only apply when the module is the main module (this
# repository's development and CI) and are ignored by consumers, who resolve the tagged requires.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT=agents/go
MODPREFIX=github.com/onuragtas/openlog/agents/go
# Library modules that are tagged; examples is not a library and is never tagged.
TAGGED_MODULES=("$AGENT" "$AGENT/instrumentation/grpc" "$AGENT/instrumentation/chi" "$AGENT/instrumentation/gin" "$AGENT/instrumentation/echo")

die() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error::$*"; else echo "error: $*" >&2; fi
  exit 1
}

semver_ok() {
  [[ "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]]
}

check_version_arg() {
  local v="${1:-}"
  [ -n "$v" ] || die "VERSION required (SemVer without a leading v, e.g. 0.4.0 or 0.5.0-beta.1)"
  semver_ok "$v" || die "VERSION=$v is not SemVer without a leading v"
  local major="${v%%.*}"
  [ "$major" -lt 2 ] || die "version $v: Go modules at major version >= 2 need a /v$major module path suffix (not implemented)"
}

# Every go.mod below agents/go except the core one.
submodule_gomods() {
  (cd "$ROOT" && find "$AGENT" -mindepth 2 -name go.mod -not -path '*/vendor/*' | LC_ALL=C sort)
}

# "path version" of every in-repo require of a go.mod.
internal_requires() {
  go mod edit -json "$1" | awk -v pre="$MODPREFIX" '
    /"Require"/ {inreq=1} /"Replace"/ {inreq=0}
    inreq && /"Path":/ {gsub(/[",]/, "", $2); p=$2}
    inreq && /"Version":/ {gsub(/[",]/, "", $2); if (p == pre || index(p, pre "/") == 1) print p, $2}'
}

has_replace() {
  go mod edit -json "$1" | awk -v want="$2" '
    /"Replace"/ {inrep=1} inrep && /"Old"/ {old=1} inrep && /"New"/ {old=0}
    inrep && old && /"Path":/ {gsub(/[",]/, "", $2); if ($2 == want) found=1}
    END {exit found ? 0 : 1}'
}

version_go() {
  sed -n -E 's/^const Version = "(.*)"$/\1/p' "$ROOT/$AGENT/version.go"
}

cmd_prepare() {
  local v="${1:-}"
  check_version_arg "$v"
  make -s -C "$ROOT/$AGENT" set-version VERSION="$v"
  local gomod path ver
  while read -r gomod; do
    while read -r path ver; do
      [ -n "$path" ] || continue
      has_replace "$ROOT/$gomod" "$path" || die "$gomod requires $path without a replace to the local module"
      go mod edit -require="$path@v$v" "$ROOT/$gomod"
    done < <(internal_requires "$ROOT/$gomod")
  done < <(submodule_gomods)
  cmd_check "$v"
  echo "prepared Go agent modules for v$v; review and commit:"
  (cd "$ROOT" && git status --short -- "$AGENT" 2>/dev/null || true)
}

cmd_check() {
  local v="${1:-}"
  check_version_arg "$v"
  local failed=0 got gomod path ver
  got="$(version_go)"
  if [ "$got" != "$v" ]; then
    echo "$AGENT/version.go: Version = \"$got\", want \"$v\" (run: make release-prepare VERSION=$v)" >&2
    failed=1
  fi
  while read -r gomod; do
    while read -r path ver; do
      [ -n "$path" ] || continue
      if [ "$ver" != "v$v" ]; then
        echo "$gomod: require $path $ver, want v$v (run: make release-prepare VERSION=$v)" >&2
        failed=1
      fi
      if ! has_replace "$ROOT/$gomod" "$path"; then
        echo "$gomod: require $path has no replace to the local module" >&2
        failed=1
      fi
    done < <(internal_requires "$ROOT/$gomod")
  done < <(submodule_gomods)
  [ "$failed" = 0 ] || die "Go agent modules are not prepared for v$v"
  echo "Go agent modules prepared for v$v"
}

cmd_verify() {
  local v="${1:-}" gomod path ver
  if [ -z "$v" ]; then
    v="$(while read -r gomod; do internal_requires "$ROOT/$gomod"; done < <(submodule_gomods) | awk '{print $2}' | sort -u)"
    [ "$(printf '%s\n' "$v" | grep -c .)" = 1 ] || die "in-repo requires disagree on the version: $(echo "$v" | tr '\n' ' ')"
    v="${v#v}"
  fi
  semver_ok "$v" || die "version $v is not SemVer"
  # Global: the EXIT trap runs after this function returns.
  VERIFY_TMP="$(mktemp -d)"
  trap 'chmod -R u+w "$VERIFY_TMP" 2>/dev/null; rm -rf "$VERIFY_TMP"' EXIT
  local tmp="$VERIFY_TMP" realcache
  realcache="$(go env GOMODCACHE)"
  local all_modules=()
  while read -r gomod; do all_modules+=("$(dirname "$gomod")"); done < <(cd "$ROOT" && find "$AGENT" -name go.mod -not -path '*/vendor/*' | LC_ALL=C sort)
  (cd "$ROOT" && go run ./scripts/gomodproxy -out "$tmp/proxy" -version "v$v" "${all_modules[@]}")

  while read -r gomod; do
    [ -n "$(internal_requires "$ROOT/$gomod")" ] || continue
    local dir src dst
    dir="$(dirname "$gomod")"
    src="$ROOT/$dir"
    dst="$tmp/src/$dir"
    echo "== $dir without replace (in-repo modules from the local proxy at v$v, nothing from the network)"
    # Third-party modules into the module cache first, resolved through the replaced tree.
    (cd "$src" && GOWORK=off GOFLAGS=-mod=readonly go mod download)
    mkdir -p "$dst"
    cp -R "$src/." "$dst/"
    while read -r path ver; do
      go mod edit -dropreplace="$path" "$dst/go.mod"
    done < <(internal_requires "$dst/go.mod")
    # A throwaway module cache: the unreleased in-repo versions must never land in the real one
    # (a later real tag with other content would then fail checksum verification). Third-party
    # modules come read-only from the real cache's download directory; nothing from the network.
    (cd "$dst" && GOWORK=off GOFLAGS=-mod=mod GOMODCACHE="$tmp/modcache" GONOSUMDB="$MODPREFIX" \
      GOPROXY="file://$tmp/proxy,file://$realcache/cache/download,off" \
      go build ./...) || die "$dir does not build against the requires v$v without replace"
  done < <(submodule_gomods)
  echo "Go agent module requires v$v resolve and build without replace"
}

cmd_modules() { printf '%s\n' "${TAGGED_MODULES[@]}"; }

cmd_tags() {
  local v="${1:-}"
  check_version_arg "$v"
  local m
  for m in "${TAGGED_MODULES[@]}"; do echo "$m/v$v"; done
}

case "${1:-}" in
  prepare) cmd_prepare "${2:-}" ;;
  check) cmd_check "${2:-}" ;;
  verify) cmd_verify "${2:-}" ;;
  modules) cmd_modules ;;
  tags) cmd_tags "${2:-}" ;;
  *) sed -n '2,12p' "$0" >&2; exit 2 ;;
esac
