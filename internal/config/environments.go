package config

import (
	"fmt"
	"sort"
	"strings"
)

// Environment is a named set of the three endpoints the CLI talks to. They are
// selected together because they are only meaningful together: a credential minted by
// one environment's auth host is worthless to another's data host, and mixing them
// produces a 401 that looks like a broken login rather than a misconfiguration.
type Environment struct {
	Name    string
	APIURL  string
	AuthURL string
	AppURL  string
}

// DefaultEnvironment is what everyone gets without asking. Production is the default
// precisely because most people running this are not developers of it.
const DefaultEnvironment = "prod"

// environments is the built-in registry. Hostnames follow the platform's own
// convention (`<surface>-dev.mirador.org`, with the dev app on beta); anything not
// covered here is still reachable by setting the URLs individually.
var environments = map[string]Environment{
	"prod": {
		Name:    "prod",
		APIURL:  "https://api.mirador.org",
		AuthURL: "https://auth.mirador.org",
		AppURL:  "https://app.mirador.org",
	},
	"dev": {
		Name:    "dev",
		APIURL:  "https://api-dev.mirador.org",
		AuthURL: "https://auth-dev.mirador.org",
		AppURL:  "https://beta.mirador.org",
	},
	"local": {
		Name:    "local",
		APIURL:  "http://localhost:8055",
		AuthURL: "http://localhost:8057",
		AppURL:  "http://localhost:3000",
	},
}

// environmentAliases spares anyone from having to remember which spelling this
// particular tool chose. `prd` is what the platform's own charts call production.
var environmentAliases = map[string]string{
	"prd":         "prod",
	"production":  "prod",
	"development": "dev",
	"localhost":   "local",
}

// LookupEnvironment resolves a name or alias, case-insensitively.
func LookupEnvironment(name string) (Environment, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if alias, ok := environmentAliases[key]; ok {
		key = alias
	}
	env, ok := environments[key]
	if !ok {
		return Environment{}, fmt.Errorf("unknown environment %q (known: %s)", name, strings.Join(EnvironmentNames(), ", "))
	}
	return env, nil
}

// EnvironmentNames lists the canonical names, for help text and error messages.
func EnvironmentNames() []string {
	names := make([]string, 0, len(environments))
	for name := range environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Environments returns the registry in a stable order.
func Environments() []Environment {
	names := EnvironmentNames()
	out := make([]Environment, 0, len(names))
	for _, name := range names {
		out = append(out, environments[name])
	}
	return out
}
