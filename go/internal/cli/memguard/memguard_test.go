package memguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testConfig returns a config whose every side effect is captured, with the
// live-heap reading driven by a caller-supplied sequence of samples.
func testConfig(samples ...uint64) (*config, *[]string, *bytes.Buffer, *bool) {
	var log []string
	var stderr bytes.Buffer
	killed := false
	i := 0
	cfg := &config{
		limit:    512 << 20,
		interval: time.Millisecond,
		liveHeap: func() uint64 {
			v := samples[i]
			if i < len(samples)-1 {
				i++
			}
			return v
		},
		forceGC: func() { log = append(log, "gc") },
		dump: func() (string, error) {
			log = append(log, "dump")
			return "/tmp/heap.pprof", nil
		},
		stderr: &stderr,
		kill:   func() { log = append(log, "kill"); killed = true },
	}
	return cfg, &log, &stderr, &killed
}

func TestCheckDoesNothingBelowLimit(t *testing.T) {
	cfg, log, stderr, killed := testConfig(100 << 20)
	if check(cfg) {
		t.Fatal("check tripped below the limit")
	}
	if *killed {
		t.Fatal("process killed below the limit")
	}
	if len(*log) != 0 {
		t.Fatalf("expected no side effects below the limit, got %v", *log)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no output below the limit, got %q", stderr.String())
	}
}

// A single sample over the ceiling can be uncollected garbage rather than a
// leak: the Go GC is free to let dead objects pile up between cycles. A
// churn-heavy but leak-free command must survive that, so the guard forces a
// collection and re-reads before condemning the process.
func TestCheckForcesGCBeforeKillingAndSparesCollectableGarbage(t *testing.T) {
	cfg, log, stderr, killed := testConfig(600<<20, 100<<20)
	if check(cfg) {
		t.Fatal("check tripped on garbage that a GC reclaimed")
	}
	if *killed {
		t.Fatal("process killed on garbage that a GC reclaimed")
	}
	if len(*log) != 1 || (*log)[0] != "gc" {
		t.Fatalf("expected exactly one forced GC and nothing else, got %v", *log)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no output when the GC reclaimed the overage, got %q", stderr.String())
	}
}

func TestCheckDumpsThenKillsWhenLiveHeapStaysOverLimit(t *testing.T) {
	cfg, log, stderr, killed := testConfig(600 << 20)
	if !check(cfg) {
		t.Fatal("check did not trip on a live heap over the limit")
	}
	if !*killed {
		t.Fatal("process not killed on a live heap over the limit")
	}
	// Order matters: the profile is the whole point of the trip, so it must be
	// written before the process dies.
	want := []string{"gc", "dump", "kill"}
	if len(*log) != len(want) {
		t.Fatalf("expected %v, got %v", want, *log)
	}
	for i := range want {
		if (*log)[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, *log)
		}
	}
	out := stderr.String()
	for _, want := range []string{"600 MiB", "512 MiB", "/tmp/heap.pprof", "go tool pprof"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stderr missing %q:\n%s", want, out)
		}
	}
}

// A failed dump must not stop the kill: the ceiling is the contract, the
// profile is best-effort.
func TestCheckKillsEvenWhenTheDumpFails(t *testing.T) {
	cfg, log, stderr, killed := testConfig(600 << 20)
	cfg.dump = func() (string, error) { return "", os.ErrPermission }
	if !check(cfg) {
		t.Fatal("check did not trip when the dump failed")
	}
	if !*killed {
		t.Fatal("process not killed when the dump failed")
	}
	if (*log)[len(*log)-1] != "kill" {
		t.Fatalf("kill did not happen last, got %v", *log)
	}
	if !strings.Contains(stderr.String(), "could not write a heap profile") {
		t.Fatalf("dump failure not reported:\n%s", stderr.String())
	}
}

func TestLimitFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string
		want uint64
	}{
		{name: "unset uses the default ceiling", set: "", want: DefaultLimit},
		{name: "override in megabytes", set: "1024", want: 1024 << 20},
		{name: "zero disables the guard", set: "0", want: 0},
		{name: "surrounding whitespace tolerated", set: " 64 ", want: 64 << 20},
		{name: "garbage falls back to the default", set: "lots", want: DefaultLimit},
		{name: "negative falls back to the default", set: "-1", want: DefaultLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(limitEnvVar, tc.set)
			if got := limitFromEnv(); got != tc.want {
				t.Fatalf("limitFromEnv() = %d, want %d", got, tc.want)
			}
		})
	}
}

// Start must be a no-op when the guard is disabled, so an operator who needs
// the ceiling out of the way gets no sampling goroutine either.
func TestStartDisabledByZeroLimit(t *testing.T) {
	t.Setenv(limitEnvVar, "0")
	if Start() {
		t.Fatal("Start reported a running guard with the limit disabled")
	}
}

func TestStartRunsWithADefaultLimit(t *testing.T) {
	t.Setenv(limitEnvVar, "4096")
	if !Start() {
		t.Fatal("Start did not report a running guard")
	}
}

// The real dump path must produce a parseable, non-empty profile — a guard
// that kills the process and leaves a zero-byte file behind is worthless.
func TestWriteHeapProfileProducesANonEmptyFile(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	path, err := writeHeapProfile()
	if err != nil {
		t.Fatalf("writeHeapProfile: %v", err)
	}
	if filepath.Ext(path) != ".pprof" {
		t.Fatalf("profile path %q does not end in .pprof", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat profile: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("heap profile is empty")
	}
}

func TestFormatMiB(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{in: 0, want: "0 MiB"},
		{in: 512 << 20, want: "512 MiB"},
		{in: 1 << 20, want: "1 MiB"},
		// Truncating, not rounding: a reported figure must never read as if it
		// had already crossed a ceiling it has not.
		{in: (1 << 20) - 1, want: "0 MiB"},
	} {
		if got := formatMiB(tc.in); got != tc.want {
			t.Fatalf("formatMiB(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
