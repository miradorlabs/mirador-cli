package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Claude configures Claude Code's OpenTelemetry export.
//
// Claude Code reads environment variables out of the `env` object in its settings file,
// which is what makes this installable without touching the user's shell profile. The
// variable names are Claude Code's own contract, documented under "Monitoring usage";
// they are spelled out as constants here so a rename upstream is a one-line change and
// a compile-time-visible one.
type Claude struct{}

const (
	// claudeEnableTelemetry is the master switch. Without it every OTEL_* variable
	// below is inert.
	claudeEnableTelemetry = "CLAUDE_CODE_ENABLE_TELEMETRY"
	// claudeEnhancedTelemetry gates spans specifically. OTEL_TRACES_EXPORTER alone
	// produces nothing without it, which is the single easiest way to end up
	// "connected" and see no traces.
	claudeEnhancedTelemetry = "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"

	otelTracesExporter  = "OTEL_TRACES_EXPORTER"
	otelLogsExporter    = "OTEL_LOGS_EXPORTER"
	otelMetricsExporter = "OTEL_METRICS_EXPORTER"

	otelProtocol           = "OTEL_EXPORTER_OTLP_PROTOCOL"
	otelEndpoint           = "OTEL_EXPORTER_OTLP_ENDPOINT"
	otelHeaders            = "OTEL_EXPORTER_OTLP_HEADERS"
	otelResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"

	// The four content switches. All default off upstream; Mirador writes them
	// explicitly either way so the file states the redaction posture rather than
	// leaving it to a default that could change.
	otelLogUserPrompts       = "OTEL_LOG_USER_PROMPTS"
	otelLogAssistantResponse = "OTEL_LOG_ASSISTANT_RESPONSES"
	otelLogToolDetails       = "OTEL_LOG_TOOL_DETAILS"
	otelLogToolContent       = "OTEL_LOG_TOOL_CONTENT"

	// exporterOTLP / exporterNone are the only two values Mirador writes. A disabled
	// signal is written as an explicit "none" rather than omitted, so the config says
	// what it does instead of depending on an upstream default.
	exporterOTLP = "otlp"
	exporterNone = "none"

	// protocolHTTPProtobuf is chosen over grpc because it traverses ordinary HTTPS
	// proxies and corporate TLS interception, which the gRPC transport frequently
	// does not.
	protocolHTTPProtobuf = "http/protobuf"
)

// perSignalOverrides are the variables that take precedence over the generic ones
// Mirador writes. Claude Code resolves each signal's exporter from the per-signal value
// when present, and *merges* the generic headers into it — so a stale
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT pointing at another vendor keeps receiving that
// signal's spans, now carrying Mirador's Authorization header. That is a live server key
// handed to a third party, and nothing downstream would show it: `telemetry status` reads
// the generic endpoint and would report Mirador.
//
// Mirador never writes these. They are detected before a connect and reported, and only
// removed when the user explicitly asks — they are the user's settings.
var perSignalOverrides = []struct {
	endpoint, protocol, headers string
	signal                      Signal
}{
	{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", SignalTraces},
	{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_HEADERS", SignalLogs},
	{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_HEADERS", SignalMetrics},
}

// claudeManagedKeys is every variable Mirador sets, and therefore exactly what
// disconnect removes. A key absent from this list would be orphaned in the user's
// config forever; a key wrongly present would delete a setting Mirador never made.
//
// The per-signal overrides above are deliberately absent: Mirador does not set them, so
// disconnect must not delete them.
var claudeManagedKeys = []string{
	claudeEnableTelemetry,
	claudeEnhancedTelemetry,
	otelTracesExporter,
	otelLogsExporter,
	otelMetricsExporter,
	otelProtocol,
	otelEndpoint,
	otelHeaders,
	otelResourceAttributes,
	otelLogUserPrompts,
	otelLogAssistantResponse,
	otelLogToolDetails,
	otelLogToolContent,
}

func (Claude) Name() string        { return "claude" }
func (Claude) DisplayName() string { return "Claude Code" }

// claudeVersionRE pulls the semver out of `claude --version`, whose output is
// "2.0.14 (Claude Code)". Matching loosely on the leading version keeps this working if
// the parenthetical changes.
var claudeVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+[^\s]*`)

// Detect runs `claude --version`. A missing binary is reported as not-found rather than
// as an error: connecting an uninstalled harness is allowed, since the config is read
// whenever it is eventually started.
func (Claude) Detect(ctx context.Context) Detection {
	path, err := exec.LookPath("claude")
	if err != nil {
		return Detection{}
	}

	// A hung version probe should not hang the command.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		// It is installed — that is what matters — but it would not say which version.
		return Detection{Found: true, Path: path}
	}
	return Detection{
		Found:   true,
		Path:    path,
		Version: claudeVersionRE.FindString(strings.TrimSpace(string(out))),
	}
}

// ConfigPath is ~/.claude/settings.json, or $CLAUDE_CONFIG_DIR/settings.json when Claude
// Code has been pointed elsewhere. Honouring that variable matters: writing to the
// default path while Claude reads another is a connect that silently does nothing.
func (Claude) ConfigPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Render maps an Exporter onto Claude Code's variables.
func (Claude) Render(e Exporter) map[string]string {
	traces := e.HasSignal(SignalTraces)

	env := map[string]string{
		claudeEnableTelemetry: "1",

		otelTracesExporter:  exporterFor(traces),
		otelLogsExporter:    exporterFor(e.HasSignal(SignalLogs)),
		otelMetricsExporter: exporterFor(e.HasSignal(SignalMetrics)),

		otelProtocol: protocolHTTPProtobuf,
		otelEndpoint: e.Endpoint,

		// Off unless explicitly opted into. Written rather than omitted so the file is
		// an explicit statement of what is and is not captured.
		otelLogUserPrompts:       boolValue(e.IncludePrompts),
		otelLogAssistantResponse: boolValue(e.IncludePrompts),
		otelLogToolDetails:       boolValue(e.IncludeToolContent),
		otelLogToolContent:       boolValue(e.IncludeToolContent),
	}

	// Spans are gated behind the beta flag as well as the exporter. Setting it only
	// when traces are on keeps a logs-and-metrics connect from opting the user into a
	// beta they did not ask for.
	if traces {
		env[claudeEnhancedTelemetry] = "1"
	}

	if e.APIKey != "" {
		env[otelHeaders] = "Authorization=Bearer " + e.APIKey
	}
	if attrs := e.ResourceAttributesValue(); attrs != "" {
		env[otelResourceAttributes] = attrs
	}
	return env
}

func exporterFor(on bool) string {
	if on {
		return exporterOTLP
	}
	return exporterNone
}

func boolValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// Status reads back what is currently installed.
func (c Claude) Status() (Status, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return Status{}, err
	}
	s, err := loadSettings(path)
	if err != nil {
		return Status{}, err
	}

	status := Status{
		ConfigPath: path,
		Exists:     s.existed,
		Endpoint:   s.env[otelEndpoint],

		// Absent is off — matching Claude Code's own default rather than reporting a
		// missing key as unknown.
		IncludePrompts:     isOn(s.env[otelLogUserPrompts]),
		IncludeToolContent: isOn(s.env[otelLogToolContent]),
	}

	// Connected means telemetry is on *and* pointed somewhere. A harness exporting to
	// another vendor's collector is deliberately not reported as connected to Mirador —
	// the caller compares Endpoint against its own to decide.
	status.Connected = isOn(s.env[claudeEnableTelemetry]) && status.Endpoint != ""

	for _, sig := range AllSignals {
		var key string
		switch sig {
		case SignalTraces:
			key = otelTracesExporter
		case SignalLogs:
			key = otelLogsExporter
		case SignalMetrics:
			key = otelMetricsExporter
		}
		if s.env[key] == exporterOTLP {
			status.Signals = append(status.Signals, sig)
		}
	}

	// Traces need the beta flag as well as the exporter; without it the exporter is set
	// and no span is ever produced. Reporting traces as on in that state would send
	// someone hunting in Mirador for data the harness never sent.
	if !isOn(s.env[claudeEnhancedTelemetry]) {
		status.Signals = withoutSignal(status.Signals, SignalTraces)
	}

	status.KeyPrefix = maskKeyFromHeaders(s.env[otelHeaders])
	status.ProjectID = resourceAttribute(s.env[otelResourceAttributes], AttrProjectID)

	// Counted whether or not the harness reports as connected: a config with telemetry
	// switched off but the key still present has managed keys to clean up, and that is
	// precisely the state disconnect must not walk away from.
	for _, key := range claudeManagedKeys {
		if _, ok := s.env[key]; ok {
			status.ManagedKeys++
		}
	}

	// Compared against the endpoint actually configured here, so status reports whether
	// this file is internally consistent — not whether it agrees with some other project.
	status.Conflicts = claudeConflicts(s.env, status.Endpoint)
	return status, nil
}

// ConflictsWith reports what in the existing config would defeat an export aimed at
// endpoint: a generic endpoint already pointing elsewhere, or any per-signal override.
func (c Claude) ConflictsWith(endpoint string) ([]Conflict, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return nil, err
	}
	s, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	return claudeConflicts(s.env, endpoint), nil
}

// claudeConflicts finds every setting that would send data — and with it Mirador's
// Authorization header — somewhere other than endpoint, or break the export outright.
//
// endpoint is the generic endpoint that will be in effect: the one about to be written
// during a connect, or the one already on disk when reporting status.
func claudeConflicts(env map[string]string, endpoint string) []Conflict {
	var out []Conflict

	// The generic endpoint pointing at another collector is not a leak on its own — it
	// is overwritten — but it means a connect would silently replace a working export
	// the user set up. Reported so that replacement is a decision, not an accident.
	if current := env[otelEndpoint]; current != "" && current != endpoint && isOn(env[claudeEnableTelemetry]) {
		out = append(out, Conflict{
			Key:    otelEndpoint,
			Value:  current,
			Reason: "telemetry is already exporting here; connecting replaces it",
		})
	}

	for _, o := range perSignalOverrides {
		if v := env[o.endpoint]; v != "" && v != endpoint {
			out = append(out, Conflict{
				Key:   o.endpoint,
				Value: v,
				// This is the whole reason the check exists.
				Reason:     "overrides the endpoint for " + string(o.signal) + ", which would receive Mirador's credential",
				Credential: true,
			})
		}
		if v := env[o.headers]; v != "" {
			// The value is a header bag that may itself hold a credential, so it is
			// named but never printed.
			out = append(out, Conflict{
				Key:    o.headers,
				Reason: "merges into the headers for " + string(o.signal) + ", overriding Mirador's",
			})
		}
		if v := env[o.protocol]; v != "" && v != protocolHTTPProtobuf {
			out = append(out, Conflict{
				Key:    o.protocol,
				Value:  v,
				Reason: "sends " + string(o.signal) + " over a protocol Mirador's endpoint does not serve",
			})
		}
	}
	return out
}

// Connect merges Mirador's variables into the settings file, leaving every other
// setting — hooks, permissions, model, statusLine — untouched.
//
// clearConflicts removes the per-signal overrides reported by ConflictsWith. Without it
// they are left alone, which is why the caller must refuse to connect while any remain:
// writing the credential and leaving the override in place is the leak.
func (c Claude) Connect(env map[string]string, clearConflicts bool) error {
	path, err := c.ConfigPath()
	if err != nil {
		return err
	}
	s, err := loadSettings(path)
	if err != nil {
		return err
	}

	if clearConflicts {
		for _, conflict := range claudeConflicts(s.env, env[otelEndpoint]) {
			// The generic endpoint is about to be overwritten by the merge anyway;
			// deleting it here would be a no-op with a confusing name.
			if conflict.Key == otelEndpoint {
				continue
			}
			delete(s.env, conflict.Key)
		}
	}

	s.merge(env)
	// tighten: the merged env carries the server key.
	return s.save(true)
}

// Disconnect removes only the keys Mirador set.
func (c Claude) Disconnect() (int, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return 0, err
	}
	s, err := loadSettings(path)
	if err != nil {
		return 0, err
	}
	removed := s.remove(claudeManagedKeys)
	if removed == 0 {
		return 0, nil
	}
	// The credential is gone, so there is nothing left to tighten for.
	return removed, s.save(false)
}

// Backup exposes the pre-modification copy so `connect` can tell the user where it is.
func (c Claude) Backup() (string, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return "", err
	}
	s, err := loadSettings(path)
	if err != nil {
		return "", err
	}
	return s.backup()
}

// ManagedKeys is what Disconnect would remove, for a preview.
func (Claude) ManagedKeys() []string { return claudeManagedKeys }

// isOn treats Claude Code's accepted truthy spellings as on, and everything else —
// including "0", "false", and absent — as off.
func isOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func withoutSignal(signals []Signal, drop Signal) []Signal {
	out := signals[:0]
	for _, s := range signals {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

// maskKeyFromHeaders extracts the key from an OTEL_EXPORTER_OTLP_HEADERS value and
// returns only its head. The full key is never returned: status output lands in
// terminals, screenshots, and bug reports.
func maskKeyFromHeaders(headers string) string {
	for pair := range strings.SplitSeq(headers, ",") {
		name, value, found := strings.Cut(pair, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "Authorization") {
			continue
		}
		key := strings.TrimSpace(value)
		key = strings.TrimSpace(strings.TrimPrefix(key, "Bearer"))
		return MaskKey(key)
	}
	return ""
}

// MaskKey renders a credential as a recognizable but unusable prefix.
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	// Keep the mir_srv_ prefix plus a few characters — enough to match against a key
	// list in the web app, far short of enough to authenticate.
	const keep = 12
	if len(key) <= keep {
		return "…"
	}
	return key[:keep] + "…"
}

// resourceAttribute reads one key out of an OTEL_RESOURCE_ATTRIBUTES value.
func resourceAttribute(raw, key string) string {
	for pair := range strings.SplitSeq(raw, ",") {
		name, value, found := strings.Cut(pair, "=")
		if found && strings.TrimSpace(name) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
