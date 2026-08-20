// Package config resolves CLI settings from flags, environment, and the on-disk
// profile, and persists the non-secret half of that state.
//
// Config and credentials live in two files on purpose: ~/.mirador/config.json is
// safe to read, diff, or check into a dotfiles repo, while ~/.mirador/credentials.json
// holds live tokens and is written 0600. Keeping them apart means "share my mirador
// config" never means "share my access token".
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultProfile = "default"

	configFileName      = "config.json"
	credentialsFileName = "credentials.json"
	dirName             = ".mirador"
)

// Profile is the non-secret half of a profile: where to talk to and what is selected.
type Profile struct {
	// Endpoint overrides. Empty means production. A profile that sets these is how
	// you point the CLI at a different deployment without passing flags every time.
	APIURL           string `json:"api_url,omitempty"`
	AuthURL          string `json:"auth_url,omitempty"`
	AppURL           string `json:"app_url,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	ProjectName      string `json:"project_name,omitempty"`
}

type File struct {
	ActiveProfile string              `json:"active_profile"`
	Profiles      map[string]*Profile `json:"profiles"`
}

// Config is the fully resolved view a command works against.
type Config struct {
	ProfileName string
	// APIURL is the data plane (traces, logs, metrics, dashboards). AuthURL is the
	// credential surface — separate hosts, so an auth outage cannot take reads down.
	APIURL  string
	AuthURL string
	AppURL  string

	OrganizationID   string
	OrganizationName string
	ProjectID        string
	ProjectName      string

	// APIKey is a mir_srv_ server key from MIRADOR_API_KEY. When set it replaces the
	// OAuth credential entirely — this is the CI and agent path, where a browser
	// login is impossible and a fixed project is the point.
	//
	// json:"-" so this struct can never be rendered by `-o json` with a live
	// credential in it. Report whether a key is in use, never its value.
	APIKey string `json:"-"`
}

// Overrides are the flag values that win over everything else.
type Overrides struct {
	Profile   string
	APIURL    string
	AuthURL   string
	AppURL    string
	ProjectID string
}

// Load resolves settings in precedence order: flag, environment variable, active
// profile, built-in default (production).
func Load(o Overrides) (*Config, error) {
	file, err := LoadFile()
	if err != nil {
		return nil, err
	}

	name := firstNonEmpty(o.Profile, os.Getenv("MIRADOR_PROFILE"), file.ActiveProfile, DefaultProfile)
	profile := file.Profiles[name]
	if profile == nil {
		profile = &Profile{}
	}

	cfg := &Config{
		ProfileName:      name,
		APIURL:           strings.TrimRight(firstNonEmpty(o.APIURL, os.Getenv("MIRADOR_API_URL"), profile.APIURL, DefaultAPIURL), "/"),
		AuthURL:          strings.TrimRight(firstNonEmpty(o.AuthURL, os.Getenv("MIRADOR_AUTH_URL"), profile.AuthURL, DefaultAuthURL), "/"),
		AppURL:           strings.TrimRight(firstNonEmpty(o.AppURL, os.Getenv("MIRADOR_APP_URL"), profile.AppURL, DefaultAppURL), "/"),
		OrganizationID:   firstNonEmpty(os.Getenv("MIRADOR_ORGANIZATION_ID"), profile.OrganizationID),
		OrganizationName: profile.OrganizationName,
		ProjectID:        firstNonEmpty(o.ProjectID, os.Getenv("MIRADOR_PROJECT_ID"), profile.ProjectID),
		ProjectName:      profile.ProjectName,
		APIKey:           strings.TrimSpace(os.Getenv("MIRADOR_API_KEY")),
	}
	// A project named by flag or env is not the one recorded in the profile, so the
	// stored display name would be a lie.
	if cfg.ProjectID != profile.ProjectID {
		cfg.ProjectName = ""
	}

	for _, endpoint := range []struct{ name, value string }{
		{"api", cfg.APIURL},
		{"auth", cfg.AuthURL},
		{"app", cfg.AppURL},
	} {
		if err := validateEndpoint(endpoint.name, endpoint.value); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// validateEndpoint refuses to send credentials over cleartext to anywhere but loopback.
//
// Every one of these URLs carries a secret at some point: the auth endpoint receives the
// authorization code, the PKCE verifier, and the refresh token; the api endpoint receives the
// access token. A mistyped, copied, or hostile `http://` endpoint would put all of them on the
// wire in the clear, and nothing else in the CLI would notice. http is allowed for loopback
// only, because a local stack has no certificate and never leaves the machine.
func validateEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s URL %q: %w", name, raw, err)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid %s URL %q: missing host", name, raw)
	}

	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf(
			"refusing to use %s URL %q: http would send your credentials in cleartext — use https (http is allowed only for localhost)",
			name, raw)
	default:
		return fmt.Errorf("invalid %s URL %q: scheme must be https", name, raw)
	}
}

func isLoopbackHost(host string) bool {
	// Covers 127.0.0.0/8 and ::1, not just the literal 127.0.0.1 a check on the string would.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if host != "localhost" {
		return false
	}
	// "localhost" is conventionally loopback, but /etc/hosts or DNS can point it
	// elsewhere — and http is only ever safe to loopback. Resolve it and require every
	// address to be loopback, so a poisoned mapping to a real host cannot smuggle the
	// credential onto the wire in cleartext. If it cannot be resolved at all, allow it:
	// we cannot prove it hostile, and failing closed would break legitimate offline
	// local development.
	addrs, err := net.LookupHost(host)
	if err != nil {
		return true
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return len(addrs) > 0
}

func LoadFile() (*File, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{ActiveProfile: DefaultProfile, Profiles: map[string]*Profile{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Profiles == nil {
		file.Profiles = map[string]*Profile{}
	}
	if file.ActiveProfile == "" {
		file.ActiveProfile = DefaultProfile
	}
	return &file, nil
}

func SaveFile(file *File) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// UpdateProfile applies mutate to the named profile and persists the result.
func UpdateProfile(name string, mutate func(*Profile)) error {
	file, err := LoadFile()
	if err != nil {
		return err
	}
	if name == "" {
		name = file.ActiveProfile
	}
	profile := file.Profiles[name]
	if profile == nil {
		profile = &Profile{}
	}
	mutate(profile)
	file.Profiles[name] = profile
	return SaveFile(file)
}

func Dir() (string, error) {
	if custom := os.Getenv("MIRADOR_CONFIG_DIR"); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, dirName), nil
}

func ConfigPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

func CredentialsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialsFileName), nil
}

// WriteFileAtomic writes via a temp file in the same directory then renames, so a
// crash mid-write cannot leave a half-written config or a truncated credential file.
//
// The temp file is fsync'd before the rename and the directory is fsync'd after it, so
// the durability the credential-refresh path assumes actually holds: once this returns,
// the new bytes have reached disk, not just the page cache. Without that, a power loss
// right after a token rotation could leave the superseded refresh token on disk while
// the server has already invalidated it — stranding the session.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Chmod before writing so the secret is never briefly readable at the default mode.
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// fsync the directory so the rename itself survives a crash, not just the bytes.
	// Best-effort: not every platform or filesystem supports opening a directory for
	// sync, and a failure here does not mean the data was lost.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
