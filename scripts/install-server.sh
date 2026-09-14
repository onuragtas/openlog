#!/bin/sh
# openlog server installer: the `single` Docker Compose profile on one Linux host, running the signed
# release image with automatic updates.
#
#   curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
#     sudo sh -s -- --email you@example.com
#
# Options (environment variable in brackets):
#   --email EMAIL         owner (admin) email; prompted on a terminal   [OPENLOG_OWNER_EMAIL]
#   --password PASS       owner password, 8-256 characters; prompted on a terminal, otherwise
#                         generated and printed once                    [OPENLOG_OWNER_PASSWORD]
#   --version V           version to install (default: latest on the channel) [OPENLOG_VERSION]
#   --channel C           stable (default) or beta                      [OPENLOG_CHANNEL]
#   --dir DIR             installation directory (default /opt/openlog-server) [OPENLOG_SERVER_DIR]
#   --updater MODE        auto (default: backup, migrate, update, roll back on failure),
#                         notify (report new versions only) or off      [OPENLOG_UPDATER_MODE]
#   --domain HOST|URL     public UI address behind your TLS reverse proxy, e.g. openlog.example.com
#                         (https assumed: Secure session cookie); http://HOST keeps plain HTTP
#   --cors-origins LIST   browser OTLP origins for ingest :4318, e.g. https://app.example.com
#   --project NAME        docker compose project (default openlog)
#   --index-url URL       release index                                 [OPENLOG_RELEASE_INDEX_URL]
#   --bundle-url URL      tar.gz with the compose files: openlog-compose-<v>/ or deploy/compose/ (default: the release
#                         asset openlog-compose-<v>.tar.gz, sha256 checked against manifest.json; releases without
#                         it: the GitHub source archive of v<version>)
#   --install-docker      install Docker Engine with https://get.docker.com when it is missing
#   --no-start            write the files, do not pull or start anything
#
# Re-running is safe: .env (secrets, passwords, license key) is kept, missing settings are added,
# the compose files are replaced by those of the requested/latest version and the stack is updated.
# A running version newer than the requested one is kept (an explicit older --version is refused).
#
# Trust model: this bootstrap relies on HTTPS (GitHub). Later updates are applied by openlog-updater,
# which verifies the Ed25519-signed release manifest (docs/operations/upgrading.md).
set -eu

GITHUB=https://github.com/onuragtas/openlog
IMAGE_REPO=ghcr.io/onuragtas/openlog

email=${OPENLOG_OWNER_EMAIL:-}
password=${OPENLOG_OWNER_PASSWORD:-}
version=${OPENLOG_VERSION:-}
channel=${OPENLOG_CHANNEL:-}
dir=${OPENLOG_SERVER_DIR:-/opt/openlog-server}
updater=${OPENLOG_UPDATER_MODE:-}
domain=
cors=
cors_set=0
project=openlog
index_url=${OPENLOG_RELEASE_INDEX_URL:-$GITHUB/releases/latest/download/index.json}
bundle_url=
install_docker=0
start=1
password_generated=0
tmpdir=

log() { printf 'openlog-install-server: %s\n' "$*" >&2; }
warn() { log "WARNING: $*"; }
die() {
	log "error: $*"
	exit 1
}
usage() { sed -n '2,32s/^# \{0,1\}//p' "$0" 2>/dev/null | grep . || echo "see $GITHUB#install-on-a-server-docker-compose-single-machine"; }

cleanup() {
	[ -z "$tmpdir" ] || rm -rf "$tmpdir"
	stty echo 2>/dev/null </dev/tty || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM

while [ $# -gt 0 ]; do
	case $1 in
	-h | --help)
		usage
		exit 0
		;;
	--install-docker)
		install_docker=1
		shift
		continue
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
	--email) email=$val ;;
	--password) password=$val ;;
	--version) version=$val ;;
	--channel) channel=$val ;;
	--dir) dir=$val ;;
	--updater) updater=$val ;;
	--domain) domain=$val ;;
	--cors-origins)
		cors=$val
		cors_set=1
		;;
	--project) project=$val ;;
	--index-url) index_url=$val ;;
	--bundle-url) bundle_url=$val ;;
	*) die "unknown option: $opt (see --help)" ;;
	esac
done

# --- validation ---------------------------------------------------------------------------------
nl='
'
version=${version#v}
explicit_version=0
[ -z "$version" ] || explicit_version=1
if [ -n "$version" ] && ! printf '%s' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'; then
	die "--version $version is not a SemVer version"
fi
case $channel in "" | stable | beta) ;; *) die "--channel must be stable or beta" ;; esac
case $updater in "" | auto | notify | off) ;; *) die "--updater must be auto, notify or off" ;; esac
case $dir in /*) dir=${dir%/} ;; *) die "--dir must be an absolute path" ;; esac
printf '%s' "$project" | grep -Eq '^[a-z0-9][a-z0-9_-]*$' || die "--project must be lowercase letters, digits, - and _"
case $email in *' '* | *"'"* | *'"'* | *"$nl"*) die "--email contains invalid characters" ;; esac
case $password in *"'"* | *"$nl"*) die "--password must not contain ' or a newline" ;; esac
case $cors in *' '* | *"'"* | *'"'* | *"$nl"*) die "--cors-origins: comma-separated origins without spaces" ;; esac
public_scheme=https
case $domain in
"") ;;
http://*)
	public_scheme=http
	domain=${domain#http://}
	;;
https://*) domain=${domain#https://} ;;
esac
domain=${domain%/}
case $domain in *[!A-Za-z0-9.:-]*) die "--domain must be a host name, optionally with scheme and port" ;; esac
for u in "$index_url" "$bundle_url"; do
	case $u in
	"" | https://*) ;;
	http://*) warn "$u is not HTTPS; only use plain HTTP for local testing" ;;
	*) die "URL must start with https:// or http://: $u" ;;
	esac
done

[ "$(uname -s)" = Linux ] || die "only Linux is supported"
is_root=0
[ "$(id -u)" != 0 ] || is_root=1

# --- helpers ------------------------------------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

fetch() { # url dest
	if have curl; then
		curl -fsSL --retry 3 --proto '=https,http' -o "$2" "$1" || die "download failed: $1"
	elif have wget; then
		wget -q -O "$2" "$1" || die "download failed: $1"
	else
		die "curl or wget is required"
	fi
}

try_fetch() { # url dest (fails quietly)
	if have curl; then
		curl -fsSL --retry 3 --proto '=https,http' -o "$2" "$1" 2>/dev/null
	elif have wget; then
		wget -q -O "$2" "$1" 2>/dev/null
	else
		return 1
	fi
}

sha256_of() { # file
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif have openssl; then
		openssl dgst -sha256 "$1" | sed 's/.*= *//'
	else
		return 1
	fi
}

# port_parts VALUE DEFAULT prints "HOST PORT" ("-" = all interfaces) of an OPENLOG_*_PORT value. Compose publishes
# "${OPENLOG_API_PORT:-8080}:8080", so a value is PORT, HOST:PORT or [IPv6]:PORT.
port_parts() {
	pp_v=${1:-$2}
	case $pp_v in
	\[*\]:*)
		pp_h=${pp_v%%\]:*}
		pp_h=${pp_h#\[}
		pp_p=${pp_v##*\]:}
		;;
	*:*:*) return 1 ;;
	*:*)
		pp_h=${pp_v%:*}
		pp_p=${pp_v##*:}
		;;
	*)
		pp_h=
		pp_p=$pp_v
		;;
	esac
	case $pp_p in "" | *[!0-9]*) return 1 ;; esac
	[ "$pp_p" -ge 1 ] && [ "$pp_p" -le 65535 ] || return 1
	case $pp_h in *[!0-9A-Za-z.:-]*) return 1 ;; esac
	printf '%s %s\n' "${pp_h:--}" "$pp_p"
}

# local_host BIND_HOST prints the address local checks connect to (URL form).
local_host() {
	case $1 in
	"" | - | 0.0.0.0 | ::) printf '127.0.0.1' ;;
	*:*) printf '[%s]' "$1" ;;
	*) printf '%s' "$1" ;;
	esac
}

# is_loopback BIND_HOST: published on the loopback interface only (reachable through a local reverse proxy).
is_loopback() {
	case $1 in
	127.* | ::1 | localhost) return 0 ;;
	esac
	return 1
}

# env_port VARIABLE DEFAULT prints "HOST PORT" of a port setting in .env (HOST "-" = all interfaces); a malformed
# value stops the installer (callers: x=$(env_port ...) under set -e).
env_port() {
	ep_raw=
	[ ! -f "$env_file" ] || ep_raw=$(env_get "$1")
	port_parts "$ep_raw" "$2" || die "$1='$ep_raw' in $env_file: want PORT, HOST:PORT or [IPv6]:PORT"
}

http_get() { # url (prints the body, fails quietly)
	if have curl; then
		curl -fsS --max-time 5 "$1" 2>/dev/null
	elif have wget; then
		wget -q -T 5 -O - "$1" 2>/dev/null
	else
		return 1
	fi
}

rand_hex() { # bytes
	if have openssl; then
		openssl rand -hex "$1"
	else
		od -An -tx1 -N "$1" /dev/urandom | tr -d ' \n'
		echo
	fi
}

rand_base64() { # bytes
	if have openssl; then
		openssl rand -base64 "$1"
	elif have base64; then
		head -c "$1" /dev/urandom | base64 | tr -d '\n'
		echo
	else
		die "openssl or base64 is required to generate OPENLOG_SECRETS_KEY"
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

json_str() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"; }

# .env access. Values are written unquoted when safe, otherwise single-quoted (literal in compose).
env_has() { grep -q "^$1=" "$env_file"; }
env_get() {
	sed -n "s/^$1=//p" "$env_file" | tail -n 1 | sed -e "s/^'\\(.*\\)'\$/\\1/" -e 's/^"\(.*\)"$/\1/'
}
env_set() { # key value
	case $2 in
	*[!A-Za-z0-9._@+/=:,*-]*) line="$1='$2'" ;;
	*) line="$1=$2" ;;
	esac
	etmp="$env_file.tmp.$$"
	(umask 077 && : >"$etmp")
	if env_has "$1"; then
		K=$1 L=$line awk 'index($0, ENVIRON["K"] "=") == 1 { if (!done) print ENVIRON["L"]; done = 1; next } { print }' "$env_file" >"$etmp"
	else
		{
			cat "$env_file"
			printf '%s\n' "$line"
		} >"$etmp"
	fi
	cat "$etmp" >"$env_file"
	rm -f "$etmp"
}

tty_ok() { (exec </dev/tty) 2>/dev/null; }
prompt() { # message -> answer on stdout
	printf '%s' "$1" >/dev/tty
	IFS= read -r answer </dev/tty || answer=
	printf '%s' "$answer"
}
prompt_secret() {
	printf '%s' "$1" >/dev/tty
	stty -echo 2>/dev/null </dev/tty || true
	IFS= read -r answer </dev/tty || answer=
	stty echo 2>/dev/null </dev/tty || true
	printf '\n' >/dev/tty
	printf '%s' "$answer"
}

port_in_use() { # port
	if have ss; then
		ss -Htln 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
	elif have netstat; then
		netstat -tln 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
	else
		return 1
	fi
}

server_ip() {
	ip=
	if have ip; then
		ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1)
	fi
	if [ -z "$ip" ] && have hostname; then
		ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	fi
	printf '%s' "${ip:-<server-ip>}"
}

# --- Docker -------------------------------------------------------------------------------------
tmpdir=$(mktemp -d)

if ! have docker; then
	if [ "$install_docker" = 1 ]; then
		[ "$is_root" = 1 ] || die "--install-docker needs root"
		log "installing Docker Engine with https://get.docker.com"
		fetch https://get.docker.com "$tmpdir/get-docker.sh"
		sh "$tmpdir/get-docker.sh" || die "Docker installation failed"
		if have systemctl; then systemctl enable --now docker >/dev/null 2>&1 || true; fi
	else
		die "Docker Engine is not installed. Install it (https://docs.docker.com/engine/install/, e.g. curl -fsSL https://get.docker.com | sudo sh) or re-run with --install-docker"
	fi
fi
docker info >/dev/null 2>&1 || {
	if [ "$is_root" = 1 ]; then
		die "the Docker daemon is not running (systemctl start docker)"
	fi
	die "cannot talk to the Docker daemon: run as root (sudo) or as a member of the docker group"
}
if ! compose_version=$(docker compose version --short 2>/dev/null); then
	if [ "$install_docker" = 1 ] && [ "$is_root" = 1 ]; then
		log "Docker Compose v2 is missing; installing Docker with https://get.docker.com"
		fetch https://get.docker.com "$tmpdir/get-docker.sh"
		sh "$tmpdir/get-docker.sh" || die "Docker installation failed"
		compose_version=$(docker compose version --short 2>/dev/null) || die "docker compose is still unavailable"
	else
		die "the Docker Compose v2 plugin is missing ('docker compose version' fails). Install docker-compose-plugin (https://docs.docker.com/compose/install/linux/) or re-run with --install-docker"
	fi
fi
case $compose_version in
v1.* | 1.*) die "Docker Compose $compose_version is too old; openlog needs Compose v2.20 or newer" ;;
esac
log "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo '?'), Compose $compose_version"

if [ -r /proc/meminfo ]; then
	mem_mb=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo)
	[ "${mem_mb:-0}" -ge 7500 ] || warn "${mem_mb} MB RAM; 8 GB is the recommended minimum (ClickHouse and Kafka are memory hungry)"
fi
docker_root=$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)
docker_root=${docker_root:-/var/lib/docker}
if [ -d "$docker_root" ]; then
	free_gb=$(df -Pk "$docker_root" 2>/dev/null | awk 'NR == 2 {print int($4 / 1048576)}')
	[ -z "$free_gb" ] || [ "$free_gb" -ge 50 ] || warn "$free_gb GB free under $docker_root; 50 GB is the recommended starting point"
fi

# --- install directory and existing state --------------------------------------------------------
mkdir -p "$dir" 2>/dev/null || die "cannot create $dir (run as root or choose --dir)"
[ -w "$dir" ] || die "$dir is not writable (run as root or choose --dir)"
env_file=$dir/.env
fresh=1
[ ! -f "$env_file" ] || fresh=0

dc() { docker compose -p "$project" -f "$dir/docker-compose.yml" --env-file "$env_file" "$@"; }

running=$(docker ps -q --filter "label=com.docker.compose.project=$project" 2>/dev/null | head -n 1)
admin=$(env_port OPENLOG_ADMIN_PORT 9464)
admin_host=${admin% *}
admin_port=${admin#* }
running_version=
if [ -n "$running" ]; then
	running_version=$(json_str "$(http_get "http://$(local_host "$admin_host"):$admin_port/readyz" | tr -d ' \t\r\n')" version)
fi

# --- resolve the release ------------------------------------------------------------------------
if [ -z "$channel" ]; then
	channel=stable
	[ "$fresh" = 1 ] || channel=$(env_get OPENLOG_UPDATE_CHANNEL)
	case $channel in stable | beta) ;; *) channel=stable ;; esac
fi
if [ -z "$version" ]; then
	log "resolving the latest $channel release from $index_url"
	fetch "$index_url" "$tmpdir/index.json"
	index=$(tr -d ' \t\r\n' <"$tmpdir/index.json")
	case $index in *'"product":"openlog"'*) ;; *) die "$index_url is not an openlog release index" ;; esac
	entries=$(printf '%s' "$index" | sed -n 's/.*"stable":\[\([^]]*\)\].*/\1/p')
	if [ "$channel" = beta ]; then
		entries="$entries},$(printf '%s' "$index" | sed -n 's/.*"beta":\[\([^]]*\)\].*/\1/p')"
	fi
	for e in $(printf '%s' "$entries" | tr '}' '\n'); do
		v=$(json_str "$e" version)
		[ -n "$v" ] || continue
		if [ -z "$version" ] || [ "$(semver_cmp "$v" "$version")" = 1 ]; then version=$v; fi
	done
	[ -n "$version" ] || die "no $channel release found in $index_url"
fi
index_entries=${entries:-}
case $running_version in
"" | 0.0.0-dev*) ;;
*)
	if [ "$(semver_cmp "$running_version" "$version")" = 1 ]; then
		if [ "$explicit_version" = 1 ]; then
			die "openlog $running_version is running; refusing to downgrade to $version (database migrations are not reversible; restore a backup instead, docs/operations/upgrading.md)"
		fi
		log "openlog $running_version is running, newer than $version: keeping it"
		version=$running_version
	fi
	;;
esac
log "openlog $version, install directory $dir, compose project $project"

# --- compose bundle -----------------------------------------------------------------------------
# The release's compose files: docker-compose.yml, .env.example and the ClickHouse config it mounts. Default: the
# release asset openlog-compose-<v>.tar.gz, its sha256 checked against the release's manifest.json (openlog-updater
# later verifies the manifest signature and keeps these files at the running version). Releases without the asset:
# deploy/compose of the tag's source archive.
have tar || die "tar is required"
mkdir -p "$tmpdir/src"
compose_src=
if [ -z "$bundle_url" ]; then
	asset=openlog-compose-$version.tar.gz
	manifest_url=$GITHUB/releases/download/v$version/manifest.json
	for e in $(printf '%s' "$index_entries" | tr '}' '\n'); do
		[ "$(json_str "$e" version)" = "$version" ] || continue
		m=$(json_str "$e" manifest_url)
		[ -z "$m" ] || manifest_url=$m
	done
	asset_url=
	asset_sum=
	if try_fetch "$manifest_url" "$tmpdir/manifest.json"; then
		asset_re=$(printf '%s' "$asset" | sed 's/[.]/\\./g')
		entry=$(tr -d ' \t\r\n' <"$tmpdir/manifest.json" |
			sed -n "s|.*\"name\":\"$asset_re\",\"url\":\"\([^\"]*\)\",\"sha256\":\"\([0-9a-f]\{64\}\)\".*|\1 \2|p")
		asset_url=${entry% *}
		asset_sum=${entry#* }
	fi
	if [ -n "$asset_url" ] && [ -n "$asset_sum" ]; then
		log "downloading the compose files from $asset_url"
		fetch "$asset_url" "$tmpdir/bundle.tar.gz"
		got=$(sha256_of "$tmpdir/bundle.tar.gz") || die "sha256sum, shasum or openssl is required to verify $asset"
		[ "$got" = "$asset_sum" ] || die "$asset: sha256 $got does not match $manifest_url ($asset_sum)"
		tar -xzf "$tmpdir/bundle.tar.gz" -C "$tmpdir/src" || die "cannot extract $asset"
		[ -f "$tmpdir/src/openlog-compose-$version/docker-compose.yml" ] || die "$asset contains no openlog-compose-$version/docker-compose.yml"
		compose_src=$tmpdir/src/openlog-compose-$version
		log "compose files verified (sha256 $asset_sum from manifest.json)"
	else
		bundle_url=$GITHUB/archive/refs/tags/v$version.tar.gz
		log "release $version has no compose bundle asset; using deploy/compose of the source archive"
	fi
fi
if [ -z "$compose_src" ]; then
	log "downloading the compose files from $bundle_url"
	fetch "$bundle_url" "$tmpdir/bundle.tar.gz"
	tar -xzf "$tmpdir/bundle.tar.gz" -C "$tmpdir/src" || die "cannot extract $bundle_url"
	for f in "$tmpdir"/src/openlog-compose-*/docker-compose.yml "$tmpdir"/src/deploy/compose/docker-compose.yml "$tmpdir"/src/*/deploy/compose/docker-compose.yml; do
		if [ -f "$f" ]; then
			compose_src=${f%/docker-compose.yml}
			break
		fi
	done
	[ -n "$compose_src" ] || die "$bundle_url contains no openlog-compose-*/ or deploy/compose/ docker-compose.yml"
fi
[ -f "$compose_src/.env.example" ] || die "the compose files contain no .env.example"
rm -f "$compose_src/.env"
cp -R "$compose_src/." "$dir/"
# These files replace any compose bundle openlog-updater staged or was replacing (it would otherwise restore it).
rm -rf "$dir/.bundle-staging" "$dir/.bundle-swap.json"
mkdir -p "$dir/backups" "$dir/releases"
printf '%s\n' "$version" >"$dir/.bundle-version"
if [ -f "$dir/docker-compose.override.yml" ]; then
	warn "$dir/docker-compose.override.yml is ignored: install-server.sh and openlog-updater use only docker-compose.yml and .env (put settings in .env; data exports have the volume data-exports). Move its settings to .env and remove it"
fi

# --- .env ---------------------------------------------------------------------------------------
if [ "$fresh" = 1 ]; then
	if [ -z "$email" ]; then
		tty_ok || die "--email is required (no terminal to ask)"
		email=$(prompt "Owner (admin) email: ")
	fi
	case $email in ?*@?*.?*) ;; *) die "invalid owner email: $email" ;; esac
	if [ -z "$password" ] && tty_ok; then
		password=$(prompt_secret "Owner password (8-256 characters, empty = generate): ")
		if [ -n "$password" ]; then
			[ "$(prompt_secret "Repeat the password: ")" = "$password" ] || die "the passwords do not match"
		fi
	fi
	if [ -z "$password" ]; then
		password=$(rand_hex 12)
		password_generated=1
	fi
	case $password in *"'"* | *"$nl"*) die "the password must not contain ' or a newline" ;; esac
	if [ "${#password}" -lt 8 ] || [ "${#password}" -gt 256 ]; then die "the owner password must be 8-256 characters"; fi

	(umask 077 && cp "$dir/.env.example" "$env_file")
	chmod 600 "$env_file"
	license_key=olk_$(rand_hex 24)
	env_set OPENLOG_POSTGRES_PASSWORD "$(rand_hex 16)"
	env_set OPENLOG_CLICKHOUSE_PASSWORD "$(rand_hex 16)"
	env_set OPENLOG_SECRETS_KEY "$(rand_base64 32)"
	env_set OPENLOG_BOOTSTRAP_LICENSE_KEY "$license_key"
	env_set OPENLOG_LOADGEN_KEY "$license_key"
	env_set OPENLOG_LICENSE_KEYS "$license_key=default"
	env_set OPENLOG_BOOTSTRAP_OWNER_EMAIL "$email"
	env_set OPENLOG_BOOTSTRAP_OWNER_PASSWORD "$password"
	env_set OPENLOG_UPDATE_CHECK enabled
	env_set OPENLOG_UPDATE_CHANNEL "$channel"
	env_set OPENLOG_UPDATER_MODE "${updater:-auto}"
	env_set OPENLOG_INGEST_CORS_ALLOWED_ORIGINS "$cors"
	log "created $env_file (mode 0600) with generated secrets"
else
	chmod 600 "$env_file"
	log "keeping $env_file (secrets and passwords are never regenerated)"
	[ -z "$email" ] || [ "$email" = "$(env_get OPENLOG_BOOTSTRAP_OWNER_EMAIL)" ] ||
		warn "--email is ignored: the owner already exists; add users in the UI (Settings)"
	[ -z "$password" ] || [ "$password" = "$(env_get OPENLOG_BOOTSTRAP_OWNER_PASSWORD)" ] || warn "--password is ignored: change the password in the UI (Settings -> Security)"
	# Settings added by newer releases. Credentials are never taken from the example: a missing value
	# means the compose default has been in use since the volumes were created.
	while IFS= read -r l; do
		case $l in [A-Z]*=*) ;; *) continue ;; esac
		k=${l%%=*}
		env_has "$k" && continue
		case $k in
		OPENLOG_POSTGRES_PASSWORD | OPENLOG_CLICKHOUSE_* | OPENLOG_SECRETS_KEY | OPENLOG_BOOTSTRAP_* | OPENLOG_LICENSE_KEYS | OPENLOG_LOADGEN_KEY | OPENLOG_IMAGE) continue ;;
		esac
		printf '%s\n' "$l" >>"$env_file"
		log "added $k to $env_file"
	done <"$dir/.env.example"
	# Nothing can be encrypted without a key, so a missing one is safe to generate.
	if [ -z "$(env_get OPENLOG_SECRETS_KEY)" ]; then
		env_set OPENLOG_SECRETS_KEY "$(rand_base64 32)"
		log "generated OPENLOG_SECRETS_KEY"
	fi
	[ -z "$updater" ] || env_set OPENLOG_UPDATER_MODE "$updater"
	[ "$cors_set" = 0 ] || env_set OPENLOG_INGEST_CORS_ALLOWED_ORIGINS "$cors"
	env_set OPENLOG_UPDATE_CHANNEL "$channel"
fi
env_set OPENLOG_IMAGE "$IMAGE_REPO:$version"

ip=$(server_ip)
# Port settings may be HOST:PORT: checks connect to the bound address, URLs use the port only.
api=$(env_port OPENLOG_API_PORT 8080)
otlp=$(env_port OPENLOG_OTLP_HTTP_PORT 4318)
grpc=$(env_port OPENLOG_OTLP_GRPC_PORT 4317)
admin=$(env_port OPENLOG_ADMIN_PORT 9464)
api_host=${api% *} api_port=${api#* }
otlp_host=${otlp% *} otlp_port=${otlp#* }
grpc_host=${grpc% *} grpc_port=${grpc#* }
admin_host=${admin% *} admin_port=${admin#* }
if is_loopback "$api_host" && [ -z "$domain" ]; then
	warn "OPENLOG_API_PORT publishes the UI on $api_host only: put a reverse proxy in front and re-run with --domain"
fi
if [ -n "$domain" ]; then
	env_set OPENLOG_PUBLIC_URL "$public_scheme://$domain"
	if [ "$public_scheme" = https ]; then env_set OPENLOG_COOKIE_SECURE true; else env_set OPENLOG_COOKIE_SECURE false; fi
elif [ "$fresh" = 1 ] || ! env_has OPENLOG_PUBLIC_URL; then
	env_set OPENLOG_PUBLIC_URL "http://$ip:$api_port"
	[ "$fresh" = 0 ] || env_set OPENLOG_COOKIE_SECURE false
fi
updater=$(env_get OPENLOG_UPDATER_MODE)
updater=${updater:-notify}

# --- start --------------------------------------------------------------------------------------
if [ "$start" = 0 ]; then
	log "--no-start: files are ready in $dir"
	log "start: docker compose -p $project -f $dir/docker-compose.yml --env-file $env_file up -d --wait"
	exit 0
fi

if [ -z "$running" ]; then
	for p in "$grpc_port" "$otlp_port" "$api_port" "$admin_port"; do
		! port_in_use "$p" || warn "port $p is already in use; set it in $env_file (OPENLOG_*_PORT) if the start fails"
	done
fi

log "pulling images"
dc --profile updater pull --quiet || die "docker compose pull failed"
log "starting (first start: 1-3 minutes)"
dc up -d --wait --wait-timeout 600 || {
	dc ps -a >&2 || true
	die "the stack did not become healthy; logs: docker compose -p $project -f $dir/docker-compose.yml logs openlog bootstrap"
}
if [ "$updater" = off ]; then
	dc --profile updater rm -sf openlog-updater >/dev/null 2>&1 || true
else
	dc --profile updater up -d --wait --wait-timeout 120 openlog-updater || die "openlog-updater did not start"
fi

readyz_url="http://$(local_host "$admin_host"):$admin_port/readyz"
ready=
i=0
while [ $i -lt 60 ]; do
	ready=$(http_get "$readyz_url" | tr -d ' \t\r\n') && [ -n "$ready" ] && break
	i=$((i + 1))
	sleep 2
done
[ -n "$ready" ] || die "$readyz_url does not answer"
log "ready: $ready"

# --- summary ------------------------------------------------------------------------------------
ui_url=$(env_get OPENLOG_PUBLIC_URL)
dc_cmd="docker compose -p $project -f $dir/docker-compose.yml --env-file $env_file"
direct="http://$ip:$api_port"
if is_loopback "$api_host"; then direct="http://$(local_host "$api_host"):$api_port on this server only"; fi
cat <<EOF

================================================================================================
 openlog $(json_str "$ready" version) is running
================================================================================================
 UI:            $ui_url   (direct: $direct)
 Owner:         $(env_get OPENLOG_BOOTSTRAP_OWNER_EMAIL)
EOF
if [ "$password_generated" = 1 ]; then
	printf ' Password:      %s   (generated, shown once; change it under Settings -> Security)\n' "$password"
fi
cat <<EOF
 License key:   $(env_get OPENLOG_BOOTSTRAP_LICENSE_KEY)
 Configuration: $env_file (secrets; keep a copy with your backups)

 Install the infra agent on each host:
   curl -fsSL $GITHUB/releases/latest/download/install.sh |
     sudo sh -s -- --license-key $(env_get OPENLOG_BOOTSTRAP_LICENSE_KEY) --endpoint http://$ip:$otlp_port

 Updates:       openlog-updater mode '$updater'.
EOF
case $updater in
auto) echo "                New releases are installed automatically (PostgreSQL backup, migrations, health check, rollback)." ;;
notify) echo "                New releases are only reported in the UI; re-run this installer to upgrade." ;;
*) echo "                Disabled; re-run this installer to upgrade." ;;
esac
cat <<EOF
                Re-running the installer upgrades the compose files and keeps $env_file.
 Back up:       cp $env_file <safe place>
                $dc_cmd exec -T postgres pg_dump -U openlog -d openlog -Fc > openlog.dump
                (the updater also keeps dumps in $dir/backups)
 Manage:        $dc_cmd ps | logs -f openlog | down (keeps data)
EOF
api_upstream="$(local_host "$api_host"):$api_port"
otlp_upstream="$(local_host "$otlp_host"):$otlp_port"
if [ -n "$domain" ] && [ "$public_scheme" = https ]; then
	cat <<EOF

 HTTPS: point a TLS reverse proxy for $domain at $api_upstream, and optionally an ingest host at
 $otlp_upstream for agents (then use --endpoint https://<ingest host>). Caddy example (/etc/caddy/Caddyfile):
   $domain {
     reverse_proxy $api_upstream
   }
   ingest.$domain {
     reverse_proxy $otlp_upstream
   }
 The session cookie is Secure: sign in through https://$domain, not the direct URL.
EOF
else
	cat <<EOF

 HTTPS: put a reverse proxy (Caddy, nginx) in front of $api_upstream and $otlp_upstream, then re-run with
 --domain <host> (sets OPENLOG_PUBLIC_URL and OPENLOG_COOKIE_SECURE=true).
EOF
fi
expose_users=$api_port
if is_loopback "$api_host"; then expose_users="nothing directly ($api_port is bound to $api_host: users reach the UI through the reverse proxy)"; fi
expose_hosts=
is_loopback "$grpc_host" || expose_hosts=$grpc_port
is_loopback "$otlp_host" || expose_hosts="$expose_hosts${expose_hosts:+/}$otlp_port"
[ -n "$expose_hosts" ] || expose_hosts="nothing directly (OTLP ports are bound to the loopback interface: use the reverse proxy)"
echo " Firewall:      expose $expose_users to users and $expose_hosts to monitored hosts; keep $admin_port internal."
