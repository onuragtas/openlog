#!/bin/sh
# openlog-php-agent postinstall (deb postinst / rpm %post / apk post-install and post-upgrade).
#
# Points /opt/openlog/php-agent/current at the packaged version and enables openlog.so for the PHP runtimes found on
# the host (openlog-php-install). PHP-FPM and Apache are not restarted: they load the extension on their next reload
# (`openlog-php-install install --reload`). Never fails the package transaction: a runtime that cannot be enabled is
# reported and left as it was. @VERSION@ is replaced by packaging/build-artifacts.sh.
#
# Arguments: deb "configure <old-version>"; rpm "1" (install) or "2" (upgrade); apk none.
set -e

VERSION='@VERSION@'
ROOT=/opt/openlog/php-agent

case "${1:-}" in
abort-upgrade|abort-remove|abort-deconfigure) exit 0 ;;
esac

ln -sfn "versions/$VERSION" "$ROOT/current.new"
mv -f "$ROOT/current.new" "$ROOT/current" 2>/dev/null || { rm -rf "$ROOT/current"; ln -sfn "versions/$VERSION" "$ROOT/current"; rm -f "$ROOT/current.new"; }

# old versions left behind by upgrades (the running PHP workers keep their mapped module file)
for d in "$ROOT"/versions/*; do
  [ -d "$d" ] && [ "${d##*/}" != "$VERSION" ] && rm -rf "$d"
done

if [ "${OPENLOG_PHP_SKIP_ENABLE:-0}" = 1 ]; then
  echo "openlog-php-agent: OPENLOG_PHP_SKIP_ENABLE=1, run 'openlog-php-install install' to enable PHP runtimes"
  exit 0
fi
"$ROOT/current/bin/openlog-php-install" install || \
  echo "openlog-php-agent: some PHP runtimes were not enabled (see above); 'openlog-php-install status' shows the state"
echo "openlog-php-agent: reload PHP-FPM / Apache to load openlog.so (or: openlog-php-install install --reload)"
exit 0
