#!/usr/bin/env bash
# Builds the wheel openlog_agent-<pep440>-py3-none-any.whl and the sdist openlog_agent-<pep440>.tar.gz (+ .sha256 each)
# of openlog-agent into a release directory. `make release-local` lists the wheel in the signed manifest (component
# python-agent, format whl); release.yml attaches both to the GitHub release, so `pip install <release URL>` works
# without PyPI, and publishes the same files to PyPI when PYPI_PUBLISH=true.
#
#   agents/python/scripts/release-dist.sh 0.9.0 dist/v0.9.0            # python:3.12-slim (Docker)
#   PY_BUILD=local agents/python/scripts/release-dist.sh 0.9.0 DIR     # Python on the host (CI with setup-python)
#   RUN_TESTS=0 …                                                     # skip the unit and end-to-end tests
#
# Product SemVer → PEP 440: 1.2.0-beta.3 → 1.2.0b3, 1.2.0-rc.1 → 1.2.0rc1, 1.2.0-alpha.1 → 1.2.0a1.
# The sources are copied first, so the version bump never touches the checkout.
set -euo pipefail
version="${1:?version}"
out="${2:?release directory}"
here="$(cd "$(dirname "$0")/.." && pwd)"
pyv="$version"
case "$version" in
  *-beta.*) pyv="${version%%-beta.*}b${version##*-beta.}" ;;
  *-rc.*) pyv="${version%%-rc.*}rc${version##*-rc.}" ;;
  *-alpha.*) pyv="${version%%-alpha.*}a${version##*-alpha.}" ;;
esac
mkdir -p "$out"
out="$(cd "$out" && pwd)"
export PYV="$pyv" RUN_TESTS="${RUN_TESTS:-1}"

# shellcheck disable=SC2016 # expanded by the inner shell
build='set -e
sed -i.bak -E "s/^__version__ = \".*\"/__version__ = \"$PYV\"/" src/openlog_agent/version.py && rm -f src/openlog_agent/version.py.bak
grep -q "^__version__ = \"$PYV\"" src/openlog_agent/version.py
python -m pip install -q build==1.6.1 twine==7.0.0
if [ "$RUN_TESTS" = 1 ]; then
  python -m pip install -q -e . -r tests/requirements-test.txt
  python -m pytest -q -p no:cacheprovider tests/unit tests/e2e
fi
rm -rf dist
python -m build --outdir dist .
python -m twine check --strict dist/*
cp "dist/openlog_agent-$PYV-py3-none-any.whl" "dist/openlog_agent-$PYV.tar.gz" "$OUT/"'

if [ "${PY_BUILD:-docker}" = local ]; then
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  tar -C "$here" --exclude=dist --exclude=build --exclude=.pytest_cache --exclude=__pycache__ -cf - . | tar -C "$work" -xf -
  (cd "$work" && OUT="$out" sh -c "$build")
else
  docker run --rm -v "$here":/src:ro -v "$out":/out -v "${PY_PACK_CACHE:-openlog-python-pack-pip}":/root/.cache/pip \
    -e PYV -e RUN_TESTS -e OUT=/out -e PIP_ROOT_USER_ACTION=ignore -e PIP_DISABLE_PIP_VERSION_CHECK=1 \
    "python:${PYTHON_VERSION:-3.12}-slim" sh -c '
      set -e
      mkdir -p /work
      tar -C /src --exclude=dist --exclude=build --exclude=.pytest_cache --exclude=__pycache__ -cf - . | tar -C /work -xf -
      cd /work
      sh -c "$1"
      chown "$(stat -c %u:%g /out)" "/out/openlog_agent-$PYV-py3-none-any.whl" "/out/openlog_agent-$PYV.tar.gz"' release-dist "$build"
fi

cd "$out"
for f in "openlog_agent-$pyv-py3-none-any.whl" "openlog_agent-$pyv.tar.gz"; do
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f" > "$f.sha256"; else shasum -a 256 "$f" > "$f.sha256"; fi
  cat "$f.sha256"
done
