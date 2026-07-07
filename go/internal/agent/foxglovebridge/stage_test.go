package foxglovebridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	path := filepath.Join(root, "humble", "wendy-ros2-bridge")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "ELFish" {
		t.Fatalf("staged = %q err=%v", got, err)
	}

	// Record ModTime to prove idempotency: second Stage should not rewrite the file.
	stat1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first stage: %v", err)
	}
	modTime1 := stat1.ModTime()

	// Sleep to ensure any rewrite would be detectable.
	time.Sleep(10 * time.Millisecond)

	// Idempotent second run.
	if err := Stage(root); err != nil {
		t.Fatalf("second stage: %v", err)
	}

	// Verify the file was not rewritten (ModTime unchanged).
	stat2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after second stage: %v", err)
	}
	modTime2 := stat2.ModTime()
	if !modTime1.Equal(modTime2) {
		t.Fatalf("file was rewritten (ModTime changed %v -> %v); skip-on-hash logic may be broken", modTime1, modTime2)
	}
}
