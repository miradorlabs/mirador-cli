package cmd

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type integrationListResponse struct {
	Integrations   []integration `json:"integrations"`
	OrganizationID string        `json:"organization_id"`
}

type integration struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
}

func newIntegrationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "integration",
		Aliases: []string{"integrations", "channel", "channels"},
		Short:   "List notification channels",
		Long: `Notification channels are the delivery targets a metric alert names in its
` + "`notifications`" + ` list. They are organization-scoped, so one slug is shared by every
project in the org.

Read-only here, and secrets are never returned: webhook URLs, headers and routing
keys stay server-side. Create channels in the web app.`,
	}
	cmd.AddCommand(newIntegrationListCommand(), newIntegrationGetCommand())
	return cmd
}

func newIntegrationListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the organization's notification channels",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			var resp integrationListResponse
			if err := client.Get(ctx, "/v1/integrations", nil, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Integrations))
			for _, i := range resp.Integrations {
				rows = append(rows, []string{
					i.Slug,
					output.Truncate(i.DisplayName, 40),
					i.Type,
					formatBool(i.Enabled),
				})
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"SLUG", "NAME", "TYPE", "ENABLED"},
				Rows:    rows,
			}, resp)
		},
	}
}

func newIntegrationGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <slug>",
		Short: "Show one notification channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			var doc integration
			if err := client.Get(ctx, "/v1/integrations/"+url.PathEscape(args[0]), nil, &doc); err != nil {
				return err
			}
			return output.KeyValues(cmd.OutOrStdout(), format, [][2]string{
				{"slug", doc.Slug},
				{"name", doc.DisplayName},
				{"type", doc.Type},
				{"enabled", formatBool(doc.Enabled)},
			}, doc)
		},
	}
}
