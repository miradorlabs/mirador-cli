package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/config"
	"github.com/miradorlabs/mirador-cli/internal/harness"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

func newTelemetryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "telemetry",
		Aliases: []string{"otel"},
		Short:   "Connect agent harnesses to Mirador telemetry",
		Long: `Configures an agent CLI to export OpenTelemetry to Mirador.

Connecting mints a server key scoped to one project and writes it, along with the
OTLP endpoint, into the harness's own configuration. Nothing is added to your shell
profile, and no other setting in that file is touched.

Prompts, model responses, and tool content are excluded by default. Turning them on
is an explicit flag, because it sends what you and the model said — and what your
tools read and wrote — off this machine.

Supported: ` + strings.Join(harness.Names(), ", ") + `.`,
	}
	cmd.AddCommand(newTelemetryConnectCommand(), newTelemetryStatusCommand(), newTelemetryDisconnectCommand())
	return cmd
}

type connectFlags struct {
	signals            string
	includePrompts     bool
	includeToolContent bool
	keyName            string
	apiKey             string
	identity           string
	assumeYes          bool
	force              bool
}

func newTelemetryConnectCommand() *cobra.Command {
	var f connectFlags

	cmd := &cobra.Command{
		Use:   "connect <" + strings.Join(harness.Names(), "|") + ">",
		Short: "Point an agent harness at Mirador",
		Long: `Mints a server key for the selected project and writes the harness's telemetry
configuration.

The key is created server-side and returned exactly once; it goes straight into the
harness config, and the file is tightened to 0600 because it now holds a credential.
Your existing settings in that file are preserved — only Mirador's own keys are
written, and ` + "`telemetry disconnect`" + ` removes exactly those.

Pass --api-key to install a key you already hold instead of minting a new one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTelemetryConnect(cmd, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.signals, "signals", "", "comma-separated signals to export: traces, logs, metrics (default all)")
	fl.BoolVar(&f.includePrompts, "include-prompts", false, "also export prompt text and model responses")
	fl.BoolVar(&f.includeToolContent, "include-tool-content", false, "also export tool parameters, input, and output")
	fl.StringVar(&f.keyName, "key-name", "", "name for the minted key (defaults to <harness>@<hostname>)")
	fl.StringVar(&f.apiKey, "api-key", "", "install this existing mir_srv_ key instead of minting a new one")
	fl.StringVar(&f.identity, "identity", "", "value for enduser.id (defaults to your global git email; \"none\" to omit)")
	fl.BoolVarP(&f.assumeYes, "yes", "y", false, "skip the confirmation prompt")
	fl.BoolVar(&f.force, "force", false, "remove conflicting per-signal OTLP settings instead of refusing to connect")
	return cmd
}

func runTelemetryConnect(cmd *cobra.Command, name string, f connectFlags) error {
	h, err := harness.Lookup(name)
	if err != nil {
		return err
	}
	signals, err := harness.ParseSignals(f.signals)
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := requireProject(cfg); err != nil {
		return err
	}
	// A server key is bound to a project id, so the id has to be known locally. Under
	// MIRADOR_API_KEY the project is fixed by the key's own grant and never recorded in
	// the profile, which is a different failure than "no project selected".
	if cfg.ProjectID == "" {
		return errors.New("telemetry connect needs an explicit project — pass --project or run `mirador project use`")
	}

	// ConfigPath before anything else: if the file cannot even be located, nothing below
	// is worth doing, and a key minted here would be stranded.
	configPath, err := h.ConfigPath()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	detection := h.Detect(ctx)

	// The exporter this connect intends to install. Built before the key exists so the
	// conflict check can run first: a per-signal endpoint or a beta-tracing redirect left
	// in place would keep exporting to whoever owns it while inheriting the Authorization
	// header Mirador is about to write — handing out a live server key. Once the key is
	// on disk that is invisible, so it has to be caught here.
	intended := harness.Exporter{
		Endpoint:           cfg.OTLPURL,
		Signals:            signals,
		ResourceAttributes: resourceAttributes(ctx, h, cfg, f.identity),
		IncludePrompts:     f.includePrompts,
		IncludeToolContent: f.includeToolContent,
	}
	conflicts, err := h.ConflictsWith(intended)
	if err != nil {
		return err
	}

	printConnectPlan(out, h, cfg, detection, configPath, signals, f)
	printConflicts(out, conflicts, f.force)

	// A conflict outside the user file — exported in the shell, or set in a project
	// settings file that outranks it — cannot be cleared by writing to the user file, so
	// --force must not pretend otherwise. Connecting anyway would install the credential
	// while the override kept deciding where telemetry goes.
	if blocking := unclearable(conflicts); len(blocking) > 0 {
		return fmt.Errorf(
			"%s reads these from outside your user settings, where Mirador cannot change them: %s — unset or remove them, then retry",
			h.DisplayName(), strings.Join(blocking, ", "))
	}
	if len(conflicts) > 0 && !f.force {
		return fmt.Errorf(
			"%s already has OTLP settings that would override this connect — remove them, or pass --force to have Mirador remove them",
			h.DisplayName())
	}

	if !f.assumeYes {
		ok, err := confirm(cmd, fmt.Sprintf("Connect %s to Mirador?", h.DisplayName()))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled. Nothing was written.")
			return nil
		}
	}

	key, keyMeta, minted, err := resolveKey(ctx, cfg, h, f)
	if err != nil {
		return err
	}

	// Back up before the merge. Best-effort: a user who asked to connect should not be
	// blocked because a backup could not be written, but they should hear about it.
	if backup, err := backupHarnessConfig(h, cfg.OTLPURL); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not back up %s (%v).\n", configPath, err)
	} else if backup != "" {
		fmt.Fprintf(out, "\nBacked up %s\n", backup)
	}

	intended.APIKey = key
	if err := h.Connect(h.Render(intended), f.force); err != nil {
		// Only when this invocation created it. A key supplied with --api-key already
		// existed and is still perfectly good, so telling the user to go revoke it would
		// send them to destroy a working credential over an unrelated write failure.
		if minted {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nA key (%s) was minted before this failed. Revoke it in the web app if you do not retry.\n",
				keyMeta.KeyPrefix)
		}
		return err
	}

	fmt.Fprintf(out, "\nConnected. Restart %s, then run a prompt.\n", h.DisplayName())
	fmt.Fprintf(out, "View traces with: mirador trace list --filter 'service.name=\"%s\"'\n", harness.ServiceName(h))
	return nil
}

// printConnectPlan shows exactly what a connect would do, before it does any of it.
// The redaction lines are always printed, including when they are off — "off" is the
// answer to the question a reader actually has.
func printConnectPlan(
	out io.Writer,
	h harness.Harness,
	cfg *config.Config,
	detection harness.Detection,
	configPath string,
	signals []harness.Signal,
	f connectFlags,
) {
	if detection.Found {
		version := detection.Version
		if version == "" {
			version = "version unknown"
		}
		fmt.Fprintf(out, "%s found: %s\n", h.DisplayName(), version)
	} else {
		// Not an error: the config is read whenever it is eventually started.
		fmt.Fprintf(out, "%s not found on PATH — the configuration will still be written.\n", h.DisplayName())
	}
	fmt.Fprintf(out, "  Mirador project: %s\n", displayOrg(cfg.ProjectName, cfg.ProjectID))
	fmt.Fprintf(out, "  Endpoint:        %s\n", cfg.OTLPURL)

	fmt.Fprintln(out, "\n  Signals:")
	for _, s := range harness.AllSignals {
		mark := " "
		if containsSignal(signals, s) {
			mark = "✓"
		}
		fmt.Fprintf(out, "    %s %s\n", mark, signalLabel(s))
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "    Prompts:      %s\n", onOff(f.includePrompts))
	fmt.Fprintf(out, "    Tool content: %s\n", onOff(f.includeToolContent))

	fmt.Fprintln(out, "\n  This will update:")
	fmt.Fprintf(out, "    %s\n", configPath)
	if f.apiKey == "" {
		fmt.Fprintln(out, "\n  A new server key will be minted for this project.")
	} else {
		fmt.Fprintf(out, "\n  Installing the key you supplied (%s).\n", harness.MaskKey(f.apiKey))
	}
	fmt.Fprintln(out)
}

// resolveKey either installs a key the caller already holds or mints a new one. The
// bool reports which happened, because only a key this invocation created is the
// caller's to clean up if a later step fails.
func resolveKey(ctx context.Context, cfg *config.Config, h harness.Harness, f connectFlags) (string, api.ServerKey, bool, error) {
	if key := strings.TrimSpace(f.apiKey); key != "" {
		if !strings.HasPrefix(key, "mir_srv_") {
			return "", api.ServerKey{}, false, errors.New("--api-key expects a mir_srv_ server key")
		}
		return key, api.ServerKey{KeyPrefix: harness.MaskKey(key)}, false, nil
	}

	name := strings.TrimSpace(f.keyName)
	if name == "" {
		name = h.Name() + "@" + harness.Hostname()
	}

	client, err := newClient(cfg)
	if err != nil {
		return "", api.ServerKey{}, false, err
	}
	key, meta, err := client.CreateServerKey(ctx, cfg.ProjectID, name,
		"Created by mirador telemetry connect "+h.Name())
	if err != nil {
		return "", api.ServerKey{}, false, err
	}
	return key, meta, true, nil
}

// resourceAttributes are stamped on everything the harness emits.
//
// identity overrides the default enduser.id. The literal "none" omits it — worth having
// because the default is a real email address written into a global config file, and
// there is no other way to say "do not label my sessions".
func resourceAttributes(ctx context.Context, h harness.Harness, cfg *config.Config, identity string) map[string]string {
	attrs := map[string]string{
		harness.AttrServiceName: harness.ServiceName(h),
		harness.AttrProjectID:   cfg.ProjectID,
	}

	switch identity = strings.TrimSpace(identity); identity {
	case "none":
	case "":
		// git's *global* email: this lands in a global config and labels every future
		// session, so a repository-local address would follow the user out of the repo
		// it was set in. Resolved here and written as a literal — config holds strings,
		// not shell.
		if email := harness.GitEmail(ctx); email != "" {
			attrs[harness.AttrEnduserID] = email
		}
	default:
		attrs[harness.AttrEnduserID] = identity
	}
	return attrs
}

// printConflicts explains what is in the way, naming each variable so the user can go
// and look at it. A header value is never printed: it is the one that may hold someone
// else's credential.
func printConflicts(out io.Writer, conflicts []harness.Conflict, force bool) {
	if len(conflicts) == 0 {
		return
	}

	if force {
		fmt.Fprintln(out, "  Conflicting settings, which --force will remove where it can:")
	} else {
		fmt.Fprintln(out, "  Conflicting settings:")
	}
	for _, c := range conflicts {
		if c.Value != "" {
			fmt.Fprintf(out, "    %s=%s\n", c.Key, output.SanitizeTerminal(c.Value))
		} else {
			fmt.Fprintf(out, "    %s\n", c.Key)
		}
		marker := " "
		if c.Credential {
			marker = "!"
		}
		fmt.Fprintf(out, "     %s [%s] %s\n", marker, c.Scope, output.SanitizeTerminal(c.Reason))
		if !c.Clearable {
			// Say it here as well as in the error: this is the one the user has to go
			// and fix themselves.
			fmt.Fprintf(out, "       Mirador cannot change this — it is outside your user settings.\n")
		}
	}
	fmt.Fprintln(out)
}

// unclearable names the conflicts --force cannot resolve, which are fatal.
func unclearable(conflicts []harness.Conflict) []string {
	var out []string
	for _, c := range conflicts {
		if !c.Clearable {
			out = append(out, c.Key)
		}
	}
	return out
}

// backupHarnessConfig snapshots the file before it is merged, when the harness exposes
// a way to. Not part of the Harness interface: a harness whose config is not a single
// file it owns has nothing meaningful to snapshot.
func backupHarnessConfig(h harness.Harness, endpoint string) (string, error) {
	type backuper interface {
		Backup(endpoint string) (string, error)
	}
	if b, ok := h.(backuper); ok {
		return b.Backup(endpoint)
	}
	return "", nil
}

func newTelemetryStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status [" + strings.Join(harness.Names(), "|") + "]",
		Short: "Show which harnesses are connected",
		Long: `Reads each harness's own configuration and reports what is installed there.

This is the harness's view, not Mirador's: it says what the harness is configured to
send, not whether anything has arrived. With no argument it reports every harness.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			format, err := resolveFormat()
			if err != nil {
				return err
			}

			targets := harness.All()
			if len(args) == 1 {
				h, err := harness.Lookup(args[0])
				if err != nil {
					return err
				}
				targets = []harness.Harness{h}
			}

			rows := make([][]string, 0, len(targets))
			report := make([]telemetryStatus, 0, len(targets))
			for _, h := range targets {
				entry := describeStatus(cmd.Context(), h, cfg)
				report = append(report, entry)
				rows = append(rows, []string{
					h.Name(),
					entry.Installed,
					entry.State,
					entry.Signals,
					entry.Prompts,
					entry.ToolContent,
				})
			}

			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"HARNESS", "INSTALLED", "TELEMETRY", "SIGNALS", "PROMPTS", "TOOL CONTENT"},
				Rows:    rows,
			}, telemetryStatusReport{Harnesses: report})
		},
	}
}

type telemetryStatusReport struct {
	Harnesses []telemetryStatus `json:"harnesses"`
}

// telemetryStatus is the machine-readable view. KeyPrefix is the masked head only —
// the key itself is never rendered, in any format.
type telemetryStatus struct {
	Harness     string `json:"harness"`
	Installed   string `json:"installed"`
	Version     string `json:"version,omitempty"`
	State       string `json:"state"`
	ConfigPath  string `json:"config_path,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	KeyPrefix   string `json:"key_prefix,omitempty"`
	Signals     string `json:"signals,omitempty"`
	Prompts     string `json:"prompts,omitempty"`
	ToolContent string `json:"tool_content,omitempty"`
	// Conflicts names the per-signal overrides that make Endpoint above only part of
	// the truth. Reporting a Mirador endpoint while a per-signal override quietly sends
	// that signal — and the credential — elsewhere is the failure this exists to prevent.
	Conflicts []string `json:"conflicts,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func describeStatus(ctx context.Context, h harness.Harness, cfg *config.Config) telemetryStatus {
	entry := telemetryStatus{Harness: h.Name(), Installed: "no"}

	detection := h.Detect(ctx)
	if detection.Found {
		entry.Installed = "yes"
		entry.Version = detection.Version
	}

	st, err := h.Status()
	if err != nil {
		// An unsupported harness is a state, not a failure — reporting it as an error
		// would make `telemetry status` exit non-zero just for listing Codex.
		var unsupported *harness.ErrUnsupported
		if errors.As(err, &unsupported) {
			entry.State = "unsupported"
			return entry
		}
		entry.State = "error"
		entry.Error = err.Error()
		return entry
	}

	entry.ConfigPath = st.ConfigPath
	entry.Endpoint = st.Endpoint
	entry.ProjectID = st.ProjectID
	entry.KeyPrefix = st.KeyPrefix
	for _, c := range st.Conflicts {
		entry.Conflicts = append(entry.Conflicts, c.Key)
	}

	switch {
	case !st.Connected:
		// Settings left behind after the switch was turned off still hold the key, and
		// `disconnect` still has work to do — so this is not the same as "nothing here".
		if st.ManagedKeys > 0 {
			entry.State = "settings present, not exporting"
			return entry
		}
		entry.State = "not connected"
		return entry
	case len(st.Conflicts) > 0:
		// Say this rather than "connected": some signal is going somewhere else, and the
		// endpoint column alone would be a lie.
		entry.State = "connected, overridden"
	case st.Endpoint != cfg.OTLPURL:
		// Telemetry is on, but aimed somewhere else. Saying "connected" here would be
		// wrong in the way that costs the most time to discover.
		entry.State = "connected elsewhere"
	default:
		entry.State = "connected"
	}

	entry.Signals = joinSignals(st.Signals)
	if entry.Signals == "" {
		entry.Signals = "none"
	}
	entry.Prompts = onOff(st.IncludePrompts)
	entry.ToolContent = onOff(st.IncludeToolContent)
	return entry
}

func newTelemetryDisconnectCommand() *cobra.Command {
	var assumeYes bool

	cmd := &cobra.Command{
		Use:   "disconnect <" + strings.Join(harness.Names(), "|") + ">",
		Short: "Stop a harness exporting to Mirador",
		Long: `Removes the telemetry settings Mirador wrote, and nothing else.

The server key stays live — it is bound to the project, not to this machine, and may
be in use elsewhere. Revoke it in the web app when you are done with it; the key's
masked prefix is printed so you can find it in the list.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := harness.Lookup(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			st, err := h.Status()
			if err != nil {
				return err
			}
			// Keyed off the settings actually present, not off Connected. A config with
			// telemetry switched off, or with the endpoint deleted, is not "connected" —
			// but it still has Mirador's server key sitting in it, and that is the state
			// where walking away would be worst.
			if st.ManagedKeys == 0 {
				fmt.Fprintf(out, "%s has no Mirador telemetry settings. Nothing to do.\n", h.DisplayName())
				return nil
			}
			if !st.Connected {
				fmt.Fprintf(out, "%s is not exporting, but Mirador settings are still present.\n\n", h.DisplayName())
			}

			fmt.Fprintf(out, "This will remove Mirador's telemetry settings from:\n  %s\n", st.ConfigPath)
			if st.KeyPrefix != "" {
				fmt.Fprintf(out, "\nThe server key %s stays live — revoke it in the web app.\n", st.KeyPrefix)
			}
			fmt.Fprintln(out)

			if !assumeYes {
				ok, err := confirm(cmd, fmt.Sprintf("Disconnect %s?", h.DisplayName()))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "Cancelled. Nothing was changed.")
					return nil
				}
			}

			result, err := h.Disconnect()
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "\nDisconnected. Removed %d setting(s) from %s.\n", result.Removed, st.ConfigPath)
			if result.Restored > 0 {
				fmt.Fprintf(out, "Restored %d setting(s) to the value held before Mirador connected.\n", result.Restored)
			}
			if len(result.Skipped) > 0 {
				// Changed after the connect, so they are somebody's deliberate edit and
				// not Mirador's to throw away.
				fmt.Fprintf(out, "Left alone (changed since connecting): %s\n", strings.Join(result.Skipped, ", "))
			}
			if result.Unjournaled {
				fmt.Fprintf(out, "No record of the original values survived, so they were removed rather than restored.\n")
			}
			fmt.Fprintf(out, "Restart %s for it to take effect.\n", h.DisplayName())
			return nil
		},
	}

	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// confirm asks a yes/no question. It refuses to run without a terminal rather than
// assuming yes: this command writes a credential into a config file, and a piped
// invocation that meant to be non-interactive should say so with --yes.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if !output.Interactive() {
		return false, fmt.Errorf("%s — no terminal to confirm on; pass --yes to proceed non-interactively", question)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "%s [Y/n] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		// EOF with nothing typed is a decline, not a crash.
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
			return false, nil
		}
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func containsSignal(signals []harness.Signal, want harness.Signal) bool {
	return slices.Contains(signals, want)
}

func joinSignals(signals []harness.Signal) string {
	parts := make([]string, 0, len(signals))
	for _, s := range signals {
		parts = append(parts, string(s))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// signalLabel names a signal the way the docs do, so the plan reads as prose rather
// than as a list of OTLP nouns.
func signalLabel(s harness.Signal) string {
	switch s {
	case harness.SignalTraces:
		return "Agent traces"
	case harness.SignalLogs:
		return "Structured events"
	case harness.SignalMetrics:
		return "Token and cost metrics"
	default:
		return string(s)
	}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
