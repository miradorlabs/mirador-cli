# Security model

What `mirador-cli` protects, from whom, and where the remaining edges are. Written to be
argued with — if a claim here is wrong, that is a bug.

## What is at stake

| Secret | Lifetime | Where it lives | If leaked |
|---|---|---|---|
| Access token `mir_cli_` | 1 h | `~/.mirador/credentials.json` (0600); Redis server-side | Read every project in the org until it expires or is revoked |
| Refresh token `mir_clr_` | 30 d, rotated per use | same file; SHA-256 only server-side | Mint access tokens until detected or revoked |
| Authorization code `mir_cod_` | 60 s, single use | never written to disk | Useless without the PKCE verifier |
| PKCE verifier | one login | process memory only | Useless without the code |
| Server key `mir_srv_` | until revoked | `MIRADOR_API_KEY` env | Read the one project it is bound to |

A CLI credential is **org-scoped**: it reaches every project in one organization and no
other organization. It cannot mint API keys or other credentials.

It is **not read-only**. The data API carries a few conditional writes — dashboards, and
(since the metric-alerts surface landed) `PUT`/`DELETE /v1/metric-alerts/{slug}` — and a CLI
token can reach them. Those writes are attributed to the real user id rather than a key id,
so the audit trail names a person; but a leaked token can modify alerting, not just read it.
The CLI deliberately exposes no command for them, which limits accident, not intent.

## The login flow, and what each control is for

```
verifier (memory)          ──never leaves the process──┐
challenge = S256(verifier) ──through the browser──▶ server stores it with the code
code ──through the browser──▶ CLI ──with the verifier──▶ server
```

| Control | Attack it stops |
|---|---|
| PKCE S256, verifier never in the URL | A code read from browser history, a screenshare, or a proxy log is not redeemable |
| `state`, 128-bit, constant-time compared | Another page or local process injecting a code into your session |
| Code single-use, 60 s, bound to the loopback port | Replay; redemption by a listener other than the one that asked |
| Listener on `127.0.0.1` only, never `0.0.0.0` | Anyone else on your network reaching the callback |
| `Host` header must name the loopback listener | DNS rebinding: a page on a domain resolving to 127.0.0.1 reading the response |
| A mismatched callback is ignored, not fatal | An attacker who guesses the port cancelling your login |
| Refresh rotation + reuse detection | A stolen refresh token outliving discovery — see below |
| https required for non-loopback endpoints | A mistyped or hostile endpoint putting every secret on the wire in cleartext |
| Membership re-checked on every rotation | A removed user keeping access for the refresh token's full 30 days |

### Refresh token reuse detection

Rotation alone is not enough. If a thief refreshes first, the real client's next refresh
just fails, the user logs in again, and the thief's rotated session runs for its full term
with nobody aware.

So the server keeps the hash it rotated away from. Presenting a superseded token proves the
token existed in two places — and since the server cannot tell which party is legitimate, it
revokes the session and makes **both** re-authenticate. The user sees an unexpected logout,
which is the signal that something was wrong.

## What this does *not* protect against

Stated plainly, because a threat model that claims to cover everything is not a threat model.

- **Anything running as your user.** A process with your UID can read
  `~/.mirador/credentials.json`. File permissions stop other users, not your own code. If you
  run untrusted code, assume the credential is gone and `mirador logout`.
- **Root, or anyone who can read your disk.** Full-disk encryption is the control here.
- **A compromised Redis.** An attacker who can *write* Redis can insert a token hash and forge
  a credential. This is true of the existing API keys too; Redis is inside the trust boundary.
- **The one-hour window after offboarding.** Removing a user stops refresh immediately and
  revokes the session on their next attempt, but an access token already in hand stays valid
  until it expires or someone revokes the session from the web app. To cut it to zero, revoke
  the session explicitly.
- **A malicious browser extension.** It can see the authorize URL and the redirect. It cannot
  see the verifier, so it cannot redeem the code — but it can watch you approve.

## Reporting

Do not open a public issue for a vulnerability. Contact the Mirador team directly.

## For reviewers

Properties worth re-checking after any change to `internal/auth` or `internal/api`:

- The verifier never appears in a URL, a log, or a file (`TestLogin_ExchangesCodeWithTheVerifierAndPort`).
- No credential is ever rendered by `-o json` (`TestConfigView_NeverCarriesTheAPIKey` — this
  shipped broken once; `config show -o json` printed `MIRADOR_API_KEY` in full).
- Credential calls go to the auth host, never the data host (`TestClient_RoutesCredentialCallsToTheAuthHost`).
- Cleartext endpoints are refused (`TestLoad_RefusesCleartextRemoteEndpoints`).
- Hostile callbacks neither authenticate nor cancel (`TestLogin_HostileCallbacksAloneNeverAuthenticate`,
  `TestLogin_WrongStateDoesNotCancelThePendingLogin`).

`go run golang.org/x/vuln/cmd/govulncheck@latest ./...` runs in CI on every push and weekly —
weekly because a dependency can become vulnerable without us touching a line.
