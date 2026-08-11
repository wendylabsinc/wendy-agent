package memguard

import "os"

// kill terminates this process immediately. Windows has no SIGKILL, so this
// exits with 137 — the code a unix shell reports for a SIGKILL — to keep the
// signature of a memory-limit trip identical across platforms.
func kill() {
	os.Exit(137)
}
