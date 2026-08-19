package cmd

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

func newDashboardCommand() *cobra.Command {
	return resource{
		singular:   "dashboard",
		plural:     "dashboards",
		path:       "/v1/dashboards",
		collection: "dashboards",
		short:      "Manage dashboards",
		long: `Dashboards are documents of widgets, addressed by slug.

` + "`apply`" + ` writes the whole document — it is a replace, not a patch, so an omitted
optional field is cleared. Applying the same file twice is a no-op, which makes a
directory of dashboards safe to re-apply from CI.`,
		listHeaders: []string{"SLUG", "TITLE", "WIDGETS", "UPDATED"},
		listRow: func(item map[string]any) []string {
			return []string{
				stringField(item, "slug"),
				output.Truncate(stringField(item, "title"), 40),
				numberField(item, "widget_count"),
				localTime(stringField(item, "updated_at")),
			}
		},
		detail: func(doc map[string]any) [][2]string {
			pairs := [][2]string{
				{"slug", stringField(doc, "slug")},
				{"title", stringField(doc, "title")},
				{"description", stringField(doc, "description")},
				{"updated", localTime(stringField(doc, "updated_at"))},
			}
			// The widget payload has no useful table shape; the count orients the
			// reader and -o json/yaml carries the documents themselves.
			if widgets, ok := doc["widgets"].([]any); ok {
				pairs = append(pairs, [2]string{"widgets", strconv.Itoa(len(widgets))})
			}
			return pairs
		},
	}.command()
}
