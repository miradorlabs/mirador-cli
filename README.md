# mirador-cli

The command-line client for the [Mirador](https://mirador.org) API — traces, OpenTelemetry
logs, PromQL metrics, and dashboards, from your terminal.

```console
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

### Homebrew (macOS and Linux)

```bash
brew install miradorlabs/tap/mirador
mirador --version
```

Upgrades are `brew upgrade mirador`.

### Direct download

Every release publishes static binaries for macOS, Linux and Windows on
[Releases](https://github.com/miradorlabs/mirador-cli/releases), with a `checksums.txt`
alongside them:

```bash
VERSION=v0.1.0
OS=$(uname -s)            # Darwin | Linux
ARCH=$(uname -m)          # arm64 | x86_64
curl -sSL -o mirador.tar.gz \
  "https://github.com/miradorlabs/mirador-cli/releases/download/${VERSION}/mirador_${OS}_${ARCH}.tar.gz"
tar -xzf mirador.tar.gz mirador
sudo mv mirador /usr/local/bin/
```

CGO is off, so the binary is static — no matching libc required.

### From source

```bash
make install    # → $(go env GOPATH)/bin/mirador, on your PATH
make build      # → ./bin/mirador, for hacking on it
```

`make install` places the binary explicitly rather than using `go install`, which would
name it `mirador-cli` after the module path.

### Shell completions

Homebrew wires these up automatically. Otherwise:

```bash
mirador completion zsh  > "${fpath[1]}/_mirador"          # zsh
mirador completion bash > /etc/bash_completion.d/mirador  # bash
mirador completion fish > ~/.config/fish/completions/mirador.fish
```

## Endpoints and environments

The CLI talks to three hosts, and knows which is which:

| Host | Used for |
|---|---|
| `auth.mirador.org` | `login`, `logout`, token refresh, `project list`, `org list` |
| `api.mirador.org` | `trace`, `log`, `metric`, `dashboard` |
| `app.mirador.org` | the browser page `login` opens |

Credentials are minted on their own host so an auth outage cannot take reads down with it — the
data API validates tokens directly against Redis and never calls the auth host per request.

**Production is the default.** Nothing needs configuring to use Mirador.

Working on Mirador itself? Select an environment and all three hosts move together:

```bash
mirador config envs           # what is available
mirador --env dev whoami      # one command against dev
```

| Environment | Auth | API | App |
|---|---|---|---|
| `prod` *(default)* | `auth.mirador.org` | `api.mirador.org` | `app.mirador.org` |
| `dev` | `auth-dev.mirador.org` | `api-dev.mirador.org` | `beta.mirador.org` |
| `local` | `localhost:8057` | `localhost:8055` | `localhost:3000` |

`prd`, `production`, `development` and `localhost` are accepted as aliases.

They move together because they are only meaningful together — a credential minted by one
environment's auth host is worthless to another's data host. Picking hosts one at a time is how
you end up authenticating against prod while reading dev.

For anything not in that table, set the hosts individually; an explicit URL always beats the
environment, so you can point one host at a laptop without abandoning the preset for the other two:

```bash
mirador --env dev --api-url http://localhost:8055 trace list
```

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
| `mirador config show \| profiles \| use \| set \| envs` | Manage profiles and environments |

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

Resolution order for every setting: **flag → environment variable → active profile → environment
preset**. The preset supplies the baseline endpoints; anything more specific overrides it.

`~/.mirador/config.json` (`0644`) holds endpoints and selections.
`~/.mirador/credentials.json` (`0600`) holds tokens. They are separate files so you can
share, diff, or version the config without dragging credentials along.

| Environment variable | Effect |
|---|---|
| `MIRADOR_API_KEY` | Use a `mir_srv_*` server key; skips the browser entirely |
| `MIRADOR_ENV` | Environment preset: `prod` (default), `dev`, `local` |
| `MIRADOR_API_URL` | Data API base URL (traces, logs, metrics, dashboards) |
| `MIRADOR_AUTH_URL` | Auth API base URL (login, refresh, projects, organizations) |
| `MIRADOR_APP_URL` | App base URL used by `login` |
| `MIRADOR_PROFILE` | Profile to use |
| `MIRADOR_PROJECT_ID` | Project to scope to |
| `MIRADOR_ORGANIZATION_ID` | Organization to scope to |
| `MIRADOR_CONFIG_DIR` | Override `~/.mirador` |

Profiles keep environments side by side, each with **its own credential** — which is what makes
switching between them safe rather than merely convenient:

```bash
mirador config use dev-work      # creates the profile if new
mirador config set --env dev
mirador login                    # this credential belongs to this profile

mirador config use default       # back to prod, with its own login
```

A credential records the host that issued it. Point a profile somewhere else and the CLI says so
instead of sending a dev token to prod and reporting a confusing 401:

```
Error: this profile is logged in against https://auth-dev.mirador.org but is now pointed at
https://auth.mirador.org — run `mirador login` for this environment, or switch profiles with
`mirador config use <profile>`
```

`mirador config set --env <name>` clears any per-URL overrides stored on that profile, since they
outrank the environment and would otherwise silently defeat the switch.

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

To drive it by hand instead, use the `local` environment — `http` is accepted for loopback only,
so this preset is the one place cleartext is allowed:

```bash
./bin/mirador --env local login --no-browser
# or, to make it stick for a profile:
./bin/mirador config use localdev && ./bin/mirador config set --env local
```

**Tear down:** `docker rm -f mirador-local`.

## Releasing

Tagging is the whole process — `.github/workflows/release.yml` runs GoReleaser, which
builds every platform, publishes the GitHub Release, and pushes the Homebrew cask.

```bash
git tag v0.1.0 && git push origin v0.1.0
make release-dry-run   # build the archives locally first, publishing nothing
```

Two things must exist before the first release:

1. **`miradorlabs/homebrew-tap`** — a repo with a `Casks/` directory. GoReleaser commits
   `Casks/mirador.rb` into it; Homebrew needs nothing else to serve
   `brew install miradorlabs/tap/mirador`.
2. **`HOMEBREW_TAP_TOKEN`** — a repository secret holding a PAT with `contents:write` on
   the tap. The workflow's default `GITHUB_TOKEN` is scoped to this repo and cannot push
   there. Without it the release still publishes and only the tap update fails, so a
   missing token costs you a re-run, not a broken release.

While this repo is private, `brew install` needs the tap public *and* the release assets
reachable — Homebrew downloads them unauthenticated. Public tap plus private releases will
fail at download. Until the repo is public, `make install` and direct downloads by
authenticated users are the working paths.

## Contract

`CONTRACT.md` is the normative description of the auth flow and the API surface it depends
on — the shared reference for this CLI, the API gateway, and the browser approval page.

## License

MIT — see `LICENSE`. Note that `mirador-platform` is licensed differently (FSL-1.1); this
CLI matches the SDKs, since like them it is client-side code you are expected to install
and build against.
