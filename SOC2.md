# SOC 2 requirements

Mirador Labs is preparing a SOC 2 Type 1 audit. This file lists the engineering rules that contributors and coding agents follow in this repository. It is maintained in every Mirador repository and given to coding agents through `AGENTS.md`. This repository is public, so internal infrastructure details are kept out of it.

**How to use it**
- Read it before changing anything it covers: secrets, dependencies, CI, releases or data handling.
- Never weaken a control here to get a change through. If a change needs one to change, stop and ask a maintainer.
- A change that alters a control updates this file in the same pull request.

## Change management

- Every change reaches the default branch through a pull request; branch rules block force-pushes and deletion. Required CI checks must pass and are never skipped or loosened.
- Where this repository publishes releases, only Mirador release managers create release tags, and the release pipeline publishes them, never a person by hand.

## Secure development

- Never commit a secret: no keys, tokens, passwords or credentials in code, tests, fixtures, examples or CI logs. Secrets live in the CI secret store. Secret scanning with push protection stays on.
- Dependency alerts and security updates stay on. Fix confirmed vulnerabilities within **Critical 7 days, High 30 days, Medium 90 days**.
- Add dependencies only from their official registries, pinned by lockfile. Do not add a third-party SDK that sends data off the user's machine (analytics, crash reporting, AI) without Mirador's vendor review.
- Never log secrets, tokens, passwords or user content.

## Data

- No customer or personal data in this repository: tests and examples use made-up data.
- Software that runs on a user's machine sends data only to Mirador's own documented endpoints over TLS 1.2 or higher, and stores credentials in the operating system's secure store where one exists.

## Reporting security issues

- Report a suspected vulnerability privately by email to support@mirador.org (support@terma.ai for Terma) with "Security" in the subject line, never in a public issue.

## This repository

mirador-cli is the `mirador` command-line client for the Mirador API: traces, OpenTelemetry logs, PromQL metrics and dashboards.

- **Releases come only from `.github/workflows/release.yml`** on a `v*` tag. It runs govulncheck, builds with GoReleaser, attests build provenance and updates the Homebrew cask. Never publish a binary or cask by hand.
- **Keep the properties listed in `SECURITY.md` true.** The PKCE verifier never appears in a URL, log or file. No credential is rendered by `-o json`. Credential calls go only to the auth host. Cleartext non-loopback endpoints are refused.
- **Credentials.** Login tokens are stored today in `~/.mirador/credentials.json` with mode 0600, not yet in the system's credential store. A server key comes only from `MIRADOR_API_KEY` and is never written or printed.
- **Data goes only to Mirador's public endpoints over https**: `api.mirador.org`, `auth.mirador.org`, `app.mirador.org` and `otel.mirador.org` (`internal/config/endpoints.go`).
- **govulncheck runs on every push, every pull request and weekly** (`.github/workflows/security.yml`). Keep it.
