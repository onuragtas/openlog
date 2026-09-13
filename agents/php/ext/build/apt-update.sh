#!/bin/sh
# apt-get update that falls back to archive.debian.org for EOL Debian releases (php:7.1 = buster, php:7.4 = bullseye).
set -e
if apt-get update 2>/dev/null; then exit 0; fi
echo "apt-get update failed; switching to archive.debian.org" >&2
for f in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
  [ -f "$f" ] || continue
  sed -i -E 's#https?://(deb|security)\.debian\.org#http://archive.debian.org#g; /-updates/d' "$f"
done
echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99-archive
apt-get update
