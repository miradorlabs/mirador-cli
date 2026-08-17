package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/config"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

// configView is the machine-readable shape of `config show`. It deliberately has no
// field for the API key — only a description of which credential type is in play.
type configView struct {
	Profile          string `json:"profile"`
	Environment      string `json:"environment"`
	ConfigFile       string `json:"config_file"`
	APIURL           string `json:"api_url"`
	AuthURL          string `json:"auth_url"`
	AppURL           string `json:"app_url"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	ProjectName      string `json:"project_name,omitempty"`
	Auth             string `json:"auth"`
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and switch configuration profiles",
		Long: `Profiles keep separate endpoints and selections side by side — a local
stack and production, or two organizations you move between.

Credentials are stored per profile too, so switching profiles switches identity.`,
	}
	cmd.AddCommand(newConfigShowCommand(), newConfigProfilesCommand(), newConfigUseCommand(), newConfigSetCommand(), newConfigEnvsCommand())
	return cmd
}

func newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the resolved configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			format, err := resolveFormat()
			if err != nil {
				return err
			}
			path, err := config.ConfigPath()
			if err != nil {
				return err
			}

			authMode := "cli token"
			if cfg.APIKey != "" {
				authMode = "server key (MIRADOR_API_KEY)"
			}

			// A purpose-built struct rather than cfg: the machine-readable view must
			// describe how the CLI is authenticating without ever emitting the
			// credential itself.
			view := configView{
				Profile:          cfg.ProfileName,
				Environment:      cfg.Environment,
				ConfigFile:       path,
				APIURL:           cfg.APIURL,
				AuthURL:          cfg.AuthURL,
				AppURL:           cfg.AppURL,
				OrganizationID:   cfg.OrganizationID,
				OrganizationName: cfg.OrganizationName,
				ProjectID:        cfg.ProjectID,
				ProjectName:      cfg.ProjectName,
				Auth:             authMode,
			}

			return output.KeyValues(cmd.OutOrStdout(), format, [][2]string{
				{"profile", cfg.ProfileName},
				{"environment", cfg.Environment},
				{"config file", path},
				{"api url", cfg.APIURL},
				{"auth url", cfg.AuthURL},
				{"app url", cfg.AppURL},
				{"organization", displayOrg(cfg.OrganizationName, cfg.OrganizationID)},
				{"project", displayOrg(cfg.ProjectName, cfg.ProjectID)},
				{"auth", authMode},
			}, view)
		},
	}
}

func newConfigProfilesCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "profiles",
		Aliases: []string{"list"},
		Short:   "List configured profiles",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := resolveFormat()
			if err != nil {
				return err
			}
			file, err := config.LoadFile()
			if err != nil {
				return err
			}

			rows := make([][]string, 0, len(file.Profiles))
			for name, p := range file.Profiles {
				marker := " "
				if name == file.ActiveProfile {
					marker = "*"
				}
				envName := p.Environment
				if envName == "" {
					envName = config.DefaultEnvironment
				}
				rows = append(rows, []string{marker, name, envName, p.ProjectName})
			}

			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"", "PROFILE", "ENVIRONMENT", "PROJECT"},
				Rows:    rows,
			}, file)
		},
	}
}

func newConfigUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile>",
		Short: "Switch the active profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.LoadFile()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := file.Profiles[name]; !ok {
				// Creating it implicitly is friendlier than erroring: `config set` and
				// `login` both work against a profile that does not exist yet.
				file.Profiles[name] = &config.Profile{}
			}
			file.ActiveProfile = name
			if err := config.SaveFile(file); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active profile is now %s.\n", name)
			return nil
		},
	}
}

func newConfigEnvsCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "envs",
		Aliases: []string{"environments"},
		Short:   "List the built-in environments",
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

			envs := config.Environments()
			rows := make([][]string, 0, len(envs))
			for _, e := range envs {
				marker := " "
				if e.Name == cfg.Environment {
					marker = "*"
				}
				rows = append(rows, []string{marker, e.Name, e.AuthURL, e.APIURL, e.AppURL})
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"", "NAME", "AUTH", "API", "APP"},
				Rows:    rows,
			}, envs)
		},
	}
}

func newConfigSetCommand() *cobra.Command {
	var env string
	var apiURL, authURL, appURL string

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set endpoints on the active profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env == "" && apiURL == "" && authURL == "" && appURL == "" {
				return fmt.Errorf("nothing to set — pass --env, or --api-url / --auth-url / --app-url")
			}
			if env != "" {
				if _, err := config.LookupEnvironment(env); err != nil {
					return err
				}
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := config.UpdateProfile(cfg.ProfileName, func(p *config.Profile) {
				if env != "" {
					p.Environment = env
					// Clear any stored per-URL overrides: they outrank the environment,
					// so leaving them would silently defeat the switch the user just asked
					// for. Anything still wanted can be re-set in the same command.
					p.APIURL, p.AuthURL, p.AppURL = "", "", ""
				}
				if apiURL != "" {
					p.APIURL = apiURL
				}
				if authURL != "" {
					p.AuthURL = authURL
				}
				if appURL != "" {
					p.AppURL = appURL
				}
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated profile %s.\n", cfg.ProfileName)
			return nil
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "environment preset: "+strings.Join(config.EnvironmentNames(), ", "))
	cmd.Flags().StringVar(&apiURL, "api-url", "", "Mirador data API base URL")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "Mirador auth API base URL")
	cmd.Flags().StringVar(&appURL, "app-url", "", "Mirador app base URL")
	return cmd
}
