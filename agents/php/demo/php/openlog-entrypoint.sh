#!/bin/sh
# Writes the openlog extension ini from the environment, then runs the container command.
#   OPENLOG_AGENT          on (default) | off (extension loaded, transaction tracer disabled) | none (not loaded)
#   OPENLOG_SERVICE_NAME   service.name of this container
#   OPENLOG_TRANSPORT      default unix:///run/openlog-infra-agent/php.sock
#   OPENLOG_TT_THRESHOLD_MS transaction tracer threshold (default 500)
#   OPENLOG_INI_EXTRA      extra ini lines (newline separated), appended verbatim
# Images built with OPENLOG_EXT=0 have no openlog.so: the ini stays empty and the apps run uninstrumented.
set -e
ini_dir=${PHP_INI_DIR:-/usr/local/etc/php}/conf.d
ini="$ini_dir/zz-openlog.ini"
ext_dir=$(php -r 'echo ini_get("extension_dir");' 2>/dev/null || true)
mode=${OPENLOG_AGENT:-on}

if [ "$mode" = none ]; then
  echo "; openlog disabled (OPENLOG_AGENT=none)" > "$ini"
  echo "[openlog-entrypoint] extension not loaded (OPENLOG_AGENT=none)" >&2
elif [ ! -f "$ext_dir/openlog.so" ]; then
  echo "; openlog.so not installed (image built with OPENLOG_EXT=0)" > "$ini"
  echo "[openlog-entrypoint] openlog.so not present in $ext_dir (built with OPENLOG_EXT=0): running uninstrumented" >&2
else
  tracer=1
  [ "$mode" = off ] && tracer=0
  {
    echo "extension = openlog.so"
    echo "openlog.enabled = 1"
    echo "openlog.service_name = \"${OPENLOG_SERVICE_NAME:-php-app}\""
    echo "openlog.environment = \"${OPENLOG_ENVIRONMENT:-demo}\""
    echo "openlog.transport = \"${OPENLOG_TRANSPORT:-unix:///run/openlog-infra-agent/php.sock}\""
    echo "openlog.transaction_tracer.enabled = $tracer"
    echo "openlog.transaction_tracer.threshold_ms = ${OPENLOG_TT_THRESHOLD_MS:-500}"
    [ -n "$OPENLOG_INI_EXTRA" ] && printf '%s\n' "$OPENLOG_INI_EXTRA"
  } > "$ini"
  echo "[openlog-entrypoint] openlog.so enabled: service=${OPENLOG_SERVICE_NAME:-php-app} tracer=$tracer" >&2
fi
exec "$@"
