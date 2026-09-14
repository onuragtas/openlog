#!/usr/bin/env bash
# Builds and runs the libFuzzer target for src/ol_text.c (JSON writer, UTF-8 cleaning/truncation, SQL sanitizing,
# path/route normalization, traceparent) in a clang container with ASan + UBSan. No PHP needed.
#
#   agents/php/ext/fuzz/run.sh                     # 60 s
#   FUZZ_SECONDS=3600 JOBS=4 agents/php/ext/fuzz/run.sh
#
# The corpus and crash artifacts are kept in $OUT (default ${TMPDIR:-/tmp}/openlog-php-fuzz). A crash is reproduced
# with the same command plus the artifact path: REPRO=$OUT/crash-<sha1> agents/php/ext/fuzz/run.sh
set -euo pipefail
FUZZ_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
EXT_DIR=$(cd "$FUZZ_DIR/.." && pwd)
OUT=${OUT:-${TMPDIR:-/tmp}/openlog-php-fuzz}
IMAGE=${IMAGE:-openlog-php-fuzz:clang}
mkdir -p "$OUT/corpus"

# seeds: selector byte, parameter byte, text
seed() { printf "$2" > "$OUT/corpus/seed-$1"; }
seed utf8 '\x00\x40h\xc3\xa9llo "w\xc3\xb6rld"\n\t\x01\x7f\xe2\x82 \xf0\x9f\x98\x80 \xed\xa0\x80'
seed utf8max '\x00\xc0plain ascii text that is not truncated'
seed sql '\x01\x10SELECT * FROM users WHERE id = 42 AND name = '"'"'o'"''"'x'"'"' -- comment\n AND x = 0x1F /* c */'
seed sqldollar '\x01\x31SELECT $tag$ body $tag$, $1, E'"'"'\\n'"'"', 1.5e-3 FROM t'
seed op '\x02\x55INSERT IGNORE INTO `shop`.`orders` (a) VALUES (1)'
seed path '\x03\x20/users/123/orders/550e8400-e29b-41d4-a716-446655440000/deadbeefcafe1234/a@b.io?x=1'
seed route '\x04\x33^/wp/v2/posts/(?P<id>[\\d]+)/(:num)/(?<slug>[a-z-]+)$'
seed tp '\x05\x00 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01 '
seed num '\x06\x00\xff\xff\xff\xff\xff\xff\xff\x7f'

docker build -q -t "$IMAGE" - >/dev/null <<'EOF'
FROM debian:trixie
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends clang libclang-rt-dev \
    && rm -rf /var/lib/apt/lists/*
EOF

docker run --rm --name "openlog-php-fuzz-$$" -v "$EXT_DIR":/ext:ro -v "$OUT":/out \
  -e SECS="${FUZZ_SECONDS:-60}" -e JOBS="${JOBS:-1}" -e REPRO="${REPRO:+/out/$(basename "${REPRO:-x}")}" "$IMAGE" sh -ec '
  clang -g -O1 -fsanitize=fuzzer,address,undefined -fno-sanitize-recover=undefined -Wall -Wextra -Wno-unused-parameter \
    -I/ext/src /ext/fuzz/fuzz_text.c /ext/src/ol_text.c -o /tmp/fuzz_text
  if [ -n "$REPRO" ]; then exec /tmp/fuzz_text "$REPRO"; fi
  export UBSAN_OPTIONS=print_stacktrace=1:halt_on_error=1
  /tmp/fuzz_text -max_total_time="$SECS" -max_len=8192 -timeout=10 -jobs="$JOBS" -workers="$JOBS" \
    -artifact_prefix=/out/ -print_final_stats=1 /out/corpus
'
