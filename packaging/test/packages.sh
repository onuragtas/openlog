#!/bin/sh
# Installs the openlog-infra-agent .deb in debian:12 and the .rpm in rockylinux:9 and checks the
# layout (docs/contracts/releases-updates.md §3). systemd does not run in the containers, so the
# checks cover files, the `current` symlink, the account, the unit file and `-version`.
#
#   packaging/test/packages.sh dist/v0.9.0 0.9.0 [dist/v0.10.0-beta.1 0.10.0-beta.1]
#
# The optional second release is used for an upgrade test. Containers are removed afterwards.
set -eu

[ $# -ge 2 ] || {
	echo "usage: $0 RELEASE_DIR VERSION [UPGRADE_RELEASE_DIR UPGRADE_VERSION]" >&2
	exit 2
}
dir1=$(cd "$1" && pwd)
v1=$2
dir2=
v2=
if [ $# -ge 4 ]; then
	dir2=$(cd "$3" && pwd)
	v2=$4
fi
arch=$(docker version --format '{{.Server.Arch}}')
case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac

DEBIAN_IMAGE=${DEBIAN_IMAGE:-debian:12}
ROCKY_IMAGE=${ROCKY_IMAGE:-rockylinux:9}

# Shell run inside the container. $1 = deb|rpm.
# shellcheck disable=SC2016
checks='
set -eu
fmt=$1
R=/opt/openlog/infra-agent
say() { printf "\n--- %s\n" "$*"; }
pkg_install() {
	if [ "$fmt" = deb ]; then dpkg --force-confdef --force-confold -i "$1"; else rpm -U --replacepkgs "$1"; fi
}
pkg1=/r1/openlog-infra-agent_${V1}_linux_${ARCH}.$fmt

say "install $pkg1"
pkg_install "$pkg1"
say "layout"
readlink $R/current
test "$(readlink $R/current)" = "versions/$V1"
ls -la $R $R/versions/$V1
getent passwd openlog-agent
test "$(stat -c %U:%G $R/versions/$V1/openlog-infra-agent)" = openlog-agent:openlog-agent
test -s $R/versions/$V1/manifest.json && test -s $R/versions/$V1/manifest.json.sig && echo "signed manifest present"
grep -q "\"version\": \"$V1\"" $R/versions/$V1/manifest.json
if [ "$fmt" = deb ]; then dpkg -S /opt/openlog/infra-agent | grep -q "^openlog-infra-agent:" && echo "dpkg owner of /opt/openlog/infra-agent: openlog-infra-agent"; else rpm -qf /opt/openlog/infra-agent; fi
test -f /usr/lib/systemd/system/openlog-infra-agent.service
grep -E "^(User|ExecStart)=" /usr/lib/systemd/system/openlog-infra-agent.service
stat -c "%n %U:%G %a" /etc/openlog-infra-agent/config.yaml /var/lib/openlog-infra-agent
test "$(stat -c %G:%a /etc/openlog-infra-agent/config.yaml)" = openlog-agent:640
if [ -e /etc/systemd/system/multi-user.target.wants/openlog-infra-agent.service ]; then echo "unit enabled"; else echo "systemctl not available: unit not enabled (expected without systemd)"; fi
say "-version"
/usr/bin/openlog-infra-agent -version
$R/current/openlog-infra-agent -version | grep -F "$V1"

say "reinstall keeps an edited config"
echo "# local edit" >> /etc/openlog-infra-agent/config.yaml
pkg_install "$pkg1"
grep -q "# local edit" /etc/openlog-infra-agent/config.yaml && echo "config kept"

say "reinstall keeps a newer self-updated version"
mkdir -p $R/versions/99.0.0 && cp $R/versions/$V1/openlog-infra-agent $R/versions/99.0.0/
ln -sfn versions/99.0.0 $R/current
pkg_install "$pkg1"
test "$(readlink $R/current)" = versions/99.0.0 && echo "current still versions/99.0.0"
rm -rf $R/versions/99.0.0
ln -sfn versions/$V1 $R/current

if [ -n "$V2" ]; then
	pkg2=/r2/openlog-infra-agent_${V2}_linux_${ARCH}.$fmt
	say "upgrade to $pkg2"
	pkg_install "$pkg2"
	test "$(readlink $R/current)" = "versions/$V2"
	test ! -e $R/versions/$V1/openlog-infra-agent
	ls $R/versions
	/usr/bin/openlog-infra-agent -version | grep -F "$V2"
	grep -q "# local edit" /etc/openlog-infra-agent/config.yaml && echo "config kept across upgrade"
fi

say "remove"
if [ "$fmt" = deb ]; then dpkg -r openlog-infra-agent; else rpm -e openlog-infra-agent; fi
test ! -e $R/current && test ! -e /usr/bin/openlog-infra-agent && echo "binaries removed"
ls /etc/openlog-infra-agent/
ls /etc/openlog-infra-agent/config.yaml* >/dev/null && echo "config kept (rpm: .rpmsave when modified)"
if [ "$fmt" = deb ]; then
	say "purge"
	dpkg -P openlog-infra-agent
	test ! -e /etc/openlog-infra-agent && test ! -e /var/lib/openlog-infra-agent && test ! -e /opt/openlog && echo "purged"
fi
echo "PASS $fmt"
'

run() { # image fmt
	name=openlog-pkgtest-$2-$$
	echo "=== $2 in $1 (linux/$arch)"
	set -- "$1" "$2" -v "$dir1:/r1:ro"
	[ -z "$dir2" ] || set -- "$@" -v "$dir2:/r2:ro"
	img=$1
	fmt=$2
	shift 2
	docker run --rm --name "$name" -e V1="$v1" -e V2="$v2" -e ARCH="$arch" "$@" "$img" sh -c "$checks" sh "$fmt"
}

run "$DEBIAN_IMAGE" deb
run "$ROCKY_IMAGE" rpm
echo "all package tests passed"
