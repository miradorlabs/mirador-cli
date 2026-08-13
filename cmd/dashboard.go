package cmd

import (
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type dashboardSummary struct {
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	WidgetCount int       `json:"widget_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type dashboardListResponse struct {
	Dashboards []dashboardSummary `json:"dashboards"`
	ProjectID  string             `json:"project_id"`
}

func newDashboardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"dashboards"},
		Short:   "Read dashboards",
		Long: `Read-only. Dashboard writes go through the web app or the API directly —
the CLI deliberately does not expose them, so a mistyped command cannot replace a
dashboard someone depends on.`,
	}
	cmd.AddCommand(newDashboardListCommand(), newDashboardGetCommand())
	return cmd
}

func newDashboardListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List dashboards in the current project",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			var resp dashboardListResponse
			if err := client.Get(ctx, "/v1/dashboards", nil, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Dashboards))
			for _, d := range resp.Dashboards {
				rows = append(rows, []string{
					d.Slug,
					output.Truncate(d.Title, 40),
					strconv.Itoa(d.WidgetCount),
					d.UpdatedAt.Local().Format(time.RFC3339),
				})
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"SLUG", "TITLE", "WIDGETS", "UPDATED"},
				Rows:    rows,
			}, resp)
		},
	}
}

func newDashboardGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <slug>",
		Short: "Show one dashboard, including its widgets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			// The widget payload has no useful table shape, so the detail view is a
			// header and the document itself comes back through -o json/yaml.
			var doc map[string]any
			if err := client.Get(ctx, "/v1/dashboards/"+url.PathEscape(args[0]), nil, &doc); err != nil {
				return err
			}

			pairs := [][2]string{
				{"slug", stringField(doc, "slug")},
				{"title", stringField(doc, "title")},
				{"description", stringField(doc, "description")},
				{"updated", stringField(doc, "updated_at")},
			}
			if widgets, ok := doc["widgets"].([]any); ok {
				pairs = append(pairs, [2]string{"widgets", strconv.Itoa(len(widgets))})
			}
			return output.KeyValues(cmd.OutOrStdout(), format, pairs, doc)
		},
	}
}

func stringField(doc map[string]any, key string) string {
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}
