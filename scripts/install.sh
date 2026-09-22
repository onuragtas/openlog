#!/bin/sh
# openlog infrastructure agent installer (Linux and macOS; Windows: install.ps1 or the .msi of the release).
#
#   curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
#     sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318
#
# Options (environment variable in brackets):
#   --license-key KEY     ingest license key                      [OPENLOG_LICENSE_KEY]
#   --endpoint URL        OTLP/HTTP endpoint of openlog-ingest     [OPENLOG_ENDPOINT]
#   --version V           version to install (default: latest on the channel) [OPENLOG_VERSION]
#   --channel C           stable (default) or beta                 [OPENLOG_CHANNEL]
#   --method M            auto (default), deb, rpm or tarball (macOS: tarball only) [OPENLOG_INSTALL_METHOD]
#   --base-url URL        releases root or mirror; files are fetched from URL/v<version>/
#                         and the index from URL/index.json      [OPENLOG_RELEASE_BASE_URL]
#   --index-url URL       release index                            [OPENLOG_RELEASE_INDEX_URL]
#   --no-start            install and configure, but do not (re)start the service
#   --no-docker-access    do not add openlog-agent to the docker group, now or on later upgrades
#                         (creates /etc/openlog-infra-agent/no-docker-access) [OPENLOG_AGENT_DOCKER_ACCESS=0]
#   --no-php-access       do not add PHP-FPM pool users to the openlog-php socket group, now or later
#                         (creates /etc/openlog-infra-agent/no-php-access) [OPENLOG_AGENT_PHP_ACCESS=0]
#   --no-ebpf-profiler    do not install openlog-ebpf-profiler (whole-host CPU profiling, on by default on
#                         Linux deb/rpm; CAP_BPF+CAP_PERFMON). Remembered, like --no-docker-access
#
# macOS (darwin amd64/arm64): the agent runs as root under launchd (label org.openlog.infra-agent,
# /Library/LaunchDaemons/org.openlog.infra-agent.plist), CLI /usr/local/bin/openlog-infra-agent, config root:wheel 0600,
# log /var/log/openlog-infra-agent.log. --no-docker-access and --no-php-access have no effect. Uninstall:
#   sudo openlog-infra-agent -uninstall-service && sudo rm -rf /opt/openlog/infra-agent /usr/local/bin/openlog-infra-agent
#   (add /etc/openlog-infra-agent /var/lib/openlog-infra-agent to remove configuration and state)
#
# PHP: php.sock (PHP agent spans) is 0660 with group openlog-php. openlog-agent and every PHP-FPM pool user
# (per-site users of HestiaCP, cPanel, Plesk, …) plus the Apache/nginx user are added to it and the affected PHP-FPM
# services are reloaded; pools created later are granted at the next: systemctl restart openlog-infra-agent
#
# Docker: when a docker group exists, openlog-agent is added to it (container names, ports, IPs).
# That membership is root-equivalent. Revert: gpasswd -d openlog-agent docker &&
#   touch /etc/openlog-infra-agent/no-docker-access && systemctl restart openlog-infra-agent
#
# Trust model: this bootstrap download relies on HTTPS. The installer fetches manifest.json from
# the release source and checks the size and SHA-256 of the downloaded package against it. Every
# later update is applied by the agent itself, which verifies the Ed25519 signature of the release
# manifest against the public keys compiled into it (docs/contracts/releases-updates.md). When a root-owned agent that
# supports `-verify-release` is already installed, it also verifies the signature of the new release manifest and the
# downloaded artifact before anything is installed.
#
# Re-running the installer is safe: it upgrades to the requested/latest version, keeps an agent
# that already updated itself to a newer version, and updates license key and endpoint when given.
# Every run reconciles the installation with the running release (`openlog-infra-agent -reconcile`:
# systemd unit, account, docker group, root-owned install root). Hosts installed before the unit's
# privileged pre-start step existed need one re-run (or package upgrade) to get full self-updates.
set -eu

GITHUB_RELEASES=https://github.com/onuragtas/openlog/releases
ROOT=/opt/openlog/infra-agent
CONFIG_DIR=/etc/openlog-infra-agent
CONFIG=$CONFIG_DIR/config.yaml
STATE_DIR=/var/lib/openlog-infra-agent
UNIT=openlog-infra-agent.service
USER_NAME=openlog-agent
PKG=openlog-infra-agent
LAUNCHD_LABEL=org.openlog.infra-agent
PLIST=/Library/LaunchDaemons/$LAUNCHD_LABEL.plist

license_key=${OPENLOG_LICENSE_KEY:-}
endpoint=${OPENLOG_ENDPOINT:-}
version=${OPENLOG_VERSION:-}
channel=${OPENLOG_CHANNEL:-stable}
method=${OPENLOG_INSTALL_METHOD:-auto}
base_url=${OPENLOG_RELEASE_BASE_URL:-}
index_url=${OPENLOG_RELEASE_INDEX_URL:-}
start=1
docker_access=${OPENLOG_AGENT_DOCKER_ACCESS:-1}
docker_added=0
DOCKER_OPT_OUT=$CONFIG_DIR/no-docker-access
php_access=${OPENLOG_AGENT_PHP_ACCESS:-1}
PHP_OPT_OUT=$CONFIG_DIR/no-php-access
ebpf_profiler=${OPENLOG_EBPF_PROFILER:-1}
EBPF_OPT_OUT=$CONFIG_DIR/no-ebpf-profiler
# Asked for by name: then a host that cannot run it is an error rather than something to skip quietly.
ebpf_explicit=0
# Empty: neither flag was given, so an earlier opt-out stands.
ebpf_opt_out=
tmpdir=

log() { printf 'openlog-install: %s\n' "$*" >&2; }
die() {
	log "error: $*"
	exit 1
}
usage() { sed -n '2,38s/^# \{0,1\}//p' "$0" 2>/dev/null || echo "see https://github.com/onuragtas/openlog/blob/master/docs/operations/releasing.md"; }

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
	--no-docker-access)
		docker_access=0
		shift
		continue
		;;
	--no-php-access)
		php_access=0
		shift
		continue
		;;
	--with-ebpf-profiler)
		ebpf_profiler=1
		ebpf_explicit=1
		ebpf_opt_out=0
		shift
		continue
		;;
	--no-ebpf-profiler)
		ebpf_profiler=0
		ebpf_opt_out=1
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

# Platform: owner group of root-owned files, account and CLI link differ between Linux and macOS.
case $(uname -s) in
Linux)
	goos=linux
	ROOT_GROUP=root
	CLI_LINK=/usr/bin/openlog-infra-agent
	;;
Darwin)
	goos=darwin
	ROOT_GROUP=wheel
	CLI_LINK=/usr/local/bin/openlog-infra-agent
	case $method in
	auto | tarball) method=tarball ;;
	*) die "--method $method is not supported on macOS (tarball only)" ;;
	esac
	;;
*) die "only Linux and macOS are supported (Windows: install.ps1 or the .msi of the release)" ;;
esac
[ "$(id -u)" = 0 ] || die "run as root, e.g. curl -fsSL …/install.sh | sudo sh -s -- --license-key KEY --endpoint URL"

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m) (amd64 and arm64 are supported)" ;;
esac

case $docker_access in 0 | false | no | off) docker_access=0 ;; *) docker_access=1 ;; esac
if [ "$docker_access" = 0 ] && [ "$goos" = linux ]; then
	# Persist the opt-out before a package is installed, so its postinstall (and later upgrades) skip it too.
	mkdir -p "$CONFIG_DIR"
	touch "$DOCKER_OPT_OUT"
	OPENLOG_AGENT_DOCKER_ACCESS=0
	export OPENLOG_AGENT_DOCKER_ACCESS
fi
case $php_access in 0 | false | no | off) php_access=0 ;; *) php_access=1 ;; esac
if [ "$php_access" = 0 ] && [ "$goos" = linux ]; then
	# Same for PHP-FPM pool users (openlog-php socket group, php-agent.md §1).
	mkdir -p "$CONFIG_DIR"
	touch "$PHP_OPT_OUT"
	OPENLOG_AGENT_PHP_ACCESS=0
	export OPENLOG_AGENT_PHP_ACCESS
fi

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
	chown -h "root:$ROOT_GROUP" "$tmp" 2>/dev/null || true
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

# trusted_dir DIR: DIR and everything below it are owned by root and not writable by group or others.
# Root executes the current binary (ExecStartPre=+), so a version an older agent wrote itself is not kept.
trusted_dir() {
	[ -d "$1" ] && [ ! -L "$1" ] || return 1
	[ -z "$(find "$1" \( ! -user 0 -o -perm -0020 -o -perm -0002 -o -type l \) -print 2>/dev/null | head -n 1)" ]
}

# reconcile_supported BINARY: the release has `-reconcile` (unit, account, docker group, ownership).
reconcile_supported() { [ -x "$1" ] && "$1" -help 2>&1 | grep -q -- -reconcile; }

# flag_supported BINARY FLAG: the release's -help lists FLAG (e.g. -configure, -verify-release).
flag_supported() { [ -x "$1" ] && "$1" -help 2>&1 | grep -q -- "$2"; }

# create_config SOURCE: a new config file from SOURCE (or empty keys), readable by the agent only.
create_config() {
	mkdir -p "$CONFIG_DIR"
	if [ -n "$1" ] && [ -f "$1" ]; then cp "$1" "$CONFIG"; else printf 'license_key: ""\nendpoint: ""\n' >"$CONFIG"; fi
	if [ "$goos" = darwin ]; then
		chown root:wheel "$CONFIG"
		chmod 0600 "$CONFIG"
	else
		chown "root:$USER_NAME" "$CONFIG"
		chmod 0640 "$CONFIG"
	fi
}

launchd_loaded() { launchctl print "system/$LAUNCHD_LABEL" >/dev/null 2>&1; }

# Legacy steps for releases without `-reconcile`: add the agent user to an existing docker group (Docker Engine API:
# container names, ports, IPs), unless opted out. Idempotent; sets docker_added=1 when the membership is new. Newer
# releases implement the same rules in `openlog-infra-agent -reconcile`.
grant_docker_access() {
	getent group docker >/dev/null 2>&1 || return 0
	if [ "$docker_access" = 0 ]; then
		log "not adding $USER_NAME to the docker group (opt-out recorded in $DOCKER_OPT_OUT)"
		return 0
	fi
	[ ! -e "$DOCKER_OPT_OUT" ] || return 0
	if getent group docker | cut -d: -f4 | tr ',' '\n' | grep -qx "$USER_NAME"; then
		return 0
	fi
	if command -v usermod >/dev/null 2>&1; then
		usermod -aG docker "$USER_NAME" || return 0
	elif command -v gpasswd >/dev/null 2>&1; then
		gpasswd -a "$USER_NAME" docker >/dev/null || return 0
	elif command -v adduser >/dev/null 2>&1; then
		adduser "$USER_NAME" docker >/dev/null || return 0
	else
		log "warning: cannot add $USER_NAME to the docker group (no usermod, gpasswd or adduser)"
		return 0
	fi
	docker_added=1
	log "added $USER_NAME to the docker group for container metadata and discovery"
	log "  docker group membership is root-equivalent: whoever controls $USER_NAME controls Docker and thus the host"
	log "  revert: gpasswd -d $USER_NAME docker && touch $DOCKER_OPT_OUT && systemctl restart openlog-infra-agent"
	log "  (or install with --no-docker-access)"
}

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
log "installing openlog-infra-agent $version ($channel, $goos/$arch, method $method)"

# --- decide whether to install ------------------------------------------------------------------
running=$(running_version)
install=1
if [ -n "$running" ] && [ -x "$ROOT/current/openlog-infra-agent" ]; then
	cmp=$(semver_cmp "$running" "$version")
	legacy=0
	trusted_dir "$ROOT/versions/$running" || legacy=1
	if [ "$legacy" = 1 ] && [ "$cmp" != -1 ] && [ "$explicit_version" = 0 ]; then
		# Written by an older agent's self-update and writable by it: re-install the same version from the release.
		log "the agent runs $running from a directory writable by $USER_NAME; re-installing $running root-owned"
		version=$running
		manifest_url=$releases/v$version/manifest.json
	elif [ "$cmp" = 1 ] && [ "$explicit_version" = 0 ]; then
		log "the agent already runs $running (newer than $version); keeping it"
		install=0
	elif [ "$cmp" = 0 ] && [ "$legacy" = 0 ] && { [ "$method" = tarball ] || package_installed "$method"; }; then
		log "openlog-infra-agent $version is already installed"
		install=0
	fi
fi

if [ "$install" = 1 ]; then
	fetch "$manifest_url" "$tmpdir/manifest.json"
	manifest=$(json_flat "$tmpdir/manifest.json")
	[ "$(json_str "$manifest" product)" = openlog ] || die "$manifest_url is not an openlog manifest"
	[ "$(json_str "$manifest" version)" = "$version" ] || die "$manifest_url is for version $(json_str "$manifest" version), not $version"

	name=openlog-infra-agent_${version}_${goos}_${arch}.$method
	[ "$method" != tarball ] || name=openlog-infra-agent_${version}_${goos}_${arch}.tar.gz
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
	# A root-owned installed agent that can verify releases checks the manifest signature against its compiled-in keys.
	trusted_bin=$ROOT/current/openlog-infra-agent
	if [ -n "$running" ] && trusted_dir "$ROOT/versions/$running" && flag_supported "$trusted_bin" -verify-release; then
		fetch "$manifest_url.sig" "$tmpdir/manifest.json.sig"
		"$trusted_bin" -verify-release "$tmpdir/manifest.json" -artifact "$tmpdir/$name" >&2 ||
			die "$name: the signature of the release manifest or the artifact does not verify ($trusted_bin -verify-release)"
		log "release signature verified by the installed agent $running"
	fi

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
		top=$tmpdir/x/openlog-infra-agent_${version}_${goos}_${arch}
		[ -x "$top/openlog-infra-agent" ] || die "unexpected archive layout in $name"
		[ "$goos" = darwin ] || ensure_user
		# Root-owned: root executes the current binary in the unit's privileged pre-start step.
		install -d -m 0755 -o root -g "$ROOT_GROUP" "$ROOT" "$ROOT/versions"
		dest=$ROOT/versions/$version
		rm -rf "$dest.new"
		cp -R "$top" "$dest.new"
		# The signed manifest of the installed version; the agent needs it for rollback_floor.
		if [ -f "$tmpdir/manifest.json.sig" ]; then
			cp "$tmpdir/manifest.json.sig" "$dest.new/manifest.json.sig"
		else
			fetch "$manifest_url.sig" "$dest.new/manifest.json.sig"
		fi
		cp "$tmpdir/manifest.json" "$dest.new/manifest.json"
		chown -R "root:$ROOT_GROUP" "$dest.new"
		chmod -R go-w "$dest.new"
		rm -rf "$dest"
		mv "$dest.new" "$dest"
		mkdir -p "$CONFIG_DIR/discovery.d"
		# macOS: -configure below creates the file from the release's embedded example.
		if [ ! -f "$CONFIG" ] && { [ "$goos" = linux ] || ! flag_supported "$dest/openlog-infra-agent" -configure; }; then
			create_config "$dest/packaging/config.example.yaml"
		fi
		if ! reconcile_supported "$dest/openlog-infra-agent" && [ -d /etc/systemd/system ]; then
			# Older release: -reconcile below installs the unit otherwise.
			cp "$dest/packaging/systemd/$UNIT" "/etc/systemd/system/$UNIT"
			chmod 0644 "/etc/systemd/system/$UNIT"
		fi
		mkdir -p "${CLI_LINK%/*}"
		ln -sfn "$ROOT/current/openlog-infra-agent" "$CLI_LINK"
		switch_current "$version"
		;;
	esac
	# An explicit --version wins over a newer self-updated version (packages keep the newer one).
	if [ "$explicit_version" = 1 ] && [ "$(running_version)" != "$version" ]; then
		switch_current "$version"
	fi
fi

# --- optional: the eBPF whole-host profiler -------------------------------------------------------
# A separate package on purpose (docs/contracts/ebpf-profiler.md §2): it runs with capabilities the
# infra agent deliberately does not have, so it is installed only when asked for. Verified against the
# same signed manifest as the agent — its artifacts are in there because the release builds them before
# the manifest is written.
# Installed by default, so a host that cannot take it must not fail the agent's installation: skipped with
# a reason. Asked for by name (--with-ebpf-profiler), the same conditions are errors — someone who typed
# the flag is owed a failure rather than a silent no-op.
ebpf_unavailable() {
	if [ "$ebpf_explicit" = 1 ]; then
		die "$1"
	fi
	log "openlog-ebpf-profiler skipped: $1"
	return 1
}

install_ebpf_profiler() {
	[ "$goos" = linux ] || ebpf_unavailable "whole-host profiling is Linux only (this host is $goos)" || return 0
	case $method in
	deb | rpm) ;;
	*) ebpf_unavailable "whole-host profiling needs the deb or rpm method (this run uses $method)" || return 0 ;;
	esac
	# The agent may already have been up to date, in which case the manifest was never fetched.
	[ -f "$tmpdir/manifest.json" ] || fetch "$manifest_url" "$tmpdir/manifest.json"
	ebpf_manifest=$(json_flat "$tmpdir/manifest.json")
	ebpf_name=openlog-ebpf-profiler_${version}_linux_${arch}.$method
	# shellcheck disable=SC2020 # split the flat JSON into one line per object
	ebpf_artifact=$(printf '%s' "$ebpf_manifest" | tr '{}' '\n\n' | grep -F "\"name\":\"$ebpf_name\"" | head -n 1 || true)
	[ -n "$ebpf_artifact" ] || { ebpf_unavailable "release $version has no artifact $ebpf_name"; return 0; }
	ebpf_want_sha=$(json_str "$ebpf_artifact" sha256)
	ebpf_want_size=$(json_num "$ebpf_artifact" size)
	ebpf_url=$(json_str "$ebpf_artifact" url)
	[ -z "$base_url" ] || ebpf_url=$base_url/v$version/$ebpf_name
	printf '%s' "$ebpf_want_sha" | grep -Eq '^[0-9a-f]{64}$' || { ebpf_unavailable "manifest has no valid sha256 for $ebpf_name"; return 0; }

	log "downloading $ebpf_url"
	fetch "$ebpf_url" "$tmpdir/$ebpf_name"
	ebpf_got_size=$(wc -c <"$tmpdir/$ebpf_name" | tr -d ' ')
	ebpf_got_sha=$(sha256_of "$tmpdir/$ebpf_name")
	[ "$ebpf_got_size" = "$ebpf_want_size" ] || die "$ebpf_name: size $ebpf_got_size does not match the manifest ($ebpf_want_size)"
	[ "$ebpf_got_sha" = "$ebpf_want_sha" ] || die "$ebpf_name: sha256 $ebpf_got_sha does not match the manifest ($ebpf_want_sha)"
	log "sha256 verified: $ebpf_got_sha"

	case $method in
	deb) DEBIAN_FRONTEND=noninteractive dpkg --force-confdef --force-confold -i "$tmpdir/$ebpf_name" >&2 ;;
	rpm) rpm -U --replacepkgs --oldpackage "$tmpdir/$ebpf_name" >&2 ;;
	esac
	# Said plainly, the way the docker group's root-equivalence is: an operator is entitled to know what
	# this just granted without reading a contract.
	log "openlog-ebpf-profiler installed; it runs with CAP_BPF and CAP_PERFMON and samples every process on this host"

	# The installer already knows the key and the endpoint, and the profiler needs the same ones the agent
	# uses. Making someone type them again into a second file is friction with nothing bought for it, and
	# it is why the service would otherwise sit enabled but stopped: its package ships a placeholder key,
	# and it refuses to start on one rather than restart-looping.
	ebpf_env=/etc/openlog-ebpf-profiler/openlog-ebpf-profiler.env
	ebpf_key=$license_key
	ebpf_endpoint=$endpoint
	# Not given on this run: take what the agent is already configured with.
	[ -n "$ebpf_key" ] || ebpf_key=$(sed -n 's/^license_key:[[:space:]]*"\{0,1\}\([^"[:space:]#]*\).*/\1/p' "$CONFIG" 2>/dev/null | head -n 1)
	[ -n "$ebpf_endpoint" ] || ebpf_endpoint=$(sed -n 's/^endpoint:[[:space:]]*"\{0,1\}\([^"[:space:]#]*\).*/\1/p' "$CONFIG" 2>/dev/null | head -n 1)
	if [ -f "$ebpf_env" ] && [ -n "$ebpf_key" ]; then
		set -- -e "s|^OPENLOG_LICENSE_KEY=.*|OPENLOG_LICENSE_KEY=$ebpf_key|"
		[ -z "$ebpf_endpoint" ] || set -- "$@" -e "s|^OPENLOG_ENDPOINT=.*|OPENLOG_ENDPOINT=$ebpf_endpoint|"
		sed "$@" "$ebpf_env" >"$tmpdir/ebpf.env"
		# Written back through the existing file so its mode and owner (0640 root:openlog-ebpf) survive.
		cat "$tmpdir/ebpf.env" >"$ebpf_env"
		rm -f "$tmpdir/ebpf.env"
		log "openlog-ebpf-profiler configured from the same license key and endpoint as the agent"
		if [ "$start" = 1 ] && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
			systemctl start openlog-ebpf-profiler ||
				log "openlog-ebpf-profiler did not start; run: openlog-ebpf-profiler -self-test"
		fi
	else
		log "set OPENLOG_LICENSE_KEY in $ebpf_env, then: systemctl start openlog-ebpf-profiler"
	fi
}

# The choice has to outlive this run: an operator who said no must not have it reinstalled by the next
# upgrade or by the agent's own updater. Recorded as a file the way --no-docker-access and
# --no-php-access are; neither flag given leaves an earlier decision alone.
case $ebpf_opt_out in
1)
	mkdir -p "$CONFIG_DIR" 2>/dev/null || true
	: >"$EBPF_OPT_OUT" 2>/dev/null || log "could not record the opt-out in $EBPF_OPT_OUT"
	;;
0) rm -f "$EBPF_OPT_OUT" 2>/dev/null || true ;;
*) [ ! -f "$EBPF_OPT_OUT" ] || ebpf_profiler=0 ;;
esac
if [ "$ebpf_profiler" = 1 ]; then
	install_ebpf_profiler
fi

bin=$ROOT/current/openlog-infra-agent
if [ "$goos" = darwin ]; then
	install -d -m 0750 -o root -g wheel "$STATE_DIR"
else
	install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$STATE_DIR"
fi

# --- configuration ------------------------------------------------------------------------------
if [ "$goos" = darwin ] && flag_supported "$bin" -configure; then
	# Creates the config from the embedded example (root:wheel 0600) if missing and sets only the given keys.
	set -- -configure -config "$CONFIG"
	[ -z "$license_key" ] || set -- "$@" -license-key "$license_key"
	[ -z "$endpoint" ] || set -- "$@" -endpoint "$endpoint"
	"$bin" "$@" >&2 || die "$bin -configure failed"
else
	[ -f "$CONFIG" ] || create_config ""
	[ -z "$license_key" ] || set_config_value license_key "$license_key"
	[ -z "$endpoint" ] || set_config_value endpoint "$endpoint"
fi
has_license_key || log "warning: no license_key in $CONFIG; pass --license-key"

# --- reconcile ----------------------------------------------------------------------------------
# systemd unit, account, docker group and openlog-php group with the PHP-FPM pool users (opt-outs above), ownership: the
# same step the agent's privileged pre-start step runs on every start. It logs what it changed to stderr. Package
# installs already ran it in their postinstall; then this is a no-op.
restart_needed=0
if reconcile_supported "$bin"; then
	out=$("$bin" -reconcile -reconcile-context install -config "$CONFIG") || log "warning: reconcile reported errors (see above)"
	case $out in *restart-required*) restart_needed=1 ;; esac
elif [ "$goos" = linux ]; then
	grant_docker_access
	restart_needed=$docker_added
fi

# --- service ------------------------------------------------------------------------------------
if [ "$goos" = darwin ]; then
	# launchd reads the plist at bootstrap only: a changed plist (restart-required) needs bootout + bootstrap.
	if [ ! -f "$PLIST" ]; then
		log "warning: $PLIST is missing (the release has no -reconcile); start the agent with: $CLI_LINK -config $CONFIG"
	elif [ "$start" = 1 ] && { has_license_key || { [ "$restart_needed" = 1 ] && launchd_loaded; }; }; then
		if launchd_loaded && [ "$restart_needed" = 1 ]; then
			launchctl bootout "system/$LAUNCHD_LABEL" >/dev/null 2>&1 || true
			i=0
			while launchd_loaded && [ "$i" -lt 20 ]; do
				sleep 1
				i=$((i + 1))
			done
		fi
		if launchd_loaded; then
			launchctl kickstart -k "system/$LAUNCHD_LABEL"
		else
			launchctl bootstrap system "$PLIST"
		fi
		log "service $LAUNCHD_LABEL (re)started"
	elif [ "$restart_needed" = 1 ]; then
		log "the new launchd job applies after: launchctl bootout system/$LAUNCHD_LABEL; launchctl bootstrap system $PLIST"
	fi
elif command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl enable "$UNIT" >/dev/null 2>&1
	if [ "$start" = 1 ] && has_license_key; then
		systemctl restart "$UNIT"
		log "service $UNIT (re)started"
	elif [ "$restart_needed" = 1 ] && [ "$start" = 1 ]; then
		systemctl try-restart "$UNIT" || true
	elif [ "$restart_needed" = 1 ]; then
		log "the new unit or groups apply after: systemctl restart $UNIT"
	fi
else
	log "systemd is not running; start the agent with: /usr/bin/openlog-infra-agent -config $CONFIG"
fi

log "done: $("$ROOT/current/openlog-infra-agent" -version 2>/dev/null || echo "openlog-infra-agent $version")"
