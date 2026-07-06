package flasher

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyFlashFailure(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "log.txt")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if got := classifyFlashFailure(write("...\n${s_ERROR_ADB_TIMEOUT}\n...")); got != "the flashing gadget never came up over USB (ADB timeout)" {
		t.Errorf("adb timeout classify = %q", got)
	}
	if got := classifyFlashFailure(write("boom: No such file or directory")); got != "a required flash file was missing (see log)" {
		t.Errorf("missing file classify = %q", got)
	}
	if got := classifyFlashFailure(write("some other unexpected explosion")); got != "see the log for details" {
		t.Errorf("default classify = %q", got)
	}
}

func TestFlashFailure_ClassifiesUnreachable(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.txt")
	os.WriteFile(logPath, []byte("${s_ERROR_ADB_TIMEOUT}"), 0o644)
	boom := errors.New("exit status 5")

	// Zero bytes written → the device is untouched → ErrGadgetUnreachable.
	err := flashFailure(io.Discard, logPath, "3m56s", 0, boom)
	if !errors.Is(err, ErrGadgetUnreachable) {
		t.Fatalf("maxBytes=0 should wrap ErrGadgetUnreachable, got %v", err)
	}

	// Bytes were written → a real mid-flash failure, not "unreachable".
	err = flashFailure(io.Discard, logPath, "5m0s", 1<<30, boom)
	if errors.Is(err, ErrGadgetUnreachable) {
		t.Fatalf("maxBytes>0 must not be ErrGadgetUnreachable, got %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("underlying error should be wrapped, got %v", err)
	}
}
