// Package cmd wires the mirador command tree.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/auth"
	"github.com/miradorlabs/mirador-cli/internal/config"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

type globalFlags struct {
	profile   string
	env       string
	apiURL    string
	authURL   string
	appURL    string
	projectID string
	output    string
}

var flags globalFlags

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "mirador",
		Short: "Query Mirador traces, logs, metrics, and dashboards",
		Long: `mirador is the command-line client for the Mirador API.

Run ` + "`mirador login`" + ` once to authorize this machine in your browser. The CLI
stores a user credential scoped to your organization, so you can switch between
projects with ` + "`mirador project use`" + ` without ever handling an API key.

For CI and agents, set MIRADOR_API_KEY to a mir_srv_ server key instead; that
pins one project and skips the browser entirely.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.profile, "profile", "", "configuration profile to use")
	pf.StringVar(&flags.env, "env", "", "environment to talk to: "+strings.Join(config.EnvironmentNames(), ", ")+" (default prod)")
	pf.StringVar(&flags.apiURL, "api-url", "", "Mirador data API base URL")
	pf.StringVar(&flags.authURL, "auth-url", "", "Mirador auth API base URL")
	pf.StringVar(&flags.appURL, "app-url", "", "Mirador app base URL (used by login)")
	pf.StringVarP(&flags.projectID, "project", "p", "", "project to scope this command to")
	pf.StringVarP(&flags.output, "output", "o", "", "output format: table, json, yaml, csv")

	root.AddCommand(
		newLoginCommand(),
		newLogoutCommand(),
		newWhoamiCommand(),
		newProjectCommand(),
		newOrgCommand(),
		newConfigCommand(),
		newTraceCommand(),
		newLogCommand(),
		newMetricCommand(),
		newDashboardCommand(),
	)
	return root
}

func Execute() int {
	if err := NewRootCommand().Execute(); err != nil {
		// A missing credential is the one error with an obvious next step, so say it
		// rather than surfacing a file-not-found.
		if errors.Is(err, auth.ErrNotLoggedIn) {
			fmt.Fprintln(os.Stderr, "Error: not logged in. Run `mirador login` (or set MIRADOR_API_KEY for a server key).")
			return 1
		}
		var wrongEnv *auth.ErrWrongEnvironment
		if errors.As(err, &wrongEnv) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", wrongEnv)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func loadConfig() (*config.Config, error) {
	return config.Load(config.Overrides{
		Profile:     flags.profile,
		Environment: flags.env,
		APIURL:      flags.apiURL,
		AuthURL:     flags.authURL,
		AppURL:      flags.appURL,
		ProjectID:   flags.projectID,
	})
}

func resolveFormat() (output.Format, error) {
	return output.Resolve(flags.output)
}

// newClient builds an authenticated client. Commands that read project-scoped data
// call requireProject first so the missing-project case is a clear local message
// rather than a 400 from the gateway.
func newClient(cfg *config.Config) (*api.Client, error) {
	return api.New(cfg, api.Options{Version: Version, ProjectID: cfg.ProjectID})
}

func requireProject(cfg *config.Config) error {
	if cfg.APIKey != "" || cfg.ProjectID != "" {
		return nil
	}
	return errors.New("no project selected — run `mirador project use <name-or-id>` or pass --project")
}

// setupProjectCommand is the preamble every project-scoped read shares: resolve
// config, insist on a project locally rather than letting the gateway 400, build a
// client, and settle the output format.
func setupProjectCommand() (context.Context, *api.Client, output.Format, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, "", err
	}
	if err := requireProject(cfg); err != nil {
		return nil, nil, "", err
	}
	format, err := resolveFormat()
	if err != nil {
		return nil, nil, "", err
	}
	client, err := newClient(cfg)
	if err != nil {
		return nil, nil, "", err
	}
	return context.Background(), client, format, nil
}
