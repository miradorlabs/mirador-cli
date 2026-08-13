package config

import (
	"os"
	"path/filepath"
	"testing"
)

func seedConfig(t *testing.T, file *File) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MIRADOR_CONFIG_DIR", dir)
	if file != nil {
		if err := SaveFile(file); err != nil {
			t.Fatalf("SaveFile: %v", err)
		}
	}
	return dir
}

func TestLoad_PrecedenceIsFlagThenEnvThenProfile(t *testing.T) {
	seedConfig(t, &File{
		ActiveProfile: DefaultProfile,
		Profiles: map[string]*Profile{
			DefaultProfile: {APIURL: "https://profile.example", ProjectID: "project-from-profile"},
		},
	})

	t.Run("profile supplies the value when nothing overrides it", func(t *testing.T) {
		cfg, err := Load(Overrides{})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIURL != "https://profile.example" {
			t.Errorf("APIURL = %q", cfg.APIURL)
		}
		if cfg.ProjectID != "project-from-profile" {
			t.Errorf("ProjectID = %q", cfg.ProjectID)
		}
	})

	t.Run("environment beats the profile", func(t *testing.T) {
		t.Setenv("MIRADOR_API_URL", "https://env.example")
		cfg, err := Load(Overrides{})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIURL != "https://env.example" {
			t.Errorf("APIURL = %q, want the environment value", cfg.APIURL)
		}
	})

	t.Run("flag beats the environment", func(t *testing.T) {
		t.Setenv("MIRADOR_API_URL", "https://env.example")
		cfg, err := Load(Overrides{APIURL: "https://flag.example"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIURL != "https://flag.example" {
			t.Errorf("APIURL = %q, want the flag value", cfg.APIURL)
		}
	})
}

func TestLoad_FallsBackToDefaultsWithNoConfigFile(t *testing.T) {
	seedConfig(t, nil)

	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatalf("Load must succeed before the first login: %v", err)
	}
	if cfg.APIURL != DefaultAPIURL {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, DefaultAPIURL)
	}
	if cfg.ProfileName != DefaultProfile {
		t.Errorf("ProfileName = %q, want %q", cfg.ProfileName, DefaultProfile)
	}
}

func TestLoad_TrimsTrailingSlashFromURLs(t *testing.T) {
	seedConfig(t, nil)

	// Paths are concatenated onto these, so a trailing slash would produce //v1/...
	cfg, err := Load(Overrides{APIURL: "https://api.example/", AppURL: "https://app.example/"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != "https://api.example" {
		t.Errorf("APIURL = %q", cfg.APIURL)
	}
	if cfg.AppURL != "https://app.example" {
		t.Errorf("AppURL = %q", cfg.AppURL)
	}
}

func TestLoad_DropsStoredProjectNameWhenProjectIsOverridden(t *testing.T) {
	seedConfig(t, &File{
		ActiveProfile: DefaultProfile,
		Profiles: map[string]*Profile{
			DefaultProfile: {ProjectID: "project-a", ProjectName: "Project A"},
		},
	})

	cfg, err := Load(Overrides{ProjectID: "project-b"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Keeping "Project A" here would label a read of project-b with the wrong name.
	if cfg.ProjectName != "" {
		t.Errorf("ProjectName = %q, want it cleared when --project names a different project", cfg.ProjectName)
	}
}

func TestUpdateProfile_CreatesTheProfileAndPersists(t *testing.T) {
	seedConfig(t, nil)

	if err := UpdateProfile("staging", func(p *Profile) {
		p.ProjectID = "project-x"
		p.ProjectName = "Project X"
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	file, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got := file.Profiles["staging"]
	if got == nil || got.ProjectID != "project-x" {
		t.Fatalf("profile not persisted: %+v", got)
	}
}

func TestSaveFile_WritesConfigWorldReadableButNotTheCredentialFile(t *testing.T) {
	dir := seedConfig(t, &File{ActiveProfile: DefaultProfile, Profiles: map[string]*Profile{}})

	info, err := os.Stat(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	// The config holds no secrets; the credential file (0600) is the one that does.
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("config mode = %o, want 644", perm)
	}
}
