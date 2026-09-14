#!/usr/bin/env bash
# Regenerates test/interop/go-sampler-fixtures.json from the Go agent's sampler (agents/go/sampler.go).
# The generator test is copied into a temporary copy of agents/go, so agents/go itself is not modified.
# Requires Go (version of agents/go/go.mod). Run after changing either agent's sampling code.
set -euo pipefail

node_dir="$(cd "$(dirname "$0")/.." && pwd)"
go_dir="$(cd "$node_dir/../go" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cp -R "$go_dir/." "$tmp/"
cp "$node_dir/test/interop/go/fixtures_gen_test.go.txt" "$tmp/fixtures_gen_test.go"
(
  cd "$tmp"
  OPENLOG_NODE_FIXTURES="$node_dir/test/interop/go-sampler-fixtures.json" \
    go test -tags openlog_node_fixtures -run TestGenerateNodeFixtures -count=1 .
)
echo "wrote $node_dir/test/interop/go-sampler-fixtures.json"
