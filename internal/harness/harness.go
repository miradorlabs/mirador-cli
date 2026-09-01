// Package harness configures agent CLIs — Claude Code today, Codex next — to export
// OpenTelemetry to Mirador.
//
// Each harness owns exactly one thing: the translation between a Mirador Exporter and
// whatever configuration file and environment-variable names that vendor happens to
// use. Nothing outside this package knows that Claude Code reads ~/.claude/settings.json
// or spells its telemetry switch CLAUDE_CODE_ENABLE_TELEMETRY, which is what keeps
// `mirador telemetry` a single command tree rather than one per vendor.
//
// The split between Render and Connect is deliberate: Render is pure, so the CLI can
// show a user exactly what a connect would write — including which redaction switches
// are off — and get a yes before anything touches disk.
package harness

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Signal is one OTLP telemetry stream.
type Signal string

const (
	SignalTraces  Signal = "traces"
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
)

// AllSignals is the default: everything Mirador can ingest. Ordered most- to
// least-structural, which is also the order they are reported in.
var AllSignals = []Signal{SignalTraces, SignalLogs, SignalMetrics}

// ParseSignals turns a comma-separated --signals value into a deduplicated,
// canonically-ordered list. An unknown name is an error rather than a silent drop: a
// typo'd signal would otherwise look connected and emit nothing.
func ParseSignals(raw string) ([]Signal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AllSignals, nil
	}

	seen := map[Signal]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		name := Signal(strings.ToLower(strings.TrimSpace(part)))
		if name == "" {
			continue
		}
		switch name {
		case SignalTraces, SignalLogs, SignalMetrics:
			seen[name] = true
		default:
			return nil, fmt.Errorf("unknown signal %q (want traces, logs, or metrics)", part)
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("--signals was empty (want traces, logs, or metrics)")
	}

	out := make([]Signal, 0, len(seen))
	for _, s := range AllSignals {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// Exporter is the vendor-neutral description of where a harness should send telemetry
// and how much of it to include.
type Exporter struct {
	// Endpoint is the OTLP base URL. Signal paths (/v1/traces …) are appended by the
	// harness's own OTel SDK, not here.
	Endpoint string
	// APIKey is the mir_srv_ key the harness presents. It ends up inside a config file
	// on disk, which is why writers in this package tighten that file's mode.
	APIKey string
	// Signals are the streams to turn on. Streams not listed are explicitly disabled
	// rather than left unset, so the resulting config is unambiguous.
	Signals []Signal
	// ResourceAttributes are stamped on everything the harness emits. This is how a
	// trace gets attributed to a person and a Mirador project.
	ResourceAttributes map[string]string

	// IncludePrompts and IncludeToolContent are opt-in, and default off. They are
	// separate switches because they leak different things: prompts and responses are
	// what the user and model said, tool content is what ran and what came back.
	IncludePrompts     bool
	IncludeToolContent bool
}

// HasSignal reports whether a stream is enabled.
func (e Exporter) HasSignal(s Signal) bool {
	return slices.Contains(e.Signals, s)
}

// ResourceAttributesValue renders OTEL_RESOURCE_ATTRIBUTES. Keys are sorted so the
// value is byte-stable across runs — an unstable ordering would make every connect
// look like a change to the config file.
//
// Values containing a comma or an equals sign would corrupt the W3C Baggage-style
// encoding this variable uses, so they are dropped rather than written out broken.
func (e Exporter) ResourceAttributesValue() string {
	keys := make([]string, 0, len(e.ResourceAttributes))
	for k, v := range e.ResourceAttributes {
		if k == "" || v == "" {
			continue
		}
		if strings.ContainsAny(k, ",=") || strings.ContainsAny(v, ",=") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+e.ResourceAttributes[k])
	}
	return strings.Join(pairs, ",")
}

// Detection is what the CLI could learn about an installed harness. NotFound is not an
// error: connecting a harness that is not installed yet is legitimate — the config is
// read at its next start, whenever that is.
type Detection struct {
	Found   bool
	Version string
	// Path is the executable that answered, for a status line that can say which one.
	Path string
}

// Status is the currently-installed state, read back from the harness's own config.
type Status struct {
	// ConfigPath is the file consulted, whether or not it exists.
	ConfigPath string
	Exists     bool
	// Connected is true only when this harness points at a Mirador OTLP endpoint.
	// A harness exporting to someone else's collector is deliberately not "connected".
	Connected bool
	Endpoint  string
	Signals   []Signal

	IncludePrompts     bool
	IncludeToolContent bool

	// KeyPrefix is the masked head of the configured key, never the key itself.
	KeyPrefix string
	// ProjectID is read back from the stamped resource attributes, so status can say
	// which project the harness actually reports to rather than which one is selected.
	ProjectID string
}

// Harness is one configurable agent CLI.
type Harness interface {
	// Name is the command-line token: `mirador telemetry connect <name>`.
	Name() string
	// DisplayName is how it is written in prose — "Claude Code".
	DisplayName() string

	// Detect looks for an installed binary and its version.
	Detect(ctx context.Context) Detection

	// ConfigPath is the file Connect and Disconnect write.
	ConfigPath() (string, error)

	// Render translates an Exporter into this harness's environment variables. Pure:
	// no file is read and none is written, so the CLI can preview it.
	Render(Exporter) map[string]string

	// Status reads the config file back.
	Status() (Status, error)

	// Connect merges env into the config, preserving every setting it does not own.
	Connect(env map[string]string) error

	// Disconnect removes only the keys this harness set, and reports how many went.
	Disconnect() (int, error)
}

// registry is fixed at compile time. A harness is a code-level integration — it has to
// know a vendor's file format and variable names — so there is nothing a runtime
// registration would enable.
var registry = []Harness{Claude{}, Codex{}}

// All returns every known harness, in the order they are listed by `--help`.
func All() []Harness {
	out := make([]Harness, len(registry))
	copy(out, registry)
	return out
}

// Names lists the tokens accepted by connect/status/disconnect.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, h := range registry {
		out = append(out, h.Name())
	}
	return out
}

// Lookup resolves a command-line token to a harness.
func Lookup(name string) (Harness, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, h := range registry {
		if h.Name() == want {
			return h, nil
		}
	}
	return nil, fmt.Errorf("unknown harness %q (want %s)", name, strings.Join(Names(), " or "))
}

// ErrUnsupported is returned by a harness that is registered but not yet implemented,
// so `--help` can name it before it works.
type ErrUnsupported struct {
	Harness string
	Reason  string
}

func (e *ErrUnsupported) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s telemetry is not supported yet — %s", e.Harness, e.Reason)
	}
	return fmt.Sprintf("%s telemetry is not supported yet", e.Harness)
}
