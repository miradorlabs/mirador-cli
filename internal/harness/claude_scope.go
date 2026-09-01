package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Claude Code resolves a setting from several files, and the user-level one Mirador
// writes is the *lowest* precedence of them: managed settings, then `--settings`, then
// `.claude/settings.local.json`, then `.claude/settings.json`, then
// `~/.claude/settings.json`. An `env` block is an ordinary key and follows that order.
//
// So checking only the file Mirador writes is not enough to know where telemetry will
// actually go. A project file — or a variable exported in the shell — can define
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and win, while the user file supplies the generic
// Authorization header carrying Mirador's key. Neither of those is Mirador's to edit, so
// they are reported as conflicts that --force cannot clear and connect must refuse.
//
// This is a best-effort scan of the directory the CLI happens to be run from. Claude Code
// may later run somewhere else entirely, with different project settings; that is stated
// in the docs rather than papered over here.

// redirectKeys are the settings that decide where telemetry goes, and therefore the ones
// worth hunting for outside the user file.
func redirectKeys() []string {
	keys := []string{otelEndpoint, otelHeaders, claudeBetaTracingEndpoint}
	for _, o := range perSignalOverrides {
		keys = append(keys, o.endpoint, o.headers, o.protocol)
	}
	return keys
}

// environmentConflicts reports redirect settings exported in the process environment.
//
// Whether a settings-file `env` entry beats an already-exported shell variable is not
// documented for the OTEL_* names, so this refuses to guess: an exported value that
// disagrees with what Mirador is about to install is reported, and the user is told to
// unset it. Being wrong in the other direction would mean writing a credential into a
// config whose destination is decided elsewhere.
func environmentConflicts(e Exporter) []Conflict {
	var out []Conflict
	for _, key := range redirectKeys() {
		value := os.Getenv(key)
		if value == "" {
			continue
		}
		if !relevantToExporter(key, e) {
			continue
		}
		if expected, ok := expectedValue(key, e); ok && value == expected {
			continue
		}
		out = append(out, Conflict{
			Key:        key,
			Value:      redactIfHeader(key, value),
			Reason:     "exported in your shell, where it takes effect regardless of what is written to the settings file",
			Credential: isRedirect(key),
			Scope:      ScopeEnvironment,
			Clearable:  false,
		})
	}
	return out
}

// projectConflicts reports redirect settings in the project files that outrank the user
// file. It walks up from the working directory so a subdirectory of a repository still
// finds the project's settings, stopping at the repository root.
func projectConflicts(e Exporter) []Conflict {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}

	var out []Conflict
	for {
		// Local before shared, matching Claude Code's own precedence.
		for _, name := range []string{"settings.local.json", "settings.json"} {
			path := filepath.Join(dir, ".claude", name)
			out = append(out, conflictsInProjectFile(path, e)...)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break // repository root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root
		}
		dir = parent
	}
	return out
}

func conflictsInProjectFile(path string, e Exporter) []Conflict {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Env               map[string]string `json:"env"`
		OtelHeadersHelper string            `json:"otelHeadersHelper"`
	}
	if json.Unmarshal(data, &doc) != nil {
		// A project file this CLI cannot parse is not this CLI's to complain about; the
		// user file is still checked, and Claude Code will report its own parse error.
		return nil
	}

	var out []Conflict
	for _, key := range redirectKeys() {
		value, ok := doc.Env[key]
		if !ok || value == "" || !relevantToExporter(key, e) {
			continue
		}
		if expected, ok := expectedValue(key, e); ok && value == expected {
			continue
		}
		out = append(out, Conflict{
			Key:        key,
			Value:      redactIfHeader(key, value),
			Reason:     "set in " + path + ", which Claude Code applies over your user settings",
			Credential: isRedirect(key),
			Scope:      ScopeProject,
			Clearable:  false,
		})
	}
	if doc.OtelHeadersHelper != "" {
		out = append(out, Conflict{
			Key:        claudeOtelHeadersHelper,
			Value:      doc.OtelHeadersHelper,
			Reason:     "set in " + path + ", and supplies its own OTLP headers over your user settings",
			Credential: true,
			Scope:      ScopeProject,
			Clearable:  false,
		})
	}
	return out
}

// relevantToExporter drops keys for signals Mirador is not exporting: they cannot
// redirect telemetry that is never sent.
func relevantToExporter(key string, e Exporter) bool {
	for _, o := range perSignalOverrides {
		if key == o.endpoint || key == o.headers || key == o.protocol {
			return e.HasSignal(o.signal)
		}
	}
	if key == claudeBetaTracingEndpoint {
		return e.HasSignal(SignalTraces) || e.HasSignal(SignalLogs)
	}
	return true
}

// expectedValue is what Mirador would itself write for a key, so a value that already
// agrees is not reported as a conflict.
func expectedValue(key string, e Exporter) (string, bool) {
	switch key {
	case otelEndpoint:
		return e.Endpoint, true
	}
	for _, o := range perSignalOverrides {
		switch key {
		case o.endpoint:
			return e.SignalEndpoint(o.signal), true
		case o.protocol:
			return protocolHTTPProtobuf, true
		}
	}
	return "", false
}

// isRedirect reports whether a key can move telemetry — and Mirador's Authorization
// header with it — to a host Mirador does not control.
func isRedirect(key string) bool {
	if key == claudeBetaTracingEndpoint {
		return true
	}
	for _, o := range perSignalOverrides {
		if key == o.endpoint {
			return true
		}
	}
	return false
}

// redactIfHeader keeps a header bag out of terminal output: it may hold somebody else's
// credential, and naming the variable is enough to act on.
func redactIfHeader(key, value string) string {
	if key == otelHeaders {
		return ""
	}
	for _, o := range perSignalOverrides {
		if key == o.headers {
			return ""
		}
	}
	return value
}
