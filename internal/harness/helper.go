package harness

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/miradorlabs/mirador-cli/internal/config"
)

// The headers helper is how the server key reaches the harness without ever sitting in
// the harness's own config file. Claude Code's otelHeadersHelper setting names a script
// it runs at startup (and roughly every 29 minutes after); whatever JSON object of
// headers the script prints is merged into the OTLP export. So the settings file — the
// one users read, edit, and keep in dotfiles — carries only a path, and the credential
// lives in a 0700 script under Mirador's own directory, next to the other secrets this
// CLI already guards.
//
// The helper applies to http/protobuf and http/json only, which is fine: the connect
// always writes http/protobuf.

// HelpersDir is where every helper script lives: ~/.mirador/helpers.
func HelpersDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "helpers"), nil
}

// HelperFilePath names the script for one harness+project pair. Per-project, so two
// projects connected from one machine hold their own keys and revoking one cannot
// break the other.
func HelperFilePath(h Harness, projectID string) (string, error) {
	dir, err := HelpersDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, h.Name()+"-otel-"+projectID), nil
}

// helperKeyRE matches the one secret a helper carries. Keys are prefix + hex, so the
// character class is exact; nothing else in the script looks like this.
var helperKeyRE = regexp.MustCompile(`mir_srv_[0-9a-f]+`)

// writeHelper writes the script, 0700 in a 0700 directory: it both holds a secret and
// must be executable by the harness running as this user, and nobody else has business
// with either.
func writeHelper(path, key string) error {
	if strings.ContainsAny(key, `'"\$`+"`\n") {
		// A key is prefix+hex so this cannot happen — but if it ever does, refusing
		// beats writing a script that injects the surprise into a shell.
		return errors.New("key contains characters that cannot be embedded in a helper script")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	script := fmt.Sprintf(`#!/bin/sh
# Written by 'mirador telemetry connect'. Claude Code runs this to fetch the
# Authorization header for its OTLP export, so the key never sits in
# settings.json. Managed by 'mirador telemetry disconnect'; do not edit.
echo '{"Authorization": "Bearer %s"}'
`, key)
	return writeFileAtomic(path, []byte(script), 0o700)
}

// keyFromHelper extracts the key from a helper script, or "" when the file is missing
// or holds none. Read tolerantly — a hand-edited helper should still yield its key for
// status display and reuse rather than erroring the whole command.
func keyFromHelper(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return helperKeyRE.FindString(string(data))
}

func deleteHelper(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// isOwnHelper reports whether a configured otelHeadersHelper value points into
// Mirador's helpers directory — the test that separates "our credential delivery"
// from "someone else's headers script", which conflict detection must flag.
func isOwnHelper(value string) bool {
	dir, err := HelpersDir()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return strings.HasPrefix(abs, dir+string(filepath.Separator))
}
