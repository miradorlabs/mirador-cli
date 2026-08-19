package cmd

import (
	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

type listOrganizationsResponse struct {
	Organizations []organization `json:"organizations"`
}

func newOrgCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "org",
		Aliases: []string{"orgs", "organization", "organizations"},
		Short:   "Inspect your organizations",
		Long: `A credential is issued against one organization at login time.

To work in a different one, run ` + "`mirador login`" + ` again and pick it in the
browser — switching organizations means a new credential, not a local setting.`,
	}
	cmd.AddCommand(newOrgListCommand())
	return cmd
}

func newOrgListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List organizations you belong to",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			format, err := resolveFormat()
			if err != nil {
				return err
			}
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			var resp listOrganizationsResponse
			if err := client.AuthGet(cmd.Context(), "/v1/organizations", nil, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Organizations))
			for _, o := range resp.Organizations {
				marker := " "
				if o.ID == cfg.OrganizationID {
					marker = "*"
				}
				rows = append(rows, []string{marker, o.Name, o.ID, o.Role})
			}

			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"", "NAME", "ID", "ROLE"},
				Rows:    rows,
			}, resp)
		},
	}
}
