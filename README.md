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
brew tap miradorlabs/tap
brew trust miradorlabs/tap
brew install mirador
mirador --version
```

`brew trust` is required once. Homebrew will not load a third-party tap until you
trust it — formulae and casks are executable Ruby — and reports the tap as untrusted
rather than installing from it. The choice is recorded in `~/.homebrew/trust.json`.

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

## Endpoints

The CLI talks to three Mirador hosts, and knows which is which:

| Host | Used for |
|---|---|
| `auth.mirador.org` | `login`, `logout`, token refresh, `project list`, `org list` |
| `api.mirador.org` | `trace`, `log`, `metric`, `dashboard`, `metric-alert`, `derived-metric` |
| `app.mirador.org` | the browser page `login` opens |

Credentials are minted on their own host so an auth outage cannot take reads down with it — the
data API validates tokens directly against Redis and never calls the auth host per request.

**Nothing needs configuring.** These are the defaults, and for Mirador's hosted service they are
the only endpoints you need.

Running a self-hosted deployment? Point the CLI at it with environment variables:

```bash
export MIRADOR_API_URL=https://api.example.com
export MIRADOR_AUTH_URL=https://auth.example.com
export MIRADOR_APP_URL=https://app.example.com
```

Or store them on a profile so they persist and you can switch between deployments:

```bash
mirador config use staging
mirador config set --api-url https://api.example.com --auth-url https://auth.example.com
mirador --profile staging whoami
```

Each host is set independently, and a credential is bound to the auth host that minted it — point
the CLI somewhere else and it refuses the old credential rather than sending it to the new host.

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

The CLI covers the API gateway's full surface: every operation in its OpenAPI
document maps to a command. A test (`cmd/parity_test.go`) asserts that each mapped
command exists and is runnable — it guards against a command being dropped or
renamed, not against a command calling the wrong endpoint.

| Command | What it does |
|---|---|
| `mirador login` / `logout` / `whoami` | Manage and inspect this machine's credential |
| `mirador project list \| use \| show` | List and select projects |
| `mirador org list` | Organizations you belong to |
| `mirador trace list \| get \| events \| tags \| attributes` | Query traces and discover filterable keys |
| `mirador log query \| stats \| attributes \| tail` | Query logs, summarize volume, follow live |
| `mirador metric list \| query \| range` | Explore metrics and run PromQL |
| `mirador dashboard list \| get \| apply \| delete` | Manage dashboards |
| `mirador metric-alert list \| get \| apply \| delete` | Manage metric alerts |
| `mirador derived-metric list \| get \| apply \| delete \| dry-run` | Author derived metrics |
| `mirador integration list \| get` | Notification channels alerts can name |
| `mirador config show \| profiles \| use \| set` | Manage configuration profiles |

## Managing resources as files

Dashboards, metric alerts and derived metrics share one contract: a slug is the
identity, and `apply` writes the whole document. Re-applying an unchanged file is a
no-op, so a directory of definitions is safe to apply from CI on every push.

```bash
mirador dashboard apply -f dashboards/payments.yaml     # slug read from the file
mirador metric-alert apply latency -f alerts/latency.yaml
mirador derived-metric dry-run -f metrics/slippage.yaml  # preview before committing
mirador derived-metric apply -f metrics/slippage.yaml
```

The file is YAML or JSON, and carries the resource's own fields. A top-level `slug:`
names it, so files are self-describing:

```yaml
slug: payments
title: Payments
default_time_window: 24h
widgets: []
```

**Writes are conditional, never blind.** `apply` reads the current revision and
replaces exactly that one; if someone else wrote in between, it fails rather than
clobbering them. `delete` works the same way. Both accept `--etag` to assert a
specific revision, and `apply --create` fails if the slug already exists.

Because a replace is a full-document write rather than a patch, an optional field
omitted from the file is cleared. Start from `mirador <resource> get <slug> -o yaml`
when editing something that already exists.

## Following logs

```bash
mirador log tail --filter 'service.name="checkout"' --window 15m
```

Replays the window, then streams live, reconnecting automatically and resuming from
the last record it saw. Delivery is best effort: a burst larger than `--page-size`
drops its middle to stay current, and records can repeat across a reconnect. Use
`mirador log query` when completeness matters.

## Output

`-o table` (default on a terminal), `json`, `yaml`, or `csv`.

Output defaults to JSON when stdout is not a terminal or when an agent harness is detected
(`CLAUDECODE`, `CURSOR_TRACE_ID`, and similar), because a piped or agent-driven invocation
almost always wants to parse the result. `-o` always wins.

The table view is a summary; `-o json` returns the complete object, including fields no
column shows. When a result is truncated or paged, the CLI says so on stderr rather than
letting a partial answer look complete.

## Configuration

Resolution order for every setting: **flag → environment variable → active profile → built-in
default**. The built-in default is always Mirador production.

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

Profiles keep separate selections side by side, each with **its own credential** — which is what
makes switching between them safe rather than merely convenient:

```bash
mirador config use research      # creates the profile if new
mirador login                    # this credential belongs to this profile
mirador project use checkout

mirador config use default       # back, with its own login and project
```

A credential records the host that issued it. Point a profile at a different deployment and the
CLI says so instead of sending the old token to the new host and reporting a confusing 401:

```
Error: this profile is logged in against https://auth.example.com but is now pointed at
https://auth.mirador.org — run `mirador login` for this deployment, or switch profiles with
`mirador config use <profile>`
```

## Development

```bash
make check    # fmt + vet + test
make build
```

The auth flow, credential handling, config precedence, and project matching are covered by
`go test ./...`; the login test drives the real loopback listener end to end.

### Editor hooks

`.claude/hooks/` holds two PostToolUse hooks that run on every Go edit, mirroring
`mirador-platform`: `go-fix.sh` applies the `go fix` modernizers to the edited
file's package, then `format-go.sh` runs goimports. Registered in
`.claude/settings.json`, so formatting is enforced rather than remembered.

Both are best-effort by design — a missing tool, a malformed payload, or a
mid-refactor package that will not compile is a silent no-op. A hook that failed
would block the edit that triggered it, which is far worse than unformatted code
that `make check` catches anyway.

`.claude/settings.local.json` is gitignored for per-developer overrides.

## Releasing

Tagging is the whole process — `.github/workflows/release.yml` runs GoReleaser, which
builds every platform, publishes the GitHub Release, and pushes the Homebrew cask.

```bash
git tag v0.1.0 && git push origin v0.1.0
make release-dry-run   # build the archives locally first, publishing nothing
```

Two things must exist before the first release:

1. **`miradorlabs/homebrew-tap`** — the repo must exist, but nothing needs to be in it.
   GoReleaser commits `Casks/mirador.rb` through the GitHub contents API, which creates
   the directory on the way; Homebrew needs nothing else to serve
   `brew install miradorlabs/tap/mirador`. The tap already hosts `hush` as a formula,
   and the two coexist — different directory, different name.
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
