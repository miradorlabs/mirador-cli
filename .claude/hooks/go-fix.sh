#!/usr/bin/env bash
# PostToolUse hook: apply `go fix` modernizers to the package(s) of edited Go files.
#
# Scoped to each edited file's package rather than the whole module, so it stays
# fast enough to run on every edit. Registered to run BEFORE format-go.sh, because
# the modernizers add imports (slices, maps) that goimports then has to reconcile.
#
# Best-effort: a non-compiling package or a missing `go` is a silent no-op. A hook
# that fails an edit because the code is mid-refactor would be worse than useless.

set -u

command -v go >/dev/null 2>&1 || exit 0

hook_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=lib.sh
. "$hook_dir/lib.sh"

# Claude Code sets CLAUDE_PROJECT_DIR, but the repo root is also two levels above
# this script — deriving it keeps the hook runnable by hand (and testable) without
# emitting an unbound-variable error under `set -u`.
project_dir="${CLAUDE_PROJECT_DIR:-$(cd "$hook_dir/../.." && pwd)}"

input="$(cat)"
paths="$(extract_paths "$input")" || exit 0
[ -z "$paths" ] && exit 0

# go fix is package-scoped, so reduce the edited files to their directories and run
# once per directory rather than once per file.
dirs="$(printf '%s\n' "$paths" | go_files | while IFS= read -r p; do dirname "$p"; done | awk 'NF && !seen[$0]++')"
[ -z "$dirs" ] && exit 0

while IFS= read -r d; do
  [ -z "$d" ] && continue
  ( cd "$project_dir" && go fix "$d" >/dev/null 2>&1 ) || true
done <<< "$dirs"

exit 0
