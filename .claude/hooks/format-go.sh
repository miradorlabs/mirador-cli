#!/usr/bin/env bash
# PostToolUse hook: auto-format Go files after edits.
#
# Mirrors the `make check` formatting step so it is enforced rather than advisory,
# and runs after go-fix.sh so any imports the modernizers introduced get grouped
# correctly. Unlike mirador-platform there is no proto branch — this repo has none.
#
# Best-effort: a missing formatter is a silent no-op.

set -u

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

input="$(cat)"
paths="$(extract_paths "$input")" || exit 0
[ -z "$paths" ] && exit 0

printf '%s\n' "$paths" | go_files | while IFS= read -r p; do
  # goimports is a superset of gofmt and also fixes the import block, so prefer it.
  if command -v goimports >/dev/null 2>&1; then
    goimports -w "$p" >/dev/null 2>&1 || true
  elif command -v gofmt >/dev/null 2>&1; then
    gofmt -w "$p" >/dev/null 2>&1 || true
  fi
done

exit 0
