package services

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// MigrateLegacyConfigDir moves provisioning state from oldDir into newDir at
// startup, before anything reads it — so the in-place self-updater (which runs
// no package hook) can't drop a device's enrollment. Idempotent, no-clobber
// (never overwrites live state under newDir), best-effort (logs, never blocks).
//
// provisioning.json moves LAST: it's the enrollment signal, so a partial
// failure can only leave the device un-provisioned (safe), never enrolled
// without its device-key.pem (broken).
func MigrateLegacyConfigDir(logger *zap.Logger, oldDir, newDir string) {
	if oldDir == "" || newDir == "" || oldDir == newDir {
		return
	}
	if info, err := os.Stat(oldDir); err != nil || !info.IsDir() {
		return // nothing to migrate — the common steady state
	}
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		logger.Warn("config migration: cannot read legacy dir", zap.String("dir", oldDir), zap.Error(err))
		return
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		logger.Warn("config migration: cannot create unified dir", zap.String("dir", newDir), zap.Error(err))
		return
	}

	migrated := 0
	move := func(name string) {
		src := filepath.Join(oldDir, name)
		if _, err := os.Lstat(src); err != nil {
			return // already moved or never present
		}
		dst := filepath.Join(newDir, name)

		// config.json ships as a "{}" placeholder: a customized legacy copy wins
		// over an absent/placeholder dst, never over a real one.
		if name == "config.json" {
			if !isPlaceholderConfig(dst) {
				return
			}
		} else if _, err := os.Stat(dst); err == nil {
			return // no-clobber
		}

		if err := moveFile(src, dst); err != nil {
			logger.Warn("config migration: move failed", zap.String("file", name), zap.Error(err))
			return
		}
		migrated++
	}

	for _, e := range entries {
		if e.Name() != "provisioning.json" {
			move(e.Name())
		}
	}
	move("provisioning.json") // last — see doc comment

	if migrated > 0 {
		logger.Info("Migrated legacy agent config", zap.String("from", oldDir), zap.String("to", newDir), zap.Int("files", migrated))
	}

	_ = os.Remove(oldDir) // succeeds only if empty; leftover files are preserved
}

// isPlaceholderConfig reports whether path is missing, empty, or the shipped
// "{}" placeholder (whitespace ignored).
func isPlaceholderConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true // absent → safe to overwrite
	}
	trimmed := strings.TrimSpace(string(data))
	return trimmed == "" || trimmed == "{}"
}

// moveFile renames src to dst, falling back to a durable copy when they are on
// different filesystems — the device case, where the legacy dir is on the rootfs
// and the unified dir is bind-mounted from the /data partition (rename → EXDEV).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return copyAndRemove(src, dst)
}

// copyAndRemove copies src to dst via syncWriteFile (fsync'd, mode-preserving)
// then removes src. The fsync matters: the key lands on /data flash, where a
// non-synced rename could truncate it on power loss.
func copyAndRemove(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := syncWriteFile(dst, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Remove(src)
}
