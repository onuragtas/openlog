# Shared helpers for the spike scripts (sourced). Run from anywhere.
PHP_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ROOT=$(cd "$PHP_DIR/../.." && pwd)
RESULTS="$PHP_DIR/spike/results"
# Always an explicit project name: never touch the shared `openlog` stack.
DC=(docker compose -p openlog-phpspike -f "$PHP_DIR/spike/docker-compose.spike.yml")
# Shared local openlog (compose project `openlog`): Query API + owner login for reading traces back.
API=${OPENLOG_API:-http://127.0.0.1:8080}
OPENLOG_EMAIL=${OPENLOG_EMAIL:-admin@openlog.local}
OPENLOG_PASSWORD=${OPENLOG_PASSWORD:-openlog-dev-password}
COOKIES="$RESULTS/.openlog-session"

# cgroup v2 CPU time (microseconds) of a compose service container
cpu_usec() { docker exec "openlog-phpspike-$1-1" awk '/^usage_usec/{print $2}' /sys/fs/cgroup/cpu.stat; }

# "<workers>,<avg VmRSS kB>" of the PHP-FPM workers of a pool. /proc/<pid>/status is readable without
# CAP_SYS_PTRACE (smaps_rollup of www-data workers is not, even for root inside the container).
fpm_mem() {
  docker exec "openlog-phpspike-php-$1-1" sh -c '
    n=0; sum=0
    for p in /proc/[0-9]*; do
      if tr "\0" " " < $p/cmdline 2>/dev/null | grep -q "pool '"$2"'"; then
        kb=$(awk "/^VmRSS:/{print \$2}" $p/status 2>/dev/null)
        if [ -n "$kb" ]; then n=$((n + 1)); sum=$((sum + kb)); fi
      fi
    done
    [ "$n" -gt 0 ] && echo "$n,$((sum / n))" || echo "0,0"'
}

k6() { "${DC[@]}" run --rm -T k6 "$@"; }

wait_http() {
  for _ in $(seq 1 120); do
    if curl -fsS -m 3 -o /dev/null "$1" 2>/dev/null; then return 0; fi
    sleep 1
  done
  echo "timeout waiting for $1" >&2; return 1
}

port_of() { case "$1" in base) echo 28801;; a) echo 28802;; b) echo 28803;; esac; }
