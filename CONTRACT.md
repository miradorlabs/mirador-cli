# Mirador CLI auth contract

The contract between `mirador-cli`, the auth gateway (`mirador-platform/gateways/auth`), the API
gateway (`mirador-platform/gateways/api`), the web gateway, and the `/cli/auth` page in
`mirador-frontend`. Everything in this document is normative — all five surfaces are built against
it.

## Two hosts

| Host | Serves | Component |
|------|--------|-----------|
| `auth.mirador.org` | Credential lifecycle, and the org/project listings a fresh credential needs | `gateways/auth` |
| `api.mirador.org` | Traces, logs, metrics, dashboards | `gateways/api` |

They are separate because the credential surface has properties nothing else shares: it is the only
unauthenticated write, the only credential-minting endpoint, and the highest-value attack target.
Its own host means its own edge policy, its own rate limits, and its own blast radius.

**The auth gateway is not in the read path.** Access tokens are validated by the data gateway
directly against Redis; no gateway calls the auth gateway per request. An auth outage stops new
logins, not existing reads. Any design that puts a synchronous call from `api` to `auth` on the
request path breaks this and must be rejected.

## Why not an API key

A `mir_srv_*` server key is bound to exactly one project, so an API-key CLI can never offer
`mirador project use`. CLI credentials are therefore **user-scoped and org-scoped**, and the project
is chosen per request. Server keys remain supported for CI (`MIRADOR_API_KEY`), where a fixed
project is the point.

## Token formats

| Token | Format | Lifetime | Storage |
|-------|--------|----------|---------|
| Authorization code | `mir_cod_` + 32 hex | 60 s, single use | Postgres `account.cli_authorization_codes`, SHA-256 of the code |
| Access token | `mir_cli_` + 32 hex | 1 h | Redis `account:cli_token:<sha256>` with matching TTL |
| Refresh token | `mir_clr_` + 64 hex | 30 d, rotated on every use | Postgres `account.cli_sessions`, SHA-256 only |

Hashing uses `core/util.HashValue` (SHA-256 hex), matching the existing server-key path. No token is
ever stored in plaintext, and no token is ever logged.

The access token is opaque and resolved through Redis rather than signed as a JWT: it reuses the
`gateways/internal/apikey.Resolver` shape the gateways already run, and revocation is immediate
instead of bounded by a signature TTL.

## The flow

```
  mirador-cli                     browser / app.mirador.org            mirador-platform
  ───────────                     ────────────────────────            ────────────────
  verifier = 43-128 chars
  challenge = BASE64URL(SHA256(verifier))
  state = 32 hex
  listen on 127.0.0.1:<port>
        │
        │ open ─────────────────▶ /cli/auth?challenge&state&port&label
        │                                   │
        │                                   │ Clerk session (sign in if needed)
        │                                   │ user picks an organization, clicks Authorize
        │                                   │
        │                                   ├── AuthorizeCliLogin ──▶ web gateway (JWT → sub_id)
        │                                   │                          └─▶ account API
        │                                   │                              mints code bound to
        │                                   │                              (user, org, challenge, port)
        │                                   ◀── code ──────────────────────┘
        │                                   │
        │ ◀── 302 http://127.0.0.1:<port>/callback?code&state
        │
        │ verify state matches, then
        ├── POST /v1/auth/cli/token ───────────────────────────────▶ auth gateway (public route)
        │     {grant_type: authorization_code, code, code_verifier}     └─▶ account API
        │                                                                   verify SHA256(verifier)
        │                                                                   == challenge, consume code
        ◀──── {access_token, refresh_token, expires_in, organization, user}
        │
        └── write ~/.mirador/credentials.json (0600)
```

### Why a loopback redirect and not a device code

The CLI is assumed to run on a machine with a browser. A device-code grant is a later addition for
SSH/headless use; it reuses the same code table with a `user_code` column and is out of scope here.

## HTTP surface (auth gateway)

### `POST /v1/auth/cli/token` — public, unauthenticated

This endpoint mints credentials, so it cannot itself require them. It is rate-limited by the
IP-keyed limiter, which runs before auth and is set tighter here (20/s) than on the data plane —
no legitimate client polls it.

Authorization code grant:

```json
{ "grant_type": "authorization_code", "code": "mir_cod_…", "code_verifier": "…" }
```

Refresh grant:

```json
{ "grant_type": "refresh_token", "refresh_token": "mir_clr_…" }
```

Both return:

```json
{
  "access_token": "mir_cli_…",
  "refresh_token": "mir_clr_…",
  "token_type": "Bearer",
  "expires_in": 3600,
  "organization": { "id": "…", "name": "…" },
  "user": { "id": "…", "email": "…" }
}
```

Failures use the gateway's standard `ErrorResponse` envelope with `code: UNAUTHENTICATED` and a
deliberately vague message — a consumed, expired, unknown, or verifier-mismatched code are
indistinguishable to the caller.

Refresh rotates both tokens. Presenting a superseded refresh token returns 401; the CLI treats that
as "logged out" and prompts for `mirador login`.

### `POST /v1/auth/cli/revoke` — CLI-token auth

Revokes the calling session: deletes the Redis access-token entry and stamps `revoked_at` on the
row. `mirador logout` calls it, then deletes the local credential file.

### `GET /v1/projects` — CLI token or server key

Projects in the credential's organization. A server key gets a single-element list containing its
own project, so the command still works under `MIRADOR_API_KEY`.

```json
{ "projects": [ { "id": "…", "name": "…", "organization_id": "…", "created_at": "…" } ] }
```

### `GET /v1/organizations` — CLI-token auth

Organizations the authenticated user belongs to, with the role. Server keys get 403 — a server key
has no user identity to enumerate against.

### `GET /v1/whoami` — CLI token or server key

Describes the *credential*: organization, auth type, and — under a CLI token — the user. It reports
no project, because a CLI credential has none.

```json
{
  "organization_id": "…",
  "auth_type": "cli_token",
  "user_id": "…",
  "email": "…",
  "session_id": "…"
}
```

## HTTP surface (API gateway)

### `GET /v1/identity` — extended

The per-request counterpart to `/v1/whoami`. `project_id` is the *effective* project for this
request: the key's bound project under a server key, or the `X-Mirador-Project` value under a CLI
token — empty when none was sent, which is not the same as "no project exists".

```json
{
  "organization_id": "…",
  "project_id": "…",
  "auth_type": "cli_token",
  "user_id": "…",
  "email": "…"
}
```

## Conditional writes (API gateway)

Dashboards, metric alerts and derived metrics are slug-addressed documents with an
identical write contract. The CLI implements it in one place (`cmd/resource.go`) and
registers it three times.

A `PUT` must carry exactly one precondition — a blind overwrite is never allowed:

| Header | Meaning | Failure |
|---|---|---|
| `If-None-Match: *` | create only | 412 if the slug exists |
| `If-Match: "<etag>"` | replace exactly that revision | 412 if stale, deleted or replaced |
| neither | — | 428 |
| both | — | 400 |

`DELETE` requires `If-Match`; a missing one is 428.

`mirador <resource> apply` therefore reads before it writes: a 404 becomes a create,
and a successful read supplies the ETag for a replace. That read-then-write window is
the point rather than a flaw — a concurrent change makes the write 412, which the CLI
reports as *"changed since it was read"* instead of silently discarding the other
edit. `--etag` skips the read when the caller already knows the revision, and
`--create` asserts the resource is new.

The slug lives in the URL and never in the body: every `*Input` schema is
`additionalProperties: false`, so a document containing `slug:` would be rejected.
The CLI strips it after using it as the identity, which is what lets a file be
self-describing.

A replace whose body matches the stored document is a no-op — same revision, same
ETag — so re-applying a directory of definitions from CI is safe.


## Per-request project scoping

Under a CLI token every project-scoped endpoint requires the project to be named explicitly:

```
X-Mirador-Project: <project uuid>
```

The gateway resolves the project through `core/account.Directory` (one Redis GET) and rejects it
with 403 unless `project.organization_id` equals the token's organization. This is the whole
cross-tenant boundary for CLI credentials, so it is checked in middleware rather than per handler,
and no handler may read a project id from a request body.

Missing header under a CLI token is 400 `INVALID_ARGUMENT` naming the header, so `mirador project
use` is the obvious fix. Under a server key the header is ignored — the key already pins the
project, and honouring it would let a key read outside its grant.

## Client credential storage

`~/.mirador/config.json` (0644) holds non-secret profile state:

```json
{
  "active_profile": "default",
  "profiles": {
    "default": {
      "api_url": "https://api.mirador.org",
      "auth_url": "https://auth.mirador.org",
      "app_url": "https://app.mirador.org",
      "organization_id": "…",
      "project_id": "…"
    }
  }
}
```

`~/.mirador/credentials.json` (0600) holds the secrets, keyed by profile:

```json
{
  "default": {
    "access_token": "mir_cli_…",
    "refresh_token": "mir_clr_…",
    "expires_at": "2026-08-13T12:00:00Z"
  }
}
```

They are separate files so the config can be diffed, shared, or checked into a dotfiles repo without
dragging credentials along.

Resolution order for every setting is flag → environment (`MIRADOR_*`) → active profile → default.
`MIRADOR_API_KEY` short-circuits the whole auth path and sends a server key instead, which is what
CI and agent runners use.

## Loopback redirect safety

The `/cli/auth` page never redirects to a caller-supplied URL. It accepts a `port` integer, rejects
anything outside 1024-65535, and constructs `http://127.0.0.1:<port>/callback` itself. `state` is
echoed back and verified by the CLI, which closes the listener after the first callback and fails if
the code arrives without a matching state.

The authorization code is bound to the port it was issued for, so a code phished out of one CLI's
callback cannot be redeemed by a listener on a different port.
