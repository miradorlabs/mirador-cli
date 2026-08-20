package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// apiOperations is every operation in the API gateway's OpenAPI document, mapped to
// the command that serves it. The CLI's stated contract is full parity with that
// surface, so this table is the executable form of that claim: an endpoint added to
// the gateway should fail this test until a command exists for it.
//
// This test asserts the command *exists and is runnable*, not that it calls the exact
// path/host in the key — that mapping is verified by the per-package client tests. One
// entry is deliberately indirect: `GET /v1/identity` (data plane) is served for the
// user by `whoami`, which calls the auth host's `/v1/whoami`; the two return the same
// identity and the CLI exposes no separate command for the data-plane endpoint.
//
// Regenerate the left column with:
//
//	python3 -c 'import yaml,sys; s=yaml.safe_load(open(sys.argv[1])); \
//	  print("\n".join(f"{v.upper()} {p}" for p in sorted(s["paths"]) \
//	  for v in s["paths"][p] if v in ("get","post","put","patch","delete")))' \
//	  ../mirador-platform/gateways/api/spec/openapi.yaml
var apiOperations = map[string]string{
	"GET /v1/identity":                  "whoami",
	"GET /v1/traces":                    "trace list",
	"GET /v1/traces/{trace_id}":         "trace get",
	"GET /v1/traces/{trace_id}/events":  "trace events",
	"GET /v1/traces/tags":               "trace tags",
	"GET /v1/traces/attributes":         "trace attributes",
	"GET /v1/logs":                      "log query",
	"GET /v1/logs/stats":                "log stats",
	"GET /v1/logs/attributes":           "log attributes",
	"GET /v1/logs/stream":               "log tail",
	"GET /v1/metrics":                   "metric list",
	"GET /v1/metrics/query":             "metric query",
	"GET /v1/metrics/query_range":       "metric range",
	"GET /v1/dashboards":                "dashboard list",
	"GET /v1/dashboards/{slug}":         "dashboard get",
	"PUT /v1/dashboards/{slug}":         "dashboard apply",
	"DELETE /v1/dashboards/{slug}":      "dashboard delete",
	"GET /v1/metric-alerts":             "metric-alert list",
	"GET /v1/metric-alerts/{slug}":      "metric-alert get",
	"PUT /v1/metric-alerts/{slug}":      "metric-alert apply",
	"DELETE /v1/metric-alerts/{slug}":   "metric-alert delete",
	"GET /v1/derived-metrics":           "derived-metric list",
	"GET /v1/derived-metrics/{slug}":    "derived-metric get",
	"PUT /v1/derived-metrics/{slug}":    "derived-metric apply",
	"DELETE /v1/derived-metrics/{slug}": "derived-metric delete",
	"POST /v1/derived-metrics/dry-run":  "derived-metric dry-run",
	"GET /v1/integrations":              "integration list",
	"GET /v1/integrations/{slug}":       "integration get",
}

// TestEveryAPIOperationHasACommand fails if an operation's command is missing or was
// renamed. Parity is the whole point of the command surface; losing it silently is
// the failure this guards.
func TestEveryAPIOperationHasACommand(t *testing.T) {
	root := NewRootCommand()

	for operation, path := range apiOperations {
		t.Run(operation, func(t *testing.T) {
			cmd, _, err := root.Find(strings.Fields(path))
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			// Find falls back to the closest parent when a leaf is missing, so the
			// resolved name has to be checked rather than trusted.
			leaf := strings.Fields(path)[len(strings.Fields(path))-1]
			if cmd.Name() != leaf {
				t.Fatalf("`mirador %s` resolves to %q — the command for %s does not exist",
					path, cmd.CommandPath(), operation)
			}
			if cmd.RunE == nil && cmd.Run == nil {
				t.Errorf("`mirador %s` exists but does nothing", path)
			}
		})
	}
}

// TestWriteCommandsRequireAFile guards the ergonomics of apply: forgetting -f should
// be a local usage error, not a request that sends an empty document.
func TestWriteCommandsRequireAFile(t *testing.T) {
	root := NewRootCommand()

	for _, path := range []string{"dashboard apply", "metric-alert apply", "derived-metric apply", "derived-metric dry-run"} {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		flag := cmd.Flags().Lookup("file")
		if flag == nil {
			t.Errorf("`mirador %s` has no --file flag", path)
			continue
		}
		if flag.Annotations[cobra.BashCompOneRequiredFlag] == nil {
			t.Errorf("`mirador %s` does not require --file", path)
		}
	}
}

// TestResourceCommandsShareTheSameVerbs keeps the three conditional-write resources
// from drifting apart. They implement one API contract, so a user who learns
// `dashboard apply` should not find `metric-alert` spelling it differently.
func TestResourceCommandsShareTheSameVerbs(t *testing.T) {
	root := NewRootCommand()
	want := []string{"apply", "delete", "get", "list"}

	for _, group := range []string{"dashboard", "metric-alert", "derived-metric"} {
		cmd, _, err := root.Find([]string{group})
		if err != nil {
			t.Fatalf("%s: %v", group, err)
		}
		have := map[string]bool{}
		for _, sub := range cmd.Commands() {
			have[sub.Name()] = true
		}
		for _, verb := range want {
			if !have[verb] {
				t.Errorf("`mirador %s` is missing the %q verb the other resources have", group, verb)
			}
		}
	}
}
