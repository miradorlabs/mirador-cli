package cmd

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

func newDerivedMetricCommand() *cobra.Command {
	return resource{
		singular:   "derived-metric",
		plural:     "derived-metrics",
		path:       "/v1/derived-metrics",
		collection: "derived_metrics",
		short:      "Manage derived metrics",
		long: `A derived metric turns each finished trace into one gauge datapoint.

The authoring loop is: write a program, preview it with ` + "`dry-run`" + ` against recent
traces, then commit it with ` + "`apply`" + `. Query the result by its metric_name through
` + "`mirador metric query`" + `.`,
		listHeaders: []string{"SLUG", "NAME", "METRIC", "UNIT", "ENABLED", "UPDATED"},
		listRow: func(item map[string]any) []string {
			return []string{
				stringField(item, "slug"),
				output.Truncate(stringField(item, "display_name"), 30),
				stringField(item, "metric_name"),
				stringField(item, "unit"),
				boolField(item, "enabled"),
				localTime(stringField(item, "updated_at")),
			}
		},
		detail: func(doc map[string]any) [][2]string {
			return [][2]string{
				{"slug", stringField(doc, "slug")},
				{"name", stringField(doc, "display_name")},
				{"metric_name", stringField(doc, "metric_name")},
				{"unit", stringField(doc, "unit")},
				{"enabled", boolField(doc, "enabled")},
				{"description", stringField(doc, "description")},
				{"updated", localTime(stringField(doc, "updated_at"))},
			}
		},
	}.command(newDerivedMetricDryRunCommand())
}

// newDerivedMetricDryRunCommand previews a program without committing it. This is
// the step that makes derived metrics authorable from a terminal at all: a CEL
// program that compiles but selects nothing is otherwise only discoverable after it
// has been live long enough to not emit.
func newDerivedMetricDryRunCommand() *cobra.Command {
	var (
		file  string
		limit int
	)

	cmd := &cobra.Command{
		Use:   "dry-run",
		Short: "Preview a derived-metric program against recent traces",
		Long: `Runs a candidate program over recent finished traces and reports, per trace,
whether it would have emitted and what value.

Accepts the same file as ` + "`apply`" + ` — only the program is sent, so one document
serves both. A partial sample still succeeds: check traces_selected.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readDocument(cmd.InOrStdin(), file)
			if err != nil {
				return err
			}
			program, ok := doc["program"]
			if !ok {
				return fmt.Errorf("%s has no top-level `program:` to preview", describeSource(file))
			}

			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			body := map[string]any{"program": program}
			if limit > 0 {
				body["trace_limit"] = limit
			}

			var result map[string]any
			if err := client.Post(ctx, "/v1/derived-metrics/dry-run", body, &result); err != nil {
				return err
			}

			results, _ := result["trace_results"].([]any)
			rows := make([][]string, 0, len(results))
			for _, raw := range results {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				outcome, value := "not emitted", ""
				if emitted, ok := item["emitted"].(map[string]any); ok {
					outcome = "emitted"
					value = numberField(emitted, "value")
				} else if skipped, ok := item["not_emitted"].(map[string]any); ok {
					if reason := stringField(skipped, "reason"); reason != "" {
						outcome = reason
					}
				}
				rows = append(rows, []string{
					stringField(item, "trace_id"),
					outcome,
					value,
				})
			}

			if format == output.FormatTable {
				fmt.Fprintf(cmd.OutOrStdout(), "%s traces sampled: %s\n",
					numberField(result, "traces_selected"), summarizeOutcomes(result["outcome_counts"]))
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"TRACE", "OUTCOME", "VALUE"},
				Rows:    rows,
			}, result)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "YAML or JSON document containing the program, or - for stdin")
	cmd.Flags().IntVar(&limit, "trace-limit", 0, "how many recent finished traces to sample (default: server's)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// summarizeOutcomes renders the outcome histogram in a stable order, since ranging
// a map would reorder the summary line on every run.
func summarizeOutcomes(raw any) string {
	counts, ok := raw.(map[string]any)
	if !ok || len(counts) == 0 {
		return "no outcomes"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+numberField(counts, k))
	}
	return joinWith(parts, ", ")
}

func numberField(doc map[string]any, key string) string {
	switch v := doc[key].(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case string:
		return v
	default:
		return ""
	}
}
