package services

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func mkfile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func wantContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != want {
		t.Errorf("%s = %q; want %q", path, b, want)
	}
}

func wantAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s absent; stat err = %v", path, err)
	}
}

func TestMigrateLegacyConfigDir_MovesState(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent")
	newDir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(oldDir, "provisioning.json"), `{"cert":"x"}`, 0o600)
	mkfile(t, filepath.Join(oldDir, "device-key.pem"), "KEY", 0o600)
	mkfile(t, filepath.Join(oldDir, ".provisioned"), "1", 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir)

	wantContent(t, filepath.Join(newDir, "provisioning.json"), `{"cert":"x"}`)
	wantContent(t, filepath.Join(newDir, "device-key.pem"), "KEY")
	wantContent(t, filepath.Join(newDir, ".provisioned"), "1")
	wantAbsent(t, oldDir) // emptied and removed

	// Sensitive key keeps its restrictive mode.
	if fi, err := os.Stat(filepath.Join(newDir, "device-key.pem")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("device-key.pem mode = %o; want 600", fi.Mode().Perm())
	}
}

func TestMigrateLegacyConfigDir_Idempotent(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent")
	newDir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(oldDir, "provisioning.json"), "P", 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir)
	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir) // no-op, must not error/panic

	wantContent(t, filepath.Join(newDir, "provisioning.json"), "P")
}

func TestMigrateLegacyConfigDir_NoClobber(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent")
	newDir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(newDir, "provisioning.json"), "NEW", 0o600)
	mkfile(t, filepath.Join(oldDir, "provisioning.json"), "OLD", 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir)

	wantContent(t, filepath.Join(newDir, "provisioning.json"), "NEW") // live state kept
	wantContent(t, filepath.Join(oldDir, "provisioning.json"), "OLD") // legacy left untouched
}

func TestMigrateLegacyConfigDir_ConfigPlaceholderOverwritten(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent")
	newDir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(newDir, "config.json"), "{}\n", 0o600)
	mkfile(t, filepath.Join(oldDir, "config.json"), `{"k":1}`, 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir)

	wantContent(t, filepath.Join(newDir, "config.json"), `{"k":1}`)
}

func TestMigrateLegacyConfigDir_RealConfigPreserved(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent")
	newDir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(newDir, "config.json"), `{"real":true}`, 0o600)
	mkfile(t, filepath.Join(oldDir, "config.json"), `{"old":true}`, 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir)

	wantContent(t, filepath.Join(newDir, "config.json"), `{"real":true}`)
}

func TestMigrateLegacyConfigDir_NoLegacyDir(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "etc", "wendy-agent") // never created
	newDir := filepath.Join(base, "etc", "wendyos")

	MigrateLegacyConfigDir(zap.NewNop(), oldDir, newDir) // clean no-op

	wantAbsent(t, newDir)
}

func TestMigrateLegacyConfigDir_SameDirNoop(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "etc", "wendyos")
	mkfile(t, filepath.Join(dir, "provisioning.json"), "P", 0o600)

	MigrateLegacyConfigDir(zap.NewNop(), dir, dir) // old == new

	wantContent(t, filepath.Join(dir, "provisioning.json"), "P")
}

// copyAndRemove is the cross-filesystem fallback exercised on WendyOS (rootfs ->
// /data bind mount), where os.Rename returns EXDEV. A unit test can't span two
// filesystems, so call it directly to pin its content/mode/source-removal contract.
func TestCopyAndRemove_PreservesModeAndRemovesSrc(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src", "device-key.pem")
	dst := filepath.Join(base, "dst", "device-key.pem")
	mkfile(t, src, "PRIVATE", 0o600)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyAndRemove(src, dst); err != nil {
		t.Fatalf("copyAndRemove: %v", err)
	}

	wantContent(t, dst, "PRIVATE")
	wantAbsent(t, src)
	if fi, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("dst mode = %o; want 600", fi.Mode().Perm())
	}
}
