package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/config"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

type project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	OrganizationID string `json:"organization_id"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type listProjectsResponse struct {
	Projects []project `json:"projects"`
}

func newProjectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Aliases: []string{"projects"},
		Short:   "List and select projects",
		Long: `Projects are the scope every trace, log, and metric read runs against.

A user credential can reach every project in its organization, so switching is a
local change — no re-authentication, no new key.`,
	}
	cmd.AddCommand(newProjectListCommand(), newProjectUseCommand(), newProjectShowCommand())
	return cmd
}

func newProjectListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List projects in the current organization",
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

			projects, err := fetchProjects(cmd.Context(), client)
			if err != nil {
				return err
			}

			rows := make([][]string, 0, len(projects))
			for _, p := range projects {
				marker := " "
				if p.ID == cfg.ProjectID {
					marker = "*"
				}
				rows = append(rows, []string{marker, p.Name, p.ID, output.Truncate(p.Description, 48)})
			}

			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"", "NAME", "ID", "DESCRIPTION"},
				Rows:    rows,
			}, listProjectsResponse{Projects: projects})
		},
	}
}

func newProjectUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use [name-or-id]",
		Short: "Select the project subsequent commands read from",
		Long: `Records the project in the active profile.

With no argument on a terminal it presents a picker; otherwise it matches the
argument against project names and ids.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			projects, err := fetchProjects(cmd.Context(), client)
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				return fmt.Errorf("no projects available in this organization")
			}

			var selected *project
			if len(args) == 1 {
				selected, err = matchProject(projects, args[0])
			} else {
				selected, err = pickProject(cmd, projects)
			}
			if err != nil {
				return err
			}

			if err := config.UpdateProfile(cfg.ProfileName, func(p *config.Profile) {
				p.ProjectID = selected.ID
				p.ProjectName = selected.Name
				p.OrganizationID = selected.OrganizationID
			}); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Now using project %s (%s).\n", selected.Name, selected.ID)
			return nil
		},
	}
}

func newProjectShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the currently selected project",
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
			if cfg.ProjectID == "" {
				return fmt.Errorf("no project selected — run `mirador project use`")
			}

			current := project{ID: cfg.ProjectID, Name: cfg.ProjectName, OrganizationID: cfg.OrganizationID}
			return output.KeyValues(cmd.OutOrStdout(), format, [][2]string{
				{"name", cfg.ProjectName},
				{"id", cfg.ProjectID},
				{"organization", displayOrg(cfg.OrganizationName, cfg.OrganizationID)},
				{"profile", cfg.ProfileName},
			}, current)
		},
	}
}

func fetchProjects(ctx context.Context, client *api.Client) ([]project, error) {
	var resp listProjectsResponse
	if err := client.AuthGet(ctx, "/v1/projects", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Projects, nil
}

// matchProject resolves an argument against ids first, then exact names, then a
// unique case-insensitive prefix. An ambiguous prefix is an error rather than a
// guess — silently picking one of several projects would send reads somewhere the
// user did not intend.
func matchProject(projects []project, query string) (*project, error) {
	query = strings.TrimSpace(query)
	for i := range projects {
		if projects[i].ID == query {
			return &projects[i], nil
		}
	}
	for i := range projects {
		if strings.EqualFold(projects[i].Name, query) {
			return &projects[i], nil
		}
	}

	var matches []*project
	lower := strings.ToLower(query)
	for i := range projects {
		if strings.HasPrefix(strings.ToLower(projects[i].Name), lower) {
			matches = append(matches, &projects[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no project matches %q — run `mirador project list`", query)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return nil, fmt.Errorf("%q matches several projects (%s) — use the full name or the id", query, strings.Join(names, ", "))
	}
}

// pickProject prompts for a selection. It refuses to run without a terminal: a
// script that reaches this path wanted an argument, and blocking on stdin would
// hang a pipeline instead of failing it.
func pickProject(cmd *cobra.Command, projects []project) (*project, error) {
	if !output.Interactive() {
		return nil, fmt.Errorf("no project given and no terminal to prompt on — pass a name or id")
	}

	out := cmd.ErrOrStderr()
	fmt.Fprintln(out, "Select a project:")
	for i, p := range projects {
		fmt.Fprintf(out, "  %2d) %s  %s\n", i+1, p.Name, p.ID)
	}
	fmt.Fprint(out, "\nNumber or name: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read selection: %w", err)
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return nil, fmt.Errorf("no selection made")
	}

	if n, convErr := strconv.Atoi(answer); convErr == nil {
		if n < 1 || n > len(projects) {
			return nil, fmt.Errorf("selection %d is out of range (1-%d)", n, len(projects))
		}
		return &projects[n-1], nil
	}
	return matchProject(projects, answer)
}
