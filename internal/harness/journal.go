package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/miradorlabs/mirador-cli/internal/config"
)

// journal records exactly what a connect changed, so a disconnect can put it back.
//
// Without it, ownership has to be inferred — "the endpoint points at Mirador, so
// Mirador must have written this" — and inference gets it wrong in both directions. A
// config someone assembled by hand against the Mirador endpoint looks like Mirador's
// and gets deleted wholesale on disconnect; a Mirador config whose endpoint was edited
// out looks like the user's and gets treated as the state worth preserving. Recording
// the values removes the guess: a key is Mirador's if Mirador wrote it and it still
// holds what Mirador wrote.
//
// It lives under ~/.mirador rather than in the harness's own config, because it is
// Mirador's bookkeeping and has no business in a file the user reads and edits. It can
// hold a previous Authorization header, so it is written 0600 like the credential file.
type journal struct {
	Harness    string `json:"harness"`
	ConfigPath string `json:"config_path"`

	// Installed is what Mirador wrote, keyed by variable. A key whose current value no
	// longer matches has been edited since, and disconnect leaves it alone.
	Installed map[string]string `json:"installed"`
	// Previous is what each of those keys held beforehand. A nil value means the key
	// was absent, which is different from being present and empty — restoring the two
	// differently is the whole point of the pointer.
	Previous map[string]*string `json:"previous"`
	// Cleared holds conflicting settings --force removed, so disconnect can put those
	// back too. They were never Mirador's, and taking them was the price of connecting.
	Cleared map[string]string `json:"cleared,omitempty"`
}

func journalPath(harness string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "telemetry", harness+".json"), nil
}

// loadJournal returns nil when there is no record, which is not an error: a config
// connected by an older build, or edited by hand, simply has none.
func loadJournal(harness string) (*journal, error) {
	path, err := journalPath(harness)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		// A corrupt journal must not wedge disconnect: treat it as absent and fall back
		// to removing the managed keys.
		return nil, nil
	}
	return &j, nil
}

func (j *journal) save() error {
	path, err := journalPath(j.Harness)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	// 0600: Previous may hold the Authorization header that was there before.
	return writeFileAtomic(path, append(data, '\n'), settingsMode)
}

func deleteJournal(harness string) error {
	path, err := journalPath(harness)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// newJournal captures the state of env before a connect overwrites it.
func newJournal(harnessName, configPath string, existing, installing map[string]string, cleared map[string]string) *journal {
	j := &journal{
		Harness:    harnessName,
		ConfigPath: configPath,
		Installed:  make(map[string]string, len(installing)),
		Previous:   make(map[string]*string, len(installing)),
	}
	for key, value := range installing {
		j.Installed[key] = value
		if prior, ok := existing[key]; ok {
			// Copied, not aliased: the loop variable would otherwise be shared.
			prior := prior
			j.Previous[key] = &prior
		} else {
			j.Previous[key] = nil
		}
	}
	if len(cleared) > 0 {
		j.Cleared = cleared
	}
	return j
}

// apply undoes the connect against env, and reports what it did.
//
// A key is only touched when it still holds the value Mirador installed. Anything else
// is somebody's later edit — possibly the whole reason they are disconnecting — and
// silently discarding it would be the same class of bug as never having journaled.
func (j *journal) apply(env map[string]string) DisconnectResult {
	var result DisconnectResult

	for key, installed := range j.Installed {
		current, present := env[key]
		if !present {
			continue
		}
		if current != installed {
			result.Skipped = append(result.Skipped, key)
			continue
		}

		if prior := j.Previous[key]; prior != nil {
			env[key] = *prior
			result.Restored++
		} else {
			delete(env, key)
			result.Removed++
		}
	}

	// Conflicts --force took away go back only if nothing has claimed the key since.
	for key, value := range j.Cleared {
		if _, taken := env[key]; !taken {
			env[key] = value
			result.Restored++
		}
	}
	return result
}
