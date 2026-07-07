package foxglovebridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageWritesEmbedded(t *testing.T) {
	// Inject a fake binary set so the test is arch-independent.
	saved := binaries
	binaries = map[string][]byte{"humble": []byte("ELFish")}
	defer func() { binaries = saved }()

	root := t.TempDir()
	if err := Stage(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "humble", "wendy-ros2-bridge"))
	if err != nil || string(got) != "ELFish" {
		t.Fatalf("staged = %q err=%v", got, err)
	}
	// Idempotent second run.
	if err := Stage(root); err != nil {
		t.Fatalf("second stage: %v", err)
	}
}
