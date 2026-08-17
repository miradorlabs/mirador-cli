#!/usr/bin/env bash
# Seed a user, organization and project into a locally-running mirador stack.
#
# Why this exists: the obvious path — POST /api/logon, which auto-provisions — needs
# Clerk to look up the new user's email, and the local stack runs with Clerk disabled
# so the HMAC test verifier can stand in for a browser session. An *existing* user with
# an active org skips that Clerk call entirely, so seeding one directly is what makes
# the rest of the flow work offline.
#
# Writes to both Postgres and Redis on purpose. Inserting rows alone is not enough: the
# gateways authorize projects against the Redis directory that the Account API normally
# writes on mutation, and a SQL insert bypasses that. Miss it and every project-scoped
# read 403s while the row plainly exists.
#
# Usage: scripts/local-seed.sh [container-name]   (default: mirador-local)
set -euo pipefail

CONTAINER="${1:-mirador-local}"
SUB_ID="${MIRADOR_LOCAL_SUB_ID:-local-cli-test-user}"
ORG_NAME="${MIRADOR_LOCAL_ORG_NAME:-Local Test Org}"
PROJECT_NAME="${MIRADOR_LOCAL_PROJECT_NAME:-checkout}"

docker ps --filter "name=^${CONTAINER}$" --format '{{.Names}}' | grep -q . || {
  echo "error: container '${CONTAINER}' is not running" >&2
  exit 1
}

lower_uuid() { uuidgen | tr 'A-Z' 'a-z'; }
ORG_ID=$(lower_uuid)
PROJECT_ID=$(lower_uuid)
USER_ID=$(lower_uuid)

# The fullstack image's Postgres superuser is `postgres`; there is no `mirador` role.
docker exec -i "$CONTAINER" psql -U postgres -d mirador -v ON_ERROR_STOP=1 -q <<SQL
INSERT INTO account.organizations (id, name, organization_status)
  VALUES ('${ORG_ID}', '${ORG_NAME}', 'activated');

INSERT INTO account.users (id, sub_id)
  VALUES ('${USER_ID}', '${SUB_ID}')
  ON CONFLICT (sub_id) DO NOTHING;

INSERT INTO account.organization_users (user_id, organization_id, role, status)
  SELECT id, '${ORG_ID}', 'admin', 'activated'
  FROM account.users WHERE sub_id = '${SUB_ID}';

INSERT INTO account.organization_projects (id, organization_id, created_by, name, status)
  SELECT '${PROJECT_ID}', '${ORG_ID}', id, '${PROJECT_NAME}', 'active'
  FROM account.users WHERE sub_id = '${SUB_ID}';
SQL

# Mirror the directory writes the Account API makes on mutation.
docker exec "$CONTAINER" redis-cli -n 0 SET \
  "account:org:${ORG_ID}" \
  "{\"id\":\"${ORG_ID}\",\"name\":\"${ORG_NAME}\"}" >/dev/null
docker exec "$CONTAINER" redis-cli -n 0 SET \
  "account:project:${PROJECT_ID}" \
  "{\"id\":\"${PROJECT_ID}\",\"organization_id\":\"${ORG_ID}\",\"name\":\"${PROJECT_NAME}\"}" >/dev/null

echo "seeded:"
echo "  sub_id       ${SUB_ID}"
echo "  organization ${ORG_ID}  (${ORG_NAME})"
echo "  project      ${PROJECT_ID}  (${PROJECT_NAME})"

# Emitted for scripts/local-e2e.sh to pick up without re-querying.
printf '%s' "$ORG_ID" > "${TMPDIR:-/tmp}/mirador-local-org-id"
printf '%s' "$PROJECT_ID" > "${TMPDIR:-/tmp}/mirador-local-project-id"
