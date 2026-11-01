#!/bin/sh
# Stop hook: ends a turn once with a request to run the docs-check and parity-check skills, when the code differs from the upstream
# branch and that difference has not been asked about yet. Documentation is left out of the hash, so writing it does not ask again.
input=$(cat)
case "$input" in *'"stop_hook_active":true'*|*'"stop_hook_active": true'*) exit 0 ;; esac

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0
excludes="':(exclude)*_test.go' ':(exclude)*.gen.ts' ':(exclude)*.g.dart' ':(exclude)*.md' ':(exclude)*.meta'"

# What differs under these paths, tracked and untracked.
changed() {
  tracked=$(eval "git diff @{u} -- $1 $excludes" 2>/dev/null) || tracked=$(eval "git diff HEAD -- $1 $excludes" 2>/dev/null)
  untracked=$(eval "git ls-files --others --exclude-standard -- $1 $excludes" 2>/dev/null)
  [ -z "$tracked$untracked" ] && return 0
  printf '%s\n%s' "$tracked" "$untracked" | git hash-object --stdin
}

# Says whether this hash is new for this check, and remembers it.
is_new() {
  [ -n "$2" ] || return 1
  state="$(git rev-parse --git-dir)/openlog-$1"
  [ "$(cat "$state" 2>/dev/null)" = "$2" ] && return 1
  printf '%s\n' "$2" > "$state"
}

reason=""
if is_new docs-check "$(changed 'internal cmd agents web/src mobile/lib schema migrations deploy Dockerfile')"; then
  reason="Code changed and its documentation has not been checked. Run the docs-check skill now and write what is missing."
fi
if is_new parity-check "$(changed 'web/src mobile/lib docs/contracts/openapi.yaml')"; then
  reason="$reason A console or the API changed and web and mobile parity has not been checked. Run the parity-check skill now and build what this change left missing."
fi
[ -z "$reason" ] && exit 0
printf '{"decision":"block","reason":"%s If nothing is missing, or the work is still in progress and will be checked when finished, say so in one line."}\n' "${reason# }"
