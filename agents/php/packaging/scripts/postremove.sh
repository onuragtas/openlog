#!/bin/sh
# openlog-php-agent postremove (deb postrm / rpm %postun / apk post-deinstall): after a removal (not an upgrade) the
# `current` link and version directories left by upgrades are removed.
#
# Arguments: deb "remove" | "purge" | "upgrade" | …; rpm "0" (erase) or "1" (upgrade); apk the package version
# (post-deinstall only runs on removal).
set -e

case "${1:-remove}" in
remove|purge|0) ;;
*.*|*-r[0-9]*) ;; # apk: version
*) exit 0 ;;
esac

rm -f /opt/openlog/php-agent/current
rm -rf /opt/openlog/php-agent/versions
rmdir /opt/openlog/php-agent 2>/dev/null || true
exit 0
