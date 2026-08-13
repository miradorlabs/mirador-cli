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

## Contract

`CONTRACT.md` is the normative description of the auth flow and the API surface it depends
on — the shared reference for this CLI, the API gateway, and the browser approval page.
