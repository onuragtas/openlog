#!/usr/bin/env bash
# Runs every `-tags integration` Go test package against throwaway Docker dependencies (make integration,
# .github/workflows/long-tests.yml; docs/operations/ci.md).
#
#   test/integration/run.sh [shared|self|renderer|all]      (default all)
#
# shared    PostgreSQL 16 + single-node ClickHouse cluster "openlog" (hostname clickhouse, for ON CLUSTER DDL) from
#           internal/usage/testdata/docker-compose.yml, compose project openlog-inttest (host ports 19010, 18130,
#           27441). Packages that read OPENLOG_TEST_POSTGRES_DSN / OPENLOG_TEST_CLICKHOUSE_ADDR run one at a time,
#           each with its own PostgreSQL database; ClickHouse is shared (the tests seed unique tenant ids).
# self      packages that start and remove their own compose project (openlog-pgtest, -alerttest, -fleettest,
#           -shardtest, -apmshard); run without the DSN variables, one package at a time.
# renderer  builds the openlog-renderer image (Dockerfile target renderer) and runs internal/renderer/chrome.
#
# INTTEST_KEEP=1 keeps the shared project running afterwards.
set -euo pipefail

cd "$(dirname "$0")/../.."
phase=${1:-all}
project=openlog-inttest
compose=(docker compose -p "$project" -f internal/usage/testdata/docker-compose.yml)
gotest=(go test -tags integration -count=1 -timeout 20m)

# DSN/ClickHouse packages (shared dependencies).
shared_pkgs=(
	./internal/deletion
	./internal/dataexport
	./internal/usage
	./internal/quota
	./internal/oql
	./internal/api
	./internal/dashboard
	./internal/savedview
	./internal/operator
	./internal/updatereq
)
# Packages that start their own compose project when OPENLOG_TEST_POSTGRES_DSN is unset.
self_pkgs=(
	./internal/store/postgres
	./internal/alert
	./internal/fleet
	./internal/processor
	./internal/apm
)

failed=()

run_shared() {
	"${compose[@]}" up -d --wait --wait-timeout 300
	for pkg in "${shared_pkgs[@]}"; do
		db=inttest_$(basename "$pkg")
		"${compose[@]}" exec -T postgres psql -q -U openlog -d openlog -c "DROP DATABASE IF EXISTS $db" -c "CREATE DATABASE $db"
		echo "::group::$pkg"
		if ! OPENLOG_TEST_POSTGRES_DSN="postgres://openlog:openlog@127.0.0.1:27441/$db?sslmode=disable" \
			OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19010 "${gotest[@]}" "$pkg"; then
			failed+=("$pkg")
			"${compose[@]}" logs --no-color --tail 100
		fi
		echo "::endgroup::"
	done
	if [ "${INTTEST_KEEP:-}" != 1 ]; then "${compose[@]}" down -v --remove-orphans; fi
}

run_self() {
	for pkg in "${self_pkgs[@]}"; do
		echo "::group::$pkg"
		env -u OPENLOG_TEST_POSTGRES_DSN -u OPENLOG_TEST_CLICKHOUSE_ADDR "${gotest[@]}" "$pkg" || failed+=("$pkg")
		echo "::endgroup::"
	done
}

run_renderer() {
	echo "::group::./internal/renderer/chrome"
	docker build -q --target renderer -t openlog-renderer:inttest .
	OPENLOG_TEST_RENDERER_IMAGE=openlog-renderer:inttest "${gotest[@]}" ./internal/renderer/chrome || failed+=(./internal/renderer/chrome)
	echo "::endgroup::"
}

case $phase in
shared) run_shared ;;
self) run_self ;;
renderer) run_renderer ;;
all)
	run_shared
	run_self
	run_renderer
	;;
*)
	echo "usage: $0 [shared|self|renderer|all]" >&2
	exit 2
	;;
esac

if [ ${#failed[@]} -gt 0 ]; then
	echo "FAILED packages: ${failed[*]}" >&2
	exit 1
fi
echo "integration tests passed ($phase)"
