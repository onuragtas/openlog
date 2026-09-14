#!/bin/sh
# Install test of the openlog-php-agent packages built by packaging/build-artifacts.sh, with distribution PHP:
#   deb  debian:12       php8.2-cli php8.2-fpm php8.2-cgi (mods-available / phpenmod layout)
#   rpm  almalinux:9     php-cli php-fpm (PHP 8.0, /etc/php.d)
#   apk  alpine:3.20     php83 php83-fpm (musl, /etc/php83/conf.d)
# Each: install -> openlog loaded by every SAPI binary, status --json, openlog\stats() works; reinstall (upgrade path)
# -> still loaded; remove -> no ini file left, PHP still starts. Needs modules 8.2-nts-glibc, 8.0-nts-glibc and
# 8.3-nts-musl in the packages.
#
#   agents/php/packaging/test.sh dist/v0.9.1 0.9.1 [deb rpm apk]
set -eu
[ $# -ge 2 ] || { echo "usage: $0 DIR VERSION [deb] [rpm] [apk]" >&2; exit 2; }
dir=$(cd "$1" && pwd)
version=$2
shift 2
formats=${*:-deb rpm apk}
arch=$(docker version --format '{{.Server.Arch}}')
case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
pkg="openlog-php-agent_${version}_linux_$arch"

# shellcheck disable=SC2016
checks='
set -eu
say() { printf "\n--- %s\n" "$*"; }
loaded() { for b in $BINS; do "$b" -m | grep -qx openlog || { echo "openlog not loaded by $b"; "$b" -m | head -3; exit 1; }; echo "$b: openlog loaded"; done; }
say "install"; eval "$INSTALL"
loaded
openlog-php-install status --json
php -r "var_dump(extension_loaded(\"openlog\"), is_array(openlog\\stats()));" | tr "\n" " "; echo
ls -l /opt/openlog/php-agent
say "reinstall (upgrade path)"; eval "$REINSTALL"
loaded
say "remove"; eval "$REMOVE"
left=$(grep -rl "managed by openlog-php-install" /etc 2>/dev/null || true)
[ -z "$left" ] || { echo "ini files left: $left"; exit 1; }
for b in $BINS; do "$b" -m >/dev/null; if "$b" -m | grep -qx openlog; then echo "$b still loads openlog"; exit 1; fi; done
[ ! -e /opt/openlog/php-agent/current ] || { echo "current link left"; exit 1; }
echo "PASS"
'

status=0
for fmt in $formats; do
  case $fmt in
    deb)
      image=debian:12
      setup='apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq php8.2-cli php8.2-fpm php8.2-cgi >/dev/null'
      bins='/usr/bin/php8.2 /usr/sbin/php-fpm8.2 /usr/bin/php-cgi8.2'
      install="dpkg -i /pkg/$pkg.deb"; reinstall="dpkg -i /pkg/$pkg.deb"; remove="dpkg -r openlog-php-agent" ;;
    rpm)
      image=almalinux:9
      setup='dnf install -y -q php-cli php-fpm >/dev/null'
      bins='/usr/bin/php /usr/sbin/php-fpm'
      install="rpm -i /pkg/$pkg.rpm"; reinstall="rpm -U --replacepkgs /pkg/$pkg.rpm"; remove="rpm -e openlog-php-agent" ;;
    apk)
      image=alpine:3.20
      setup='apk add --no-cache -q php83 php83-fpm >/dev/null'
      bins='/usr/bin/php83 /usr/sbin/php-fpm83'
      install="apk add --allow-untrusted -q /pkg/$pkg.apk"; reinstall="apk fix -q openlog-php-agent"; remove="apk del -q openlog-php-agent" ;;
    *) echo "unknown format $fmt"; exit 2 ;;
  esac
  echo "=== $fmt ($image)"
  if docker run --rm --name "openlog-php-pkgtest-$fmt-$$" -v "$dir":/pkg:ro -e BINS="$bins" -e INSTALL="$install" \
      -e REINSTALL="$reinstall" -e REMOVE="$remove" "$image" sh -c "$setup && command -v php >/dev/null || ln -s /usr/bin/php83 /usr/bin/php; $checks"; then
    echo "=== $fmt: PASS"
  else
    echo "=== $fmt: FAIL"; status=1
  fi
done
exit $status
