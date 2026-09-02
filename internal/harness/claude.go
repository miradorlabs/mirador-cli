package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

	// The detailed-beta-tracing pair. Together these send logs and traces to
	// BETA_TRACING_ENDPOINT *instead of* through the configured exporters — a redirect
	// that bypasses OTEL_EXPORTER_OTLP_ENDPOINT entirely, so checking only the OTLP
	// variables would miss it.
	//
	// Anthropic treats this as part of the same boundary: managed settings that pin an
	// endpoint or credential strip a developer-set BETA_TRACING_ENDPOINT. Mirador writes
	// user settings, not managed settings, so it gets none of that protection and has to
	// do the check itself.
	claudeBetaTracingDetailed = "ENABLE_BETA_TRACING_DETAILED"
	claudeBetaTracingEndpoint = "BETA_TRACING_ENDPOINT"

	// claudeOtelHeadersHelper names a script that generates OTLP headers dynamically.
	// It is a *top-level* setting rather than an env entry, so a scan of the `env` block
	// never sees it — and it supplies the same Authorization header Mirador writes,
	// which means an existing helper decides what the export authenticates with while
	// Mirador reports itself connected.
	claudeOtelHeadersHelper = "otelHeadersHelper"
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

	// In helper mode the credential travels through the headers-helper script instead;
	// writing it here too would defeat the point of keeping it out of the settings file.
	if e.APIKey != "" && e.HelperPath == "" {
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
	// Helper mode keeps the key out of the settings file entirely; the prefix worth
	// reporting then lives in the helper script.
	if status.KeyPrefix == "" {
		if helper := stringSetting(s.root, claudeOtelHeadersHelper); helper != "" && isOwnHelper(helper) {
			status.KeyPrefix = MaskKey(keyFromHelper(helper))
		}
	}
	status.ProjectID = resourceAttribute(s.env[otelResourceAttributes], AttrProjectID)

	// With a journal, ownership is value-based: a key edited since connect is the user's
	// and must not make a later disconnect fall back to deleting it. Without a journal,
	// retain the legacy name-based count so older installations can still be cleaned up.
	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return Status{}, err
	}
	if j != nil {
		for key, installed := range j.Installed {
			if current, ok := s.env[key]; ok && current == installed {
				status.ManagedKeys++
			}
		}
		for key, installed := range j.InstalledSettings {
			if stringSetting(s.root, key) == installed {
				status.ManagedKeys++
			}
		}
	} else {
		for _, key := range claudeManagedKeys {
			if _, ok := s.env[key]; ok {
				status.ManagedKeys++
			}
		}
	}

	// Compared against the endpoint actually configured here, so status reports whether
	// this file is internally consistent — not whether it agrees with some other project.
	status.Conflicts = claudeConflicts(s.env, s.root, Exporter{
		Endpoint: status.Endpoint,
		Signals:  status.Signals,
	})
	return status, nil
}

// ConflictsWith reports what in the existing config would defeat the export e describes:
// a generic endpoint already pointing elsewhere, a per-signal override, or the
// detailed-beta-tracing redirect.
func (c Claude) ConflictsWith(e Exporter) ([]Conflict, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return nil, err
	}
	s, err := loadSettings(path)
	if err != nil {
		return nil, err
	}
	return claudeConflicts(s.env, s.root, e), nil
}

// claudeConflicts finds every setting that would send data — and with it Mirador's
// Authorization header — somewhere other than e.Endpoint, or break the export outright.
//
// e describes the export that will be in effect: the one about to be written during a
// connect, or the one already on disk when reporting status.
func claudeConflicts(env map[string]string, root map[string]json.RawMessage, e Exporter) []Conflict {
	var out []Conflict

	// Not an env entry, so nothing above would find it. Mirador's own helper is exempt:
	// it is the credential delivery this connect manages, not a foreign override.
	if helper := stringSetting(root, claudeOtelHeadersHelper); helper != "" && !isOwnHelper(helper) {
		out = append(out, Conflict{
			Key:        claudeOtelHeadersHelper,
			Value:      helper,
			Reason:     "supplies its own OTLP headers, which decide what the export authenticates with instead of Mirador's key",
			Credential: true,
			Scope:      ScopeUserSettings,
			Clearable:  true,
		})
	}

	// The generic endpoint pointing at another collector is not a disclosure — it is
	// overwritten — but a connect would replace an export the user set up. Reported
	// whether or not telemetry is currently switched on: a disabled config still holds
	// the destination someone chose, and connect would overwrite it just the same.
	if current := env[otelEndpoint]; current != "" && current != e.Endpoint {
		reason := "telemetry is already exporting here; connecting replaces it"
		if !isOn(env[claudeEnableTelemetry]) {
			reason = "a previously configured destination; connecting replaces it"
		}
		out = append(out, Conflict{
			Key: otelEndpoint, Value: current, Reason: reason,
			Scope: ScopeUserSettings, Clearable: true,
		})
	}

	for _, o := range perSignalOverrides {
		// A signal Mirador is not exporting cannot be redirected away from Mirador.
		if !e.HasSignal(o.signal) {
			continue
		}
		// Compared against the per-signal URL, not the base. The generic endpoint gets
		// `/v1/<signal>` appended; a per-signal variable does not, so the bare base URL
		// here posts to the wrong path and the suffixed URL is the only correct value.
		if v := env[o.endpoint]; v != "" && v != e.SignalEndpoint(o.signal) {
			reason := "overrides the endpoint for " + string(o.signal) + ", which would receive Mirador's credential"
			if v == e.Endpoint {
				reason = "is Mirador's base URL, which a per-signal endpoint does not append /v1/" +
					string(o.signal) + " to — " + string(o.signal) + " would post to the wrong path"
			}
			out = append(out, Conflict{
				Key:        o.endpoint,
				Value:      v,
				Reason:     reason,
				Credential: v != e.Endpoint,
				Scope:      ScopeUserSettings,
				Clearable:  true,
			})
		}
		if v := env[o.headers]; v != "" {
			// The value is a header bag that may itself hold a credential, so it is
			// named but never printed.
			out = append(out, Conflict{
				Key:       o.headers,
				Reason:    "merges into the headers for " + string(o.signal) + ", overriding Mirador's",
				Scope:     ScopeUserSettings,
				Clearable: true,
			})
		}
		if v := env[o.protocol]; v != "" && v != protocolHTTPProtobuf {
			out = append(out, Conflict{
				Key:       o.protocol,
				Value:     v,
				Reason:    "sends " + string(o.signal) + " over a protocol Mirador's endpoint does not serve",
				Scope:     ScopeUserSettings,
				Clearable: true,
			})
		}
	}

	// Detailed beta tracing diverts logs and traces to its own endpoint instead of the
	// exporters, so it defeats the connect without touching a single OTEL_* variable.
	// Only logs and traces move; a metrics-only connect is unaffected.
	//
	// Both halves are required for it to do anything: a saved endpoint with the switch
	// off is dormant, and blocking on that would be a false positive that also talks
	// --force into deleting two settings for no reason.
	if endpoint := env[claudeBetaTracingEndpoint]; endpoint != "" &&
		isOn(env[claudeBetaTracingDetailed]) &&
		(e.HasSignal(SignalTraces) || e.HasSignal(SignalLogs)) {
		out = append(out, Conflict{
			Key:    claudeBetaTracingEndpoint,
			Value:  endpoint,
			Reason: "detailed beta tracing sends logs and traces here instead of to Mirador",
			// Whether this carries OTEL_EXPORTER_OTLP_HEADERS is undocumented. Assumed
			// yes: the safe assumption for a redirect is that the credential follows it,
			// and Anthropic's managed settings strip this variable alongside credentials.
			Credential: true,
			Scope:      ScopeUserSettings,
			Clearable:  true,
		})
	}

	// Everything above is in the file Mirador owns. These are not, and outrank it.
	out = append(out, environmentConflicts(e)...)
	out = append(out, projectConflicts(e)...)
	return out
}

// stringSetting reads a top-level string out of the raw document.
func stringSetting(root map[string]json.RawMessage, key string) string {
	raw, ok := root[key]
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// Connect merges Mirador's variables into the settings file, leaving every other
// setting — hooks, permissions, model, statusLine — untouched.
//
// clearConflicts removes the per-signal overrides reported by ConflictsWith. Without it
// they are left alone, which is why the caller must refuse to connect while any remain:
// writing the credential and leaving the override in place is the leak.
func (c Claude) Connect(e Exporter, clearConflicts bool) error {
	env := c.Render(e)
	// The top-level settings this connect owns, alongside the env block. Today that is
	// only the headers helper; recorded in the journal the same way env keys are.
	settings := map[string]string{}
	if e.HelperPath != "" {
		settings[claudeOtelHeadersHelper] = e.HelperPath
	}

	path, err := c.ConfigPath()
	if err != nil {
		return err
	}
	s, err := loadSettings(path)
	if err != nil {
		return err
	}
	previousJournal, err := loadJournal(c.Name(), path)
	if err != nil {
		return err
	}

	cleared := map[string]string{}
	clearedSettings := map[string]string{}
	if clearConflicts {
		for _, conflict := range claudeConflicts(s.env, s.root, e) {
			// Only this file's settings are Mirador's to touch. A shell export or a
			// project file is somebody else's, and the caller refuses the connect rather
			// than pretending --force dealt with it.
			if !conflict.Clearable {
				continue
			}
			// The generic endpoint is about to be overwritten by the merge anyway;
			// deleting it here would be a no-op with a confusing name.
			if conflict.Key == otelEndpoint {
				continue
			}
			if conflict.Key == claudeOtelHeadersHelper {
				delete(s.root, claudeOtelHeadersHelper)
				clearedSettings[claudeOtelHeadersHelper] = conflict.Value
				continue
			}
			if value, ok := s.env[conflict.Key]; ok {
				cleared[conflict.Key] = value
			}
			delete(s.env, conflict.Key)
			// The beta-tracing switch and its endpoint are a pair — the switch alone
			// does nothing, so leaving it behind would just be dead config.
			if conflict.Key == claudeBetaTracingEndpoint {
				if value, ok := s.env[claudeBetaTracingDetailed]; ok {
					cleared[claudeBetaTracingDetailed] = value
				}
				delete(s.env, claudeBetaTracingDetailed)
			}
		}
	}

	// Recorded before the merge overwrites anything, so disconnect can put the previous
	// values back rather than inferring which keys were Mirador's.
	j := newJournal(c.Name(), path, s.env, env, cleared, clearedSettings, previousJournal)
	for key, value := range settings {
		j.InstalledSettings[key] = value
		if prior := stringSetting(s.root, key); prior != "" {
			prior := prior
			j.PreviousSettings[key] = &prior
		} else {
			j.PreviousSettings[key] = nil
		}
	}

	// The helper script goes down first: the settings file about to be written points
	// at it, and a harness starting between the two writes must find the credential
	// already there. Idempotent, so a re-run after a crash converges.
	if e.HelperPath != "" {
		if err := writeHelper(e.HelperPath, e.APIKey); err != nil {
			return err
		}
	}

	// Persist ownership first. If this fails, the credential-bearing settings have not
	// changed. A crash after this point but before the settings write leaves a harmless
	// journal whose installed values do not match, rather than an unjournaled credential.
	if err := j.save(); err != nil {
		return err
	}
	s.merge(env)
	for key, value := range settings {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		s.root[key] = encoded
	}
	// tighten only when the merged env carries the server key inline. In helper mode
	// the settings file holds a path, not a secret, so the user's own mode survives —
	// including a dotfiles-friendly 0644.
	defer pruneJournals()
	if err := s.save(e.HelperPath == ""); err != nil {
		// Put the preceding journal back so a failed reconnect does not replace the
		// ownership record for the still-current installation.
		var rollbackErr error
		if previousJournal != nil {
			rollbackErr = previousJournal.save()
		} else {
			rollbackErr = deleteJournal(c.Name(), path)
		}
		if rollbackErr != nil {
			return fmt.Errorf("write settings: %w (also failed to restore telemetry journal: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

// Disconnect undoes what the recorded connect did.
//
// With a journal it restores each key to the value it held beforehand and leaves alone
// any key edited since — a config someone has adjusted is their decision, not stale
// Mirador state. Without one (an older connect, a hand-edited config) it falls back to
// removing the managed keys, which is the best that can be done without a record and is
// reported as such.
func (c Claude) Disconnect() (DisconnectResult, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return DisconnectResult{}, err
	}
	s, err := loadSettings(path)
	if err != nil {
		return DisconnectResult{}, err
	}

	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return DisconnectResult{}, err
	}

	var result DisconnectResult
	var remaining *journal
	switch {
	// loadJournal only returns a record written for this exact config, so a non-nil
	// journal is by construction the right one.
	case j != nil:
		result, remaining = j.apply(s.env)
		// Top-level settings Mirador installed: same ownership rule as env keys. The
		// helper file itself goes with its setting — it holds the credential, and a
		// disconnect that left it behind would strand a live key on disk.
		for key, installed := range j.InstalledSettings {
			current := stringSetting(s.root, key)
			if current != installed {
				if current != "" {
					result.Skipped = append(result.Skipped, key)
					remaining.InstalledSettings[key] = installed
					remaining.PreviousSettings[key] = cloneString(j.PreviousSettings[key])
				}
				continue
			}
			if key == claudeOtelHeadersHelper && isOwnHelper(installed) {
				if err := deleteHelper(installed); err != nil {
					return result, err
				}
			}
			if prior := j.PreviousSettings[key]; prior != nil {
				encoded, err := json.Marshal(*prior)
				if err != nil {
					return result, err
				}
				s.root[key] = encoded
				result.Restored++
			} else {
				delete(s.root, key)
				result.Removed++
			}
		}
		for key, value := range j.ClearedSettings {
			if _, taken := s.root[key]; taken {
				result.Skipped = append(result.Skipped, key)
				remaining.ClearedSettings[key] = value
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return result, err
			}
			s.root[key] = encoded
			result.Restored++
		}
	default:
		// No record: an older connect, or a hand-edited config.
		result.Removed = s.remove(claudeManagedKeys)
		result.Unjournaled = true
	}

	sort.Strings(result.Skipped)
	defer pruneJournals()
	if result.Removed > 0 || result.Restored > 0 {
		// The credential is gone, so there is nothing left to tighten for.
		if err := s.save(false); err != nil {
			return result, err
		}
	}

	if j == nil {
		return result, nil
	}
	if remaining != nil && !remaining.empty() {
		return result, remaining.save()
	}
	return result, deleteJournal(c.Name(), path)
}

// exporterFromEnv reconstructs the shape of a rendered env map, so code holding only
// the map can still ask which signals it turns on.
func exporterFromEnv(env map[string]string) Exporter {
	e := Exporter{Endpoint: env[otelEndpoint]}
	for _, pair := range []struct {
		key    string
		signal Signal
	}{
		{otelTracesExporter, SignalTraces},
		{otelLogsExporter, SignalLogs},
		{otelMetricsExporter, SignalMetrics},
	} {
		if env[pair.key] == exporterOTLP {
			e.Signals = append(e.Signals, pair.signal)
		}
	}
	return e
}

// Backup exposes the pre-modification copy so `connect` can tell the user where it is.
//
// Whether the stored backup may be replaced comes from the journal, not from what the
// endpoint happens to say. Reading ownership off the endpoint gets it wrong both ways: a
// config someone wrote by hand against the Mirador endpoint looks like Mirador's, so its
// backup is kept stale and the config itself is later deleted; a Mirador config whose
// endpoint was edited out looks like the user's, so it overwrites the real original.
//
// endpoint is retained for the no-journal case — an older connect, or a hand-edited
// config — where the endpoint is the only evidence available.
//
// The rule the journal expresses: snapshot whatever Mirador did not write, and leave the
// snapshot alone when re-connecting over a config this CLI installed — including after a
// disconnect-and-reconfigure cycle, where a never-overwritten backup would otherwise go
// stale and the next disconnect would delete the only live copy.
func (c Claude) Backup(endpoint string) (string, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return "", err
	}
	s, err := loadSettings(path)
	if err != nil {
		return "", err
	}

	// A journal for this exact file means the current contents are Mirador's own work,
	// so the stored backup is still the pre-Mirador state and must be kept.
	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return "", err
	}
	if j != nil {
		return s.backup(false)
	}
	// No record: fall back to the endpoint, which is the only evidence there is.
	return s.backup(s.env[otelEndpoint] != endpoint)
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

// keyFromHeaders extracts the raw key from an OTEL_EXPORTER_OTLP_HEADERS value, or ""
// when none is configured. Callers that display it must mask it; the raw form exists
// for reuse, where handing back a masked key would mint an orphan instead.
func keyFromHeaders(headers string) string {
	for pair := range strings.SplitSeq(headers, ",") {
		name, value, found := strings.Cut(pair, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "Authorization") {
			continue
		}
		key := strings.TrimSpace(value)
		return strings.TrimSpace(strings.TrimPrefix(key, "Bearer"))
	}
	return ""
}

// CurrentCredential returns the key this config already presents to endpoint for
// projectID, whichever way it is delivered — helper script or inline header. It is the
// reuse path: a reconnect that minted a fresh key every time would leave a trail of
// live orphans nobody remembers, and the key already here is exactly as scoped as the
// one a mint would produce. Both endpoint and project must match: a key for another
// project would be rejected server-side, and one for another deployment would be
// disclosed to it.
func (c Claude) CurrentCredential(endpoint, projectID string) (string, bool) {
	path, err := c.ConfigPath()
	if err != nil {
		return "", false
	}
	s, err := loadSettings(path)
	if err != nil {
		return "", false
	}
	if s.env[otelEndpoint] != endpoint {
		return "", false
	}
	if resourceAttribute(s.env[otelResourceAttributes], AttrProjectID) != projectID {
		return "", false
	}
	if helper := stringSetting(s.root, claudeOtelHeadersHelper); helper != "" && isOwnHelper(helper) {
		if key := keyFromHelper(helper); key != "" {
			return key, true
		}
	}
	if key := keyFromHeaders(s.env[otelHeaders]); strings.HasPrefix(key, "mir_srv_") {
		return key, true
	}
	return "", false
}

// maskKeyFromHeaders extracts the key from an OTEL_EXPORTER_OTLP_HEADERS value and
// returns only its head. The full key is never returned: status output lands in
// terminals, screenshots, and bug reports.
func maskKeyFromHeaders(headers string) string {
	return MaskKey(keyFromHeaders(headers))
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
