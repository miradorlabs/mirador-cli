package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/auth"
	"github.com/miradorlabs/mirador-cli/internal/config"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

func newLoginCommand() *cobra.Command {
	var noBrowser bool
	var label string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorize this machine in your browser",
		Long: `Opens your browser, has you approve the CLI against one of your
organizations, and stores the resulting credential in ~/.mirador/credentials.json.

The credential is scoped to the organization, not a project, so you can switch
projects afterwards without logging in again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.APIKey != "" {
				return errors.New("MIRADOR_API_KEY is set — unset it to log in as a user, or keep using the server key")
			}

			if label == "" {
				label = auth.DefaultLabel()
			}

			client := api.NewAnonymous(cfg.AuthURL, Version)
			cred, err := auth.Login(cmd.Context(), client, auth.LoginOptions{
				AppURL:    cfg.AppURL,
				Label:     label,
				NoBrowser: noBrowser,
				Out:       cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			if err := auth.SaveCredential(cfg.ProfileName, cred); err != nil {
				return err
			}

			result := client.LastLogin()
			orgName := ""
			if result != nil {
				orgName = result.OrganizationName
			}
			if err := config.UpdateProfile(cfg.ProfileName, func(p *config.Profile) {
				applyLogin(p, cred, orgName)
			}); err != nil {
				return err
			}

			who := cred.UserEmail
			if who == "" {
				who = "your account"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nLogged in as %s in %s.\n",
				who, displayOrg(orgName, cred.OrganizationID))
			fmt.Fprintln(cmd.OutOrStdout(), "Next: `mirador project list` to see what you can read.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the authorization URL instead of opening a browser")
	cmd.Flags().StringVar(&label, "label", "", "name shown for this session (defaults to the hostname)")
	return cmd
}

func newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke this machine's credential",
		Long: `Revokes the session server-side and deletes the local credential.

Revoking server-side is what makes this meaningful: deleting the local file alone
would leave a live token that anyone holding a copy could keep using.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			client, err := newClient(cfg)
			if err != nil {
				if errors.Is(err, auth.ErrNotLoggedIn) {
					fmt.Fprintln(cmd.OutOrStdout(), "Already logged out.")
					return nil
				}
				return err
			}

			// A failed revoke must not strand the local credential — the user asked to be
			// logged out, so report it and still clear the file.
			if err := client.RevokeSession(cmd.Context()); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not revoke the session server-side (%v).\n", err)
			}
			if err := auth.DeleteCredential(cfg.ProfileName); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out.")
			return nil
		},
	}
}

type identityResponse struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	AuthType       string `json:"auth_type"`
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
}

func newWhoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity and scope of the current credential",
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
			client, err := newClient(cfg)
			if err != nil {
				return err
			}

			var identity identityResponse
			if err := client.AuthGet(cmd.Context(), "/v1/whoami", nil, &identity); err != nil {
				return err
			}

			pairs := [][2]string{
				{"profile", cfg.ProfileName},
				{"api", cfg.APIURL},
				{"auth endpoint", cfg.AuthURL},
				{"auth", identity.AuthType},
			}
			if identity.Email != "" {
				pairs = append(pairs, [2]string{"user", identity.Email})
			} else if identity.UserID != "" {
				pairs = append(pairs, [2]string{"user", identity.UserID})
			}
			pairs = append(pairs, [2]string{"organization", displayOrg(cfg.OrganizationName, identity.OrganizationID)})
			// whoami describes the credential, which is org-scoped; the selected project
			// is local state, so it comes from the profile rather than the response.
			if cfg.ProjectID != "" {
				pairs = append(pairs, [2]string{"project", displayOrg(cfg.ProjectName, cfg.ProjectID)})
			} else {
				pairs = append(pairs, [2]string{"project", "(none selected)"})
			}

			return output.KeyValues(cmd.OutOrStdout(), format, pairs, identity)
		},
	}
}

// applyLogin folds a freshly minted credential into a profile. When the credential is
// for a different organization than the profile last recorded, the remembered project
// is cleared: it belonged to the old organization and would only earn a confusing 403
// on the next read. The old organization must be compared *before* it is overwritten —
// comparing after the assignment would always match and never clear the stale project.
func applyLogin(p *config.Profile, cred *auth.Credential, orgName string) {
	if p.OrganizationID != cred.OrganizationID {
		p.ProjectID = ""
		p.ProjectName = ""
	}
	p.OrganizationID = cred.OrganizationID
	p.OrganizationName = orgName
}

// displayOrg prefers the human name and keeps the id alongside it, since the id is
// what the user needs when scripting or filing a support request.
func displayOrg(name, id string) string {
	if name == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", name, id)
}
