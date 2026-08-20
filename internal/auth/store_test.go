package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/miradorlabs/mirador-cli/internal/config"
)

func sampleCredential() *Credential {
	return &Credential{
		AccessToken:  "mir_cli_x",
		RefreshToken: "mir_clr_x",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
}

func TestSaveCredential_WritesFile0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes do not apply on Windows")
	}
	dir := t.TempDir()
	t.Setenv("MIRADOR_CONFIG_DIR", dir)

	if err := SaveCredential(config.DefaultProfile, sampleCredential()); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat credentials.json: %v", err)
	}
	// This file holds live access and refresh tokens; it must never be group- or
	// world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials.json mode = %o, want 600", perm)
	}
}

func TestMutateCredentialFile_PreservesOtherProfiles(t *testing.T) {
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	if err := SaveCredential("work", sampleCredential()); err != nil {
		t.Fatalf("SaveCredential(work): %v", err)
	}
	// A second profile's write is a whole-file read-modify-write; without the lock and
	// re-read it would clobber the first profile's entry.
	if err := SaveCredential("personal", sampleCredential()); err != nil {
		t.Fatalf("SaveCredential(personal): %v", err)
	}

	if _, err := LoadCredential("work"); err != nil {
		t.Errorf("work profile was lost after writing personal: %v", err)
	}
	if _, err := LoadCredential("personal"); err != nil {
		t.Errorf("personal profile not saved: %v", err)
	}
}

func TestDeleteCredential_RemovesOnlyTheNamedProfile(t *testing.T) {
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	if err := SaveCredential("work", sampleCredential()); err != nil {
		t.Fatalf("SaveCredential(work): %v", err)
	}
	if err := SaveCredential("personal", sampleCredential()); err != nil {
		t.Fatalf("SaveCredential(personal): %v", err)
	}

	if err := DeleteCredential("work"); err != nil {
		t.Fatalf("DeleteCredential(work): %v", err)
	}

	if _, err := LoadCredential("work"); err != ErrNotLoggedIn {
		t.Errorf("work profile should be gone, got err=%v", err)
	}
	if _, err := LoadCredential("personal"); err != nil {
		t.Errorf("personal profile should survive deleting work: %v", err)
	}
}

// TestDeleteCredential_MissingProfileIsNoError keeps logout idempotent: clearing a
// profile that was never logged in must not error.
func TestDeleteCredential_MissingProfileIsNoError(t *testing.T) {
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())
	if err := DeleteCredential("never-existed"); err != nil {
		t.Errorf("deleting a missing profile should be a no-op, got %v", err)
	}
}
