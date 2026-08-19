package cmd

import (
	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

func newMetricAlertCommand() *cobra.Command {
	return resource{
		singular:   "metric-alert",
		plural:     "metric-alerts",
		aliases:    []string{"alert", "alerts"},
		path:       "/v1/metric-alerts",
		collection: "metric_alerts",
		short:      "Manage metric alerts",
		long: `Metric alerts evaluate a condition against a metric and notify channels when it
holds.

` + "`notifications`" + ` in an alert document names channels by their integration slug;
list the ones available with ` + "`mirador integration list`" + `.`,
		listHeaders: []string{"SLUG", "NAME", "SEVERITY", "ENABLED", "UPDATED"},
		listRow: func(item map[string]any) []string {
			return []string{
				stringField(item, "slug"),
				output.Truncate(stringField(item, "display_name"), 40),
				stringField(item, "severity"),
				boolField(item, "enabled"),
				localTime(stringField(item, "updated_at")),
			}
		},
		detail: func(doc map[string]any) [][2]string {
			return [][2]string{
				{"slug", stringField(doc, "slug")},
				{"name", stringField(doc, "display_name")},
				{"severity", stringField(doc, "severity")},
				{"enabled", boolField(doc, "enabled")},
				{"notifications", joinStrings(doc["notifications"])},
				{"updated", localTime(stringField(doc, "updated_at"))},
			}
		},
	}.command()
}
