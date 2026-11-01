#!/bin/sh
# Stop hook: ends a turn once with a request to run the docs-check skill, when the code differs from the upstream branch and
# that difference has not been asked about yet. Documentation is left out of the hash, so writing it does not ask again.
input=$(cat)
case "$input" in *'"stop_hook_active":true'*|*'"stop_hook_active": true'*) exit 0 ;; esac

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0
paths='internal cmd agents web/src schema migrations deploy Dockerfile'
excludes="':(exclude)*_test.go' ':(exclude)*.gen.ts' ':(exclude)*.md' ':(exclude)*.meta'"

tracked=$(eval "git diff @{u} -- $paths $excludes" 2>/dev/null) || tracked=$(eval "git diff HEAD -- $paths $excludes" 2>/dev/null)
untracked=$(eval "git ls-files --others --exclude-standard -- $paths $excludes" 2>/dev/null)
[ -z "$tracked$untracked" ] && exit 0

hash=$(printf '%s\n%s' "$tracked" "$untracked" | git hash-object --stdin)
state="$(git rev-parse --git-dir)/openlog-docs-check"
[ "$(cat "$state" 2>/dev/null)" = "$hash" ] && exit 0
printf '%s\n' "$hash" > "$state"

cat <<'JSON'
{"decision":"block","reason":"Code changed and its documentation has not been checked. Run the docs-check skill now and write what is missing. If nothing is missing, or the work is still in progress and will be checked when finished, say so in one line."}
JSON
