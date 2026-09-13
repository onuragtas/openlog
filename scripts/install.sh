#!/bin/sh
# openlog infrastructure agent installer.
#
#   curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
#     sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318
#
# Options (environment variable in brackets):
#   --license-key KEY     ingest license key                      [OPENLOG_LICENSE_KEY]
#   --endpoint URL        OTLP/HTTP endpoint of openlog-ingest     [OPENLOG_ENDPOINT]
#   --version V           version to install (default: latest on the channel) [OPENLOG_VERSION]
#   --channel C           stable (default) or beta                 [OPENLOG_CHANNEL]
#   --method M            auto (default), deb, rpm or tarball      [OPENLOG_INSTALL_METHOD]
#   --base-url URL        releases root or mirror; files are fetched from URL/v<version>/
#                         and the index from URL/index.json      [OPENLOG_RELEASE_BASE_URL]
#   --index-url URL       release index                            [OPENLOG_RELEASE_INDEX_URL]
#   --no-start            install and configure, but do not (re)start the service
#
# Trust model: this bootstrap download relies on HTTPS. The installer fetches manifest.json from
# the release source and checks the size and SHA-256 of the downloaded package against it. Every
# later update is applied by the agent itself, which verifies the Ed25519 signature of the release
# manifest against the public keys compiled into it (docs/contracts/releases-updates.md).
#
# Re-running the installer is safe: it upgrades to the requested/latest version, keeps an agent
# that already updated itself to a newer version, and updates license key and endpoint when given.
set -eu

GITHUB_RELEASES=https://github.com/onuragtas/openlog/releases
ROOT=/opt/openlog/infra-agent
CONFIG_DIR=/etc/openlog-infra-agent
CONFIG=$CONFIG_DIR/config.yaml
STATE_DIR=/var/lib/openlog-infra-agent
UNIT=openlog-infra-agent.service
USER_NAME=openlog-agent
PKG=openlog-infra-agent

license_key=${OPENLOG_LICENSE_KEY:-}
endpoint=${OPENLOG_ENDPOINT:-}
version=${OPENLOG_VERSION:-}
channel=${OPENLOG_CHANNEL:-stable}
method=${OPENLOG_INSTALL_METHOD:-auto}
base_url=${OPENLOG_RELEASE_BASE_URL:-}
index_url=${OPENLOG_RELEASE_INDEX_URL:-}
start=1
tmpdir=

log() { printf 'openlog-install: %s\n' "$*" >&2; }
die() {
	log "error: $*"
	exit 1
}
usage() { sed -n '2,24s/^# \{0,1\}//p' "$0" 2>/dev/null || echo "see https://github.com/onuragtas/openlog/blob/main/docs/operations/releasing.md"; }

cleanup() { [ -z "$tmpdir" ] || rm -rf "$tmpdir"; }
trap cleanup EXIT INT TERM

while [ $# -gt 0 ]; do
	case $1 in
	-h | --help)
		usage
		exit 0
		;;
	--no-start)
		start=0
		shift
		continue
		;;
	--*=*)
		opt=${1%%=*}
		val=${1#*=}
		shift
		;;
	--*)
		opt=$1
		[ $# -ge 2 ] || die "$opt needs a value"
		val=$2
		shift 2
		;;
	*) die "unknown argument: $1 (see --help)" ;;
	esac
	case $opt in
	--license-key) license_key=$val ;;
	--endpoint) endpoint=$val ;;
	--version) version=$val ;;
	--channel) channel=$val ;;
	--method) method=$val ;;
	--base-url) base_url=$val ;;
	--index-url) index_url=$val ;;
	*) die "unknown option: $opt (see --help)" ;;
	esac
done

# --- validation ---------------------------------------------------------------------------------
version=${version#v}
explicit_version=0
[ -z "$version" ] || explicit_version=1
case $channel in stable | beta) ;; *) die "--channel must be stable or beta" ;; esac
case $method in auto | deb | rpm | tarball) ;; *) die "--method must be auto, deb, rpm or tarball" ;; esac
if [ -n "$version" ] && ! printf '%s' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'; then
	die "--version $version is not a SemVer version"
fi
nl='
'
case $license_key in *'"'* | *\\* | *"$nl"* | *' '*) die "--license-key contains invalid characters" ;; esac
case $endpoint in
"" | http://* | https://*) ;;
*) die "--endpoint must start with https:// or http://" ;;
esac
case $endpoint in *'"'* | *\\* | *"$nl"* | *' '*) die "--endpoint contains invalid characters" ;; esac
base_url=${base_url%/}
for u in "$base_url" "$index_url"; do
	case $u in
	"" | https://*) ;;
	http://*) log "warning: $u is not HTTPS; only use plain HTTP for local testing" ;;
	*) die "URL must start with https:// or http://: $u" ;;
	esac
done

[ "$(uname -s)" = Linux ] || die "only Linux is supported"
[ "$(id -u)" = 0 ] || die "run as root, e.g. curl -fsSL …/install.sh | sudo sh -s -- --license-key KEY --endpoint URL"

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m) (amd64 and arm64 are supported)" ;;
esac

# --- helpers ------------------------------------------------------------------------------------
fetch() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --proto '=https,http' -o "$2" "$1" || die "download failed: $1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || die "download failed: $1"
	else
		die "curl or wget is required"
	fi
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 -r "$1" | awk '{print $1}'
	else
		die "sha256sum, shasum or openssl is required"
	fi
}

# semver_cmp A B prints -1, 0 or 1 following SemVer 2.0 precedence (build metadata ignored).
semver_cmp() {
	awk -v a="$1" -v b="$2" '
	function isnum(s) { return s ~ /^[0-9]+$/ }
	function cmpid(x, y) {
		if (isnum(x) && isnum(y)) {
			sub(/^0+/, "", x); sub(/^0+/, "", y)
			if (length(x) != length(y)) return length(x) < length(y) ? -1 : 1
		} else if (isnum(x)) return -1
		else if (isnum(y)) return 1
		if (x "" == y "") return 0
		return (x "" < y "") ? -1 : 1
	}
	BEGIN {
		sub(/^v/, "", a); sub(/^v/, "", b); sub(/\+.*/, "", a); sub(/\+.*/, "", b)
		pa = ""; pb = ""
		i = index(a, "-"); if (i) { pa = substr(a, i + 1); a = substr(a, 1, i - 1) }
		i = index(b, "-"); if (i) { pb = substr(b, i + 1); b = substr(b, 1, i - 1) }
		split(a, A, "."); split(b, B, ".")
		for (k = 1; k <= 3; k++) { c = cmpid(A[k], B[k]); if (c) { print c; exit } }
		if (pa == "" && pb == "") { print 0; exit }
		if (pa == "") { print 1; exit }
		if (pb == "") { print -1; exit }
		na = split(pa, PA, "."); nb = split(pb, PB, ".")
		for (k = 1; k <= na && k <= nb; k++) { c = cmpid(PA[k], PB[k]); if (c) { print c; exit } }
		print (na < nb) ? -1 : ((na > nb) ? 1 : 0)
	}'
}

# json_flat FILE: the JSON without whitespace (release JSON contains no spaces inside values).
json_flat() { tr -d ' \t\r\n' <"$1"; }

# json_str TEXT KEY / json_num TEXT KEY: first value of KEY in a flat JSON fragment.
json_str() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"; }
json_num() { printf '%s' "$1" | sed -n "s/.*\"$2\":\\([0-9][0-9]*\\).*/\\1/p"; }

running_version() {
	[ -L "$ROOT/current" ] || return 0
	v=$(readlink "$ROOT/current")
	v=${v%/}
	printf '%s' "${v##*/}"
}

package_installed() {
	case $1 in
	deb) dpkg-query -W -f='${Status}' "$PKG" 2>/dev/null | grep -q 'install ok installed' ;;
	rpm) rpm -q "$PKG" >/dev/null 2>&1 ;;
	*) return 1 ;;
	esac
}

ensure_user() {
	getent group "$USER_NAME" >/dev/null 2>&1 || {
		if command -v groupadd >/dev/null 2>&1; then groupadd --system "$USER_NAME"; else addgroup -S "$USER_NAME"; fi
	}
	getent passwd "$USER_NAME" >/dev/null 2>&1 || {
		nologin=/usr/sbin/nologin
		[ -x "$nologin" ] || nologin=/sbin/nologin
		if command -v useradd >/dev/null 2>&1; then
			useradd --system --gid "$USER_NAME" --home-dir "$STATE_DIR" --no-create-home --shell "$nologin" "$USER_NAME"
		else
			adduser -S -D -H -h "$STATE_DIR" -s "$nologin" -G "$USER_NAME" "$USER_NAME"
		fi
	}
}

switch_current() { # version
	tmp="$ROOT/.current.$$"
	rm -f "$tmp"
	ln -s "versions/$1" "$tmp"
	chown -h "$USER_NAME:$USER_NAME" "$tmp" 2>/dev/null || true
	if ! mv -Tf "$tmp" "$ROOT/current" 2>/dev/null; then
		rm -f "$ROOT/current" && mv -f "$tmp" "$ROOT/current"
	fi
	log "$ROOT/current -> versions/$1"
}

set_config_value() { # key value
	ctmp="$CONFIG.tmp.$$"
	if grep -q "^$1:" "$CONFIG"; then
		awk -v k="$1" -v v="$2" 'index($0, k ":") == 1 && !done { print k ": \"" v "\""; done = 1; next } { print }' "$CONFIG" >"$ctmp"
	else
		{
			cat "$CONFIG"
			printf '%s: "%s"\n' "$1" "$2"
		} >"$ctmp"
	fi
	cat "$ctmp" >"$CONFIG"
	rm -f "$ctmp"
}

has_license_key() { grep -Eq '^license_key:[[:space:]]*"?[^"[:space:]#]' "$CONFIG" 2>/dev/null; }

# --- resolve the release ------------------------------------------------------------------------
tmpdir=$(mktemp -d)
releases=${base_url:-$GITHUB_RELEASES/download}

if [ -z "$version" ]; then
	if [ -z "$index_url" ]; then
		if [ -n "$base_url" ]; then index_url=$base_url/index.json; else index_url=$GITHUB_RELEASES/latest/download/index.json; fi
	fi
	log "resolving the latest $channel release from $index_url"
	fetch "$index_url" "$tmpdir/index.json"
	index=$(json_flat "$tmpdir/index.json")
	case $index in *'"product":"openlog"'*) ;; *) die "$index_url is not an openlog release index" ;; esac
	entries=$(printf '%s' "$index" | sed -n 's/.*"stable":\[\([^]]*\)\].*/\1/p')
	if [ "$channel" = beta ]; then
		entries="$entries},$(printf '%s' "$index" | sed -n 's/.*"beta":\[\([^]]*\)\].*/\1/p')"
	fi
	manifest_url=
	for e in $(printf '%s' "$entries" | tr '}' '\n'); do
		v=$(json_str "$e" version)
		u=$(json_str "$e" manifest_url)
		[ -n "$v" ] && [ -n "$u" ] || continue
		if [ -z "$version" ] || [ "$(semver_cmp "$v" "$version")" = 1 ]; then
			version=$v
			manifest_url=$u
		fi
	done
	[ -n "$version" ] || die "no $channel release found in $index_url"
	[ -z "$base_url" ] || manifest_url=$base_url/v$version/manifest.json
else
	manifest_url=$releases/v$version/manifest.json
fi

# --- choose the installation method --------------------------------------------------------------
# shellcheck disable=SC1091
os_id=$( (. /etc/os-release 2>/dev/null && printf '%s %s' "${ID:-}" "${ID_LIKE:-}") || true)
if [ "$method" = auto ]; then
	method=tarball
	case " $os_id " in
	*" debian "* | *" ubuntu "*) command -v dpkg >/dev/null 2>&1 && method=deb ;;
	*" rhel "* | *" fedora "* | *" centos "* | *" rocky "* | *" almalinux "* | *" amzn "* | *suse*) command -v rpm >/dev/null 2>&1 && method=rpm ;;
	esac
fi
case $method in
deb) command -v dpkg >/dev/null 2>&1 || die "--method deb needs dpkg" ;;
rpm) command -v rpm >/dev/null 2>&1 || die "--method rpm needs rpm" ;;
tarball)
	if command -v dpkg-query >/dev/null 2>&1 && package_installed deb; then
		die "the deb package $PKG is installed; re-run with --method deb"
	fi
	if command -v rpm >/dev/null 2>&1 && package_installed rpm; then
		die "the rpm package $PKG is installed; re-run with --method rpm"
	fi
	;;
esac
log "installing openlog-infra-agent $version ($channel, linux/$arch, method $method)"

# --- decide whether to install ------------------------------------------------------------------
running=$(running_version)
install=1
if [ -n "$running" ] && [ -x "$ROOT/current/openlog-infra-agent" ]; then
	cmp=$(semver_cmp "$running" "$version")
	if [ "$cmp" = 1 ] && [ "$explicit_version" = 0 ]; then
		log "the agent already runs $running (newer than $version); keeping it"
		install=0
	elif [ "$cmp" = 0 ] && { [ "$method" = tarball ] || package_installed "$method"; }; then
		log "openlog-infra-agent $version is already installed"
		install=0
	fi
fi

if [ "$install" = 1 ]; then
	fetch "$manifest_url" "$tmpdir/manifest.json"
	manifest=$(json_flat "$tmpdir/manifest.json")
	[ "$(json_str "$manifest" product)" = openlog ] || die "$manifest_url is not an openlog manifest"
	[ "$(json_str "$manifest" version)" = "$version" ] || die "$manifest_url is for version $(json_str "$manifest" version), not $version"

	name=openlog-infra-agent_${version}_linux_${arch}.$method
	[ "$method" != tarball ] || name=openlog-infra-agent_${version}_linux_${arch}.tar.gz
	# shellcheck disable=SC2020 # split the flat JSON into one line per object
	artifact=$(printf '%s' "$manifest" | tr '{}' '\n\n' | grep -F "\"name\":\"$name\"" | head -n 1 || true)
	[ -n "$artifact" ] || die "release $version has no artifact $name"
	want_sha=$(json_str "$artifact" sha256)
	want_size=$(json_num "$artifact" size)
	url=$(json_str "$artifact" url)
	[ -z "$base_url" ] || url=$base_url/v$version/$name
	printf '%s' "$want_sha" | grep -Eq '^[0-9a-f]{64}$' || die "manifest has no valid sha256 for $name"

	log "downloading $url"
	fetch "$url" "$tmpdir/$name"
	got_size=$(wc -c <"$tmpdir/$name" | tr -d ' ')
	got_sha=$(sha256_of "$tmpdir/$name")
	[ "$got_size" = "$want_size" ] || die "$name: size $got_size does not match the manifest ($want_size)"
	[ "$got_sha" = "$want_sha" ] || die "$name: sha256 $got_sha does not match the manifest ($want_sha)"
	log "sha256 verified: $got_sha"

	case $method in
	deb)
		DEBIAN_FRONTEND=noninteractive dpkg --force-confdef --force-confold -i "$tmpdir/$name" >&2
		;;
	rpm)
		rpm -U --replacepkgs --oldpackage "$tmpdir/$name" >&2
		;;
	tarball)
		mkdir "$tmpdir/x"
		tar -xzf "$tmpdir/$name" -C "$tmpdir/x"
		top=$tmpdir/x/openlog-infra-agent_${version}_linux_${arch}
		[ -x "$top/openlog-infra-agent" ] || die "unexpected archive layout in $name"
		ensure_user
		mkdir -p "$ROOT/versions"
		dest=$ROOT/versions/$version
		rm -rf "$dest.new"
		cp -R "$top" "$dest.new"
		# The signed manifest of the installed version; the agent needs it for rollback_floor.
		fetch "$manifest_url.sig" "$dest.new/manifest.json.sig"
		cp "$tmpdir/manifest.json" "$dest.new/manifest.json"
		rm -rf "$dest"
		mv "$dest.new" "$dest"
		chown -R "$USER_NAME:$USER_NAME" "$ROOT"
		mkdir -p "$CONFIG_DIR/discovery.d"
		if [ ! -f "$CONFIG" ]; then
			cp "$dest/packaging/config.example.yaml" "$CONFIG"
			chown "root:$USER_NAME" "$CONFIG"
			chmod 0640 "$CONFIG"
		fi
		if [ -d /etc/systemd/system ]; then
			cp "$dest/packaging/systemd/$UNIT" "/etc/systemd/system/$UNIT"
			chmod 0644 "/etc/systemd/system/$UNIT"
		fi
		ln -sfn "$ROOT/current/openlog-infra-agent" /usr/bin/openlog-infra-agent
		switch_current "$version"
		;;
	esac
	# An explicit --version wins over a newer self-updated version (packages keep the newer one).
	if [ "$explicit_version" = 1 ] && [ "$(running_version)" != "$version" ]; then
		switch_current "$version"
	fi
fi

install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$STATE_DIR"

# --- configuration ------------------------------------------------------------------------------
[ -f "$CONFIG" ] || {
	mkdir -p "$CONFIG_DIR"
	printf 'license_key: ""\nendpoint: ""\n' >"$CONFIG"
	chown "root:$USER_NAME" "$CONFIG"
	chmod 0640 "$CONFIG"
}
[ -z "$license_key" ] || set_config_value license_key "$license_key"
[ -z "$endpoint" ] || set_config_value endpoint "$endpoint"
has_license_key || log "warning: no license_key in $CONFIG; pass --license-key"

# --- service ------------------------------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl enable "$UNIT" >/dev/null 2>&1
	if [ "$start" = 1 ] && has_license_key; then
		systemctl restart "$UNIT"
		log "service $UNIT (re)started"
	fi
else
	log "systemd is not running; start the agent with: /usr/bin/openlog-infra-agent -config $CONFIG"
fi

log "done: $("$ROOT/current/openlog-infra-agent" -version 2>/dev/null || echo "openlog-infra-agent $version")"
