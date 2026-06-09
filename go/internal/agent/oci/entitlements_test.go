package oci

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/board"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func hasGID(spec *Spec, gid uint32) bool {
	return slices.Contains(spec.Process.User.AdditionalGids, gid)
}

func hasMountDest(spec *Spec, dest string) bool {
	for _, m := range spec.Mounts {
		if m.Destination == dest {
			return true
		}
	}
	return false
}

func mountForDest(spec *Spec, dest string) (Mount, bool) {
	for _, m := range spec.Mounts {
		if m.Destination == dest {
			return m, true
		}
	}
	return Mount{}, false
}

func hasNamespace(spec *Spec, nsType string) bool {
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == nsType {
			return true
		}
	}
	return false
}

func hasEnv(spec *Spec, envPrefix string) bool {
	for _, e := range spec.Process.Env {
		if len(e) >= len(envPrefix) && e[:len(envPrefix)] == envPrefix {
			return true
		}
	}
	return false
}

func hasCapability(spec *Spec, cap string) bool {
	if spec.Process.Capabilities == nil {
		return false
	}
	return slices.Contains(spec.Process.Capabilities.Bounding, cap)
}

func hasAllowAllDeviceRule(spec *Spec) bool {
	for _, d := range spec.Linux.Resources.Devices {
		if d.Allow && d.Type == "" && d.Major == nil && d.Minor == nil {
			return true
		}
	}
	return false
}

func TestDefaultSpec_NoDangerousCapabilities(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	if hasCapability(spec, "CAP_SYS_CHROOT") {
		t.Error("default spec must not grant CAP_SYS_CHROOT (WDY-1099)")
	}
	if hasCapability(spec, "CAP_SYS_PTRACE") {
		t.Error("default spec must not grant CAP_SYS_PTRACE (WDY-1099)")
	}
}

func TestDefaultSpec_SeccompProfile(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	if spec.Linux.Seccomp == nil {
		t.Fatal("default spec must have a seccomp profile (WDY-1099)")
	}
	if spec.Linux.Seccomp.DefaultAction != ActAllow {
		t.Errorf("seccomp DefaultAction = %q, want %q", spec.Linux.Seccomp.DefaultAction, ActAllow)
	}

	deniedSyscalls := make(map[string]bool)
	for _, sc := range spec.Linux.Seccomp.Syscalls {
		if sc.Action == ActErrno {
			for _, name := range sc.Names {
				deniedSyscalls[name] = true
			}
		}
	}
	for _, required := range []string{"ptrace", "unshare"} {
		if !deniedSyscalls[required] {
			t.Errorf("seccomp profile must deny syscall %q (WDY-1099)", required)
		}
	}

	// clone must be restricted for CLONE_NEWUSER (0x10000000).
	const cloneNewuser = uint64(0x10000000)
	for _, sc := range spec.Linux.Seccomp.Syscalls {
		if sc.Action != ActErrno {
			continue
		}
		for _, name := range sc.Names {
			if name != "clone" {
				continue
			}
			for _, arg := range sc.Args {
				if arg.Op == OpMaskedEqual && arg.Value == cloneNewuser {
					return
				}
			}
		}
	}
	t.Error("seccomp profile must deny clone with CLONE_NEWUSER (WDY-1099)")
}

func TestApplyEntitlements_GPU(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementGPU},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Should add nvidia group GID 44.
	if !hasGID(spec, 44) {
		t.Error("GPU entitlement did not add GID 44")
	}

	// Should have NVIDIA device nodes.
	foundNvidiaDevice := false
	for _, dev := range spec.Linux.Devices {
		if dev.Path == "/dev/nvidia0" {
			foundNvidiaDevice = true
			break
		}
	}
	if !foundNvidiaDevice {
		t.Error("GPU entitlement did not add /dev/nvidia0 device")
	}

	// Should have NVIDIA env vars.
	if !hasEnv(spec, "NVIDIA_VISIBLE_DEVICES") {
		t.Error("GPU entitlement did not set NVIDIA_VISIBLE_DEVICES")
	}
}

func TestApplyEntitlements_Network_Host(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork, Mode: "host"},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Network namespace should be removed for host networking.
	if hasNamespace(spec, "network") {
		t.Error("host network entitlement did not remove network namespace")
	}

	// CAP_NET_ADMIN should be added.
	if !slices.Contains(spec.Process.Capabilities.Bounding, "CAP_NET_ADMIN") {
		t.Error("host network entitlement did not add CAP_NET_ADMIN")
	}
}

func TestApplyEntitlements_Network_Host_ResolvConf(t *testing.T) {
	const resolvedConf = "/run/systemd/resolve/resolv.conf"
	_, errSystemd := os.Stat(resolvedConf)
	_, errHost := os.Stat("/etc/resolv.conf")
	if errSystemd != nil && errHost != nil {
		t.Skip("no resolv.conf on host; skipping DNS mount assertion")
	}

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork, Mode: "host"},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// A container with host networking but its own mount namespace needs
	// /etc/resolv.conf bind-mounted from the host; otherwise the container
	// rootfs may have an empty file and all DNS lookups fail.
	if !hasMountDest(spec, "/etc/resolv.conf") {
		t.Fatal("host network entitlement did not mount /etc/resolv.conf")
	}

	for _, m := range spec.Mounts {
		if m.Destination == "/etc/resolv.conf" {
			if m.Source != resolvedConf && m.Source != "/etc/resolv.conf" {
				t.Errorf("/etc/resolv.conf source = %q, want %q or %q",
					m.Source, resolvedConf, "/etc/resolv.conf")
			}
			if m.Type != "bind" {
				t.Errorf("/etc/resolv.conf mount type = %q, want \"bind\"", m.Type)
			}
			break
		}
	}
}

func TestApplyEntitlements_Network_Default(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			// Empty mode defaults to "host" per the code.
			{Type: appconfig.EntitlementNetwork, Mode: "none"},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// With mode "none", the network namespace should remain (namespaced networking).
	if !hasNamespace(spec, "network") {
		t.Error("network mode 'none' should keep network namespace")
	}
}

func TestApplyEntitlements_Audio(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementAudio},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Should add audio group GID 29.
	if !hasGID(spec, 29) {
		t.Error("audio entitlement did not add GID 29")
	}

	// Should mount /dev/snd.
	if !hasMountDest(spec, "/dev/snd") {
		t.Error("audio entitlement did not add /dev/snd mount")
	}

	// PipeWire mount is conditional — only added when a real socket exists
	// on the host at either /run/pipewire/pipewire-0 (system) or
	// /run/user/*/pipewire-0 (user session).
	isSocket := func(path string) bool {
		fi, err := os.Lstat(path)
		return err == nil && fi.Mode()&os.ModeSocket != 0 && fi.Mode()&os.ModeSymlink == 0
	}
	// Mirror applyAudio's socket detection: system path first, then user session.
	var pipewireSocketSource string
	if isSocket("/run/pipewire/pipewire-0") {
		pipewireSocketSource = "/run/pipewire/pipewire-0"
	} else {
		userSockets, _ := filepath.Glob("/run/user/*/pipewire-0")
		for _, s := range userSockets {
			if isSocket(s) {
				pipewireSocketSource = s
				break
			}
		}
	}
	if pipewireSocketSource != "" {
		if !hasMountDest(spec, "/run/pipewire/pipewire-0") {
			t.Error("audio entitlement did not add /run/pipewire/pipewire-0 mount")
		}
		if !hasEnv(spec, "PIPEWIRE_RUNTIME_DIR") {
			t.Error("audio entitlement did not set PIPEWIRE_RUNTIME_DIR")
		}
		// Derive pulse path from the same source directory as applyAudio does.
		sourceDir := filepath.Dir(pipewireSocketSource)
		pulseNative := filepath.Join(sourceDir, "pulse", "native")
		if isSocket(pulseNative) {
			if !hasMountDest(spec, "/run/pipewire/pulse-native") {
				t.Error("audio entitlement did not add /run/pipewire/pulse-native mount")
			}
			if !hasEnv(spec, "PULSE_SERVER") {
				t.Error("audio entitlement did not set PULSE_SERVER when pulse socket exists")
			}
		}
	} else {
		if hasMountDest(spec, "/run/pipewire/pipewire-0") || hasMountDest(spec, "/run/pipewire/pulse-native") {
			t.Error("audio entitlement should not mount /run/pipewire when socket is absent")
		}
	}

	// Audio should remain constrained to explicit sound-device rules even
	// though it calls SetDeviceCapabilities().
	foundSoundRule := false
	for _, d := range spec.Linux.Resources.Devices {
		if d.Major != nil && *d.Major == 116 && d.Allow {
			foundSoundRule = true
			break
		}
	}
	if !foundSoundRule {
		t.Error("audio entitlement did not add sound cgroup device rule (major 116)")
	}
	if hasAllowAllDeviceRule(spec) {
		t.Error("audio entitlement should not add a generic allow-all device cgroup rule")
	}
}

func TestApplyEntitlements_Persist(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "my-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementPersist, Name: "data", Path: "/app/data"},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Should add a bind mount for /app/data.
	if !hasMountDest(spec, "/app/data") {
		t.Error("persist entitlement did not add /app/data mount")
	}

	// Verify the source path uses the volume name (shared across apps).
	for _, m := range spec.Mounts {
		if m.Destination == "/app/data" {
			expected := "/var/lib/wendy/volumes/data"
			if m.Source != expected {
				t.Errorf("persist mount source = %q, want %q", m.Source, expected)
			}
			if m.Type != "bind" {
				t.Errorf("persist mount type = %q, want %q", m.Type, "bind")
			}
			break
		}
	}
}

func TestApplyEntitlements_Bluetooth_NoProxy(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementBluetooth},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{DBusProxyAvailable: false}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Without the proxy, raw host D-Bus sockets must NOT be mounted
	// (they expose NetworkManager and other privileged services).
	if hasMountDest(spec, "/var/run/dbus") {
		t.Error("bluetooth without proxy should not mount /var/run/dbus")
	}
	if hasMountDest(spec, "/run/dbus") {
		t.Error("bluetooth without proxy should not mount /run/dbus")
	}

	// The env var should still be set so apps know the expected path.
	if !hasEnv(spec, "DBUS_SYSTEM_BUS_ADDRESS") {
		t.Error("bluetooth entitlement did not set DBUS_SYSTEM_BUS_ADDRESS")
	}
}

func TestApplyEntitlements_Bluetooth_Proxy(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "bt-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementBluetooth},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{DBusProxyAvailable: true}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Proxy mode should mount from the proxy directory.
	if !hasMountDest(spec, "/var/run/dbus") {
		t.Error("bluetooth proxy did not add /var/run/dbus mount")
	}

	// Should NOT have /run/dbus (only one mount with proxy).
	if hasMountDest(spec, "/run/dbus") {
		t.Error("bluetooth proxy should not add /run/dbus mount")
	}

	// Verify source points to proxy directory.
	for _, m := range spec.Mounts {
		if m.Destination == "/var/run/dbus" {
			expected := "/run/wendy/dbus-proxy/bt-app"
			if m.Source != expected {
				t.Errorf("proxy /var/run/dbus source = %q, want %q", m.Source, expected)
			}
		}
	}

	if !hasEnv(spec, "DBUS_SYSTEM_BUS_ADDRESS") {
		t.Error("bluetooth entitlement did not set DBUS_SYSTEM_BUS_ADDRESS")
	}
}

// TestBluetoothEntitlementDoesNotExposeNetworkManager verifies that enabling
// only the Bluetooth entitlement does not give the container unrestricted
// access to the D-Bus system bus. Mounting the raw host D-Bus socket
// (/var/run/dbus, /run/dbus) lets the container talk to every D-Bus service,
// including NetworkManager — effectively granting root-level network control
// to a container that only asked for Bluetooth.
func TestBluetoothEntitlementDoesNotExposeNetworkManager(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "bt-only-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementBluetooth},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// The raw host D-Bus system socket must NOT be bind-mounted into the
	// container. Doing so exposes every D-Bus service (NetworkManager,
	// systemd, polkit, etc.) — not just BlueZ. D-Bus access should be
	// filtered/proxied so only org.bluez is reachable.
	for _, m := range spec.Mounts {
		if m.Source == "/var/run/dbus" || m.Source == "/run/dbus" {
			t.Errorf("Bluetooth entitlement bind-mounts raw D-Bus system socket %q -> %q; "+
				"this exposes NetworkManager and other privileged D-Bus services. "+
				"D-Bus access must be scoped to BlueZ only (org.bluez).",
				m.Source, m.Destination)
		}
	}

	// The network namespace must remain intact — Bluetooth should not
	// alter network isolation.
	if !hasNamespace(spec, "network") {
		t.Error("Bluetooth-only entitlement removed the network namespace")
	}

	// CAP_NET_ADMIN must not be granted by Bluetooth alone.
	if spec.Process.Capabilities != nil &&
		slices.Contains(spec.Process.Capabilities.Bounding, "CAP_NET_ADMIN") {
		t.Error("Bluetooth entitlement should not grant CAP_NET_ADMIN")
	}
}

func assertCameraEntitlement(t *testing.T, spec *Spec, entType string) {
	t.Helper()

	if !hasGID(spec, 44) {
		t.Errorf("%s entitlement did not add GID 44", entType)
	}

	foundV4L2Rule := false
	for _, d := range spec.Linux.Resources.Devices {
		if d.Major != nil && *d.Major == 81 && d.Allow {
			foundV4L2Rule = true
			if strings.Contains(d.Access, "m") {
				t.Errorf("%s entitlement V4L2 rule must not grant mknod, got Access=%q", entType, d.Access)
			}
			break
		}
	}
	if !foundV4L2Rule {
		t.Errorf("%s entitlement did not add V4L2 cgroup device rule (major 81)", entType)
	}
	if hasAllowAllDeviceRule(spec) {
		t.Errorf("%s entitlement should not add a generic allow-all device cgroup rule", entType)
	}

	devMount, ok := mountForDest(spec, "/dev")
	if !ok {
		t.Fatalf("%s entitlement did not define /dev mount", entType)
	}
	if devMount.Source != "/dev" || devMount.Type != "bind" {
		t.Fatalf("%s entitlement /dev mount = %+v, want bind mount from host /dev", entType, devMount)
	}
	if !slices.Contains(devMount.Options, "rbind") {
		t.Errorf("%s entitlement /dev mount missing rbind option", entType)
	}
	if !slices.Contains(devMount.Options, "rw") {
		t.Errorf("%s entitlement /dev mount missing rw option", entType)
	}
	if !slices.Contains(devMount.Options, "noexec") {
		t.Errorf("%s entitlement /dev mount missing noexec option", entType)
	}
	if hasCapability(spec, "CAP_SYS_PTRACE") {
		t.Errorf("%s entitlement must not grant CAP_SYS_PTRACE (WDY-1099)", entType)
	}
	if hasCapability(spec, "CAP_SYS_CHROOT") {
		t.Errorf("%s entitlement must not grant CAP_SYS_CHROOT (WDY-1099)", entType)
	}
}

func TestApplyEntitlements_Video(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementVideo},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	assertCameraEntitlement(t, spec, "video")
}

func TestApplyEntitlements_Camera(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementCamera},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	assertCameraEntitlement(t, spec, "camera")
}

// TestApplyEntitlements_Camera_UdevMount verifies the camera entitlement
// bind-mounts the host udev runtime read-only. libcamera enumerates CSI
// cameras through libudev, which reads /run/udev/data; without this mount the
// in-container enumerator finds no cameras even though the device nodes and
// cgroup rules are present (WDY-1342). udevRuntimeDir is redirected to a real
// temp dir so the os.Stat guard is deterministic across CI hosts.
func TestApplyEntitlements_Camera_UdevMount(t *testing.T) {
	orig := udevRuntimeDir
	t.Cleanup(func() { udevRuntimeDir = orig })
	udevRuntimeDir = t.TempDir()

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementCamera},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	m, ok := mountForDest(spec, "/run/udev")
	if !ok {
		t.Fatal("camera entitlement did not bind-mount /run/udev (libcamera CSI enumeration needs it)")
	}
	if m.Source != udevRuntimeDir {
		t.Errorf("/run/udev mount source = %q, want %q", m.Source, udevRuntimeDir)
	}
	if m.Type != "bind" {
		t.Errorf("/run/udev mount type = %q, want \"bind\"", m.Type)
	}
	if !slices.Contains(m.Options, "rbind") {
		t.Error("/run/udev mount missing rbind option")
	}
	if !slices.Contains(m.Options, "ro") {
		t.Error("/run/udev mount must be read-only (ro)")
	}
}

// TestApplyEntitlements_Camera_UdevMountSkippedWhenAbsent verifies that a host
// without a udev runtime directory does not get a /run/udev mount, so the
// container can still start (runc fails a bind mount whose source is missing).
func TestApplyEntitlements_Camera_UdevMountSkippedWhenAbsent(t *testing.T) {
	orig := udevRuntimeDir
	t.Cleanup(func() { udevRuntimeDir = orig })
	udevRuntimeDir = filepath.Join(t.TempDir(), "does-not-exist")

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementCamera},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	if hasMountDest(spec, "/run/udev") {
		t.Error("camera entitlement mounted /run/udev despite host udev runtime being absent")
	}
}

func TestApplyEntitlements_Multiple(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "multi-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementGPU},
			{Type: appconfig.EntitlementAudio},
			{Type: appconfig.EntitlementNetwork, Mode: "host"},
			{Type: appconfig.EntitlementPersist, Name: "models", Path: "/models"},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// GPU
	if !hasGID(spec, 44) {
		t.Error("missing GPU GID 44")
	}

	// Audio
	if !hasGID(spec, 29) {
		t.Error("missing audio GID 29")
	}
	if !hasMountDest(spec, "/dev/snd") {
		t.Error("missing /dev/snd mount")
	}

	// Network host
	if hasNamespace(spec, "network") {
		t.Error("network namespace should be removed for host mode")
	}

	// Persist
	if !hasMountDest(spec, "/models") {
		t.Error("missing /models mount")
	}
}

func TestApplyEntitlements_Input(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementInput},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Should add input group GID 105.
	if !hasGID(spec, 105) {
		t.Error("input entitlement did not add GID 105")
	}

	// Should mount /dev/input.
	if !hasMountDest(spec, "/dev/input") {
		t.Error("input entitlement did not add /dev/input mount")
	}

	// Verify mount options.
	for _, m := range spec.Mounts {
		if m.Destination == "/dev/input" {
			if m.Source != "/dev/input" {
				t.Errorf("input mount source = %q, want %q", m.Source, "/dev/input")
			}
			if m.Type != "bind" {
				t.Errorf("input mount type = %q, want %q", m.Type, "bind")
			}
			if !slices.Contains(m.Options, "rbind") {
				t.Error("input mount missing rbind option")
			}
			if !slices.Contains(m.Options, "nosuid") {
				t.Error("input mount missing nosuid option")
			}
			if !slices.Contains(m.Options, "noexec") {
				t.Error("input mount missing noexec option")
			}
			break
		}
	}

	// Should add a cgroup rule for input devices (major 13).
	foundInputRule := false
	for _, d := range spec.Linux.Resources.Devices {
		if d.Major != nil && *d.Major == 13 && d.Allow {
			foundInputRule = true
			break
		}
	}
	if !foundInputRule {
		t.Error("input entitlement did not add cgroup device rule (major 13)")
	}
}

func TestApplyEntitlements_GPIO(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementGPIO, Pins: []int{5, 6, 13}},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Should add a cgroup rule for GPIO devices (major 254).
	foundGPIORule := false
	for _, d := range spec.Linux.Resources.Devices {
		if d.Major != nil && *d.Major == 254 && d.Allow {
			foundGPIORule = true
			break
		}
	}
	if !foundGPIORule {
		t.Error("gpio entitlement did not add cgroup device rule (major 254)")
	}

	// Should mount /dev/gpiochip0 when it exists on the host.
	t.Run("mounts /dev/gpiochip0 when present", func(t *testing.T) {
		if _, err := os.Stat("/dev/gpiochip0"); err == nil {
			if !hasMountDest(spec, "/dev/gpiochip0") {
				t.Error("gpio entitlement did not add /dev/gpiochip0 mount")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat /dev/gpiochip0: %v", err)
		} else {
			t.Skip("/dev/gpiochip0 not present on this host")
		}
	})
}

func TestApplyEntitlements_SPI(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementSPI},
		},
	}

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	foundSPIRule := false
	for _, d := range spec.Linux.Resources.Devices {
		if d.Major != nil && *d.Major == 153 && d.Allow {
			foundSPIRule = true
			break
		}
	}
	if !foundSPIRule {
		t.Error("spi entitlement did not add SPI cgroup device rule (major 153)")
	}

	// SPI group GID: if the "spi" group exists on this host, verify it was added.
	// If not (e.g. macOS, ubuntu CI), verify we didn't add a bogus GID.
	if grp, err := user.LookupGroup("spi"); err == nil {
		gid, err := strconv.ParseUint(grp.Gid, 10, 32)
		if err != nil {
			t.Fatalf("failed to parse spi group GID %q: %v", grp.Gid, err)
		}
		if !hasGID(spec, uint32(gid)) {
			t.Errorf("spi entitlement did not add spi group GID %d", gid)
		}
	} else if len(spec.Process.User.AdditionalGids) != 0 {
		t.Errorf("spi group not present on host but AdditionalGids is not empty: %v", spec.Process.User.AdditionalGids)
	}

	// Should mount /dev/spidev0.0 when it exists on the host.
	t.Run("mounts /dev/spidev0.0 when present", func(t *testing.T) {
		if _, err := os.Stat("/dev/spidev0.0"); err == nil {
			if !hasMountDest(spec, "/dev/spidev0.0") {
				t.Error("spi entitlement did not add /dev/spidev0.0 mount")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat /dev/spidev0.0: %v", err)
		} else {
			t.Skip("/dev/spidev0.0 not present on this host")
		}
	})
}

func TestApplyEntitlements_Empty(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID:        "test-app",
		Entitlements: []appconfig.Entitlement{},
	}

	originalMountCount := len(spec.Mounts)
	originalNSCount := len(spec.Linux.Namespaces)

	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}

	// Spec should remain unchanged.
	if len(spec.Mounts) != originalMountCount {
		t.Errorf("mount count changed from %d to %d with no entitlements",
			originalMountCount, len(spec.Mounts))
	}
	if len(spec.Linux.Namespaces) != originalNSCount {
		t.Errorf("namespace count changed from %d to %d with no entitlements",
			originalNSCount, len(spec.Linux.Namespaces))
	}
}

// TestApplyI2C_PathTraversal verifies that a crafted device name cannot escape /dev/i2c-
// (WDY-1015).
func TestApplyI2C_PathTraversal(t *testing.T) {
	traversalCases := []string{
		"../sda",
		"../mem",
		"../../etc/passwd",
		"i2c-1/../sda",
		"sda",
		"i2c-",
		"i2c-1a",
	}
	for _, device := range traversalCases {
		t.Run(device, func(t *testing.T) {
			spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
			cfg := &appconfig.AppConfig{
				AppID: "test-app",
				Entitlements: []appconfig.Entitlement{
					{Type: appconfig.EntitlementI2C, Device: device},
				},
			}
			if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
				t.Fatalf("ApplyEntitlements() error = %v", err)
			}
			for _, m := range spec.Mounts {
				if !slices.Contains([]string{"/dev/snd", "/dev/input", "/dev/bus/usb"}, m.Destination) &&
					len(m.Destination) >= 9 && m.Destination[:9] != "/dev/i2c-" {
					if m.Destination == "/dev/sda" || m.Destination == "/dev/mem" ||
						m.Destination == "/etc/passwd" {
						t.Errorf("path traversal via device=%q mounted %q", device, m.Destination)
					}
				}
				if m.Destination == "/dev/"+device {
					t.Errorf("unsanitized device=%q was mounted as %q", device, m.Destination)
				}
			}
		})
	}
}

// TestApplyI2C_ValidDevice verifies that a legitimate i2c-N device is still mounted.
func TestApplyI2C_ValidDevice(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementI2C, Device: "i2c-1"},
		},
	}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}
	if !hasMountDest(spec, "/dev/i2c-1") {
		t.Error("valid i2c-1 device was not mounted")
	}
}

// TestApplyPersist_PathTraversalDestination verifies that a crafted mount destination
// cannot escape the container path validation (WDY-1016).
func TestApplyPersist_PathTraversalDestination(t *testing.T) {
	traversalCases := []string{
		"relative/path",
		"../escape",
		"data",
	}
	for _, path := range traversalCases {
		t.Run(path, func(t *testing.T) {
			spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
			cfg := &appconfig.AppConfig{
				AppID: "test-app",
				Entitlements: []appconfig.Entitlement{
					{Type: appconfig.EntitlementPersist, Name: "vol", Path: path},
				},
			}
			if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
				t.Fatalf("ApplyEntitlements() error = %v", err)
			}
			if hasMountDest(spec, path) {
				t.Errorf("relative/traversal path=%q was added as a mount destination", path)
			}
		})
	}
}

// TestApplyPersist_DotDotInDestination verifies that dot-dot components in the
// mount destination are rejected (WDY-1016).
func TestApplyPersist_DotDotInDestination(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementPersist, Name: "vol", Path: "/data/../etc"},
		},
	}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}
	// filepath.Clean resolves /data/../etc → /etc, so the cleaned path must not
	// be added as a mount destination when the original contained dot-dot.
	if hasMountDest(spec, "/etc") {
		t.Error("dot-dot in persist destination was silently resolved to /etc and mounted")
	}
	if hasMountDest(spec, "/data/../etc") {
		t.Error("raw dot-dot persist destination was mounted unchanged")
	}
}

// --- Camera CSI/libcamera extra majors ---

// installFakeCameraGlobs redirects cameraExtraGlobs into a tempdir populated
// with fake device files. statMajor is overridden to return canned majors
// keyed off the file basename. boardDetect is overridden to return Generic.
// Returns the chosen majors so tests can assert on them.
func installFakeCameraGlobs(t *testing.T, files map[string]int64) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dma_heap"), 0o755); err != nil {
		t.Fatal(err)
	}

	majorsByPath := map[string]int64{}
	for name, major := range files {
		var rel string
		switch {
		case strings.HasPrefix(name, "media"):
			rel = name
		case strings.HasPrefix(name, "v4l-subdev"):
			rel = name
		case strings.HasPrefix(name, "dma_heap/"):
			rel = name
		case strings.HasPrefix(name, "nvhost-") || name == "nvmap":
			rel = name
		default:
			t.Fatalf("unknown fake device fixture %q", name)
		}
		p := filepath.Join(dir, rel)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		majorsByPath[p] = major
	}

	origCamera := cameraExtraGlobs
	origJetson := jetsonExtraGlobs
	origStat := statMajor
	origBoard := boardDetect
	t.Cleanup(func() {
		cameraExtraGlobs = origCamera
		jetsonExtraGlobs = origJetson
		statMajor = origStat
		boardDetect = origBoard
	})

	cameraExtraGlobs = []string{
		filepath.Join(dir, "media*"),
		filepath.Join(dir, "v4l-subdev*"),
		filepath.Join(dir, "dma_heap", "*"),
	}
	jetsonExtraGlobs = []string{
		filepath.Join(dir, "nvhost-*"),
		filepath.Join(dir, "nvmap"),
	}
	statMajor = func(p string) (int64, error) {
		if m, ok := majorsByPath[p]; ok {
			return m, nil
		}
		return 0, os.ErrNotExist
	}
}

func hasMajorRule(spec *Spec, want int64) bool {
	for _, d := range spec.Linux.Resources.Devices {
		if d.Allow && d.Major != nil && *d.Major == want {
			return true
		}
	}
	return false
}

func countMajorRule(spec *Spec, want int64) int {
	n := 0
	for _, d := range spec.Linux.Resources.Devices {
		if d.Allow && d.Major != nil && *d.Major == want {
			n++
		}
	}
	return n
}

// majorRuleAccess returns the Access mask of the first allow rule for the given
// major, and whether such a rule exists.
func majorRuleAccess(spec *Spec, want int64) (string, bool) {
	for _, d := range spec.Linux.Resources.Devices {
		if d.Allow && d.Major != nil && *d.Major == want {
			return d.Access, true
		}
	}
	return "", false
}

func TestApplyCamera_AddsMediaMajor(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"media0": 234,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Generic} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if !hasMajorRule(spec, 234) {
		t.Errorf("expected cgroup allow for media major 234")
	}
	if countMajorRule(spec, 234) != 1 {
		t.Errorf("media major rule should be deduplicated, got %d entries", countMajorRule(spec, 234))
	}
}

func TestApplyCamera_AddsDmaHeapMajor(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"dma_heap/system":   510,
		"dma_heap/cma":      510,
		"dma_heap/reserved": 511,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Generic} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if !hasMajorRule(spec, 510) {
		t.Errorf("expected cgroup allow for dma_heap major 510")
	}
	if !hasMajorRule(spec, 511) {
		t.Errorf("expected cgroup allow for dma_heap major 511")
	}
	if countMajorRule(spec, 510) != 1 {
		t.Errorf("major 510 must be deduplicated across multiple files")
	}
}

func TestApplyCamera_AddsV4l2SubdevMajor(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"v4l-subdev0": 81,
		"v4l-subdev1": 81,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Generic} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	// 81 is already added by the legacy v4l2Major rule; the v4l-subdev scan
	// must not duplicate it.
	if countMajorRule(spec, 81) != 1 {
		t.Errorf("major 81 should appear exactly once, got %d", countMajorRule(spec, 81))
	}
}

func TestApplyCamera_JetsonAddsNvhostMajor(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"nvhost-ctrl": 230,
		"nvhost-vi":   230,
		"nvmap":       242,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Jetson} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if !hasMajorRule(spec, 230) {
		t.Errorf("expected nvhost major 230 to be allowed on Jetson")
	}
	if !hasMajorRule(spec, 242) {
		t.Errorf("expected nvmap major 242 to be allowed on Jetson")
	}
}

func TestApplyCamera_NonJetsonOmitsNvhost(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"nvhost-ctrl": 230,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Generic} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if hasMajorRule(spec, 230) {
		t.Errorf("nvhost major 230 must not be allowed on non-Jetson hosts")
	}
}

// TestApplyCamera_DeviceRulesOmitMknod locks in least privilege: every cgroup
// device rule the camera entitlement adds — the static v4l2 major and every
// dynamically-discovered media/dma-heap/v4l-subdev/nvhost/nvmap major — must
// grant "rw" only. The mknod ('m') bit is withheld because the container opens
// host-created, bind-mounted device nodes and never needs to create its own.
func TestApplyCamera_DeviceRulesOmitMknod(t *testing.T) {
	installFakeCameraGlobs(t, map[string]int64{
		"media0":          234,
		"v4l-subdev0":     235,
		"dma_heap/system": 236,
		"nvhost-ctrl":     230,
		"nvmap":           242,
	})
	boardDetect = func() board.Info { return board.Info{Kind: board.Jetson} }

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}

	for _, major := range []int64{81, 234, 235, 236, 230, 242} {
		acc, ok := majorRuleAccess(spec, major)
		if !ok {
			t.Errorf("expected an allow rule for camera major %d", major)
			continue
		}
		if strings.Contains(acc, "m") {
			t.Errorf("camera major %d must not grant mknod, got Access=%q", major, acc)
		}
		if acc != "rw" {
			t.Errorf("camera major %d Access = %q, want %q", major, acc, "rw")
		}
	}
}
