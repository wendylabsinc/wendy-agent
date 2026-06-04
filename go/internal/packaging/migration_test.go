package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoFile resolves a path relative to the repo root (go/internal/packaging is
// three levels down) so tests exercise the real shipped scripts.
func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	parts := append([]string{filepath.Dir(thisFile), "..", "..", ".."}, rel...)
	abs, err := filepath.Abs(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("script not found at %s: %v", abs, err)
	}
	return abs
}

// migrator runs one packaging carrier's migration against old/new directories.
type migrator struct {
	name string
	run  func(t *testing.T, oldDir, newDir string)
}

// carriers returns the shell migration implementations that ship to devices.
// Both are driven directly (the deb/rpm post-install via its source-only mode
// and migrate_config_dir args; the Arch .install via its env-overridable
// _migrate_config_dir) so the tests pin the exact bytes delivered, not a
// reimplementation. agent.sh shares the same logic but is an interactive
// $SUDO installer and is not unit-driven here.
func carriers(t *testing.T) []migrator {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; skipping packaging migration tests")
	}
	postinstall := repoFile(t, "packaging", "linux", "fpm", "wendy-agent-postinstall.sh")
	archInstall := repoFile(t, "packaging", "arch", "wendy-agent", "wendy-agent.install")
	return []migrator{
		{
			name: "fpm-postinstall",
			run: func(t *testing.T, oldDir, newDir string) {
				t.Helper()
				cmd := exec.Command("bash", "-c",
					`source "$1"; migrate_config_dir "$2" "$3"`,
					"bash", postinstall, oldDir, newDir)
				cmd.Env = append(os.Environ(), "WENDY_POSTINSTALL_SOURCE_ONLY=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fpm migrate_config_dir failed: %v\n%s", err, out)
				}
			},
		},
		{
			name: "arch-install",
			run: func(t *testing.T, oldDir, newDir string) {
				t.Helper()
				cmd := exec.Command("bash", "-c",
					`source "$1"; _migrate_config_dir`,
					"bash", archInstall)
				cmd.Env = append(os.Environ(),
					"WENDY_OLD_CONFIG_DIR="+oldDir, "WENDY_NEW_CONFIG_DIR="+newDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("arch _migrate_config_dir failed: %v\n%s", err, out)
				}
			},
		},
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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

// TestPackagingMigration runs the same behavioral matrix against every shell
// carrier so deb/rpm and Arch can never diverge in migration semantics.
func TestPackagingMigration(t *testing.T) {
	for _, m := range carriers(t) {
		t.Run(m.name, func(t *testing.T) {
			t.Run("MovesState", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")
				write(t, filepath.Join(oldDir, "provisioning.json"), `{"cert":"x"}`)
				write(t, filepath.Join(oldDir, "device-key.pem"), "KEY")
				write(t, filepath.Join(oldDir, ".provisioned"), "1")

				m.run(t, oldDir, newDir)

				wantContent(t, filepath.Join(newDir, "provisioning.json"), `{"cert":"x"}`)
				wantContent(t, filepath.Join(newDir, "device-key.pem"), "KEY")
				wantContent(t, filepath.Join(newDir, ".provisioned"), "1")
				wantAbsent(t, oldDir)
			})

			t.Run("Idempotent", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")
				write(t, filepath.Join(oldDir, "provisioning.json"), "P")

				m.run(t, oldDir, newDir)
				m.run(t, oldDir, newDir)

				wantContent(t, filepath.Join(newDir, "provisioning.json"), "P")
			})

			t.Run("NoClobber", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")
				write(t, filepath.Join(newDir, "provisioning.json"), "NEW")
				write(t, filepath.Join(oldDir, "provisioning.json"), "OLD")

				m.run(t, oldDir, newDir)

				wantContent(t, filepath.Join(newDir, "provisioning.json"), "NEW")
				wantContent(t, filepath.Join(oldDir, "provisioning.json"), "OLD")
			})

			t.Run("ConfigPlaceholderOverwritten", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")
				write(t, filepath.Join(newDir, "config.json"), "{}\n")
				write(t, filepath.Join(oldDir, "config.json"), `{"k":1}`)

				m.run(t, oldDir, newDir)

				wantContent(t, filepath.Join(newDir, "config.json"), `{"k":1}`)
			})

			t.Run("RealConfigPreserved", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")
				write(t, filepath.Join(newDir, "config.json"), `{"real":true}`)
				write(t, filepath.Join(oldDir, "config.json"), `{"old":true}`)

				m.run(t, oldDir, newDir)

				wantContent(t, filepath.Join(newDir, "config.json"), `{"real":true}`)
			})

			t.Run("NoLegacyDir", func(t *testing.T) {
				base := t.TempDir()
				oldDir := filepath.Join(base, "etc", "wendy-agent")
				newDir := filepath.Join(base, "etc", "wendyos")

				m.run(t, oldDir, newDir)

				wantAbsent(t, newDir)
			})
		})
	}
}
