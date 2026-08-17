package config

import (
	"os"
	"path/filepath"
	"strings"
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
	prod, err := LookupEnvironment(DefaultEnvironment)
	if err != nil {
		t.Fatalf("LookupEnvironment: %v", err)
	}
	if cfg.APIURL != prod.APIURL {
		t.Errorf("APIURL = %q, want the prod default %q", cfg.APIURL, prod.APIURL)
	}
	// Production must be what you get without asking — most people running this are not
	// developers of it.
	if cfg.Environment != "prod" {
		t.Errorf("Environment = %q, want prod", cfg.Environment)
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

// TestLoad_RefusesCleartextRemoteEndpoints guards the CLI's most damaging misconfiguration:
// every one of these URLs carries a secret (the auth host receives the PKCE verifier and the
// refresh token; the api host receives the access token), so an http:// endpoint would put
// them on the wire in the clear with nothing else in the CLI noticing.
func TestLoad_RefusesCleartextRemoteEndpoints(t *testing.T) {
	seedConfig(t, nil)

	for _, tc := range []struct {
		name     string
		override Overrides
	}{
		{"api", Overrides{APIURL: "http://attacker.example.com"}},
		{"auth", Overrides{AuthURL: "http://attacker.example.com"}},
		{"app", Overrides{AppURL: "http://attacker.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.override)
			if err == nil {
				t.Fatal("expected a cleartext remote endpoint to be refused")
			}
			if !strings.Contains(err.Error(), "cleartext") {
				t.Errorf("error should explain why, got %v", err)
			}
		})
	}
}

// TestLoad_AllowsCleartextLoopback keeps local development working — a local stack has no
// certificate, and its traffic never leaves the machine.
func TestLoad_AllowsCleartextLoopback(t *testing.T) {
	seedConfig(t, nil)

	for _, host := range []string{
		"http://localhost:8057",
		"http://127.0.0.1:8057",
		"http://127.0.0.2:8057", // still 127.0.0.0/8
		"http://[::1]:8057",
	} {
		t.Run(host, func(t *testing.T) {
			if _, err := Load(Overrides{AuthURL: host}); err != nil {
				t.Errorf("loopback %q should be allowed: %v", host, err)
			}
		})
	}
}

// TestLoad_RejectsNonHTTPSchemes stops a config from pointing the CLI at something that is
// not a web endpoint at all — file:// would be handed to the browser opener verbatim.
func TestLoad_RejectsNonHTTPSchemes(t *testing.T) {
	seedConfig(t, nil)

	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://example.com", "not-a-url"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Load(Overrides{AppURL: raw}); err == nil {
				t.Errorf("expected %q to be rejected", raw)
			}
		})
	}
}

// TestLoad_EnvironmentSelectsAllThreeEndpoints is the point of named environments: the
// three hosts are only meaningful as a set, and picking them one at a time is how you
// end up authenticating against prod while reading dev.
func TestLoad_EnvironmentSelectsAllThreeEndpoints(t *testing.T) {
	seedConfig(t, nil)

	for _, name := range []string{"dev", "local", "prod"} {
		t.Run(name, func(t *testing.T) {
			want, err := LookupEnvironment(name)
			if err != nil {
				t.Fatalf("LookupEnvironment: %v", err)
			}
			cfg, err := Load(Overrides{Environment: name})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.APIURL != want.APIURL || cfg.AuthURL != want.AuthURL || cfg.AppURL != want.AppURL {
				t.Errorf("endpoints = %s / %s / %s, want %s / %s / %s",
					cfg.APIURL, cfg.AuthURL, cfg.AppURL, want.APIURL, want.AuthURL, want.AppURL)
			}
			if cfg.Environment != want.Name {
				t.Errorf("Environment = %q, want %q", cfg.Environment, want.Name)
			}
		})
	}
}

// TestLoad_ExplicitURLBeatsEnvironment keeps the escape hatch working: an environment is
// a baseline, not a cage — a dev pointing one host at a laptop must not have to abandon
// the preset for the other two.
func TestLoad_ExplicitURLBeatsEnvironment(t *testing.T) {
	seedConfig(t, nil)

	dev, _ := LookupEnvironment("dev")
	cfg, err := Load(Overrides{Environment: "dev", APIURL: "http://localhost:8055"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != "http://localhost:8055" {
		t.Errorf("APIURL = %q, want the explicit override", cfg.APIURL)
	}
	if cfg.AuthURL != dev.AuthURL {
		t.Errorf("AuthURL = %q, want dev's %q — overriding one host must not reset the others", cfg.AuthURL, dev.AuthURL)
	}
}

func TestLoad_EnvironmentPrecedence(t *testing.T) {
	seedConfig(t, &File{
		ActiveProfile: DefaultProfile,
		Profiles:      map[string]*Profile{DefaultProfile: {Environment: "dev"}},
	})

	t.Run("profile selects it", func(t *testing.T) {
		cfg, err := Load(Overrides{})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Environment != "dev" {
			t.Errorf("Environment = %q, want dev from the profile", cfg.Environment)
		}
	})

	t.Run("MIRADOR_ENV beats the profile", func(t *testing.T) {
		t.Setenv("MIRADOR_ENV", "local")
		cfg, err := Load(Overrides{})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Environment != "local" {
			t.Errorf("Environment = %q, want local", cfg.Environment)
		}
	})

	t.Run("the flag beats MIRADOR_ENV", func(t *testing.T) {
		t.Setenv("MIRADOR_ENV", "local")
		cfg, err := Load(Overrides{Environment: "prod"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Environment != "prod" {
			t.Errorf("Environment = %q, want prod", cfg.Environment)
		}
	})
}

func TestLookupEnvironment(t *testing.T) {
	t.Run("aliases resolve", func(t *testing.T) {
		// `prd` is what the platform's own charts call production; nobody should have to
		// remember that this tool spells it differently.
		for _, alias := range []string{"prd", "PROD", " production ", "Localhost"} {
			if _, err := LookupEnvironment(alias); err != nil {
				t.Errorf("LookupEnvironment(%q): %v", alias, err)
			}
		}
	})

	t.Run("unknown names list the options", func(t *testing.T) {
		_, err := Load(Overrides{Environment: "staging"})
		if err == nil {
			t.Fatal("expected an unknown environment to be rejected")
		}
		for _, want := range []string{"dev", "local", "prod"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should list %q, got %v", want, err)
			}
		}
	})
}
