#!/usr/bin/env bash
# Builds and tests the openlog Java agent in containers (see docker-compose.yml next to this script).
#   agents/java/test/run.sh                          ./gradlew check integrationTest
#   agents/java/test/run.sh :extension:test          any Gradle arguments
#   NO_SERVICES=1 agents/java/test/run.sh :extension:test   without databases/Kafka
#   agents/java/test/run.sh bench                    overhead micro-benchmark
#   agents/java/test/run.sh down                     remove containers and volumes (Gradle cache included)
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# JAVA_TEST_PROJECT: compose project and volume name prefix (default openlog-m4-java-test / openlog-m4-java-*)
compose=(docker compose -p "${JAVA_TEST_PROJECT:-openlog-m4-java-test}" -f "$here/docker-compose.yml")

if [ "${1:-}" = down ]; then
  "${compose[@]}" --profile runner down -v --remove-orphans
  exit 0
fi
[ $# -gt 0 ] || set -- check integrationTest
if [ -z "${NO_SERVICES:-}" ]; then
  "${compose[@]}" up -d --wait postgres mysql redis kafka
fi
"${compose[@]}" --profile runner run --rm --no-deps runner "$@"
