// Package memguard enforces a hard ceiling on the Wendy CLI's own memory use.
//
// A leak in a long-lived command — a polling TUI, a log stream, a build that
// retains every layer it touches — is otherwise invisible until the machine
// starts swapping, and by then the evidence of what was retained is gone.
// memguard samples the live heap and, once it stays above the ceiling across a
// forced collection, writes a heap profile and kills the process outright.
//
// The profile is the point. A trip names the retaining call sites, which turns
// "wendy ate my RAM again" into an actionable leak report:
//
//	go tool pprof -top /tmp/wendy-memguard-<pid>.pprof
//
// The guard reads the live heap (HeapAlloc), not process RSS. A leak grows the
// live heap; RSS also counts pages the collector has freed but not yet returned
// to the OS, so tripping on RSS would kill churn-heavy commands that are not
// leaking anything.
package memguard

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

// DefaultLimit is the ceiling the CLI is expected to stay under. Nothing the
// CLI does legitimately needs half a gigabyte of live heap: large payloads
// (OS images, container layers) are streamed or staged through temp files
// rather than buffered whole — see decompressLayerToTemp in the commands
// package.
const DefaultLimit uint64 = 512 << 20

// limitEnvVar overrides DefaultLimit, in MiB. Set it to 0 to disable the guard
// entirely, or to a larger value for a workload that genuinely needs the room.
const limitEnvVar = "WENDY_MEM_LIMIT_MB"

// sampleInterval is how often the live heap is read. A trip can overshoot the
// ceiling by roughly one interval's worth of allocation, so this is kept short:
// runtime.ReadMemStats stops the world only for tens of microseconds, which is
// invisible twice a second even inside a live TUI redraw.
const sampleInterval = 500 * time.Millisecond

// config isolates every side effect of a trip so the decision logic can be
// tested without killing the test binary.
type config struct {
	limit    uint64
	interval time.Duration
	liveHeap func() uint64
	forceGC  func()
	dump     func() (string, error)
	stderr   io.Writer
	kill     func()
}

// Start launches the watchdog and reports whether one is now running. It
// returns immediately; the sampling goroutine lives for the rest of the
// process. Call it as early as possible in main so that command startup is
// covered too.
func Start() bool {
	limit := limitFromEnv()
	if limit == 0 {
		return false
	}

	// Give the collector the same ceiling as a soft target so it works harder
	// to stay under it instead of letting the guard do all the enforcing. A
	// process that can genuinely fit inside the limit then never trips, and one
	// that cannot is killed by the check below rather than thrashing forever.
	// An explicit GOMEMLIMIT from the operator wins.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(int64(limit))
	}

	cfg := &config{
		limit:    limit,
		interval: sampleInterval,
		liveHeap: liveHeap,
		forceGC:  runtime.GC,
		dump:     writeHeapProfile,
		stderr:   os.Stderr,
		kill:     kill,
	}
	go func() {
		ticker := time.NewTicker(cfg.interval)
		defer ticker.Stop()
		for range ticker.C {
			check(cfg)
		}
	}()
	return true
}

// check reads one sample and, if the ceiling is genuinely exceeded, dumps a
// heap profile and kills the process. It reports whether it tripped; in
// production cfg.kill does not return.
func check(cfg *config) bool {
	if cfg.liveHeap() <= cfg.limit {
		return false
	}

	// One sample over the line proves nothing: the collector is free to let
	// dead objects pile up between cycles, so a command that churns hundreds of
	// megabytes without retaining any of it can read high at any instant. Force
	// a collection and re-read, and only condemn the process if the memory is
	// genuinely still reachable.
	cfg.forceGC()
	live := cfg.liveHeap()
	if live <= cfg.limit {
		return false
	}

	fmt.Fprintf(cfg.stderr, "\nwendy: memory limit exceeded: %s of live heap, ceiling is %s.\n",
		formatMiB(live), formatMiB(cfg.limit))
	if path, err := cfg.dump(); err != nil {
		fmt.Fprintf(cfg.stderr, "wendy: could not write a heap profile: %v\n", err)
	} else {
		fmt.Fprintf(cfg.stderr, "wendy: heap profile written to %s\n", path)
		fmt.Fprintf(cfg.stderr, "wendy: please attach it to a bug report — inspect it with:\n")
		fmt.Fprintf(cfg.stderr, "wendy:   go tool pprof -top %s\n", path)
	}
	fmt.Fprintf(cfg.stderr, "wendy: killing this process. Raise or disable the ceiling with %s=<MiB>.\n", limitEnvVar)

	cfg.kill()
	return true
}

// liveHeap returns the bytes of reachable heap objects.
func liveHeap() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// writeHeapProfile dumps a pprof heap profile and returns its path. The name is
// keyed by pid so concurrent CLI invocations cannot clobber each other's
// evidence, and is stable across trips within one process.
func writeHeapProfile() (string, error) {
	path := fmt.Sprintf("%s/wendy-memguard-%d.pprof", strings.TrimRight(os.TempDir(), "/\\"), os.Getpid())
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	// check has already forced a collection, so the profile reflects what is
	// actually still reachable rather than the last cycle's garbage.
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// limitFromEnv resolves the ceiling in bytes from the environment, falling back
// to DefaultLimit for anything it cannot parse. A malformed override must not
// silently disable the guard, so it warns and uses the default.
func limitFromEnv() uint64 {
	v := strings.TrimSpace(os.Getenv(limitEnvVar))
	if v == "" {
		return DefaultLimit
	}
	mb, err := strconv.ParseInt(v, 10, 64)
	if err != nil || mb < 0 {
		log.Printf("WARNING: invalid %s=%q, expected a whole number of MiB, using default %s",
			limitEnvVar, v, formatMiB(DefaultLimit))
		return DefaultLimit
	}
	return uint64(mb) << 20
}

// formatMiB renders bytes as whole MiB, truncating so a reported figure never
// reads as if it had crossed a ceiling it has not.
func formatMiB(b uint64) string {
	return fmt.Sprintf("%d MiB", b>>20)
}
