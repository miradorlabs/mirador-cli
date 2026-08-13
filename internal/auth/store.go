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
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	ExpiresAt      time.Time `json:"expires_at"`
	SessionID      string    `json:"session_id,omitempty"`
	OrganizationID string    `json:"organization_id,omitempty"`
	UserEmail      string    `json:"user_email,omitempty"`
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
	file, err := loadCredentialFile()
	if err != nil {
		return err
	}
	file[profile] = cred
	return saveCredentialFile(file)
}

func DeleteCredential(profile string) error {
	file, err := loadCredentialFile()
	if err != nil {
		return err
	}
	if _, ok := file[profile]; !ok {
		return nil
	}
	delete(file, profile)
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
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
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
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	return os.Rename(tmpName, path)
}
