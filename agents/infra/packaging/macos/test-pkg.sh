#!/bin/bash
# CI smoke test of the macOS pkg (macOS runner, passwordless sudo; changes the host). Installs the package with credentials
# from /tmp/openlog-infra-agent.env, checks layout, configuration, LaunchDaemon and (EXPECT_MANIFEST=1) the embedded
# signed manifest, re-installs it, then uninstalls.
#
#   agents/infra/packaging/macos/test-pkg.sh openlog-infra-agent_<v>_darwin_<arch>.pkg <v>
set -euo pipefail

pkg=$1
version=${2#v}
ROOT=/opt/openlog/infra-agent
CONFIG=/etc/openlog-infra-agent/config.yaml
LABEL=org.openlog.infra-agent
PLIST=/Library/LaunchDaemons/$LABEL.plist
ENV_FILE=/tmp/openlog-infra-agent.env
LICENSE_KEY=ci-test-key-7f3a9b2e
ENDPOINT=http://127.0.0.1:4318

step() { echo "test-pkg: $*"; }
diagnostics() {
	echo "---- /var/log/install.log (openlog)"
	grep -i openlog /var/log/install.log 2>/dev/null | tail -n 60 || true
	echo "---- install root"
	sudo ls -la "$ROOT" "$ROOT/versions" "$ROOT/versions/$version" 2>/dev/null || true
	sudo cat "$ROOT/reconcile-status.json" 2>/dev/null || true
	echo "---- launchd"
	sudo launchctl print "system/$LABEL" 2>/dev/null | head -n 40 || true
	sudo tail -n 30 /var/log/openlog-infra-agent.log 2>/dev/null || true
}
fail() {
	echo "test-pkg: FAIL: $*"
	diagnostics
	exit 1
}
uninstall() {
	sudo "$ROOT/current/openlog-infra-agent" -uninstall-service >/dev/null 2>&1 || true
	sudo rm -rf "$ROOT" /usr/local/bin/openlog-infra-agent /etc/newsyslog.d/openlog-infra-agent.conf
	sudo pkgutil --forget "$LABEL" >/dev/null 2>&1 || true
}
trap 'uninstall; sudo rm -rf /etc/openlog-infra-agent /var/lib/openlog-infra-agent "$ENV_FILE"' EXIT

pkgutil --check-signature "$pkg" || true
step "installer -pkg $pkg (credentials in $ENV_FILE)"
sudo sh -c "umask 077; printf 'OPENLOG_LICENSE_KEY=%s\nOPENLOG_ENDPOINT=%s\n' '$LICENSE_KEY' '$ENDPOINT' > '$ENV_FILE'"
sudo installer -pkg "$pkg" -target / || fail "installer exited with $?"

[ "$(readlink "$ROOT/current")" = "versions/$version" ] || fail "current -> $(readlink "$ROOT/current"), want versions/$version"
"$ROOT/current/openlog-infra-agent" -version | grep -F "$version" || fail "-version does not report $version"
[ -z "$(find "$ROOT" ! -user root -print -quit)" ] || fail "install root not root-owned"
[ -f "$PLIST" ] || fail "$PLIST missing"
[ -L /usr/local/bin/openlog-infra-agent ] || fail "CLI link missing"
[ ! -e "$ENV_FILE" ] || fail "$ENV_FILE was not removed"
sudo grep -q "^license_key:.*$LICENSE_KEY" "$CONFIG" || fail "license key not configured"
sudo grep -q "^endpoint:.*$ENDPOINT" "$CONFIG" || fail "endpoint not configured"
[ "$(stat -f %Su:%Lp "$CONFIG")" = root:600 ] || fail "config ownership $(stat -f %Su:%Lp "$CONFIG")"
sudo launchctl print "system/$LABEL" >/dev/null || fail "LaunchDaemon not loaded"
pkgutil --pkgs | grep -qx "$LABEL" || fail "package receipt missing"
step "installed: layout, configuration, LaunchDaemon ok"

if [ "${EXPECT_MANIFEST:-0}" = 1 ]; then
	dir=$ROOT/versions/$version
	[ -f "$dir/manifest.json" ] && [ -f "$dir/manifest.json.sig" ] || fail "embedded manifest missing"
	"$dir/openlog-infra-agent" -verify-release "$dir/manifest.json" || fail "the installed agent does not verify the embedded manifest"
	if sudo grep -q 'manifest.json' "$ROOT/reconcile-status.json"; then fail "-reconcile reports the embedded manifest as missing or unusable"; fi
	step "embedded manifest present and verified"
fi

step "re-installing (idempotent)"
sudo installer -pkg "$pkg" -target / || fail "re-install exited with $?"
[ "$(readlink "$ROOT/current")" = "versions/$version" ] || fail "current changed by the re-install"
sudo grep -q "^license_key:.*$LICENSE_KEY" "$CONFIG" || fail "license key lost by the re-install"

step "uninstall"
uninstall
[ ! -e "$PLIST" ] || fail "$PLIST still present"
if sudo launchctl print "system/$LABEL" >/dev/null 2>&1; then fail "LaunchDaemon still loaded"; fi
if pkgutil --pkgs | grep -qx "$LABEL"; then fail "receipt still present"; fi
step "done"
