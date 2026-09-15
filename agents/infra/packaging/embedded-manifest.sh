#!/bin/bash
# Signed manifest embedded in the Windows MSI and the macOS pkg (D-113). The published manifest.json cannot be part of an
# installer package (it lists the package's own sha256), so, like the deb/rpm (Makefile release-packages), packages carry a
# second manifest of the same version that lists only the archives given here. It is built with the arguments of the
# Makefile's RELEASE_MANIFEST_ARGS except --image (the agent only reads version and compatibility); the release job
# checks that everything but artifacts and images equals the published manifest.
#
#   agents/infra/packaging/embedded-manifest.sh VERSION OUT_DIR ARCHIVE...
#
# Environment: OPENLOG_RELEASE_SIGNING_KEY (required), OPENLOG_RELEASE_SIGNING_KEY_2 (optional), OPENLOG_RELEASE_PUBLIC_KEYS
# (required, verification), RELEASE_BASE_URL (releases root, default GitHub), RELEASE_NOTES_URL, RELEASE_CHANNEL,
# RELEASE_DATE (RFC 3339 released_at; release.yml passes the prepare job's date), RELEASE_COMPAT (space-separated k=v).
# OUT_DIR ends up with manifest.json and manifest.json.sig only.
set -euo pipefail

[ $# -ge 3 ] || { echo "usage: $0 VERSION OUT_DIR ARCHIVE..." >&2; exit 2; }
version=${1#v}
out=$2
shift 2
repo=$(cd "$(dirname "$0")/../../.." && pwd)
base=${RELEASE_BASE_URL:-https://github.com/onuragtas/openlog/releases/download}
notes=${RELEASE_NOTES_URL:-https://github.com/onuragtas/openlog/releases/tag/v$version}
: "${OPENLOG_RELEASE_SIGNING_KEY:?OPENLOG_RELEASE_SIGNING_KEY is required}"
: "${OPENLOG_RELEASE_PUBLIC_KEYS:?OPENLOG_RELEASE_PUBLIC_KEYS is required}"

rm -rf "$out"
mkdir -p "$out"
out=$(cd "$out" && pwd)
for a in "$@"; do
	[ -f "$a" ] || { echo "embedded-manifest: $a does not exist" >&2; exit 1; }
	cp "$a" "$out/"
done

args=(--version "$version" --base-url "${base%/}/v$version" --notes-url "$notes"
	--migrations postgres=migrations/postgres --migrations clickhouse=schema/clickhouse)
[ -z "${RELEASE_DATE:-}" ] || args+=(--released-at "$RELEASE_DATE")
[ -z "${RELEASE_CHANNEL:-}" ] || args+=(--channel "$RELEASE_CHANNEL")
for c in ${RELEASE_COMPAT:-}; do args+=(--compat "$c"); done

cd "$repo"
tool=$out/.openlog-release$(go env GOEXE)
go build -o "$tool" ./cmd/openlog-release
"$tool" build-manifest "${args[@]}" --dist "$out"
"$tool" sign --key-env OPENLOG_RELEASE_SIGNING_KEY "$out/manifest.json"
if [ -n "${OPENLOG_RELEASE_SIGNING_KEY_2:-}" ]; then
	"$tool" sign --key-env OPENLOG_RELEASE_SIGNING_KEY_2 "$out/manifest.json"
fi
"$tool" verify --keys "$OPENLOG_RELEASE_PUBLIC_KEYS" --check-artifacts "$out/manifest.json"
find "$out" -mindepth 1 ! -name manifest.json ! -name manifest.json.sig -exec rm -f {} +
echo "embedded manifest of $version in $out ($# archives)"
