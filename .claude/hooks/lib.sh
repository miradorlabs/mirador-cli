#!/usr/bin/env bash
# Shared helper for the PostToolUse hooks.
#
# Both hooks need the same thing: the list of files an edit touched, pulled out of
# the tool_input JSON on stdin. mirador-platform duplicates this parser in each
# hook; keeping it in one place here means the two cannot drift apart.
#
# Tool input shapes handled:
#   Edit / Write  → tool_input.file_path
#   MultiEdit     → tool_input.file_path (single-file form), and/or
#                   tool_input.edits[].file_path (cross-file form)
#
# Parser: jq first, python3 as a fallback, no-op if neither exists. Formatting is
# best-effort — a missing parser should never block an edit.

extract_paths() {
  local out
  if command -v jq >/dev/null 2>&1; then
    if out=$(printf '%s' "$1" | jq -r '
      [
        .tool_input.file_path?,
        ( .tool_input.edits? // [] | .[]?.file_path? )
      ]
      | map(select(. != null and . != ""))
      | .[]
    ' 2>/dev/null); then
      printf '%s' "$out"
      return 0
    fi
  fi
  if command -v python3 >/dev/null 2>&1; then
    if out=$(printf '%s' "$1" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
    ti = d.get("tool_input", {}) or {}
    fp = ti.get("file_path")
    if fp:
        print(fp)
    for e in (ti.get("edits") or []):
        f = (e or {}).get("file_path")
        if f:
            print(f)
except Exception:
    pass
' 2>/dev/null); then
      printf '%s' "$out"
      return 0
    fi
  fi
  return 1
}

# go_files reads paths on stdin and emits the .go ones that exist, deduplicated.
go_files() {
  awk 'NF && !seen[$0]++' | while IFS= read -r p; do
    [ -z "$p" ] && continue
    [ ! -f "$p" ] && continue
    case "$p" in
      *.go) printf '%s\n' "$p" ;;
    esac
  done
}
