package foxglovebridge

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// binaries is populated by the arch-specific embed file's init().
var binaries map[string][]byte

const StageRoot = "/var/wendy/ros2-bridge"

func BinaryHostPath(distro string) string {
	return filepath.Join(StageRoot, distro, "wendy-ros2-bridge")
}

// Available reports whether an embedded bridge binary exists for distro.
func Available(distro string) bool {
	b, ok := binaries[distro]
	return ok && len(b) > 0
}

// Stage writes each embedded bridge binary under root/<distro>/wendy-ros2-bridge.
// It is idempotent: a file whose contents already match is left untouched.
func Stage(root string) error {
	for distro, data := range binaries {
		if len(data) == 0 {
			continue
		}
		dir := filepath.Join(root, distro)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		path := filepath.Join(dir, "wendy-ros2-bridge")
		if existing, err := os.ReadFile(path); err == nil && sha256.Sum256(existing) == sha256.Sum256(data) {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("rename %s: %w", path, err)
		}
	}
	return nil
}
