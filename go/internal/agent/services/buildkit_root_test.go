package services

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProc fakes a /proc tree with one process whose cmdline is the given
// NUL-separated argv.
func writeProc(t *testing.T, pid string, argv ...string) string {
	t.Helper()
	dir := t.TempDir()
	pdir := filepath.Join(dir, pid)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf []byte
	for _, a := range argv {
		buf = append(buf, []byte(a)...)
		buf = append(buf, 0)
	}
	if err := os.WriteFile(filepath.Join(pdir, "cmdline"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRootFromRunningDaemon_ReadsTheFlag: the running daemon's own argv is the
// only authoritative answer. A --root passed on the command line beats anything
// a config file says, and reporting the config's path instead would produce a
// confident, wrong free-space number.
func TestRootFromRunningDaemon_ReadsTheFlag(t *testing.T) {
	proc := writeProc(t, "42", "/data/buildkit/bin/buildkitd", "--addr", "unix:///run/buildkit/buildkitd.sock", "--root", "/data/buildkit/root")
	if got, _ := pathsFromRunningDaemon(proc); got != "/data/buildkit/root" {
		t.Fatalf("got %q, want the --root value", got)
	}
}

func TestRootFromRunningDaemon_ReadsEqualsForm(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--root=/mnt/big/bk")
	if got, _ := pathsFromRunningDaemon(proc); got != "/mnt/big/bk" {
		t.Fatalf("got %q, want the --root= value", got)
	}
}

func TestPathsFromRunningDaemon_ReadsConfigFlag(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--config", "/data/etc/buildkit/custom.toml")
	root, configPath := pathsFromRunningDaemon(proc)
	if root != "" {
		t.Fatalf("got root %q, want empty when --root is absent", root)
	}
	if configPath != "/data/etc/buildkit/custom.toml" {
		t.Fatalf("got config %q, want the --config value", configPath)
	}
}

func TestPathsFromRunningDaemon_ReadsConfigEqualsForm(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--config=/data/etc/buildkit/custom.toml")
	_, configPath := pathsFromRunningDaemon(proc)
	if configPath != "/data/etc/buildkit/custom.toml" {
		t.Fatalf("got config %q, want the --config= value", configPath)
	}
}

func TestPathsFromRunningDaemon_IgnoresFlagSubstring(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--label", "note=--config=/wrong")
	_, configPath := pathsFromRunningDaemon(proc)
	if configPath != "" {
		t.Fatalf("got config %q from an unrelated argument", configPath)
	}
}

func TestBuildkitRoot_UsesRunningDaemonsConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(configPath, []byte("root = \"/data/buildkit/custom-root\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--config", configPath)
	if got := buildkitRoot(proc); got != "/data/buildkit/custom-root" {
		t.Fatalf("got %q, want root from the running daemon's custom config", got)
	}
}

func TestBuildkitRoot_RootFlagBeatsCustomConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(configPath, []byte("root = \"/wrong\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--config", configPath, "--root", "/data/buildkit/flag-root")
	if got := buildkitRoot(proc); got != "/data/buildkit/flag-root" {
		t.Fatalf("got %q, want --root to override the custom config", got)
	}
}

// A daemon started without --root must return empty, NOT the default, so the
// caller can still consult the config file.
func TestRootFromRunningDaemon_NoFlagFallsThrough(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildkitd", "--addr", "unix:///run/buildkit/buildkitd.sock")
	if got, _ := pathsFromRunningDaemon(proc); got != "" {
		t.Fatalf("got %q, want empty so the config is consulted", got)
	}
}

// Only argv[0] counts. A buildctl client, or a shell that merely mentions
// buildkitd, is not the daemon and must not be mistaken for it.
func TestRootFromRunningDaemon_IgnoresNonDaemons(t *testing.T) {
	proc := writeProc(t, "42", "/usr/local/bin/buildctl", "--root", "/wrong")
	if got, _ := pathsFromRunningDaemon(proc); got != "" {
		t.Fatalf("got %q, want empty for a non-daemon process", got)
	}
	proc = writeProc(t, "43", "/bin/sh", "-c", "buildkitd --root /also-wrong")
	if got, _ := pathsFromRunningDaemon(proc); got != "" {
		t.Fatalf("got %q, want empty for a shell mentioning buildkitd", got)
	}
}

func TestRootFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buildkitd.toml")
	if err := os.WriteFile(path, []byte("# comment\nroot = \"/data/buildkit/root\"\n\n[worker.oci]\n  gc = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rootFromConfig(path); got != "/data/buildkit/root" {
		t.Fatalf("got %q, want the top-level root", got)
	}
}

// A `root` inside a table is a different key. Stopping at the first table keeps
// this from reporting a worker's setting as the daemon's state directory.
func TestRootFromConfig_IgnoresKeysInsideTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buildkitd.toml")
	if err := os.WriteFile(path, []byte("[worker.oci]\n  root = \"/not-the-daemon-root\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rootFromConfig(path); got != "" {
		t.Fatalf("got %q, want empty when root only appears inside a table", got)
	}
}

// A missing or unreadable config must degrade to "unknown", never fail: this
// runs inside the RPC a developer is using to find out what is wrong.
func TestRootFromConfig_MissingFileIsUnknown(t *testing.T) {
	if got := rootFromConfig(filepath.Join(t.TempDir(), "absent.toml")); got != "" {
		t.Fatalf("got %q, want empty for a missing config", got)
	}
}

// TestBuildkitRoot_FallsBackToTheDocumentedDefault: with no daemon and no
// config, report what buildkitd itself would use.
func TestBuildkitRoot_FallsBackToTheDocumentedDefault(t *testing.T) {
	if got := buildkitRoot(t.TempDir()); got != defaultBuildkitRoot {
		t.Fatalf("got %q, want %q", got, defaultBuildkitRoot)
	}
}

// TestBuildkitRootSpace_WalksToAnExistingAncestor: the state directory does not
// exist until the first build, and reporting zero for a merely-absent path
// would be indistinguishable from a full disk.
func TestBuildkitRootSpace_WalksToAnExistingAncestor(t *testing.T) {
	deep := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	total, free := buildkitRootSpace(deep)
	if total == 0 {
		t.Fatal("want the ancestor filesystem's size, not zero")
	}
	if free > total {
		t.Fatalf("free %d exceeds total %d", free, total)
	}
}

func TestBuildkitRootSpace_EmptyPathIsUnknown(t *testing.T) {
	if total, free := buildkitRootSpace(""); total != 0 || free != 0 {
		t.Fatalf("got (%d, %d), want zeroes for an unknown path", total, free)
	}
}
