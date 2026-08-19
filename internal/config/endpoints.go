package config

// The endpoints the CLI talks to. Three separate hosts: an auth outage cannot take
// reads down with it, and a credential is bound to the auth host that minted it.
//
// These are the only endpoints built in. Anything else — a self-hosted deployment,
// or Mirador's own pre-production — is reached by setting MIRADOR_API_URL,
// MIRADOR_AUTH_URL and MIRADOR_APP_URL, or by storing them on a profile with
// `mirador config set`. Nothing is hardcoded here but production.
const (
	DefaultAPIURL  = "https://api.mirador.org"
	DefaultAuthURL = "https://auth.mirador.org"
	DefaultAppURL  = "https://app.mirador.org"
)
