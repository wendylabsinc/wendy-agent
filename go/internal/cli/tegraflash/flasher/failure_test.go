package flasher

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	const crashReason = "the wendy flash tooling crashed mid-write — power-cycle the Thor back into recovery mode and re-run the flash"
	const nvddReason = "a device-side write command failed mid-flash (see the log's \"Command failed\" line) — power-cycle the Thor back into recovery mode and re-run the flash"

	// The two real-world mid-write failure signatures: a shim's Go runtime
	// SIGSEGV banner, and bootburn's writer reporting its nvdd command failed
	// (ANSI-wrapped) — the latter gets its own cause-neutral message since
	// nvdd also fails for device-side reasons (write error, bad image).
	sigsegv := "SIGSEGV: segmentation violation\nPC=0x1014e7a28 m=0 sigcode=2 addr=0x0\nsignal arrived during cgo execution\n"
	if got := classifyFlashFailure(write(sigsegv)); got != crashReason {
		t.Errorf("SIGSEGV classify = %q", got)
	}
	nvdd := "\x1b[01;31mCommand failed: /tmp/nvdd --inputbin=/tmp/nvme0n1_3_0 --partsize 5619712 --device /dev/nvme0n1 --startoffset=2473766912 --l4t\x1b[0m\n"
	if got := classifyFlashFailure(write(nvdd)); got != nvddReason {
		t.Errorf("nvdd failure classify = %q", got)
	}

	// Crash evidence can sit far above the last 60 lines (the Go goroutine
	// dump plus bootburn's per-chunk logging push it up); crash markers are
	// searched in the whole log.
	filler := strings.Repeat("goroutine frame / FileWritten - 10485760 chunk line\n", 100)
	if got := classifyFlashFailure(write(sigsegv + filler)); got != crashReason {
		t.Errorf("deep SIGSEGV classify = %q", got)
	}

	// Precedence: access-denied stays first (it has the more actionable fix)...
	both := "USB access denied opening the flashing gadget: blah\nSIGSEGV: segmentation violation\n"
	if got := classifyFlashFailure(write(both)); !strings.HasPrefix(got, "USB access denied") {
		t.Errorf("access-denied should beat crash, got %q", got)
	}
	// ...a crash beats the failed nvdd command it caused...
	crashAndNvdd := sigsegv + nvdd
	if got := classifyFlashFailure(write(crashAndNvdd)); got != crashReason {
		t.Errorf("crash should beat nvdd failure, got %q", got)
	}
	// ...and both beat the generic ADB timeout (the more precise diagnosis).
	crashAndTimeout := "SIGSEGV: segmentation violation\n${s_ERROR_ADB_TIMEOUT}\n"
	if got := classifyFlashFailure(write(crashAndTimeout)); got != crashReason {
		t.Errorf("crash should beat adb timeout, got %q", got)
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
