//go:build darwin || linux

package commands

import (
	"strings"
	"testing"
)

func TestScanDDProgress_Linux(t *testing.T) {
	// GNU dd `status=progress` updates with '\r' and finishes with '\n':
	//   <bytes> bytes (<a> <unit>, <b> <unit>) copied, <s> s, <r>/s
	input := "" +
		"131072000 bytes (131 MB, 125 MiB) copied, 0.5 s, 262 MB/s\r" +
		"262144000 bytes (262 MB, 250 MiB) copied, 1.0 s, 262 MB/s\r" +
		"524288000 bytes (524 MB, 500 MiB) copied, 2.0 s, 262 MB/s\n" +
		"100+0 records in\n" +
		"100+0 records out\n"

	var got []int64
	scanDDProgress(strings.NewReader(input), func(written int64) {
		got = append(got, written)
	})

	want := []int64{131072000, 262144000, 524288000}
	if len(got) != len(want) {
		t.Fatalf("got %d updates (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("update %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestScanDDProgress_macOS(t *testing.T) {
	// BSD dd `status=progress` (Monterey+) updates with '\r' and ends with
	// a newline-terminated three-line summary on completion. Critically,
	// macOS dd right-pads the byte count with leading spaces so the column
	// stays aligned as digits grow — we must skip that whitespace, not
	// treat it as the token boundary.
	input := "" +
		"   73519857664 bytes (74 GB, 68 GiB) transferred 1.004s, 73 GB/s\r" +
		"  146314100736 bytes (146 GB, 136 GiB) transferred 1.998s, 73 GB/s\r" +
		"100+0 records in\n" +
		"100+0 records out\n" +
		"209715200 bytes transferred in 2.500 secs (83886080 bytes/sec)\n"

	var got []int64
	scanDDProgress(strings.NewReader(input), func(written int64) {
		got = append(got, written)
	})

	want := []int64{73519857664, 146314100736, 209715200}
	if len(got) != len(want) {
		t.Fatalf("got %d updates (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("update %d: got %d, want %d", i, got[i], w)
		}
	}
}

func TestScanDDProgress_IgnoresNonNumericFirstToken(t *testing.T) {
	// "records in" / "records out" lines have a "+0" suffix on the first
	// token so ParseInt rejects them and we skip silently.
	input := "starting up...\n100+0 records in\n100+0 records out\n"

	var got []int64
	scanDDProgress(strings.NewReader(input), func(written int64) {
		got = append(got, written)
	})

	if len(got) != 0 {
		t.Errorf("expected no progress updates, got %v", got)
	}
}

func TestScanDDProgress_NilCallback(t *testing.T) {
	// Should drain the reader without panicking when progressFn is nil.
	scanDDProgress(strings.NewReader("anything"), nil)
}

func TestScanDDProgress_CapturesDiagnosticsNotProgress(t *testing.T) {
	// A real dd error line is interleaved with progress updates. Only the
	// non-progress diagnostics are returned; progress spam must never be
	// retained (it can grow without bound on long writes).
	input := "" +
		"131072000 bytes (131 MB, 125 MiB) copied, 0.5 s, 262 MB/s\r" +
		"262144000 bytes (262 MB, 250 MiB) copied, 1.0 s, 262 MB/s\r" +
		"dd: /dev/disk2: Operation not permitted\n" +
		"100+0 records in\n" +
		"100+0 records out\n"

	var got []int64
	diag := scanDDProgress(strings.NewReader(input), func(written int64) {
		got = append(got, written)
	})

	if len(got) != 2 {
		t.Errorf("expected 2 progress updates, got %v", got)
	}
	if !strings.Contains(diag, "Operation not permitted") {
		t.Errorf("diagnostics missing dd error message: %q", diag)
	}
	if strings.Contains(diag, "bytes (131 MB") {
		t.Errorf("diagnostics must not retain progress spam: %q", diag)
	}
}

func TestScanDDProgress_CapturesDiagnosticsWithNilCallback(t *testing.T) {
	// Direct-install mode passes a nil progressFn but must still surface dd's
	// error output so the failure message is not lost.
	diag := scanDDProgress(strings.NewReader("dd: invalid argument\n"), nil)
	if !strings.Contains(diag, "invalid argument") {
		t.Errorf("expected diagnostics captured with nil callback, got %q", diag)
	}
}

func TestScanDDProgress_StripsControlCharsAndBounds(t *testing.T) {
	// Terminal escape sequences and NUL bytes from dd's stderr must be stripped
	// so they cannot leak into our error output, and the capture must be bounded.
	var sb strings.Builder
	sb.WriteString("dd: \x1b[31mfatal\x1b[0m\x00 error\n")
	for i := 0; i < 5000; i++ { // far exceed the diagnostics cap
		sb.WriteString("padding line that should be truncated\n")
	}

	diag := scanDDProgress(strings.NewReader(sb.String()), nil)

	if strings.ContainsRune(diag, '\x1b') || strings.ContainsRune(diag, '\x00') {
		t.Errorf("diagnostics still contain control characters: %q", diag)
	}
	if !strings.Contains(diag, "fatal") {
		t.Errorf("sanitized error text did not survive: %q", diag)
	}
	if len(diag) > maxDDDiagnostics {
		t.Errorf("diagnostics length %d exceeds cap %d", len(diag), maxDDDiagnostics)
	}
}
