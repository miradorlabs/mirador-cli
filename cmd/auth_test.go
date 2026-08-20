package cmd

import (
	"testing"

	"github.com/miradorlabs/mirador-cli/internal/auth"
	"github.com/miradorlabs/mirador-cli/internal/config"
)

// TestApplyLogin_ClearsProjectOnlyOnOrgChange guards the fix for the dead-branch bug:
// the stale-project clear compared the org id *after* overwriting it, so it never fired
// and a re-login into a different org kept pointing at the old org's project.
func TestApplyLogin_ClearsProjectOnlyOnOrgChange(t *testing.T) {
	t.Run("different org clears the remembered project", func(t *testing.T) {
		p := &config.Profile{
			OrganizationID: "org-old",
			ProjectID:      "proj-old",
			ProjectName:    "Old Project",
		}
		applyLogin(p, &auth.Credential{OrganizationID: "org-new"}, "New Org")

		if p.ProjectID != "" || p.ProjectName != "" {
			t.Errorf("project should be cleared on org change, got %q/%q", p.ProjectID, p.ProjectName)
		}
		if p.OrganizationID != "org-new" || p.OrganizationName != "New Org" {
			t.Errorf("organization should be updated, got %q/%q", p.OrganizationID, p.OrganizationName)
		}
	})

	t.Run("same org keeps the remembered project", func(t *testing.T) {
		p := &config.Profile{
			OrganizationID: "org-1",
			ProjectID:      "proj-1",
			ProjectName:    "Project One",
		}
		applyLogin(p, &auth.Credential{OrganizationID: "org-1"}, "Org One")

		if p.ProjectID != "proj-1" || p.ProjectName != "Project One" {
			t.Errorf("project should survive a same-org re-login, got %q/%q", p.ProjectID, p.ProjectName)
		}
	})

	t.Run("first login into an empty profile sets the org", func(t *testing.T) {
		p := &config.Profile{}
		applyLogin(p, &auth.Credential{OrganizationID: "org-1"}, "Org One")

		if p.OrganizationID != "org-1" || p.OrganizationName != "Org One" {
			t.Errorf("organization should be recorded, got %q/%q", p.OrganizationID, p.OrganizationName)
		}
	})
}
