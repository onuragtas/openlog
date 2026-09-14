#!/usr/bin/env bash
# Builds the npm package tarball openlog-node-<version>.tgz (+ .sha256) of openlog-node into a release directory,
# where `make release-local` lists it in the signed manifest (component node-agent, format tgz) and release.yml
# attaches it to the GitHub release, so `npm install <release URL>` works without the npm registry.
#
#   agents/node/scripts/release-pack.sh 0.9.0 dist/v0.9.0              # node:22-alpine (Docker)
#   NODE_BUILD=local agents/node/scripts/release-pack.sh 0.9.0 DIR     # Node.js on the host (CI with setup-node)
#   RUN_TESTS=0 …                                                     # skip `npm test` (package check only)
#
# The sources are copied first, so the version bump never touches the checkout.
set -euo pipefail
version="${1:?version}"
out="${2:?release directory}"
here="$(cd "$(dirname "$0")/.." && pwd)"
file="openlog-node-$version.tgz"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
export VERSION="$version" RUN_TESTS="${RUN_TESTS:-1}"

# shellcheck disable=SC2016 # expanded by the inner shell
build='set -e
npm version "$VERSION" --no-git-tag-version --allow-same-version >/dev/null
npm ci --no-audit --no-fund
if [ "$RUN_TESTS" = 1 ]; then npm test; fi
npm pack --pack-destination "$OUT"'

if [ "${NODE_BUILD:-docker}" = local ]; then
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  tar -C "$here" --exclude=node_modules --exclude=dist --exclude=.test-build -cf - . | tar -C "$work" -xf -
  (cd "$work" && OUT="$out" sh -c "$build")
else
  docker run --rm -v "$here":/src:ro -v "$out":/out -v "${NODE_PACK_CACHE:-openlog-node-pack-npm}":/root/.npm \
    -e VERSION -e RUN_TESTS -e OUT=/out "node:${NODE_VERSION:-22}-alpine" sh -c '
      set -e
      mkdir -p /work
      tar -C /src --exclude=node_modules --exclude=dist --exclude=.test-build -cf - . | tar -C /work -xf -
      cd /work
      sh -c "$1"
      chown "$(stat -c %u:%g /out)" "/out/openlog-node-$VERSION.tgz"' release-pack "$build"
fi

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then sha256sum "$file" > "$file.sha256"; else shasum -a 256 "$file" > "$file.sha256"; fi
cat "$file.sha256"
