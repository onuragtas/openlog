#!/bin/sh
# openlog-php-agent preremove (deb prerm / rpm %preun / apk pre-deinstall): on removal (not on upgrade) the ini files
# written by openlog-php-install are removed, so no PHP runtime references the module files that go away next.
#
# Arguments: deb "remove" | "upgrade" | …; rpm "0" (erase) or "1" (upgrade); apk the package version (pre-deinstall
# only runs on removal; upgrades use pre/post-upgrade).
set -e

case "${1:-remove}" in
remove|purge|0) ;;
*.*|*-r[0-9]*) ;; # apk: version
*) exit 0 ;;
esac

if [ -x /opt/openlog/php-agent/current/bin/openlog-php-install ]; then
  /opt/openlog/php-agent/current/bin/openlog-php-install uninstall || true
fi
exit 0
