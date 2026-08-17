#!/usr/bin/env bash
# End-to-end check of the CLI login flow against a locally-running stack.
#
# Exercises every real component: the CLI binary, the auth gateway, the API gateway, the
# frontend BFF, the web gateway, and account-api. The only thing simulated is the
# browser's click — this calls the same Express endpoint the /cli/auth page calls, then
# performs the same loopback redirect the page performs. That keeps the seam between the
# page and the backend under test, which is the part unit and integration tests miss.
#
# Prerequisites (see README "Testing against a local stack"):
#   - platform container running with the HMAC verifier
#   - scripts/local-seed.sh has been run
#   - frontend BFF on :3001 with GRPC_USE_SSL=false
#
# Usage: scripts/local-e2e.sh
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CLI="${MIRADOR_BIN:-$REPO/bin/mirador}"
BFF="${MIRADOR_LOCAL_BFF:-http://localhost:3001}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export MIRADOR_CONFIG_DIR="$WORK/home"
export MIRADOR_AUTH_URL="${MIRADOR_LOCAL_AUTH_URL:-http://localhost:8057}"
export MIRADOR_API_URL="${MIRADOR_LOCAL_API_URL:-http://localhost:8055}"
export MIRADOR_APP_URL="${MIRADOR_LOCAL_APP_URL:-http://localhost:3000}"
unset MIRADOR_API_KEY
mkdir -p "$MIRADOR_CONFIG_DIR"

step() { printf '\n\033[1m── %s\033[0m\n' "$1"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$1"; exit 1; }

[ -x "$CLI" ] || fail "no CLI binary at $CLI — run 'make build'"
ORG_ID=$(cat "${TMPDIR:-/tmp}/mirador-local-org-id" 2>/dev/null) \
  || fail "no seeded org — run scripts/local-seed.sh"
[ -n "$ORG_ID" ] || fail "seeded org id is empty — re-run scripts/local-seed.sh"

JWT=$(node "$REPO/scripts/local-jwt.mjs")
auth_hdr=(-H "Authorization: Bearer $JWT" -H "Content-Type: application/json")

step "1. the seeded identity resolves through the web gateway"
LOGON=$(curl -sS "${auth_hdr[@]}" -X POST "$BFF/api/logon" -d '{}')
echo "$LOGON" | grep -q "$ORG_ID" || fail "logon did not return the seeded org: $LOGON"
echo "organization resolved: $ORG_ID"

step "2. mirador login (browser handoff simulated below)"
"$CLI" login --no-browser --label local-e2e > "$WORK/login.out" 2> "$WORK/login.err" &
LOGIN_PID=$!
for _ in $(seq 1 60); do
  URL=$(grep -o "${MIRADOR_APP_URL}/cli/auth?[^ ]*" "$WORK/login.err" 2>/dev/null | head -1)
  [ -n "${URL:-}" ] && break
  sleep 0.2
done
[ -n "${URL:-}" ] || { kill $LOGIN_PID 2>/dev/null; fail "no authorize URL: $(cat "$WORK/login.err")"; }

qs() { python3 -c "
import urllib.parse as u,sys
print(u.parse_qs(u.urlparse(sys.argv[1]).query)['$1'][0])" "$URL"; }
CHALLENGE=$(qs challenge); STATE=$(qs state); PORT=$(qs port)
echo "challenge=${CHALLENGE:0:12}...  port=$PORT"

step "3. the approval page's call: BFF -> web gateway -> account API"
STATUS=$(curl -sS -o "$WORK/authz.json" -w '%{http_code}' "${auth_hdr[@]}" \
  -X POST "$BFF/api/cli/authorize" \
  -d "{\"organizationId\":\"$ORG_ID\",\"codeChallenge\":\"$CHALLENGE\",\"redirectPort\":$PORT,\"label\":\"local-e2e\"}")
[ "$STATUS" = "200" ] || { kill $LOGIN_PID 2>/dev/null; fail "authorize returned $STATUS: $(cat "$WORK/authz.json")"; }
CODE=$(python3 -c "import json;print(json.load(open('$WORK/authz.json'))['code'])")
echo "code minted: ${CODE:0:16}..."

step "4. the redirect the page performs"
curl -sS "http://127.0.0.1:$PORT/callback?code=$CODE&state=$STATE" -o /dev/null
wait $LOGIN_PID || fail "login failed: $(cat "$WORK/login.err")"
cat "$WORK/login.out"

step "5. credential file is 0600"
PERM=$(stat -f '%Lp' "$MIRADOR_CONFIG_DIR/credentials.json" 2>/dev/null \
       || stat -c '%a' "$MIRADOR_CONFIG_DIR/credentials.json")
[ "$PERM" = "600" ] || fail "credentials.json is mode $PERM, want 600"
echo "mode $PERM"

step "6. whoami / project list / project use  (auth gateway)"
"$CLI" whoami -o table || fail "whoami"
"$CLI" project list -o table || fail "project list"
"$CLI" project use "${MIRADOR_LOCAL_PROJECT_NAME:-checkout}" || fail "project use"

step "7. a project-scoped read  (API gateway)"
"$CLI" trace list -o table || fail "trace list"

step "8. the two hosts stay in their lanes"
OUT=$("$CLI" --project 00000000-0000-0000-0000-000000000000 trace list 2>&1) \
  && fail "expected an unknown project to be denied"
echo "$OUT" | grep -qi "not accessible" || fail "expected a permission denial, got: $OUT"
echo "unknown project denied"

step "9. refresh rotates without the user noticing"
BEFORE=$(python3 -c "import json;print(json.load(open('$MIRADOR_CONFIG_DIR/credentials.json'))['default']['access_token'])")
python3 - "$MIRADOR_CONFIG_DIR/credentials.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p))
d['default']['expires_at']='2020-01-01T00:00:00Z'
json.dump(d,open(p,'w'))
PY
"$CLI" whoami >/dev/null || fail "whoami after forced expiry"
AFTER=$(python3 -c "import json;print(json.load(open('$MIRADOR_CONFIG_DIR/credentials.json'))['default']['access_token'])")
[ "$BEFORE" != "$AFTER" ] || fail "access token was not rotated"
echo "rotated ${BEFORE: -8} -> ${AFTER: -8}"

step "10. logout revokes server-side"
"$CLI" logout || fail "logout"
OUT=$("$CLI" whoami 2>&1) && fail "expected whoami to fail after logout"
echo "$OUT"

printf '\n\033[32mAll steps passed against the local stack.\033[0m\n'
