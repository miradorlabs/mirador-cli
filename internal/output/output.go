// Package output renders command results in the format the caller asked for.
//
// The default is a human table, but the CLI flips to JSON automatically when it is
// not attached to a terminal or when an agent harness is detected — a piped or
// agent-driven invocation almost always wants to parse the result, and silently
// getting column-aligned text is the most common way that goes wrong.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
	FormatCSV   Format = "csv"
)

// agentEnvVars are set by the coding agents that shell out to CLIs. Their presence
// means the consumer is a model, not a person reading a terminal.
var agentEnvVars = []string{
	"CLAUDECODE",
	"CLAUDE_CODE",
	"CURSOR_TRACE_ID",
	"CLINE_ACTIVE",
	"GITHUB_COPILOT_CLI",
	"AIDER_MODEL",
}

// AgentMode reports whether the CLI is being driven by an agent harness.
func AgentMode() bool {
	for _, key := range agentEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// Interactive reports whether stdout is a terminal a human is likely watching.
func Interactive() bool {
	if AgentMode() {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Resolve turns the --output flag into a concrete format. An explicit flag always
// wins; otherwise a non-terminal or agent caller gets JSON.
func Resolve(flag string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(flag)) {
	case "":
		if Interactive() {
			return FormatTable, nil
		}
		return FormatJSON, nil
	case "table":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "csv":
		return FormatCSV, nil
	default:
		return "", fmt.Errorf("unknown output format %q (want table, json, yaml, or csv)", flag)
	}
}

// Table is the shape every list command produces. Rows carry pre-rendered strings so
// each command decides its own formatting once, rather than the renderer guessing.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Render writes the payload. JSON and YAML serialize `data` — the full object,
// including fields the table omits — so scripting is never limited to what the
// human view happens to show.
func Render(w io.Writer, format Format, table Table, data any) error {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		defer enc.Close()
		return enc.Encode(data)
	case FormatCSV:
		return renderCSV(w, table)
	default:
		return renderTable(w, table)
	}
}

func renderTable(w io.Writer, table Table) error {
	if len(table.Rows) == 0 {
		fmt.Fprintln(w, "No results.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if len(table.Headers) > 0 {
		fmt.Fprintln(tw, strings.Join(table.Headers, "\t"))
	}
	for _, row := range table.Rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

func renderCSV(w io.Writer, table Table) error {
	cw := csv.NewWriter(w)
	if len(table.Headers) > 0 {
		if err := cw.Write(table.Headers); err != nil {
			return err
		}
	}
	if err := cw.WriteAll(table.Rows); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// KeyValues renders a detail view: a fixed set of labelled fields for humans, the
// whole object for machines.
func KeyValues(w io.Writer, format Format, pairs [][2]string, data any) error {
	if format != FormatTable {
		rows := make([][]string, 0, len(pairs))
		for _, p := range pairs {
			rows = append(rows, []string{p[0], p[1]})
		}
		return Render(w, format, Table{Headers: []string{"field", "value"}, Rows: rows}, data)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range pairs {
		fmt.Fprintf(tw, "%s:\t%s\n", p[0], p[1])
	}
	return tw.Flush()
}

// Truncate keeps a table column from wrapping and destroying the alignment that
// makes it readable in the first place.
func Truncate(s string, max int) string {
	if max <= 1 || len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
