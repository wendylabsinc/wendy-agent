package flasher

import (
	"os"
	"strings"
	"testing"
)

func countEnv(env []string, kv string) int {
	n := 0
	for _, e := range env {
		if e == kv {
			n++
		}
	}
	return n
}

func TestEnvWithADB_LockAndProgress(t *testing.T) {
	env := envWithADB("", "", "", "/p/progress", "/p/lock")
	if got := countEnv(env, EnvADBProgress+"=/p/progress"); got != 1 {
		t.Errorf("progress var count = %d, want 1", got)
	}
	if got := countEnv(env, EnvADBLock+"=/p/lock"); got != 1 {
		t.Errorf("lock var count = %d, want 1", got)
	}

	env = envWithADB("", "", "", "", "")
	for _, e := range env {
		if strings.HasPrefix(e, EnvADBProgress+"=") || strings.HasPrefix(e, EnvADBLock+"=") {
			t.Errorf("unexpected env entry with empty paths: %q", e)
		}
	}
}

func TestEnvWithADB_PathPrepend(t *testing.T) {
	dir := t.TempDir() // already absolute
	env := envWithADB(dir, "", "", "", "")
	for _, e := range env {
		if val, ok := strings.CutPrefix(e, "PATH="); ok {
			if val != dir && !strings.HasPrefix(val, dir+string(os.PathListSeparator)) {
				t.Errorf("PATH does not start with adbDir: %q", val)
			}
			return
		}
	}
	t.Fatal("no PATH entry in environment")
}
