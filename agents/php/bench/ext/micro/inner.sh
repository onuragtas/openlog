#!/usr/bin/env bash
# Runs inside the micro benchmark container (see run.sh). /src/a (optional) and /src/b are extension source trees.
set -euo pipefail
ROUNDS=${ROUNDS:-11}
CPU_PHP=$(cut -d, -f1 /sys/fs/cgroup/cpuset.cpus.effective 2>/dev/null | cut -d- -f1 || echo 0)
SINK=/tmp/ol.sock

build() { # build <src> <name>
  rm -rf "/tmp/build-$2" && cp -r "$1" "/tmp/build-$2"
  (cd "/tmp/build-$2" && phpize >/dev/null && ./configure --enable-openlog >/dev/null && make -j"$(nproc)" >/dev/null 2>&1) \
    || { echo "build of $1 failed" >&2; exit 1; }
  cp "/tmp/build-$2/modules/openlog.so" "/tmp/$2.so"
}

srcs=()
[ -d /src/a ] && { build /src/a a; srcs+=(a); }
build /src/b b; srcs+=(b)

# datagram sink (blocking receive, discards)
php -n -r '$s = stream_socket_server("udg://'"$SINK"'", $e, $es, STREAM_SERVER_BIND); chmod("'"$SINK"'", 0666);
  while (true) { stream_socket_recvfrom($s, 65536); }' &
sink_pid=$!
sleep 0.5
taskset -pc "$(( CPU_PHP + 1 ))" "$sink_pid" >/dev/null 2>&1 || true

variants=${VARIANTS:-"base $(for s in "${srcs[@]}"; do printf '%s-disabled %s-off %s-on ' "$s" "$s" "$s"; done)b-lean"}

args_for() { # args_for <variant>
  local v=$1 s mode
  [ "$v" = base ] && return 0
  s=${v%%-*}; mode=${v#*-}
  printf '%s\n' -d "extension=/tmp/$s.so" -d "openlog.transport=unix://$SINK" -d openlog.service_name=micro
  case "$mode" in
    disabled) printf '%s\n' -d openlog.enabled=0 ;;
    off) printf '%s\n' -d openlog.transaction_tracer.enabled=0 ;;
    on) ;;
    lean) printf '%s\n' -d openlog.userland_hooks=0 -d openlog.transaction_tracer.enabled=0 ;;
  esac
}

app_env() { # app_env <app>: CGI request environment
  case "$1" in
    plain) echo "SCRIPT_FILENAME=/bench/plain.php REQUEST_URI=/items/7 SCRIPT_NAME=/index.php" ;;
    laravel) echo "SCRIPT_FILENAME=/srv/app/public/index.php REQUEST_URI=/health SCRIPT_NAME=/index.php DOCUMENT_ROOT=/srv/app/public" ;;
  esac
}

run_cgi() { # run_cgi <app> <variant> <n> [prefix...]
  local app=$1 v=$2 n=$3; shift 3
  local -a a; mapfile -t a < <(args_for "$v")
  # shellcheck disable=SC2046
  env -i PATH="$PATH" REDIRECT_STATUS=200 GATEWAY_INTERFACE=CGI/1.1 REQUEST_METHOD=GET SERVER_PROTOCOL=HTTP/1.1 \
    HTTP_HOST=bench.test REMOTE_ADDR=10.0.0.1 HTTP_USER_AGENT=micro $(app_env "$app") \
    taskset -c "$CPU_PHP" "$@" php-cgi -q -T "$n" "${a[@]}" >/dev/null 2>/tmp/cgi.err
}

TSV=/out/time.tsv
echo -e "app\tvariant\tround\tns_per_req" > "$TSV"
for app in $APPS; do
  n=$N_PLAIN; [ "$app" = laravel ] && n=$N_LARAVEL
  vs=($variants)
  for v in "${vs[@]}"; do run_cgi "$app" "$v" 50 || { echo "$app/$v failed:"; cat /tmp/cgi.err; exit 1; }; done # warm-up + sanity
  for r in $(seq 1 "$ROUNDS"); do
    k=$(( (r - 1) % ${#vs[@]} ))
    for v in "${vs[@]:$k}" "${vs[@]:0:$k}"; do
      # process startup (extension load, MINIT, opcache attach) is not per request: subtract a 1-request run
      t0=$(date +%s%N); run_cgi "$app" "$v" 1; t1=$(date +%s%N); run_cgi "$app" "$v" "$n"; t2=$(date +%s%N)
      echo -e "$app\t$v\t$r\t$(( ((t2 - t1) - (t1 - t0)) / (n - 1) ))" >> "$TSV"
    done
  done
done

CG=/out/callgrind.tsv
echo -e "app\tvariant\tir_per_req" > "$CG"
if [ "${CALLGRIND:-1}" = 1 ]; then
  for app in $APPS; do
    n1=20; n2=120
    for v in $variants; do
      ir=()
      for n in $n1 $n2; do
        f="/out/callgrind.$app.$v.$n"
        run_cgi "$app" "$v" "$n" valgrind --tool=callgrind --callgrind-out-file="$f" --dump-instr=no
        ir+=("$(awk '/^summary:/{print $2}' "$f")")
      done
      echo -e "$app\t$v\t$(( (ir[1] - ir[0]) / (n2 - n1) ))" >> "$CG"
      rm -f "/out/callgrind.$app.$v.$n1"
    done
  done
fi
kill "$sink_pid" 2>/dev/null || true
php /bench/summarize.php /out > /out/summary.md
