package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

const (
	// IPCSocketFilename is the single socket the agent exposes inside each
	// IPC directory. It is fixed rather than app-chosen so both sides of the
	// entitlement agree on the path without any out-of-band convention — the
	// "discovery by convention" failure the shared-persist workaround had.
	IPCSocketFilename = "ipc.sock"

	// appIPCGroupGID is reserved by WendyOS for app-provided IPC sockets, the
	// direct analogue of appSystemAPIGroupGID (2000) for agent-provided ones.
	// Membership is granted only alongside an IPC bind mount, so the GID alone
	// reaches nothing.
	appIPCGroupGID = 2001

	// ipcDirMode is setgid + rwx for owner and group, nothing for others.
	// setgid matters: a socket the provider binds inside the directory inherits
	// group appIPCGroupGID automatically, so a non-root provider does not
	// have to know the GID exists.
	ipcDirMode = os.FileMode(0o770) | os.ModeSetgid

	// ipcRootMode keeps the root traversable (containers only ever receive a
	// bind mount of one child) but not listable by anyone but root.
	ipcRootMode = os.FileMode(0o711)
)

// IPCSocketRootPath is disk-backed, not on /run, for the same reason as
// AdminAgentSocketHostPath: a container's bind mount pins the directory inode,
// and tmpfs is wiped (fresh inode) on every boot, which would strand a
// long-lived consumer on an orphaned pre-reboot inode. Behind a var so tests
// can redirect it into a tempdir.
var IPCSocketRootPath = "/var/lib/wendy/ipc"

// ipcSocketEntry is the agent's record of one IPC name.
//
// owners is keyed by container (appID + serviceName — the app's service name
// from the top-level `services` map, not the IPC name) so a multi-service app
// counts once per service container, mirroring AppSystemAPISocketManager. The
// directory outlives every individual container in owners and is removed only
// when the last one is deleted — removing it earlier would replace the inode
// that still-deployed consumers have bind-mounted.
type ipcSocketEntry struct {
	// provider is the owner key of the container that declared role "provide",
	// or "" when only consumers are deployed. It is the anti-hijack record: a
	// second app cannot claim a name that is already provided.
	provider string
	owners   map[string]string // owner key → role
}

// IPCSocketManager owns the host side of the `ipc` entitlement: the
// per-name directory, its ownership and permissions, which app is allowed to
// provide each name, and removal of a stale socket before a provider starts.
//
// SECURITY: the bind mount is the entire trust boundary, exactly as for the
// `admin` and `notifications` entitlements. A consumer receives one directory
// containing one socket and nothing else — in particular, none of the
// provider's persist volume. The provider's identity for a name is
// first-claim-wins and is held until the providing container is deleted, so a
// later app cannot take over a name that is already being served.
type IPCSocketManager struct {
	logger  *zap.Logger
	mu      sync.Mutex
	entries map[string]*ipcSocketEntry
}

func NewIPCSocketManager(logger *zap.Logger) *IPCSocketManager {
	return &IPCSocketManager{
		logger:  logger,
		entries: make(map[string]*ipcSocketEntry),
	}
}

// Ensure registers one container as an owner of an IPC name and returns the
// host directory to bind-mount for it. It is idempotent for the same container,
// so a redeploy or an agent-restart restore can call it again safely.
//
// It never creates, removes, or touches the socket itself: that is
// PrepareProvider's job, and keeping it out of Ensure is what makes Ensure safe
// to call from the restore path against a provider that is already serving.
func (m *IPCSocketManager) Ensure(name, role, appID, serviceName string) (string, error) {
	if role != appconfig.IPCRoleProvide && role != appconfig.IPCRoleConsume {
		return "", fmt.Errorf("ipc role must be %q or %q, got %q",
			appconfig.IPCRoleProvide, appconfig.IPCRoleConsume, role)
	}
	owner, err := ipcSocketOwner(name, appID, serviceName)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(IPCSocketRootPath, name)

	m.mu.Lock()
	defer m.mu.Unlock()

	entry := m.entries[name]
	if entry == nil {
		entry = &ipcSocketEntry{owners: make(map[string]string)}
		m.entries[name] = entry
	}
	if role == appconfig.IPCRoleProvide && entry.provider != "" && entry.provider != owner {
		return "", fmt.Errorf(
			"ipc service %q is already provided by %s on this device; only one app may provide an ipc name (remove the other app first)",
			name, entry.provider)
	}

	if err := m.prepareIPCDirectory(directory); err != nil {
		// Roll the registration back so a failed create does not leave a phantom
		// owner (or a phantom provider claim) blocking the name.
		if len(entry.owners) == 0 {
			delete(m.entries, name)
		}
		return "", err
	}

	entry.owners[owner] = role
	if role == appconfig.IPCRoleProvide {
		entry.provider = owner
	}
	if role == appconfig.IPCRoleConsume && entry.provider == "" {
		// A typo in the name is otherwise indistinguishable from "the provider
		// is not deployed yet" — the second thing the hardware spike called out.
		// Say so at deploy time, with the names that do exist, instead of
		// leaving the app to hand-roll a retry against a path that will never
		// appear.
		m.logger.Warn("ipc consumer has no provider on this device",
			zap.String("ipc_name", name),
			zap.String("app_id", appID),
			zap.Strings("provided_ipc_names", m.providedNamesLocked()))
	}
	return directory, nil
}

// PrepareProvider removes a stale socket left behind by a previous run of the
// provider, and must be called before the provider's task starts. A provider
// that dies without cleanup otherwise leaves a socket file that consumers can
// open but not connect to, and that the provider itself cannot rebind
// (EADDRINUSE) — the platform owns that here so no app has to `rm -f` on boot.
//
// It is a no-op for a name this container does not provide, so callers can
// invoke it for every ipc entitlement on a container without branching.
func (m *IPCSocketManager) PrepareProvider(name, appID, serviceName string) error {
	owner, err := ipcSocketOwner(name, appID, serviceName)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[name]
	if entry == nil || entry.provider != owner {
		return nil
	}

	socketPath := filepath.Join(IPCSocketRootPath, name, IPCSocketFilename)
	// Lstat, not Stat: a symlink planted in the directory must be removed as the
	// link it is, never followed to a target outside the directory.
	fi, err := os.Lstat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspecting stale ipc socket for %q: %w", name, err)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("removing stale ipc socket for %q: %w", name, err)
	}
	m.logger.Info("removed stale ipc socket before provider start",
		zap.String("ipc_name", name),
		zap.String("app_id", appID),
		zap.String("mode", fi.Mode().String()))
	return nil
}

// Release drops one container's ownership of an IPC name. The directory (and
// any socket in it) is removed only once no provider and no consumer remain, so
// a provider restart never invalidates a deployed consumer's bind mount.
func (m *IPCSocketManager) Release(name, appID, serviceName string) {
	owner, err := ipcSocketOwner(name, appID, serviceName)
	if err != nil {
		// A malformed identity cannot have been registered by Ensure, so there
		// is nothing to release.
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseOwnerLocked(name, owner)
}

// ReleaseApp drops every ownership held by an app, across all IPC names. It
// is the whole-app teardown counterpart of Release, used once containerd
// confirms the app has no remaining containers.
func (m *IPCSocketManager) ReleaseApp(appID string) {
	if appconfig.ValidateAppID(appID) != nil {
		return
	}
	prefix := ipcOwnerPrefix(appID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, entry := range m.entries {
		for owner := range entry.owners {
			if strings.HasPrefix(owner, prefix) {
				m.releaseOwnerLocked(name, owner)
			}
		}
	}
}

func (m *IPCSocketManager) releaseOwnerLocked(name, owner string) {
	entry := m.entries[name]
	if entry == nil {
		return
	}
	if _, ok := entry.owners[owner]; !ok {
		return
	}
	delete(entry.owners, owner)
	if entry.provider == owner {
		entry.provider = ""
	}
	if len(entry.owners) != 0 {
		return
	}
	delete(m.entries, name)
	if err := os.RemoveAll(filepath.Join(IPCSocketRootPath, name)); err != nil {
		m.logger.Warn("cannot remove unused ipc socket directory",
			zap.String("ipc_name", name), zap.Error(err))
	}
}

// providedNamesLocked returns the sorted set of IPC names that currently have a
// provider. Caller must hold m.mu.
func (m *IPCSocketManager) providedNamesLocked() []string {
	names := make([]string, 0, len(m.entries))
	for name, entry := range m.entries {
		if entry.provider != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ipcSocketOwner re-validates every identity that reaches the manager and
// returns the owner key. The agent must not trust an IPC name, app ID, or app
// service name that arrived over RPC or was read back from container labels
// (SOC2-CC6, NIST-SI-10) — the IPC name in particular becomes a host path
// component.
func ipcSocketOwner(name, appID, serviceName string) (string, error) {
	if err := appconfig.ValidateIPCName(name); err != nil {
		return "", err
	}
	if err := appconfig.ValidateAppID(appID); err != nil {
		return "", fmt.Errorf("invalid app ID: %w", err)
	}
	if serviceName != "" {
		if err := appconfig.ValidateServiceName(serviceName); err != nil {
			return "", fmt.Errorf("invalid service name: %w", err)
		}
	}
	return ipcOwnerPrefix(appID) + serviceName, nil
}

func ipcOwnerPrefix(appID string) string { return appID + "/" }

// prepareIPCDirectory creates (or re-asserts) the host directory for one IPC
// name. Permissions are re-applied on every call rather than only at
// creation: MkdirAll is a no-op for an existing directory, so a directory left
// behind by an older agent build would otherwise keep its old mode forever.
func (m *IPCSocketManager) prepareIPCDirectory(directory string) error {
	if err := os.MkdirAll(IPCSocketRootPath, ipcRootMode); err != nil {
		return fmt.Errorf("create ipc socket root: %w", err)
	}
	if err := os.Chmod(IPCSocketRootPath, ipcRootMode); err != nil {
		return fmt.Errorf("set ipc socket root permissions: %w", err)
	}
	if err := os.MkdirAll(directory, 0o770); err != nil {
		return fmt.Errorf("create ipc socket directory: %w", err)
	}
	// Chmod after MkdirAll: MkdirAll's mode is masked by the process umask and
	// cannot set the setgid bit at all.
	if err := os.Chmod(directory, ipcDirMode); err != nil {
		return fmt.Errorf("set ipc socket directory permissions: %w", err)
	}
	// Chown before the setgid verification below: on some filesystems chown
	// clears setgid, so the check has to see the final state.
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, 0, appIPCGroupGID); err != nil {
			return fmt.Errorf("set ipc socket directory ownership: %w", err)
		}
		if err := os.Chmod(directory, ipcDirMode); err != nil {
			return fmt.Errorf("re-set ipc socket directory permissions after chown: %w", err)
		}
	}
	// A chmod that silently drops setgid (some filesystems and non-Linux hosts
	// do exactly that, without erroring) degrades non-root providers quietly:
	// their socket would land in their own primary group instead of the ipc
	// group, and non-root consumers could not reach it. Warn rather than fail —
	// a root-run provider and consumer pair still works.
	if fi, err := os.Stat(directory); err == nil && fi.Mode()&os.ModeSetgid == 0 {
		m.logger.Warn("ipc socket directory did not retain the setgid bit; a non-root provider's socket will not inherit the ipc group",
			zap.String("directory", directory),
			zap.String("mode", fi.Mode().String()))
	}
	return nil
}
