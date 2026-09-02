package harness

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Codex configures the Codex CLI's OpenTelemetry export.
//
// Codex does not read the OTEL_* environment variables. Its export is configured in the
// `[otel]` table of config.toml, one exporter per signal, each carrying its own endpoint
// and headers — so there is no generic endpoint for a per-signal override to leak a
// credential through, and no headers-helper mechanism to keep the key out of the file.
// The key is written inline and the file tightened to 0600. The key names are Codex's
// own contract (codex-rs/config, OtelConfigToml); they are constants here so a rename
// upstream is a one-line, compile-time-visible change.
//
// Verified against Codex 0.152. What each signal carries:
//
//   - traces: spans per turn and model request, with gen_ai.usage.* token counts;
//   - logs: codex.* events — conversation starts, API requests, codex.sse_event with
//     per-response token counts, codex.user_prompt, codex.tool_decision,
//     codex.tool_result, and codex.turn_cost with the estimated USD;
//   - metrics: counters and histograms including codex.turn.token_usage and
//     codex.turn.cost_microusd.
type Codex struct{}

const (
	// The three exporters. `exporter` is the log exporter — the name predates the other
	// two — and each defaults to "none" except metrics, which defaults to "statsig":
	// OpenAI's own analytics. Connecting the metrics signal replaces that.
	codexLogExporter     = "exporter"
	codexTraceExporter   = "trace_exporter"
	codexMetricsExporter = "metrics_exporter"

	// codexLogUserPrompt is the one content switch. Off upstream; with it off the
	// codex.user_prompt event carries "[REDACTED]" and the prompt's length. Codex never
	// exports model response text, so this is the whole of the prompts posture.
	codexLogUserPrompt = "log_user_prompt"

	// codexToolResult caps the bytes of tool output on the codex.tool_result event
	// (2048 upstream). Zero drops the output. Tool *arguments* — the command that ran —
	// are logged regardless; Codex has no switch for those.
	codexToolResult         = "tool_result"
	codexToolResultMaxBytes = "max_bytes"

	// codexSpanAttributes are stamped on every span, and only spans. It is the only
	// per-user attribute Codex accepts: resource attributes are not configurable, so
	// logs and metrics carry Codex's own service.name and env and nothing of Mirador's.
	codexSpanAttributes = "span_attributes"

	// The analytics opt-out is its own table, not an otel key. When `enabled` is false
	// Codex installs no metrics exporter at all, whatever `metrics_exporter` says — the
	// one way a metrics connect can look right and send nothing.
	codexAnalyticsTable      = "analytics"
	codexAnalyticsEnabledKey = "enabled"

	codexExporterNone     = "none"
	codexExporterStatsig  = "statsig"
	codexExporterOTLPHTTP = "otlp-http"
	codexExporterOTLPGRPC = "otlp-grpc"

	codexEndpointKey = "endpoint"
	codexHeadersKey  = "headers"
	codexProtocolKey = "protocol"
	// codexProtocolBinary is http/protobuf in Codex's spelling; json is the other.
	codexProtocolBinary = "binary"

	codexAuthorizationHeader = "Authorization"

	// codexServiceName is the originator the codex CLI stamps as service.name.
	codexServiceName = "codex_cli_rs"

	codexConfigFile = "config.toml"
	// codexProfileSuffix names the profile files under CODEX_HOME: `<name>.config.toml`,
	// layered over config.toml while that profile is selected with --profile.
	codexProfileSuffix = ".config.toml"

	// The administrator-managed layers, which outrank everything the user writes. The
	// file is read on macOS and Linux; the managed preference is macOS-only and is what
	// an MDM profile delivers, as a base64-encoded config.toml. Windows has neither:
	// Codex 0.152 ignores a managed_config.toml there and says so at startup.
	codexManagedConfigUnix       = "/etc/codex/managed_config.toml"
	codexManagedPreferenceDomain = "com.openai.codex"
	codexManagedPreferenceKey    = "config_toml_base64"
)

// codexSignalKeys maps each signal to its exporter key, in report order.
var codexSignalKeys = []struct {
	signal Signal
	key    string
}{
	{SignalTraces, codexTraceExporter},
	{SignalLogs, codexLogExporter},
	{SignalMetrics, codexMetricsExporter},
}

func (Codex) Name() string        { return "codex" }
func (Codex) DisplayName() string { return "Codex" }

// SupportsHeadersHelper: headers are literal strings in config.toml, nothing else.
func (Codex) SupportsHeadersHelper() bool { return false }

// codexVersionRE pulls the semver out of `codex --version`, whose output is
// "codex-cli 0.152.0".
var codexVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+[^\s]*`)

// Detect runs `codex --version`. A missing binary is not-found rather than an error.
func (Codex) Detect(ctx context.Context) Detection {
	path, err := exec.LookPath("codex")
	if err != nil {
		return Detection{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return Detection{Found: true, Path: path}
	}
	return Detection{
		Found:   true,
		Path:    path,
		Version: codexVersionRE.FindString(strings.TrimSpace(string(out))),
	}
}

// codexHome is $CODEX_HOME, or ~/.codex. Honouring the variable matters for the same
// reason CLAUDE_CONFIG_DIR does: writing where Codex does not read is a connect that
// silently does nothing.
func codexHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// ConfigPath is $CODEX_HOME/config.toml.
func (Codex) ConfigPath() (string, error) {
	dir, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, codexConfigFile), nil
}

// Render maps an Exporter onto the otel table. Values are TOML text — the canonical
// rendering of each value, which is also the form the ownership journal records.
//
// Only the exporters for the selected signals are written. Codex's exporters are
// self-contained, so an unselected signal is simply left as it was: for logs and traces
// that is off, and for metrics it is OpenAI's own statsig route, which is not Mirador's
// to switch off on the way past.
func (Codex) Render(e Exporter) map[string]string {
	out := map[string]string{
		codexLogUserPrompt: mustRenderTOML(e.IncludePrompts),
	}
	for _, sk := range codexSignalKeys {
		if e.HasSignal(sk.signal) {
			out[sk.key] = mustRenderTOML(codexOTLPExporter(e, sk.signal))
		}
	}
	// Written only when excluding: with content on, Codex's own cap (or one the user
	// chose) stays in force. A stale zero from an earlier exclude is undone by Connect.
	if !e.IncludeToolContent {
		out[codexToolResult] = mustRenderTOML(map[string]any{codexToolResultMaxBytes: int64(0)})
	}
	if attrs := codexSpanAttributeValues(e.ResourceAttributes); len(attrs) > 0 {
		out[codexSpanAttributes] = mustRenderTOML(attrs)
	}
	return out
}

// codexOTLPExporter is the exporter value for one signal: otlp-http, binary protocol
// (which traverses ordinary HTTPS proxies, as with Claude), the signal-specific URL —
// Codex uses an exporter endpoint as-is, so it must carry /v1/<signal> — and the
// Authorization header inline.
func codexOTLPExporter(e Exporter, s Signal) map[string]any {
	inner := map[string]any{
		codexEndpointKey: e.SignalEndpoint(s),
		codexProtocolKey: codexProtocolBinary,
	}
	if e.APIKey != "" {
		inner[codexHeadersKey] = map[string]any{codexAuthorizationHeader: "Bearer " + e.APIKey}
	}
	return map[string]any{codexExporterOTLPHTTP: inner}
}

// codexSpanAttributeValues is the resource attributes minus service.name: Codex stamps
// its own on the resource, and repeating it as a span attribute would only confuse.
func codexSpanAttributeValues(attrs map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range attrs {
		if k == "" || v == "" || k == AttrServiceName {
			continue
		}
		out[k] = v
	}
	return out
}

func mustRenderTOML(v any) string {
	s, err := renderTOMLValue(v)
	if err != nil {
		// Only values built above reach here, and every one of them renders.
		panic(err)
	}
	return s
}

// codexExporterShape is what a parsed exporter value says about itself.
type codexExporterShape struct {
	// Kind is none, statsig, otlp-http, otlp-grpc, "" when absent, or "unknown".
	Kind     string
	Endpoint string
	Headers  map[string]any
}

func codexExporterOf(v any) codexExporterShape {
	switch x := v.(type) {
	case nil:
		return codexExporterShape{}
	case string:
		switch x {
		case codexExporterNone, codexExporterStatsig:
			return codexExporterShape{Kind: x}
		}
	case map[string]any:
		if len(x) == 1 {
			for kind, inner := range x {
				if kind != codexExporterOTLPHTTP && kind != codexExporterOTLPGRPC {
					break
				}
				fields, _ := inner.(map[string]any)
				endpoint, _ := fields[codexEndpointKey].(string)
				headers, _ := fields[codexHeadersKey].(map[string]any)
				return codexExporterShape{Kind: kind, Endpoint: endpoint, Headers: headers}
			}
		}
	}
	return codexExporterShape{Kind: "unknown"}
}

// codexBaseEndpoint strips the /v1/<signal> suffix Codex needs on an exporter endpoint,
// giving back the base URL a Mirador profile is configured with. An endpoint without
// the suffix is returned as-is: it is misconfigured, and status should say where it
// points rather than hide it.
func codexBaseEndpoint(endpoint string, s Signal) string {
	return strings.TrimSuffix(endpoint, "/v1/"+string(s))
}

// codexMiradorExporter reports whether v is an exporter Mirador would have written for
// endpoint and signal — otlp-http at exactly the signal URL. The key is not compared:
// a reconnect with a different key is still the same destination.
func codexMiradorExporter(v any, endpoint string, s Signal) bool {
	shape := codexExporterOf(v)
	return shape.Kind == codexExporterOTLPHTTP && endpoint != "" &&
		shape.Endpoint == (Exporter{Endpoint: endpoint}).SignalEndpoint(s)
}

// codexKeyFromExporter extracts the bearer token from an exporter's headers, or "".
func codexKeyFromExporter(v any) string {
	shape := codexExporterOf(v)
	for name, value := range shape.Headers {
		if !strings.EqualFold(name, codexAuthorizationHeader) {
			continue
		}
		s, _ := value.(string)
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Bearer"))
	}
	return ""
}

func codexSpanAttribute(otel map[string]any, key string) string {
	attrs, _ := otel[codexSpanAttributes].(map[string]any)
	s, _ := attrs[key].(string)
	return s
}

// codexToolContentOn reads the tool_result cap: absent means Codex's default, which
// sends output; only an explicit zero drops it.
func codexToolContentOn(otel map[string]any) bool {
	table, ok := otel[codexToolResult].(map[string]any)
	if !ok {
		return true
	}
	switch n := table[codexToolResultMaxBytes].(type) {
	case int64:
		return n > 0
	case float64:
		return n > 0
	default:
		return true
	}
}

func codexAnalyticsDisabled(doc map[string]any) bool {
	table, _ := doc[codexAnalyticsTable].(map[string]any)
	v, ok := table[codexAnalyticsEnabledKey].(bool)
	return ok && !v
}

// canonicalOtel renders each key of the table as TOML text, the form the journal
// compares and records. A value that cannot be rendered is reported rather than
// dropped: silently losing it would make disconnect unable to restore it.
func canonicalOtel(otel map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(otel))
	for k, v := range otel {
		s, err := renderTOMLValue(v)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", otelTable, k, err)
		}
		out[k] = s
	}
	return out, nil
}

// otelFromCanonical is the inverse of canonicalOtel.
func otelFromCanonical(values map[string]string) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for k, text := range values {
		v, err := parseTOMLValue(text)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", otelTable, k, err)
		}
		out[k] = v
	}
	return out, nil
}

// Status reads back what is currently installed.
func (c Codex) Status() (Status, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return Status{}, err
	}
	f, err := loadTOML(path)
	if err != nil {
		return Status{}, err
	}

	status := Status{
		ConfigPath:         path,
		Exists:             f.existed,
		IncludePrompts:     f.otel[codexLogUserPrompt] == true,
		IncludeToolContent: codexToolContentOn(f.otel),
		ProjectID:          codexSpanAttribute(f.otel, AttrProjectID),
	}

	// The endpoint is whichever OTLP destination the first configured signal uses, and
	// the signals are those sharing it. Codex has no on/off switch beyond the exporters
	// themselves, so an OTLP exporter present is telemetry on.
	for _, sk := range codexSignalKeys {
		shape := codexExporterOf(f.otel[sk.key])
		if shape.Kind != codexExporterOTLPHTTP && shape.Kind != codexExporterOTLPGRPC {
			continue
		}
		base := codexBaseEndpoint(shape.Endpoint, sk.signal)
		if status.Endpoint == "" {
			status.Endpoint = base
		}
		if codexMiradorExporter(f.otel[sk.key], status.Endpoint, sk.signal) {
			status.Signals = append(status.Signals, sk.signal)
			// Only a Mirador key is worth naming. The exporter may carry somebody
			// else's bearer token, and even its head has no business in a status line.
			if key := codexKeyFromExporter(f.otel[sk.key]); status.KeyPrefix == "" && strings.HasPrefix(key, "mir_srv_") {
				status.KeyPrefix = MaskKey(key)
			}
		}
	}
	status.Connected = status.Endpoint != ""

	// Compared against the endpoint actually configured here, so status reports whether
	// this file is internally consistent. Computed before the analytics check below
	// drops metrics, so that check is what gets reported.
	status.Conflicts = codexConflicts(f, Exporter{
		Endpoint: status.Endpoint,
		Signals:  status.Signals,
	})
	// A metrics exporter with analytics disabled is set and sends nothing. Reporting
	// metrics as on would send someone hunting in Mirador for data Codex never sent.
	if codexAnalyticsDisabled(f.doc) {
		status.Signals = withoutSignal(status.Signals, SignalMetrics)
	}

	// Ownership is by journal only. There is no name-based fallback here, unlike
	// Claude's: no released build ever wrote a Codex config without a journal, so a
	// config with none is somebody else's work — a company collector, say — and every
	// standard otel key in it is theirs. Counting those as managed would let disconnect
	// delete a telemetry setup Mirador never touched.
	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return Status{}, err
	}
	if j != nil {
		current, err := canonicalOtel(f.otel)
		if err != nil {
			return Status{}, err
		}
		for key, installed := range j.Installed {
			if current[key] == installed {
				status.ManagedKeys++
			}
		}
	}
	return status, nil
}

// ConflictsWith reports what in the existing config would replace, defeat, or outrank
// the export e describes.
func (c Codex) ConflictsWith(e Exporter) ([]Conflict, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := loadTOML(path)
	if err != nil {
		return nil, err
	}
	return codexConflicts(f, e), nil
}

// codexConflicts finds, for each signal e exports: an exporter already pointing
// somewhere else (replaced by the connect, so it needs consent); the analytics opt-out
// that silences metrics regardless of the exporter; and the same keys in files that
// outrank the user config, which Mirador cannot change.
//
// There is no credential-disclosure case here. Each Codex exporter carries its own
// headers, so nothing Mirador writes for one signal can be inherited by another
// destination.
func codexConflicts(f *tomlFile, e Exporter) []Conflict {
	var out []Conflict

	for _, sk := range codexSignalKeys {
		if !e.HasSignal(sk.signal) {
			continue
		}
		shape := codexExporterOf(f.otel[sk.key])
		key := otelTable + "." + sk.key
		switch shape.Kind {
		case "", codexExporterNone, codexExporterStatsig:
			// Off, or Codex's own default route. Nothing of the user's is being replaced.
			continue
		case codexExporterOTLPHTTP:
			if shape.Endpoint == e.SignalEndpoint(sk.signal) {
				continue // a reconnect
			}
			reason := "already exports " + string(sk.signal) + " here; connecting replaces it"
			if strings.TrimRight(shape.Endpoint, "/") == strings.TrimRight(e.Endpoint, "/") {
				reason = "is Mirador's base URL, which Codex posts to as-is — " + string(sk.signal) +
					" would go to the wrong path; connecting replaces it with " + e.SignalEndpoint(sk.signal)
			}
			out = append(out, Conflict{
				Key: key, Value: shape.Endpoint, Reason: reason,
				Scope: ScopeUserSettings, Clearable: true,
			})
		case codexExporterOTLPGRPC:
			out = append(out, Conflict{
				Key:    key,
				Value:  shape.Endpoint,
				Reason: "already exports " + string(sk.signal) + " here over gRPC; connecting replaces it",
				Scope:  ScopeUserSettings, Clearable: true,
			})
		default:
			out = append(out, Conflict{
				Key:    key,
				Reason: "is set to something Mirador does not recognize as an exporter; connecting replaces it",
				Scope:  ScopeUserSettings, Clearable: true,
			})
		}
	}

	// Not an otel key, and not Mirador's to flip: it is the user's opt-out from OpenAI's
	// analytics as well as the metrics switch. Refuse the metrics signal instead.
	if e.HasSignal(SignalMetrics) && codexAnalyticsDisabled(f.doc) {
		out = append(out, Conflict{
			Key:   codexAnalyticsTable + "." + codexAnalyticsEnabledKey,
			Value: "false",
			Reason: "Codex sends no metrics at all while analytics are disabled, so the metrics signal " +
				"would connect and deliver nothing — remove this setting, or connect with --signals traces,logs",
			Scope:     ScopeUserSettings,
			Clearable: false,
		})
	}

	out = append(out, codexManagedConflicts(e)...)
	out = append(out, codexProfileConflicts(e)...)
	return out
}

// codexManagedConflicts reports exporters set by an administrator, in the layers that
// outrank the user config: /etc/codex/managed_config.toml on macOS and Linux, and on
// macOS the managed preference an MDM profile delivers. A project's .codex/config.toml
// is deliberately not scanned: Codex refuses `otel` from project-local config, so a
// setting there is inert and must not block a connect.
func codexManagedConflicts(e Exporter) []Conflict {
	var out []Conflict
	if runtime.GOOS != "windows" {
		if data, err := os.ReadFile(codexManagedConfigUnix); err == nil {
			out = append(out, codexConflictsInLayer(data, codexManagedConfigUnix, ScopeManaged,
				"set in "+codexManagedConfigUnix+", which Codex applies over your user config", e)...)
		}
	}
	if runtime.GOOS == "darwin" {
		if data, ok := codexManagedPreference(); ok {
			source := codexManagedPreferenceDomain + ":" + codexManagedPreferenceKey
			out = append(out, codexConflictsInLayer(data, source, ScopeManaged,
				"set by the managed preference "+source+", which Codex applies over your user config", e)...)
		}
	}
	return out
}

// codexManagedPreference reads the MDM-delivered config through `defaults`, which
// consults the same managed-preferences domain Codex does. Absent is the ordinary case.
func codexManagedPreference() ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "defaults", "read", codexManagedPreferenceDomain, codexManagedPreferenceKey).Output()
	if err != nil {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// codexProfileConflicts reports exporters set in a profile file — `<name>.config.toml`
// beside config.toml — which is layered over the user config whenever that profile is
// selected with --profile. Which profile a session will use is not knowable here, so
// every profile is scanned; the reason names the one at fault.
func codexProfileConflicts(e Exporter) []Conflict {
	home, err := codexHome()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var out []Conflict
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, codexProfileSuffix) || name == codexConfigFile {
			continue
		}
		profile := strings.TrimSuffix(name, codexProfileSuffix)
		path := filepath.Join(home, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, codexConflictsInLayer(data, name, ScopeProfile,
			"set in "+path+", which Codex applies over your user config whenever that profile is selected with --profile "+profile, e)...)
	}
	return out
}

// codexConflictsInLayer reports any exporter for a selected signal in a higher-
// precedence layer. Any value counts, including an explicit "none": that layer decides
// the signal's destination, and the user-level exporter Mirador writes would never be
// consulted. Keys are qualified by their source, so a status report cannot mistake
// them for settings in the user config.
func codexConflictsInLayer(data []byte, source, scope, reason string, e Exporter) []Conflict {
	var doc struct {
		Otel map[string]any `toml:"otel"`
	}
	if unmarshalTOMLLenient(data, &doc) != nil {
		// A file this CLI cannot parse is not this CLI's to complain about; Codex will
		// report its own parse error.
		return nil
	}

	var out []Conflict
	for _, sk := range codexSignalKeys {
		if !e.HasSignal(sk.signal) {
			continue
		}
		v, ok := doc.Otel[sk.key]
		if !ok {
			continue
		}
		shape := codexExporterOf(v)
		value := shape.Endpoint
		if value == "" {
			value = shape.Kind
		}
		out = append(out, Conflict{
			Key:       source + ":" + otelTable + "." + sk.key,
			Value:     value,
			Reason:    reason,
			Scope:     scope,
			Clearable: false,
		})
	}
	return out
}

// Connect merges Mirador's keys into the otel table, leaving every other key — in the
// table and in the file — as it was.
//
// A key an earlier connect installed that this one does not write (the metrics exporter
// on a reconnect with fewer signals, the tool-result cap once content is back on) is
// put back to its pre-Mirador value here, provided it still holds what Mirador wrote.
// Otherwise a reconnect that asked for less would silently keep sending more.
func (c Codex) Connect(e Exporter, clearConflicts bool) error {
	rendered := c.Render(e)

	path, err := c.ConfigPath()
	if err != nil {
		return err
	}
	f, err := loadTOML(path)
	if err != nil {
		return err
	}
	previousJournal, err := loadJournal(c.Name(), path)
	if err != nil {
		return err
	}
	current, err := canonicalOtel(f.otel)
	if err != nil {
		return err
	}

	cleared := map[string]string{}
	if clearConflicts {
		for _, conflict := range codexConflicts(f, e) {
			if !conflict.Clearable {
				continue
			}
			key := strings.TrimPrefix(conflict.Key, otelTable+".")
			// About to be overwritten by the merge anyway; the journal records the
			// prior value as Previous, and disconnect restores it from there.
			if _, overwritten := rendered[key]; overwritten {
				continue
			}
			if value, ok := current[key]; ok {
				cleared[key] = value
			}
			delete(current, key)
		}
	}

	// carried is the earlier record minus the keys restored here, so the new journal
	// does not inherit ownership of a key this connect just gave back. previousJournal
	// itself is kept intact for the rollback below.
	carried := previousJournal
	if previousJournal != nil {
		copied := *previousJournal
		copied.Installed = maps.Clone(previousJournal.Installed)
		copied.Previous = maps.Clone(previousJournal.Previous)
		for key, installed := range previousJournal.Installed {
			if _, writes := rendered[key]; writes {
				continue
			}
			if current[key] != installed {
				continue // edited since; theirs now, and named on disconnect
			}
			if prior := previousJournal.Previous[key]; prior != nil {
				current[key] = *prior
			} else {
				delete(current, key)
			}
			delete(copied.Installed, key)
			delete(copied.Previous, key)
		}
		carried = &copied
	}

	j := newJournal(c.Name(), path, current, rendered, cleared, nil, carried)

	maps.Copy(current, rendered)
	f.otel, err = otelFromCanonical(current)
	if err != nil {
		return err
	}

	// Ownership first, then the file, with the same rollback as Claude's connect.
	if err := j.save(); err != nil {
		return err
	}
	defer pruneJournals()
	if err := f.save(e.APIKey != ""); err != nil {
		var rollbackErr error
		if previousJournal != nil {
			rollbackErr = previousJournal.save()
		} else {
			rollbackErr = deleteJournal(c.Name(), path)
		}
		if rollbackErr != nil {
			return fmt.Errorf("write config: %w (also failed to restore telemetry journal: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

// Disconnect undoes the recorded connect, or without a record removes the managed keys.
func (c Codex) Disconnect() (DisconnectResult, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return DisconnectResult{}, err
	}
	f, err := loadTOML(path)
	if err != nil {
		return DisconnectResult{}, err
	}
	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return DisconnectResult{}, err
	}
	current, err := canonicalOtel(f.otel)
	if err != nil {
		return DisconnectResult{}, err
	}

	// Without a journal there is nothing here that Mirador wrote — see Status — so
	// there is nothing to undo, and certainly not a foreign telemetry setup to delete.
	if j == nil {
		return DisconnectResult{}, nil
	}
	result, remaining := j.apply(current)
	sort.Strings(result.Skipped)

	defer pruneJournals()
	if result.Removed > 0 || result.Restored > 0 {
		f.otel, err = otelFromCanonical(current)
		if err != nil {
			return result, err
		}
		if err := f.save(false); err != nil {
			return result, err
		}
	}

	if remaining != nil && !remaining.empty() {
		return result, remaining.save()
	}
	return result, deleteJournal(c.Name(), path)
}

// Backup exposes the pre-modification copy. Same rule as Claude's: a journal for this
// file means its contents are Mirador's work and the stored backup is kept; without
// one, whether the config points at endpoint is the only evidence.
func (c Codex) Backup(endpoint string) (string, error) {
	path, err := c.ConfigPath()
	if err != nil {
		return "", err
	}
	f, err := loadTOML(path)
	if err != nil {
		return "", err
	}
	j, err := loadJournal(c.Name(), path)
	if err != nil {
		return "", err
	}
	if j != nil {
		return f.backup(false)
	}
	pointsAtMirador := false
	for _, sk := range codexSignalKeys {
		if codexMiradorExporter(f.otel[sk.key], endpoint, sk.signal) {
			pointsAtMirador = true
		}
	}
	return f.backup(!pointsAtMirador)
}

// ConnectNotes are the two things a Codex connect does, or cannot do, that the plan's
// generic lines do not say: connecting metrics takes them away from OpenAI's own
// route, and excluding tool content cannot exclude the tool's arguments.
func (Codex) ConnectNotes(e Exporter) []string {
	var notes []string
	if e.HasSignal(SignalMetrics) {
		notes = append(notes, "Codex sends metrics to OpenAI (statsig) unless configured otherwise; after this connect they go to Mirador instead.")
	}
	if !e.IncludeToolContent {
		notes = append(notes, "Codex has no switch for tool arguments: tool output is dropped, but the command or parameters of each tool call are still exported.")
	}
	return notes
}

// CurrentCredential returns the key already installed for endpoint and projectID, so a
// reconnect reuses it instead of minting an orphan. Both must match, for the reasons
// given on Claude's.
func (c Codex) CurrentCredential(endpoint, projectID string) (string, bool) {
	path, err := c.ConfigPath()
	if err != nil {
		return "", false
	}
	f, err := loadTOML(path)
	if err != nil {
		return "", false
	}
	if codexSpanAttribute(f.otel, AttrProjectID) != projectID {
		return "", false
	}
	for _, sk := range codexSignalKeys {
		if !codexMiradorExporter(f.otel[sk.key], endpoint, sk.signal) {
			continue
		}
		if key := codexKeyFromExporter(f.otel[sk.key]); strings.HasPrefix(key, "mir_srv_") {
			return key, true
		}
	}
	return "", false
}
