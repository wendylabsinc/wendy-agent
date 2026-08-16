package services

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func newTestIPCSocketManager(t *testing.T) *IPCSocketManager {
	t.Helper()
	// os.MkdirTemp("/tmp", …) rather than t.TempDir(): a unix socket path is
	// capped at 108 bytes and macOS t.TempDir() paths are long enough to blow
	// past it once the IPC directory and socket name are appended.
	root, err := os.MkdirTemp("/tmp", "wendy-svc-")
	if err != nil {
		t.Fatal(err)
	}
	original := IPCSocketRootPath
	IPCSocketRootPath = root
	t.Cleanup(func() {
		IPCSocketRootPath = original
		_ = os.RemoveAll(root)
	})
	return NewIPCSocketManager(zap.NewNop())
}

func TestIPCSocketManager_EnsureCreatesSetgidDirectory(t *testing.T) {
	m := newTestIPCSocketManager(t)

	dir, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if want := filepath.Join(IPCSocketRootPath, "world"); dir != want {
		t.Errorf("directory = %q, want %q", dir, want)
	}

	// setgid is what makes a socket the provider binds inherit the ipc
	// group automatically, so a non-root provider never has to know the GID.
	if ipcDirMode&os.ModeSetgid == 0 {
		t.Error("ipcDirMode does not request the setgid bit")
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat ipc directory: %v", err)
	}
	// macOS silently drops setgid when the caller is not root and not a member
	// of the directory's group, so only assert the on-disk bit where the agent
	// actually runs.
	if runtime.GOOS == "linux" && fi.Mode()&os.ModeSetgid == 0 {
		t.Errorf("ipc directory mode = %v, want the setgid bit set", fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm != 0o770 {
		t.Errorf("ipc directory perm = %04o, want 0770 (no access for others)", perm)
	}

	// The root must not be world-listable: a container only ever receives a
	// bind mount of one child, never the root.
	rootInfo, err := os.Stat(IPCSocketRootPath)
	if err != nil {
		t.Fatalf("stat ipc root: %v", err)
	}
	if perm := rootInfo.Mode().Perm(); perm != 0o711 {
		t.Errorf("ipc root perm = %04o, want 0711", perm)
	}
}

func TestIPCSocketManager_EnsureIsIdempotentForSameOwner(t *testing.T) {
	m := newTestIPCSocketManager(t)
	first, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	second, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if first != second {
		t.Errorf("Ensure returned %q then %q; want a stable directory", first, second)
	}
}

func TestIPCSocketManager_MissingProviderWarning(t *testing.T) {
	m := newTestIPCSocketManager(t)
	if _, err := m.Ensure("planner", appconfig.IPCRoleProvide, "com.example.planner", ""); err != nil {
		t.Fatalf("Ensure planner provider: %v", err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.consumer", ""); err != nil {
		t.Fatalf("Ensure world consumer: %v", err)
	}

	warning := m.MissingProviderWarning("world")
	if !strings.Contains(warning, `ipc consumer "world" has no provider`) {
		t.Fatalf("warning = %q, want missing-provider context", warning)
	}
	if !strings.Contains(warning, "planner") {
		t.Fatalf("warning = %q, want available provider name", warning)
	}

	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", ""); err != nil {
		t.Fatalf("Ensure world provider: %v", err)
	}
	if warning := m.MissingProviderWarning("world"); warning != "" {
		t.Fatalf("warning with provider present = %q, want empty", warning)
	}
}

// Only one app may provide a name: otherwise a second app could stand up its
// own socket on the name a consumer already trusts and intercept its traffic.
func TestIPCSocketManager_RejectsSecondProvider(t *testing.T) {
	m := newTestIPCSocketManager(t)
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", ""); err != nil {
		t.Fatalf("first provider Ensure: %v", err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.impostor", ""); err == nil {
		t.Fatal("a second app claimed an already-provided ipc name")
	}
	// A consumer of the same name is still fine — that is the whole feature.
	if _, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.impostor", ""); err != nil {
		t.Fatalf("consumer Ensure after provider conflict: %v", err)
	}
}

// A name freed by removing its provider becomes claimable again.
func TestIPCSocketManager_ProviderNameIsReclaimableAfterRelease(t *testing.T) {
	m := newTestIPCSocketManager(t)
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", ""); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	m.Release("world", "com.example.provider", "")
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.other", ""); err != nil {
		t.Fatalf("Ensure after release: %v", err)
	}
}

// The stale-socket problem the hardware spike hit: a provider that dies without
// cleanup leaves a socket file that refuses connections and that the provider
// cannot rebind. The platform clears it before the provider's task starts.
func TestIPCSocketManager_PrepareProviderRemovesStaleSocket(t *testing.T) {
	m := newTestIPCSocketManager(t)
	dir, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	socketPath := filepath.Join(dir, IPCSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Close without unlinking, exactly as a crashed provider leaves things.
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("test setup: expected a stale socket at %q: %v", socketPath, err)
	}

	if err := m.PrepareProvider("world", "com.example.provider", ""); err != nil {
		t.Fatalf("PrepareProvider: %v", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Errorf("stale socket still present after PrepareProvider (err=%v)", err)
	}
	// The directory itself must survive: consumers have it bind-mounted, and
	// replacing its inode would strand them.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("ipc directory removed by PrepareProvider: %v", err)
	}
}

// PrepareProvider must not let one app clear the socket of a name another app
// provides.
func TestIPCSocketManager_PrepareProviderIgnoresNonProvider(t *testing.T) {
	m := newTestIPCSocketManager(t)
	dir, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.consumer", ""); err != nil {
		t.Fatalf("consumer Ensure: %v", err)
	}
	socketPath := filepath.Join(dir, IPCSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	if err := m.PrepareProvider("world", "com.example.consumer", ""); err != nil {
		t.Fatalf("PrepareProvider: %v", err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Errorf("a consumer removed the provider's live socket: %v", err)
	}

	// And it is a harmless no-op for a name nobody registered.
	if err := m.PrepareProvider("unknown", "com.example.consumer", ""); err != nil {
		t.Errorf("PrepareProvider for an unregistered name = %v, want nil", err)
	}
}

// The directory outlives every individual container that references it:
// removing it while a consumer still has it bind-mounted would replace the
// inode the consumer is pinned to.
func TestIPCSocketManager_DirectoryOutlivesProviderWhileConsumersRemain(t *testing.T) {
	m := newTestIPCSocketManager(t)
	dir, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", "")
	if err != nil {
		t.Fatalf("provider Ensure: %v", err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.consumer", ""); err != nil {
		t.Fatalf("consumer Ensure: %v", err)
	}
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	m.Release("world", "com.example.provider", "")
	after, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("directory removed while a consumer still holds it: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("directory inode changed while a consumer still holds it")
	}

	m.Release("world", "com.example.consumer", "")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory survived the last owner (err=%v)", err)
	}
}

func TestIPCSocketManager_ReleaseAppDropsEveryClaim(t *testing.T) {
	m := newTestIPCSocketManager(t)
	worldDir, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.app", "talker")
	if err != nil {
		t.Fatalf("Ensure world: %v", err)
	}
	plannerDir, err := m.Ensure("planner", appconfig.IPCRoleConsume, "com.example.app", "listener")
	if err != nil {
		t.Fatalf("Ensure planner: %v", err)
	}

	m.ReleaseApp("com.example.app")
	for _, dir := range []string{worldDir, plannerDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%q survived ReleaseApp (err=%v)", dir, err)
		}
	}
	// Idempotent: a second sweep after deleteOne already released is a no-op.
	m.ReleaseApp("com.example.app")

	// The name is claimable again by a different app.
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.other", ""); err != nil {
		t.Fatalf("Ensure after ReleaseApp: %v", err)
	}
}

// Multi-service apps count once per service container, so one service exiting
// does not tear the directory out from under its siblings.
func TestIPCSocketManager_RefcountsPerServiceContainer(t *testing.T) {
	m := newTestIPCSocketManager(t)
	dir, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.app", "alpha")
	if err != nil {
		t.Fatalf("Ensure alpha: %v", err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleConsume, "com.example.app", "beta"); err != nil {
		t.Fatalf("Ensure beta: %v", err)
	}

	m.Release("world", "com.example.app", "alpha")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory removed while sibling service beta still holds it: %v", err)
	}
	m.Release("world", "com.example.app", "beta")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory survived the last sibling (err=%v)", err)
	}
}

// Identities that reach the manager are re-validated: the ipc name becomes
// a host path component, so a traversal attempt must never create a directory
// outside the root.
func TestIPCSocketManager_RejectsMalformedIdentities(t *testing.T) {
	m := newTestIPCSocketManager(t)
	cases := []struct {
		desc                     string
		name, role, appID, svcNm string
	}{
		{"traversal name", "../etc", appconfig.IPCRoleProvide, "com.example.app", ""},
		{"dot dot name", "..", appconfig.IPCRoleProvide, "com.example.app", ""},
		{"slash name", "a/b", appconfig.IPCRoleProvide, "com.example.app", ""},
		{"empty name", "", appconfig.IPCRoleProvide, "com.example.app", ""},
		{"uppercase name", "World", appconfig.IPCRoleProvide, "com.example.app", ""},
		{"unknown role", "world", "publish", "com.example.app", ""},
		{"empty role", "world", "", "com.example.app", ""},
		{"empty app id", "world", appconfig.IPCRoleProvide, "", ""},
		{"dotted app id", "world", appconfig.IPCRoleProvide, "..", ""},
		{"bad app service name", "world", appconfig.IPCRoleProvide, "com.example.app", "Bad_Name"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if _, err := m.Ensure(tc.name, tc.role, tc.appID, tc.svcNm); err == nil {
				t.Fatalf("Ensure(%q, %q, %q, %q) = nil error, want a rejection", tc.name, tc.role, tc.appID, tc.svcNm)
			}
		})
	}

	entries, err := os.ReadDir(IPCSocketRootPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read ipc root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected claims created %d directories: %v", len(entries), entries)
	}
}

// A failed directory creation must not leave a phantom provider claim blocking
// the name for the app that retries.
func TestIPCSocketManager_FailedEnsureLeavesNoClaim(t *testing.T) {
	m := newTestIPCSocketManager(t)
	// A regular file where the ipc directory should go makes MkdirAll fail.
	if err := os.MkdirAll(IPCSocketRootPath, 0o711); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(IPCSocketRootPath, "world")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.provider", ""); err == nil {
		t.Fatal("Ensure succeeded despite a file blocking the directory")
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure("world", appconfig.IPCRoleProvide, "com.example.other", ""); err != nil {
		t.Fatalf("a failed Ensure left the name claimed: %v", err)
	}
}
