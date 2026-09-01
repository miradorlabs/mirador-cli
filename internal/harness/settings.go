package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
)

// settingsFile is a harness config file whose telemetry lives in a JSON `env` object —
// the shape Claude Code uses, and the one Codex is expected to follow.
//
// Every value is held as json.RawMessage so a merge is non-destructive: settings this
// CLI has never heard of survive byte-for-byte, including their nested key order. Only
// top-level ordering is lost, since Go marshals map keys alphabetically. That churns a
// hand-written file once and is stable forever after.
type settingsFile struct {
	path string
	// writePath is path with symlinks resolved. Writing goes here rather than to path,
	// because the atomic rename replaces whatever name it is given — and for anyone
	// keeping ~/.claude/settings.json as a link into a dotfiles repo, that would swap
	// the link for a regular file and quietly detach the file from the repo.
	writePath string
	// root is the whole document; env is the nested object telemetry keys live in.
	root map[string]json.RawMessage
	env  map[string]string
	// existed distinguishes "no telemetry configured" from "no file at all", which
	// status reports differently.
	existed bool
	// mode is the file's mode as found, so a file that was already tighter than 0600
	// is not loosened by writing it back.
	mode fs.FileMode
}

const (
	envKey = "env"

	// settingsMode is what a settings file is written as once it holds a server key.
	// The default 0644 would leave a live credential readable by every account on the
	// machine, and the file is only ever read by the harness running as this user.
	settingsMode fs.FileMode = 0o600
)

// loadSettings reads a settings file, tolerating its absence.
func loadSettings(path string) (*settingsFile, error) {
	s := &settingsFile{
		path:      path,
		writePath: path,
		root:      map[string]json.RawMessage{},
		env:       map[string]string{},
		mode:      settingsMode,
	}

	// A missing file, or a dangling link, resolves to nothing — keep the original name
	// so a first connect creates it where it was asked to.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		s.writePath = resolved
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	s.existed = true

	if info, statErr := os.Stat(path); statErr == nil {
		s.mode = info.Mode().Perm()
	}

	// An empty file is a valid starting point; json.Unmarshal would reject it.
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}

	if err := json.Unmarshal(data, &s.root); err != nil {
		// Refusing here is the whole point: a merge into a file we cannot parse would
		// mean overwriting settings we cannot see.
		return nil, fmt.Errorf("parse %s: %w (fix or move the file, then retry)", path, err)
	}

	if raw, ok := s.root[envKey]; ok && len(raw) > 0 {
		// Claude Code requires env values to be strings. A file with a non-string value
		// is already broken for the harness, so say so rather than silently discarding it.
		if err := json.Unmarshal(raw, &s.env); err != nil {
			return nil, fmt.Errorf("parse %q in %s: %w (values must be strings)", envKey, path, err)
		}
	}
	if s.env == nil {
		s.env = map[string]string{}
	}
	return s, nil
}

// merge applies env, overwriting only the keys given.
func (s *settingsFile) merge(env map[string]string) {
	maps.Copy(s.env, env)
}

// remove deletes the named keys and reports how many were actually present, so a
// disconnect can tell "removed 12 settings" from "there was nothing to remove".
func (s *settingsFile) remove(keys []string) int {
	removed := 0
	for _, k := range keys {
		if _, ok := s.env[k]; ok {
			delete(s.env, k)
			removed++
		}
	}
	return removed
}

// save writes the document back atomically.
//
// tighten is set when the file now carries a credential; it clamps the mode to 0600
// rather than preserving a permissive one. A file already at 0400 keeps that.
func (s *settingsFile) save(tighten bool) error {
	// An env object emptied by disconnect is dropped entirely rather than left as `{}`,
	// so a full disconnect restores the file to what it looked like before.
	if len(s.env) == 0 {
		delete(s.root, envKey)
	} else {
		encoded, err := json.Marshal(s.env)
		if err != nil {
			return fmt.Errorf("encode %q: %w", envKey, err)
		}
		s.root[envKey] = encoded
	}

	// A document with nothing left in it is removed rather than written as `{}`.
	if len(s.root) == 0 {
		if !s.existed {
			return nil
		}
		if err := os.Remove(s.writePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", s.writePath, err)
		}
		return nil
	}

	data, err := json.MarshalIndent(s.root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", s.writePath, err)
	}
	data = append(data, '\n')

	mode := s.mode
	if tighten && mode&0o077 != 0 {
		mode = settingsMode
	}

	if err := os.MkdirAll(filepath.Dir(s.writePath), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.writePath), err)
	}
	return writeFileAtomic(s.writePath, data, mode)
}

// backup copies the current file alongside itself before the first modification. This
// is a user's own configuration, possibly hand-written and possibly in a dotfiles repo;
// a mangled merge should never be the only copy left.
//
// An existing backup is never overwritten. It is the pre-Mirador configuration, and it
// is the only record of it: connect overwrites the telemetry variables and disconnect
// deletes them, neither remembering what was there before. Overwriting on a re-connect
// would replace that record with a copy of Mirador's own settings, so the second connect
// would be what destroys the original — not the first.
//
// Best-effort by design: a failure to write the backup must not block the connect the
// user asked for, so the caller reports it as a warning.
func (s *settingsFile) backup() (string, error) {
	if !s.existed {
		return "", nil
	}
	// Alongside the real file, not the link that points at it.
	path := s.writePath + ".mirador.bak"
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	data, err := os.ReadFile(s.writePath)
	if err != nil {
		return "", err
	}
	// Written 0600 regardless of the source's mode: a backup of a connected config holds
	// a server key, and inheriting a permissive mode would copy it somewhere readable.
	if err := writeFileAtomic(path, data, settingsMode); err != nil {
		return "", err
	}
	return path, nil
}

// writeFileAtomic writes through a temp file in the same directory then renames, so an
// interrupted write cannot leave a half-written settings file — which for Claude Code
// would mean a config it refuses to start against.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mirador-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Chmod before writing, so a credential is never briefly readable at the default mode.
	if err := tmp.Chmod(mode); err != nil {
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
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
