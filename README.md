```text
███╗   ███╗██╗██████╗  █████╗ ██████╗  ██████╗ ██████╗      ██████╗██╗     ██╗
████╗ ████║██║██╔══██╗██╔══██╗██╔══██╗██╔═══██╗██╔══██╗    ██╔════╝██║     ██║
██╔████╔██║██║██████╔╝███████║██║  ██║██║   ██║██████╔╝    ██║     ██║     ██║
██║╚██╔╝██║██║██╔══██╗██╔══██║██║  ██║██║   ██║██╔══██╗    ██║     ██║     ██║
██║ ╚═╝ ██║██║██║  ██║██║  ██║██████╔╝╚██████╔╝██║  ██║    ╚██████╗███████╗██║
╚═╝     ╚═╝╚═╝╚═╝  ╚═╝╚═╝  ╚═╝╚═════╝  ╚═════╝ ╚═╝  ╚═╝     ╚═════╝╚══════╝╚═╝
```

# mirador-cli

[![CI](https://github.com/miradorlabs/mirador-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/miradorlabs/mirador-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/miradorlabs/mirador-cli)](https://github.com/miradorlabs/mirador-cli/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

The command-line client for the [Mirador](https://mirador.org) API — traces, OpenTelemetry
logs, PromQL metrics, and dashboards, from your terminal.

## Quick start

You need a Mirador account — sign up at [mirador.org](https://mirador.org). Then
[install the CLI](#install) and:

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
  TRACE ID    NAME                       STATUS     DURATION   CREATED
  9c41…       checkout.settlement        running    12.4s      2026-08-25T09:14:03+02:00
  b7e2…       checkout.bridge-transfer   running    3.1s       2026-08-25T09:15:47+02:00
```

The filter language and time flags are covered in
[Filters and time windows](#filters-and-time-windows).

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
VERSION=v0.1.0            # check Releases for the latest tag
OS=$(uname -s)            # Darwin | Linux
ARCH=$(uname -m)          # arm64 | x86_64
curl -sSL -O \
  "https://github.com/miradorlabs/mirador-cli/releases/download/${VERSION}/mirador_${OS}_${ARCH}.tar.gz"
curl -sSL -O \
  "https://github.com/miradorlabs/mirador-cli/releases/download/${VERSION}/checksums.txt"
shasum -a 256 --check --ignore-missing checksums.txt
tar -xzf "mirador_${OS}_${ARCH}.tar.gz" mirador
sudo mv mirador /usr/local/bin/
```

CGO is off, so the binary is static — no matching libc required.

On Windows, download `mirador_Windows_x86_64.zip` (or `arm64`) from Releases, check it
against `checksums.txt`, and put `mirador.exe` somewhere on your `PATH`.

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
# zsh — any directory on your fpath; add this one in ~/.zshrc before compinit:
#   fpath=(~/.zsh/completions $fpath)
mkdir -p ~/.zsh/completions
mirador completion zsh > ~/.zsh/completions/_mirador

# bash — the user-level completions directory, no root needed
mkdir -p ~/.local/share/bash-completion/completions
mirador completion bash > ~/.local/share/bash-completion/completions/mirador

# fish
mirador completion fish > ~/.config/fish/completions/mirador.fish
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
| `mirador telemetry connect \| status \| disconnect` | Point an agent harness at Mirador |
| `mirador config show \| profiles \| use \| set` | Manage configuration profiles |

## Agent telemetry

`mirador telemetry` configures an agent CLI to export OpenTelemetry to Mirador, so the
traces you read with `mirador trace list` include the agent sessions that produced the
work. Claude Code and Codex are supported.

```bash
mirador telemetry connect claude
mirador telemetry connect codex
```

It mints a server key scoped to the selected project — or, when this project already
has one installed here, reuses it rather than minting an orphan — and writes the OTLP
configuration into the harness's own settings. Nothing is added to your shell profile,
every other setting in that file is preserved, and a `.mirador.bak` copy of the original
is written alongside it.

| Harness | Config written | Where the key lives |
|---------|----------------|---------------------|
| Claude Code | `env` block of `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`) | a `0700` helper script under `~/.mirador/helpers/`; the settings file gets only its path |
| Codex | `[otel]` table of `~/.codex/config.toml` (or `$CODEX_HOME`) | inline in `config.toml`, which is tightened to `0600` |

**For Claude Code the key never enters `settings.json`.** It lands in the helper script,
and the settings file gets only the script's path via Claude Code's `otelHeadersHelper`
mechanism — Claude runs the script to fetch the `Authorization` header at startup and
refreshes it periodically. Your settings file therefore stays free of credentials: safe
to diff, share, or keep in a dotfiles repo, at whatever permissions you had it. Prefer
everything in one file? `--inline-key` writes the key into `settings.json` the classic
way and tightens it to `0600`.

**Codex has no equivalent mechanism.** Its exporters are configured in `config.toml`
with literal header values, so the key is written there and the file is tightened to
`0600`. Only the `[otel]` table is rewritten: the rest of `config.toml` — comments,
MCP servers, profiles, key order — is preserved byte for byte, and the result is
re-parsed and checked before it is written. Keys inside `[otel]` that Mirador does not
own (`environment`, for instance) survive too, and so do your own entries in
`span_attributes`: Mirador adds and later removes only its `enduser.id` and
`mirador.project.id` there. Comments inside that one table do not survive.

With Claude Code's enhanced telemetry on you get spans for each interaction, model
request and retry, tool execution and subagent, plus structured events and metrics for
token usage, cost, commits and lines changed.

Codex exports spans per turn and model request carrying `gen_ai.usage.*` token counts;
`codex.*` events including `codex.sse_event` with per-response input, output, cached
and reasoning token counts, `codex.user_prompt`, `codex.tool_decision`,
`codex.tool_result`, and `codex.turn_cost` with the estimated USD; and metrics
including `codex.turn.token_usage` and `codex.turn.cost_microusd`. Codex reports under
its own `service.name` of `codex_cli_rs` (the desktop app and IDE extensions use their
own), and Mirador's `enduser.id` and project attribution ride on spans as
`span_attributes`, which is the only per-user attribute Codex accepts.

```
Codex found: 0.152.0
  Mirador project: Payments (proj_123)
  Endpoint:        https://otel.mirador.org

  Signals:
    ✓ Agent traces
    ✓ Structured events
    ✓ Token and cost metrics

    Prompts:      on
    Tool content: on

  This will update:
    /Users/you/.codex/config.toml
    (the server key is written into this file, which is tightened to 0600)

  A server key will be minted for this project — unless one is already installed here, which will be reused.

  Note:
    Codex sends metrics to OpenAI (statsig) unless configured otherwise; after this connect they go to Mirador instead.

Connect Codex to Mirador? [Y/n] y

Connected. Restart Codex, then run a prompt.
View traces with: mirador trace list --filter 'attribute.service.name="codex_cli_rs"'
```

### What gets sent

**Everything, by default** — all three signals, plus prompt text and tool content. That
content is what makes an agent trace answer questions; a redacted trace mostly can't. It
also means what you and the model said leaves the machine, so redaction is a flag away,
and the two exclusions are independent:

```bash
mirador telemetry connect claude --exclude-prompts        # no prompt text or model responses
mirador telemetry connect claude --exclude-tool-content   # no tool parameters, input, or output
```

What each flag can do depends on the harness:

- **Claude Code** exports prompt text and model responses (`--exclude-prompts` drops
  both, keeping prompt *length*), and tool parameters, input and output
  (`--exclude-tool-content` drops all of them).
- **Codex** exports your prompt text (`log_user_prompt`) and never exports model
  response text, so `--exclude-prompts` is exactly that one switch. Tool output is
  previewed up to `otel.tool_result.max_bytes` (2 KiB by Codex's default; raise it in
  `config.toml` for more), and `--exclude-tool-content` sets that to zero — but Codex
  has no switch for tool *arguments*, so the command or parameters of each tool call
  are still exported. The connect plan says so before asking.

Either way the plan prints the capture posture before asking for confirmation.

Pick a subset of signals with `--signals`:

```bash
mirador telemetry connect claude --signals traces,logs
mirador telemetry connect codex --signals traces,logs
```

For Codex, signals you leave out are left exactly as they were rather than switched off:
by default that is off for logs and traces, and OpenAI's own `statsig` route for
metrics. Connecting the metrics signal replaces that route with Mirador — the plan notes
it — and disconnecting puts it back. If `config.toml` opts out of analytics
(`[analytics]` with `enabled = false`), Codex exports no metrics at all whatever the
exporter says, so a connect that includes metrics refuses rather than report a signal
that would never arrive; connect with `--signals traces,logs`, or remove the setting.

Other useful flags: `--key-name` to name the minted key, `--api-key` to install a key you
already hold instead of minting one, and `-y` to skip the prompt.

### Checking and undoing

```bash
mirador telemetry status              # every harness
mirador telemetry status codex
mirador telemetry disconnect codex
```

`status` reads the harness's own config, so it reports what the harness is set up to
send — not whether anything has arrived. A harness exporting somewhere other than the
active profile's endpoint is reported as `connected to <that endpoint> (this profile
expects <yours>)` — the everyday case being a dev-connected harness read under the prod
profile — so it is never mistaken for yours.

`disconnect` restores exactly what connect changed: settings Mirador wrote go back to
their prior values (a connect-time journal under `~/.mirador/telemetry/` records them),
anything you edited since connecting is left alone and named, and Mirador's helper
script is deleted along with its setting. The server key itself stays live, since it is
bound to the project rather than to the machine — `disconnect` prints its masked prefix
so you can find and revoke it in the web app.

Both harnesses layer settings from other places over the user file Mirador writes.
Claude Code reads project `.claude/settings.json` files and the shell environment; the
scan covers the directory you run `connect` from, so a session started elsewhere may
see different project settings. Codex applies an administrator's `managed_config.toml`
(`/etc/codex/managed_config.toml` on macOS and Linux; `~/.codex/managed_config.toml`
on Windows, which Codex 0.150 and later ignore with a startup warning but earlier builds
honour) and, on macOS, the `com.openai.codex` managed preference an MDM profile
delivers. Codex deliberately ignores `otel` in a project's `.codex/config.toml`, so that
is neither honoured nor scanned. In each layer Mirador checks not just the exporters but
everything that could overturn the plan it printed: `log_user_prompt`,
`tool_result.max_bytes`, the `enduser.id` and `mirador.project.id` span attributes, and
`[analytics] enabled`. A managed setting that contradicts the connect is a conflict
Mirador cannot clear, and connect refuses until it is removed.

Codex profile files (`<name>.config.toml` beside `config.toml`) are layered over the
user config only while that profile is selected with `--profile <name>`, and Mirador
cannot know which profile a session will use. Their overrides are therefore checked the
same way but reported as advisory — printed before the confirmation with the profile
named, listed under `warnings` in `status --output json` — without blocking the connect
or marking the harness overridden.

`disconnect codex` acts only on what its own journal records. No earlier release wrote
a Codex config, so a `[otel]` table without a journal is somebody else's — a company
collector, say — and is neither counted, described by key prefix, nor removed.

Minting a key needs a user credential, so `telemetry connect` requires `mirador login`;
a `mir_srv_` key in `MIRADOR_API_KEY` cannot mint another. Use `--api-key` to install a
key you already have.

## Filters and time windows

`--filter` (shorthand `-f` on the query commands) takes an
[AIP-160](https://google.aip.dev/160) filter expression — quoted values, `=` for
equality, `AND` to combine clauses:

```bash
mirador trace list --filter 'status="running"'
mirador log query  --filter 'severity="error" AND service.name="checkout"'
```

The filterable keys are not a fixed list — they are whatever your telemetry carries.
Discover them before writing a filter:

```bash
mirador trace attributes   # keys seen on traces, and the values seen for them
mirador log attributes     # keys seen on logs
```

`--since` and `--until` bound the query window, and each takes an RFC 3339 timestamp
(`2026-08-25T09:00:00Z`) or a relative age like `2h`. `log tail --window` replays a
fixed window on connect and accepts one of `1m`, `5m`, `15m`, `1h`, `4h`, `12h`,
`24h`, `7d`, `30d`.

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

Here `-f` is `--file` (on the query commands the same shorthand means `--filter`).
The file is YAML or JSON, and carries the resource's own fields. A top-level `slug:`
names it, so files are self-describing; an explicit slug — the argument, or `--slug` —
overrides the file's, and passing an argument and `--slug` that disagree is an error:

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

## Endpoints

The CLI talks to three Mirador hosts, and knows which is which:

| Host | Used for |
|---|---|
| `auth.mirador.org` | `login`, `logout`, token refresh, `project list`, `org list`, `telemetry connect` |
| `api.mirador.org` | `trace`, `log`, `metric`, `dashboard`, `metric-alert`, `derived-metric` |
| `app.mirador.org` | the browser page `login` opens |

A fourth, `otel.mirador.org`, is where telemetry is ingested. The CLI never calls it —
it writes that URL into an agent harness's configuration, and the harness exports there
directly. Override it with `MIRADOR_OTLP_URL` or `mirador config set --otlp-url`.

Credentials are minted on their own host so an auth outage cannot take reads down with it — the
data API validates tokens directly against Redis and never calls the auth host per request.

**Nothing needs configuring.** These are the defaults, and for Mirador's hosted service they are
the only endpoints you need.

Running a self-hosted deployment? Point the CLI at it with environment variables:

```bash
export MIRADOR_API_URL=https://api.example.com
export MIRADOR_AUTH_URL=https://auth.example.com
export MIRADOR_APP_URL=https://app.example.com
export MIRADOR_OTLP_URL=https://otel.example.com
```

Or store them on a profile so they persist and you can switch between deployments:

```bash
mirador config use staging
mirador config set --api-url https://api.example.com --auth-url https://auth.example.com
mirador --profile staging whoami
```

Each host is set independently, and a credential is bound to the auth host that minted it — point
the CLI somewhere else and it refuses the old credential rather than sending it to the new host.

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

Homebrew downloads the cask and the release archives unauthenticated, so `brew install`
works only while the tap *and* this repo's releases are both public. If either is private —
a fork, an internal build — `make install` and direct downloads by authenticated users are
the working paths.

## Contract

`CONTRACT.md` is the normative description of the auth flow and the API surface it depends
on — the shared reference for this CLI, the API gateway, and the browser approval page.

## License

MIT — see `LICENSE`. Note that `mirador-platform` is licensed differently (FSL-1.1); this
CLI matches the SDKs, since like them it is client-side code you are expected to install
and build against.
