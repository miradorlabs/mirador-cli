package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/miradorlabs/mirador-cli/internal/harness"
)

// The command tree is the whole user-facing surface; a subcommand lost in a refactor
// would not fail any other test here.
func TestTelemetryCommandTree(t *testing.T) {
	root := NewRootCommand()

	for _, path := range []string{"telemetry connect", "telemetry status", "telemetry disconnect"} {
		fields := strings.Fields(path)
		cmd, _, err := root.Find(fields)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		// Find falls back to the closest parent when a leaf is missing, so the resolved
		// name has to be checked rather than trusted.
		if leaf := fields[len(fields)-1]; cmd.Name() != leaf {
			t.Fatalf("`mirador %s` resolves to %q — the command does not exist", path, cmd.CommandPath())
		}
		if cmd.RunE == nil && cmd.Run == nil {
			t.Errorf("`mirador %s` exists but does nothing", path)
		}
	}
}

// Every capture flag is a privacy decision. A flag renamed or dropped silently would
// change what leaves the machine.
func TestTelemetryConnectFlags(t *testing.T) {
	root := NewRootCommand()
	cmd, _, err := root.Find([]string{"telemetry", "connect"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	for _, name := range []string{"signals", "include-prompts", "include-tool-content", "key-name", "api-key", "yes"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`telemetry connect` has no --%s flag", name)
		}
	}

	// The two content flags must default to off. This is the single most important
	// default in the command.
	for _, name := range []string{"include-prompts", "include-tool-content"} {
		if got := cmd.Flags().Lookup(name).DefValue; got != "false" {
			t.Errorf("--%s defaults to %q, want false — content capture must be opt-in", name, got)
		}
	}
}

// A harness typo must be a clear local error, not a request or a silent no-op.
func TestTelemetryRejectsUnknownHarness(t *testing.T) {
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"telemetry", "connect", "gemini", "--yes"})

	err := root.Execute()
	if err == nil {
		t.Fatal("connect accepted an unknown harness")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error = %q, want it to name the harness", err)
	}
	// The message should point at what is available rather than just refusing.
	for _, name := range harness.Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want it to list the supported harness %q", err, name)
		}
	}
}

func TestTelemetryConnectRequiresAHarness(t *testing.T) {
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"telemetry", "connect"})

	if err := root.Execute(); err == nil {
		t.Fatal("connect ran without naming a harness")
	}
}

// `--signals` is validated before anything is minted or written: a typo'd signal would
// otherwise leave a harness looking connected and emitting nothing.
func TestTelemetryRejectsUnknownSignalBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"telemetry", "connect", "claude",
		"--signals", "spans",
		"--api-key", "mir_srv_test",
		"--project", "770e8400-e29b-41d4-a716-446655440000",
		"--yes",
	})

	err := root.Execute()
	if err == nil {
		t.Fatal("connect accepted an unknown signal")
	}
	if !strings.Contains(err.Error(), "spans") {
		t.Errorf("error = %q, want it to name the bad signal", err)
	}

	// Nothing should have been written on the way to that error.
	h := harness.Claude{}
	st, statusErr := h.Status()
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if st.Exists {
		t.Fatal("a settings file was created despite the command failing validation")
	}
}

// --api-key installs a credential verbatim; a value that is not a server key would be
// written into the config and fail only later, at export time.
func TestTelemetryRejectsNonServerKey(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"telemetry", "connect", "claude",
		"--api-key", "mir_cli_not_a_server_key",
		"--project", "770e8400-e29b-41d4-a716-446655440000",
		"--yes",
	})

	err := root.Execute()
	if err == nil {
		t.Fatal("connect accepted a CLI token as --api-key")
	}
	if !strings.Contains(err.Error(), "mir_srv_") {
		t.Errorf("error = %q, want it to name the expected prefix", err)
	}
}

// status must work without a credential — it only reads local files — so it stays
// usable when a login has expired.
func TestTelemetryStatusNeedsNoCredential(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"telemetry", "status", "-o", "json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status failed without a credential: %v", err)
	}
	if !strings.Contains(out.String(), `"harness": "claude"`) {
		t.Errorf("status output did not report the claude harness:\n%s", out.String())
	}
}

// An unimplemented harness is a state, not a failure — listing Codex must not make
// `telemetry status` exit non-zero.
func TestTelemetryStatusReportsCodexAsUnsupported(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	var out bytes.Buffer
	root := NewRootCommand()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"telemetry", "status", "codex", "-o", "json"})

	if err := root.Execute(); err != nil {
		t.Fatalf("status on an unsupported harness returned an error: %v", err)
	}
	if !strings.Contains(out.String(), "unsupported") {
		t.Errorf("codex was not reported as unsupported:\n%s", out.String())
	}
}
