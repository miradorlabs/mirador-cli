package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const miradorEndpoint = "https://otel.mirador.org"

// miradorExporter is the export a default connect installs: every signal, Mirador's
// endpoint.
func miradorExporter() Exporter {
	return Exporter{Endpoint: miradorEndpoint, Signals: AllSignals}
}

// The finding this file exists for: Claude Code resolves each signal's exporter from
// the per-signal endpoint when one is set, and merges the *generic* headers into it. A
// connect that writes only the generic endpoint plus an Authorization header therefore
// hands Mirador's server key to whoever owns that per-signal endpoint.
func TestConflictsDetectPerSignalEndpointLeak(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"https://other-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(miradorExporter())
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Key != "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" {
		t.Errorf("conflict key = %q", conflicts[0].Key)
	}
	if !conflicts[0].Credential {
		t.Error("a foreign per-signal endpoint must be flagged as a credential disclosure")
	}
}

// The OTLP spec: a generic endpoint gets `v1/<signal>` appended, but a per-signal
// endpoint "MUST be used as-is without any modification". So only the suffixed URL is
// equivalent — and the bare base URL, which looks equivalent, posts to the wrong path.
func TestConflictsComparePerSignalEndpointAgainstTheSignalPath(t *testing.T) {
	t.Run("suffixed url is equivalent", func(t *testing.T) {
		c, _ := claudeIn(t, `{"env":{
			"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"`+miradorEndpoint+`/v1/traces"
		}}`)

		conflicts, err := c.ConflictsWith(miradorExporter())
		if err != nil {
			t.Fatalf("ConflictsWith: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("got %+v, want none — this is exactly where the generic endpoint sends traces", conflicts)
		}
	})

	t.Run("bare base url posts to the wrong path", func(t *testing.T) {
		c, _ := claudeIn(t, `{"env":{
			"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"`+miradorEndpoint+`"
		}}`)

		conflicts, err := c.ConflictsWith(miradorExporter())
		if err != nil {
			t.Fatalf("ConflictsWith: %v", err)
		}
		if len(conflicts) != 1 {
			t.Fatalf("got %+v, want the bare base URL reported — no /v1/traces is appended to a per-signal endpoint", conflicts)
		}
		// It still goes to Mirador's host, so it is a broken export rather than a
		// credential handed to a stranger.
		if conflicts[0].Credential {
			t.Error("Mirador's own base URL is not a credential disclosure")
		}
	})
}

// A signal Mirador is not exporting cannot be redirected away from Mirador, so an
// override on it must not block the connect.
func TestConflictsIgnoreOverridesForDisabledSignals(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"https://other-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(Exporter{
		Endpoint: miradorEndpoint,
		Signals:  []Signal{SignalMetrics},
	})
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("got %+v, want none — traces are not being exported", conflicts)
	}
}

// Detailed beta tracing diverts logs and traces to its own endpoint instead of the
// exporters, so it defeats a connect without touching any OTEL_* variable. Mirador
// writes user settings, which get none of the managed-settings protection that would
// otherwise strip it.
func TestConflictsDetectBetaTracingRedirect(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"ENABLE_BETA_TRACING_DETAILED":"1",
		"BETA_TRACING_ENDPOINT":"https://beta-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(miradorExporter())
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Key != "BETA_TRACING_ENDPOINT" {
		t.Fatalf("got %+v, want the beta tracing redirect reported", conflicts)
	}
	if !conflicts[0].Credential {
		t.Error("a redirect of logs and traces must be treated as a credential disclosure")
	}
}

// Beta tracing moves only logs and traces; a metrics-only connect is unaffected.
func TestConflictsIgnoreBetaTracingForMetricsOnly(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"BETA_TRACING_ENDPOINT":"https://beta-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(Exporter{
		Endpoint: miradorEndpoint,
		Signals:  []Signal{SignalMetrics},
	})
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("got %+v, want none — beta tracing does not move metrics", conflicts)
	}
}

func TestConnectClearsBetaTracingPairWhenAsked(t *testing.T) {
	c, path := claudeIn(t, `{"env":{
		"ENABLE_BETA_TRACING_DETAILED":"1",
		"BETA_TRACING_ENDPOINT":"https://beta-collector.example.com"
	}}`)

	if err := c.Connect(c.Render(fullExporter()), true); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	env := envOf(t, path)
	for _, key := range []string{"BETA_TRACING_ENDPOINT", "ENABLE_BETA_TRACING_DETAILED"} {
		if _, ok := env[key]; ok {
			// The switch alone does nothing, so leaving it is dead config.
			t.Errorf("%s survived --force", key)
		}
	}
}

func TestConflictsDetectPerSignalHeadersAndProtocol(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS":"Authorization=Bearer someone-elses-key",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL":"grpc"
	}}`)

	conflicts, err := c.ConflictsWith(miradorExporter())
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 2 {
		t.Fatalf("got %d conflicts, want 2: %+v", len(conflicts), conflicts)
	}

	for _, c := range conflicts {
		// A header bag may itself be a credential; the key is named, the value never is.
		if c.Key == "OTEL_EXPORTER_OTLP_LOGS_HEADERS" && c.Value != "" {
			t.Errorf("a header value was captured for display: %q", c.Value)
		}
	}
}

// A generic endpoint already exporting elsewhere is not a leak — it gets overwritten —
// but replacing someone's working collector must be a decision, not a side effect.
func TestConflictsReportExistingGenericEndpoint(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"CLAUDE_CODE_ENABLE_TELEMETRY":"1",
		"OTEL_EXPORTER_OTLP_ENDPOINT":"https://their-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(miradorExporter())
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Key != "OTEL_EXPORTER_OTLP_ENDPOINT" {
		t.Fatalf("got %+v, want the existing generic endpoint reported", conflicts)
	}
	// It is overwritten, so it is not a disclosure.
	if conflicts[0].Credential {
		t.Error("the generic endpoint is replaced by the connect; it is not a credential leak")
	}
}

// A destination configured while telemetry is switched off is still a destination
// someone chose, and connect overwrites it just the same. Ignoring it because nothing
// is currently exporting is how a disconnect-reconfigure-reconnect cycle silently
// destroys the replacement configuration.
func TestConflictsReportGenericEndpointEvenWhenTelemetryIsOff(t *testing.T) {
	c, _ := claudeIn(t, `{"env":{
		"CLAUDE_CODE_ENABLE_TELEMETRY":"0",
		"OTEL_EXPORTER_OTLP_ENDPOINT":"https://their-collector.example.com"
	}}`)

	conflicts, err := c.ConflictsWith(miradorExporter())
	if err != nil {
		t.Fatalf("ConflictsWith: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Key != "OTEL_EXPORTER_OTLP_ENDPOINT" {
		t.Fatalf("got %+v, want the dormant destination reported", conflicts)
	}
	if !strings.Contains(conflicts[0].Reason, "previously configured") {
		t.Errorf("reason = %q, want it to say the export is not currently live", conflicts[0].Reason)
	}
}

// The finding this rule exists for: connect (backup A), disconnect, reconfigure to B,
// reconnect. A backup that is never overwritten still holds A, so the reconnect replaces
// B and the next disconnect deletes it — B unrecoverable. The backup must track the last
// non-Mirador state, not the first one ever seen.
func TestBackupTracksTheLatestNonMiradorConfiguration(t *testing.T) {
	c, path := claudeIn(t, `{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"https://collector-a.example.com"}}`)

	if _, err := c.Backup(miradorEndpoint); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := c.Connect(c.Render(fullExporter()), true); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// The user sets up a different collector while disconnected.
	s, err := loadSettings(path)
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	s.env[otelEndpoint] = "https://collector-b.example.com"
	if err := s.save(false); err != nil {
		t.Fatalf("save: %v", err)
	}

	backup, err := c.Backup(miradorEndpoint)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(data), "collector-b") {
		t.Fatalf("backup holds a stale configuration; B is about to be overwritten and lost:\n%s", data)
	}
}

// Connect must not silently delete the user's settings. Without clearConflicts the
// override survives, which is exactly why the command refuses to connect while it does.
func TestConnectLeavesConflictsAloneByDefault(t *testing.T) {
	c, path := claudeIn(t, `{"env":{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"https://other-collector.example.com"
	}}`)

	if err := c.Connect(c.Render(fullExporter()), false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := envOf(t, path)["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]; got != "https://other-collector.example.com" {
		t.Fatalf("the override was removed without being asked: %q", got)
	}
}

func TestConnectClearsConflictsWhenAsked(t *testing.T) {
	c, path := claudeIn(t, `{"env":{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":"https://other-collector.example.com",
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS":"Authorization=Bearer someone-elses-key",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL":"grpc",
		"EDITOR":"vim"
	}}`)

	if err := c.Connect(c.Render(fullExporter()), true); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	env := envOf(t, path)
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	} {
		if _, ok := env[key]; ok {
			t.Errorf("%s survived --force", key)
		}
	}
	// Only the conflicts go. Everything else is still the user's.
	if env["EDITOR"] != "vim" {
		t.Error("--force removed an unrelated setting")
	}
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != miradorEndpoint {
		t.Errorf("generic endpoint = %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
}

// Status must not report a Mirador endpoint while a per-signal override quietly sends
// that signal somewhere else.
func TestStatusReportsConflicts(t *testing.T) {
	c, _ := claudeIn(t, "")
	if err := c.Connect(c.Render(fullExporter()), false); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Simulate an override added after the fact.
	s, err := loadSettings(mustConfigPath(t, c))
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	s.env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "https://other-collector.example.com"
	if err := s.save(false); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Conflicts) != 1 {
		t.Fatalf("status reported %d conflicts, want 1: %+v", len(st.Conflicts), st.Conflicts)
	}
	if !st.Conflicts[0].Credential {
		t.Error("status did not flag the override as a credential disclosure")
	}
}

// Connect overwrites the telemetry variables and disconnect deletes them; neither
// remembers what was there. The backup is the only record of the user's original
// collector, so a re-connect must not replace it with a copy of Mirador's own settings.
func TestBackupIsNotOverwrittenByAReconnect(t *testing.T) {
	const original = `{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"https://their-collector.example.com"}}`
	c, _ := claudeIn(t, original)

	first, err := c.Backup(miradorEndpoint)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := c.Connect(c.Render(fullExporter()), true); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// The second connect backs up a file that now contains Mirador's settings.
	second, err := c.Backup(miradorEndpoint)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if second != first {
		t.Fatalf("a second backup path was created (%q vs %q)", second, first)
	}

	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(data), "their-collector.example.com") {
		t.Fatalf("the backup no longer holds the original configuration:\n%s", data)
	}
	if strings.Contains(string(data), "mir_srv_") {
		t.Fatal("the backup was replaced with a copy of Mirador's own settings")
	}
}

// Anyone keeping ~/.claude/settings.json as a link into a dotfiles repo would find the
// link silently swapped for a regular file, detaching it from the repo.
func TestConnectWritesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}

	dir := t.TempDir()
	realDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	target := filepath.Join(realDir, "settings.json")
	if err := os.WriteFile(target, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := Claude{}
	if err := c.Connect(c.Render(fullExporter()), false); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file; a dotfiles link would be broken")
	}
	// The content must have landed in the real file, not just anywhere.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(data), "CLAUDE_CODE_ENABLE_TELEMETRY") {
		t.Fatalf("the write did not reach the symlink target:\n%s", data)
	}

	// And the backup belongs next to the real file, not next to the link.
	if _, err := os.Stat(target + ".mirador.bak"); err != nil {
		if _, err2 := c.Backup(miradorEndpoint); err2 != nil {
			t.Fatalf("Backup: %v", err2)
		}
		if _, err := os.Stat(target + ".mirador.bak"); err != nil {
			t.Errorf("backup was not written alongside the resolved file: %v", err)
		}
	}
}

// Telemetry switched off, or the endpoint deleted, leaves the server key on disk. That
// config is not "connected", and disconnect keying off Connected would walk away from it.
func TestStatusCountsManagedKeysWhenNotConnected(t *testing.T) {
	c, _ := claudeIn(t, "")
	if err := c.Connect(c.Render(fullExporter()), false); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	s, err := loadSettings(mustConfigPath(t, c))
	if err != nil {
		t.Fatalf("loadSettings: %v", err)
	}
	s.env[claudeEnableTelemetry] = "0"
	if err := s.save(false); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Connected {
		t.Fatal("telemetry is off; this must not report as connected")
	}
	if st.ManagedKeys == 0 {
		t.Fatal("no managed keys counted, so disconnect would leave the server key on disk")
	}
	if st.KeyPrefix == "" {
		t.Error("the key is still configured and should still be reported")
	}

	// And disconnect must actually clear it.
	removed, err := c.Disconnect()
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if removed == 0 {
		t.Fatal("disconnect removed nothing from a config that still held the key")
	}
	after, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if after.ManagedKeys != 0 || after.KeyPrefix != "" {
		t.Fatalf("settings survived disconnect: %+v", after)
	}
}

func mustConfigPath(t *testing.T, c Claude) string {
	t.Helper()
	path, err := c.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	return path
}

// Writing through a dangling link would replace the link with a regular file, silently
// detaching a dotfiles setup from its repo. There is no safe way to guess where the
// missing target belonged, so this is refused rather than papered over.
func TestConnectRefusesADanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	link := filepath.Join(dir, "settings.json")
	missing := filepath.Join(t.TempDir(), "not-checked-out", "settings.json")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := Claude{}
	err := c.Connect(c.Render(fullExporter()), false)
	if err == nil {
		t.Fatal("Connect followed a dangling symlink")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %q, want it to explain the broken link", err)
	}

	// The link itself must survive untouched.
	info, lerr := os.Lstat(link)
	if lerr != nil {
		t.Fatalf("lstat: %v", lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the dangling link was replaced by a regular file")
	}
}

// Disconnecting a settings file that holds nothing but Mirador's keys empties the
// document. Deleting the target of a link would leave the link dangling and break the
// next read, so a linked file is emptied to `{}` instead.
func TestDisconnectKeepsASymlinkTargetAlive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	realDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	target := filepath.Join(realDir, "settings.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := Claude{}
	if err := c.Connect(c.Render(fullExporter()), false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := c.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the symlink target was deleted, leaving the link dangling: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a regular file")
	}
	// And the file must still be readable as settings.
	st, err := c.Status()
	if err != nil {
		t.Fatalf("Status after disconnect: %v", err)
	}
	if st.ManagedKeys != 0 {
		t.Errorf("managed keys survived: %d", st.ManagedKeys)
	}
}
