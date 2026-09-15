#!/bin/bash
# Builds the macOS installer package openlog-infra-agent_<v>_darwin_<arch>.pkg (D-113): a component package (pkgbuild)
# wrapped in a product archive (productbuild) for one architecture.
#
#   agents/infra/packaging/macos/build-pkg.sh VERSION ARCH STAGE_DIR OUT.pkg [MANIFEST_DIR]
#
# STAGE_DIR is the extracted release top directory (openlog-infra-agent, LICENSE, README.md, packaging/). The payload
# installs it to /opt/openlog/infra-agent/versions/<v>/ (root:wheel, the install.sh layout); the postinstall script
# points current at it (unless a newer self-updated version is current), writes the LaunchDaemon (-reconcile
# -reconcile-context package), configures the agent when a configuration exists or credentials were provided
# (packaging/macos/postinstall) and starts it when a license key is configured.
# MANIFEST_DIR: signed manifest.json(.sig) of <v> listing the darwin tar.gz archives (embedded-manifest.sh); without it
# a backend-ordered rollback from the pkg-installed version is refused (rule 5).
# PKG_SIGN_IDENTITY (optional): "Developer ID Installer: …" identity for productbuild --sign; PKG_KEYCHAIN its keychain.
set -euo pipefail

die() { echo "build-pkg: $*" >&2; exit 1; }
[ $# -ge 4 ] || die "usage: $0 VERSION ARCH STAGE_DIR OUT.pkg [MANIFEST_DIR]"
version=${1#v}
arch=$2
stage=$3
out=$4
manifest=${5:-}
here=$(cd "$(dirname "$0")" && pwd)
identifier=org.openlog.infra-agent

case $arch in
amd64) host=x86_64 ;;
arm64) host=arm64 ;;
*) die "unsupported ARCH $arch (amd64, arm64)" ;;
esac
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]] || die "VERSION $version is not SemVer"
bin=$stage/openlog-infra-agent
[ -x "$bin" ] || die "$bin is missing or not executable"
for f in LICENSE README.md packaging/config.example.yaml; do [ -f "$stage/$f" ] || die "$stage has no $f"; done
got=$(lipo -archs "$bin" 2>/dev/null || true)
[ "$got" = "$host" ] || die "$bin is built for '$got', not $host"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dest=$work/root/opt/openlog/infra-agent/versions/$version
mkdir -p "$dest"
cp -R "$stage/." "$dest/"
if [ -n "$manifest" ]; then
	for f in manifest.json manifest.json.sig; do [ -f "$manifest/$f" ] || die "$manifest has no $f"; done
	python3 - "$manifest/manifest.json" "$version" "openlog-infra-agent_${version}_darwin_$arch.tar.gz" <<'EOF' || die "$manifest/manifest.json does not fit this package"
import json, sys
m = json.load(open(sys.argv[1]))
assert m.get("product") == "openlog" and m.get("version") == sys.argv[2], "product/version %s %s" % (m.get("product"), m.get("version"))
assert (m.get("compatibility") or {}).get("rollback_floor"), "no compatibility.rollback_floor"
assert any(a.get("name") == sys.argv[3] for a in m.get("artifacts", [])), "does not list " + sys.argv[3]
EOF
	cp "$manifest/manifest.json" "$manifest/manifest.json.sig" "$dest/"
	echo "build-pkg: embedding the signed manifest of $version"
else
	echo "build-pkg: WARNING: no MANIFEST_DIR: a backend-ordered rollback from this pkg-installed version is refused (rule 5)" >&2
fi
chmod -R go-w "$work/root"

mkdir -p "$work/scripts"
sed "s/@VERSION@/$version/g" "$here/postinstall" >"$work/scripts/postinstall"
chmod 0755 "$work/scripts/postinstall"

pkgbuild --root "$work/root" --identifier "$identifier" --version "$version" --scripts "$work/scripts" \
	--install-location / --ownership recommended "$work/openlog-infra-agent.pkg"

cat >"$work/distribution.xml" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>openlog infrastructure agent $version</title>
  <options customize="never" require-scripts="false" hostArchitectures="$host" rootVolumeOnly="true"/>
  <domains enable_anywhere="false" enable_currentUserHome="false" enable_localSystem="true"/>
  <volume-check><allowed-os-versions><os-version min="12.0"/></allowed-os-versions></volume-check>
  <choices-outline><line choice="$identifier"/></choices-outline>
  <choice id="$identifier" visible="false" title="openlog infrastructure agent"><pkg-ref id="$identifier"/></choice>
  <pkg-ref id="$identifier" version="$version" onConclusion="none">openlog-infra-agent.pkg</pkg-ref>
</installer-gui-script>
EOF

sign=()
if [ -n "${PKG_SIGN_IDENTITY:-}" ]; then
	sign=(--sign "$PKG_SIGN_IDENTITY" --timestamp)
	[ -z "${PKG_KEYCHAIN:-}" ] || sign+=(--keychain "$PKG_KEYCHAIN")
fi
mkdir -p "$(dirname "$out")"
# ${sign[@]+…}: macOS /bin/bash 3.2 treats an empty array as unbound under set -u.
productbuild --distribution "$work/distribution.xml" --package-path "$work" ${sign[@]+"${sign[@]}"} "$out"
echo "build-pkg: built $out ($(wc -c <"$out" | tr -d ' ') bytes, $host${PKG_SIGN_IDENTITY:+, signed})"
