# mirador-cli

The command-line client for the [Mirador](https://mirador.org) API — traces, OpenTelemetry
logs, PromQL metrics, and dashboards, from your terminal.

Replaces the deprecated Node CLI (`mirador-cli-deprecated`). The headline difference: you
log in through your browser instead of pasting an API key, and one credential reaches every
project in your organization.

```
$ mirador login
Opening your browser to authorize the CLI.
Waiting for authorization...

Logged in as you@example.com in Acme (org_01H…).
Next: `mirador project list` to see what you can read.

$ mirador project list
  NAME              ID                                    DESCRIPTION
  checkout          3f2b…                                 Storefront checkout flow
* payments          9a71…                                 Payment orchestration

$ mirador project use checkout
Now using project checkout (3f2b…).

$ mirador trace list --filter 'status="running"' --since 1h
```

## Install

```bash
go install github.com/miradorlabs/mirador-cli@latest
```

Or build from source:

```bash
make build      # → bin/mirador
make dist       # cross-compiled release binaries
```

## Two endpoints

The CLI talks to two hosts, and knows which is which:

| Host | Used for |
|---|---|
| `auth.mirador.org` | `login`, `logout`, token refresh, `project list`, `org list` |
| `api.mirador.org` | `trace`, `log`, `metric`, `dashboard` |

Credentials are minted on their own host so an auth outage cannot take reads down with it — the
data API validates tokens directly against Redis and never calls the auth host per request.

Both default correctly; override with `--auth-url` / `--api-url`, the matching `MIRADOR_*`
environment variables, or `mirador config set`.

## Authentication

### Browser login (people)

`mirador login` runs an OAuth 2.0 authorization-code flow with PKCE:

1. The CLI generates a code verifier, binds a listener to `127.0.0.1` on a free port, and
   opens `https://app.mirador.org/cli/auth` with the verifier's SHA-256 hash.
2. You sign in (or reuse your existing session), pick an organization, and approve.
3. The browser redirects back to the loopback listener with a single-use code, which the
   CLI trades for tokens by presenting the verifier.

The verifier never travels through the browser, so a code captured from browser history, a
shoulder-surfed URL, or a shared machine's referer log is not redeemable on its own. Codes
expire in 60 seconds, are single-use, and are bound to the port they were issued for.

Credentials land in `~/.mirador/credentials.json` (mode `0600`). The access token lasts an
hour and the CLI refreshes it silently; the refresh token rotates on every use, so a stolen
copy stops working as soon as the real client refreshes.

On a headless box, `mirador login --no-browser` prints the URL to open elsewhere. The
loopback listener still has to be reachable from the browser, so for a remote host forward
the port (`ssh -L`) or use a server key instead.

### Server key (CI and agents)

Set `MIRADOR_API_KEY` to a `mir_srv_*` key and the browser flow is skipped entirely:

```bash
export MIRADOR_API_KEY=mir_srv_...
mirador trace list --filter 'severity="error"' -o json
```

A server key is bound to one project, so `mirador project use` does not apply — the key
already decided. This is the right choice for CI, cron, and agent runners.

### Logging out

`mirador logout` revokes the session server-side before deleting the local file. Deleting
`~/.mirador/credentials.json` by hand leaves a live token behind; use the command.

## Projects

A user credential is scoped to an organization, not a project, which is what makes
switching cheap:

```bash
mirador project list          # everything you can read
mirador project use payments  # by name, unique prefix, or id
mirador project use           # interactive picker on a terminal
mirador project show          # what is selected now
mirador --project <id> trace list   # one-off override
```

The selection is stored in the active profile. Every project-scoped request sends it as the
`X-Mirador-Project` header, and the gateway rejects any project outside your organization.

## Commands

| Command | What it does |
|---|---|
| `mirador login` / `logout` / `whoami` | Manage and inspect this machine's credential |
| `mirador project list \| use \| show` | List and select projects |
| `mirador org list` | Organizations you belong to |
| `mirador trace list \| get \| events \| tags` | Query traces |
| `mirador log query \| stats` | Query OpenTelemetry logs |
| `mirador metric list \| query \| range` | Explore metrics and run PromQL |
| `mirador dashboard list \| get` | Read dashboards |
| `mirador config show \| profiles \| use \| set` | Manage profiles |

Dashboard writes are deliberately absent. They are destructive and ETag-guarded; a mistyped
CLI argument should not be able to replace a dashboard a team depends on.

## Output

`-o table` (default on a terminal), `json`, `yaml`, or `csv`.

Output defaults to JSON when stdout is not a terminal or when an agent harness is detected
(`CLAUDECODE`, `CURSOR_TRACE_ID`, and similar), because a piped or agent-driven invocation
almost always wants to parse the result. `-o` always wins.

The table view is a summary; `-o json` returns the complete object, including fields no
column shows. When a result is truncated or paged, the CLI says so on stderr rather than
letting a partial answer look complete.

## Configuration

Resolution order for every setting: **flag → environment → active profile → default**.

`~/.mirador/config.json` (`0644`) holds endpoints and selections.
`~/.mirador/credentials.json` (`0600`) holds tokens. They are separate files so you can
share, diff, or version the config without dragging credentials along.

| Environment variable | Effect |
|---|---|
| `MIRADOR_API_KEY` | Use a `mir_srv_*` server key; skips the browser entirely |
| `MIRADOR_API_URL` | Data API base URL (traces, logs, metrics, dashboards) |
| `MIRADOR_AUTH_URL` | Auth API base URL (login, refresh, projects, organizations) |
| `MIRADOR_APP_URL` | App base URL used by `login` |
| `MIRADOR_PROFILE` | Profile to use |
| `MIRADOR_PROJECT_ID` | Project to scope to |
| `MIRADOR_ORGANIZATION_ID` | Organization to scope to |
| `MIRADOR_CONFIG_DIR` | Override `~/.mirador` |

Profiles keep environments side by side:

```bash
mirador config use local
mirador config set \
  --api-url  http://localhost:8055 \
  --auth-url http://localhost:8057 \
  --app-url  http://localhost:3000
mirador login
```

## Development

```bash
make check    # fmt + vet + test
make build
```

The auth flow, credential handling, config precedence, and project matching are covered by
`go test ./...`; the login test drives the real loopback listener end to end.

## Testing against a local stack

`go test` covers the CLI in isolation. To exercise the whole login flow — CLI, auth gateway,
API gateway, the frontend BFF, the web gateway and account-api, all real — run the stack
locally. No deploy required.

**1. Patch the unreleased proto stubs into the frontend.** The web-gateway npm package only
publishes on merge to `main`, so a branch that adds an RPC cannot be run against until then:

```bash
cd ../mirador-platform && ./scripts/build/generate-gateway-stubs-local.sh
```

**2. Boot the platform.** Clerk is switched off so the HMAC test verifier can stand in for a
browser session — the gateway *refuses* to start in HMAC mode with `CLERK_SECRET_KEY` set, so
this can never be pointed at a real environment by accident:

```bash
cd ../mirador-platform
docker build -f build/fullstack/Dockerfile -t mirador-platform:latest .
docker run -d --name mirador-local --env-file .env \
  -e MIRADOR_TEMPLATE=mirador-local \
  -e CLERK_SECRET_KEY= \
  -e WEB_GATEWAY_TOKEN_VERIFIER=hmac \
  -e WEB_GATEWAY_HMAC_SECRET=test-jwt-secret-for-integration-tests \
  -e WEB_GATEWAY_HMAC_ISSUER=mirador-integration-tests \
  -e WEB_GATEWAY_HMAC_AUDIENCE=mirador-web-gateway \
  -e METRIC_OTLP_ENDPOINT= \
  -p 8051:8051 -p 8052:8052 -p 8055:8055 -p 8057:8057 -p 9092:9092 -p 9000:9000 -p 8123:8123 \
  mirador-platform:latest

# Kafka and ClickHouse make this slow; wait for both gateways rather than guessing.
until curl -sf localhost:8057/health && curl -sf localhost:8055/health; do sleep 5; done
```

**3. Seed an identity.** `POST /api/logon` auto-provisions, but only via Clerk — which is off.
Seeding an existing user skips that path:

```bash
cd ../mirador-cli && ./scripts/local-seed.sh
```

**4. Start the frontend BFF.** `GRPC_USE_SSL=false` because the local gateway is plaintext, and
the pnpm flag skips a dependency gate that fails while the stub version is unpublished:

```bash
cd ../mirador-frontend
GRPC_BASE_URL_WEB=localhost:8052 GRPC_USE_SSL=false PORT=3001 \
  pnpm --config.verify-deps-before-run=false server:dev
```

**5. Run the flow.**

```bash
cd ../mirador-cli && make build && ./scripts/local-e2e.sh
```

It drives login through the same Express endpoint the `/cli/auth` page calls, then performs the
same loopback redirect, and asserts credential file permissions, project scoping, cross-tenant
denial, silent refresh, and revoke-on-logout.

To drive it by hand instead, point the CLI at the local hosts — `http` is accepted for loopback
only:

```bash
export MIRADOR_AUTH_URL=http://localhost:8057 \
       MIRADOR_API_URL=http://localhost:8055 \
       MIRADOR_APP_URL=http://localhost:3000
./bin/mirador login --no-browser
```

**Tear down:** `docker rm -f mirador-local`.

## Contract

`CONTRACT.md` is the normative description of the auth flow and the API surface it depends
on — the shared reference for this CLI, the API gateway, and the browser approval page.
