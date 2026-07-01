package oshealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// writeJSONAtomic marshals v and writes it to dir/name via a temp file,
// fsync, and rename, so a crash mid-write never leaves a truncated file
// behind. The fsyncs matter: the gate reboots the device right after writing,
// and the record must survive that.
func writeJSONAtomic(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir %s: %w", dir, err)
	}
	// MkdirAll only applies the mode to directories it creates; keep a
	// pre-existing state dir private too.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restricting state dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", name, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("syncing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming %s into place: %w", name, err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the rename itself; best-effort because some filesystems
// reject fsync on directories.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// readJSON unmarshals dir/name into v. The second return value reports
// whether the file exists; a malformed file returns (true, error).
func readJSON(dir, name string, v any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("decoding %s: %w", name, err)
	}
	return true, nil
}

// removeIfExists deletes dir/name, treating a missing file as success.
func removeIfExists(dir, name string) error {
	err := os.Remove(filepath.Join(dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
