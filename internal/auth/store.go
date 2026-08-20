// Package auth implements the CLI side of the Mirador PKCE loopback login: the
// browser handoff, the code exchange, credential storage, and transparent refresh.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/miradorlabs/mirador-cli/internal/config"
)

// ErrNotLoggedIn is what every command surfaces when there is no usable credential.
// Callers turn it into "run mirador login" rather than a raw file error.
var ErrNotLoggedIn = errors.New("not logged in")

// Credential is one profile's stored tokens. expires_at is the access token's own
// expiry, tracked so the CLI can refresh proactively instead of discovering the
// expiry as a failed request.
type Credential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	SessionID    string    `json:"session_id,omitempty"`
	// AuthURL records the host that minted this credential. A token is only valid to
	// the environment that issued it, so sending one somewhere else is never useful —
	// and the 401 it earns looks like a broken login rather than a wrong endpoint.
	// Empty on credentials written before this field existed; those are not checked.
	AuthURL        string `json:"auth_url,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	UserEmail      string `json:"user_email,omitempty"`
}

// ErrWrongEnvironment reports a credential issued by a different auth host than the
// one currently configured.
type ErrWrongEnvironment struct {
	IssuedBy     string
	ConfiguredAs string
}

func (e *ErrWrongEnvironment) Error() string {
	return fmt.Sprintf(
		"this profile is logged in against %s but is now pointed at %s — run `mirador login` for this environment, or switch profiles with `mirador config use <profile>`",
		e.IssuedBy, e.ConfiguredAs)
}

// CheckEnvironment reports whether the credential belongs to authURL.
func (c *Credential) CheckEnvironment(authURL string) error {
	if c.AuthURL == "" || authURL == "" || c.AuthURL == authURL {
		return nil
	}
	return &ErrWrongEnvironment{IssuedBy: c.AuthURL, ConfiguredAs: authURL}
}

// Expired reports whether the access token is spent. The skew means a token that
// will die mid-flight is treated as already dead, so a slow request cannot land
// with an expired credential.
func (c *Credential) Expired() bool {
	const skew = 60 * time.Second
	return c.ExpiresAt.IsZero() || time.Now().Add(skew).After(c.ExpiresAt)
}

type credentialFile map[string]*Credential

// LoadCredential returns the credential for a profile, or ErrNotLoggedIn.
func LoadCredential(profile string) (*Credential, error) {
	file, err := loadCredentialFile()
	if err != nil {
		return nil, err
	}
	cred := file[profile]
	if cred == nil || cred.AccessToken == "" {
		return nil, ErrNotLoggedIn
	}
	return cred, nil
}

func SaveCredential(profile string, cred *Credential) error {
	return mutateCredentialFile(func(file credentialFile) {
		file[profile] = cred
	})
}

func DeleteCredential(profile string) error {
	return mutateCredentialFile(func(file credentialFile) {
		delete(file, profile)
	})
}

// mutateCredentialFile runs mutate against the on-disk credential map under an
// exclusive cross-process lock, then persists the result. Locking the whole
// read-modify-write is what stops two concurrent `mirador` processes from clobbering
// each other: without it, each reads the map, changes its own profile, and writes the
// whole thing back — and the second writer erases the first's unrelated entry.
func mutateCredentialFile(mutate func(credentialFile)) error {
	path, err := config.CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	unlock, err := lockCredentialFile(path)
	if err != nil {
		return err
	}
	defer unlock()

	file, err := loadCredentialFile()
	if err != nil {
		return err
	}
	mutate(file)
	return saveCredentialFile(file)
}

func loadCredentialFile() (credentialFile, error) {
	path, err := config.CredentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return credentialFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var file credentialFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if file == nil {
		file = credentialFile{}
	}
	return file, nil
}

func saveCredentialFile(file credentialFile) error {
	path, err := config.CredentialsPath()
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
	// 0600: this file holds live access and refresh tokens.
	return config.WriteFileAtomic(path, append(data, '\n'), 0o600)
}
