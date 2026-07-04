package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.uber.org/zap"
)

// Jetson A/B rootfs redundancy is gated by a UEFI variable the firmware reads
// at boot. When it is missing or zero the device runs single-slot: an OTA
// writes the inactive slot and requests a slot switch the firmware ignores, so
// the update silently rolls back. These values mirror wendyos-update's
// tegrauefi connector and the builder's wendyos-tegra-arm-rootfs-redundancy
// boot service byte-for-byte.
const (
	rootfsRedundancyEfivar    = "/sys/firmware/efi/efivars/RootfsRedundancyLevel-781e084c-a330-417c-b678-38e696380cb9"
	rootfsRetryCountMaxEfivar = "/sys/firmware/efi/efivars/RootfsRetryCountMax-781e084c-a330-417c-b678-38e696380cb9"

	// appBPartition exists only on an A/B-flashed device. Its absence means the
	// device is genuinely single-slot and cannot be armed in software.
	appBPartition = "/dev/disk/by-partlabel/APP_b"

	// armAttemptMarker is the reboot-loop guard, shared with the boot service.
	armAttemptMarker = "/data/wendyos-update/rootfs-redundancy-arm-attempted"

	// armScript is the image-native arm-and-reboot helper (present on current
	// images, absent on older ones — the case the agent must handle itself).
	armScript = "/usr/sbin/wendyos-tegra-arm-rootfs-redundancy"
)

// efivar payload = 4 attribute bytes (0x07 = NV|BS|RT) + a UINT32 value.
var (
	rootfsRedundancyArmedBytes = []byte{0x07, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}
	rootfsRetryCountMaxBytes   = []byte{0x07, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00}
)

const (
	redundancyArmingMessage    = "Arming A/B rootfs redundancy and rebooting device; the update will resume automatically once it is back online."
	redundancyNoSlotMessage    = "cannot update: this Jetson has no B rootfs slot (no APP_b partition), so A/B redundancy cannot be armed in software. Reflash the device with an A/B image, then retry."
	redundancyArmFailedMessage = "cannot update: rootfs A/B redundancy was armed on a previous attempt but the firmware still reports it inactive. This device needs a reflash with an A/B image."
)

type armDecision int

const (
	// armNotNeeded: not a Jetson, or already armed — proceed with the install.
	armNotNeeded armDecision = iota
	// armPossible: unarmed, APP_b present, no prior attempt — arm and reboot.
	armPossible
	// armImpossibleNoSlot: unarmed with no APP_b — needs a reflash.
	armImpossibleNoSlot
	// armFailedPreviously: attempt marker set but still unarmed — needs a reflash.
	armFailedPreviously
)

// redundancyArmer decides whether Jetson A/B rootfs redundancy must be armed
// before an OTA and performs the arm+reboot. All OS interactions are seams so
// the decision logic is unit-testable.
type redundancyArmer struct {
	logger *zap.Logger

	isJetson    func() bool
	readEfivar  func(path string) ([]byte, error)
	statPath    func(path string) error
	lookPath    func(file string) (string, bool)
	runScript   func(path string) error
	writeEfivar func(path string, data []byte) error
	writeMarker func(path string) error
	reboot      func() error
}

func newRedundancyArmer(logger *zap.Logger) *redundancyArmer {
	return &redundancyArmer{
		logger:      logger,
		isJetson:    jetsonDetected,
		readEfivar:  os.ReadFile,
		statPath:    func(p string) error { _, err := os.Stat(p); return err },
		lookPath:    func(f string) (string, bool) { p, err := exec.LookPath(f); return p, err == nil },
		runScript:   runArmScript,
		writeEfivar: writeEfivarFile,
		writeMarker: writeMarkerFile,
		reboot:      rebootSystem,
	}
}

// makeRedundancyArmer is an indirection so UpdateOS handler tests can inject a
// stubbed armer without real OS access.
var makeRedundancyArmer = newRedundancyArmer

func (a *redundancyArmer) armed() bool {
	raw, err := a.readEfivar(rootfsRedundancyEfivar)
	if err != nil {
		return false
	}
	return len(raw) >= 8 && (raw[4]|raw[5]|raw[6]|raw[7]) != 0
}

func (a *redundancyArmer) decide() armDecision {
	if !a.isJetson() || a.armed() {
		return armNotNeeded
	}
	if a.statPath(appBPartition) != nil {
		return armImpossibleNoSlot
	}
	if a.statPath(armAttemptMarker) == nil {
		return armFailedPreviously
	}
	return armPossible
}

// arm arms A/B rootfs redundancy and reboots. Call only when decide() returned
// armPossible. On the delegate path the on-device script writes the marker,
// arms the efivar, and reboots itself; on the fallback path the agent does all
// three. A non-nil return means arming failed before any reboot was triggered.
func (a *redundancyArmer) arm() error {
	if path, ok := a.lookPath(armScript); ok {
		a.logger.Info("arming rootfs A/B redundancy via on-device script", zap.String("script", path))
		return a.runScript(path)
	}
	a.logger.Info("on-device arm script absent; arming rootfs A/B redundancy directly")
	if err := a.writeMarker(armAttemptMarker); err != nil {
		return fmt.Errorf("writing arm attempt marker: %w", err)
	}
	if err := a.writeEfivar(rootfsRedundancyEfivar, rootfsRedundancyArmedBytes); err != nil {
		return fmt.Errorf("arming RootfsRedundancyLevel: %w", err)
	}
	if err := a.writeEfivar(rootfsRetryCountMaxEfivar, rootfsRetryCountMaxBytes); err != nil {
		return fmt.Errorf("writing RootfsRetryCountMax: %w", err)
	}
	return a.reboot()
}

func jetsonDetected() bool {
	_, err := exec.LookPath("nvbootctrl")
	return err == nil
}

func runArmScript(path string) error {
	cmd := exec.Command(path)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	return cmd.Run()
}

// writeEfivarFile writes attribute-header+value bytes to an efivarfs file.
// efivarfs marks existing variables immutable, so clear the flag first (best
// effort); a not-yet-existing variable is created by the write. The single
// os.WriteFile call performs one write() of the whole payload, as efivarfs
// requires — mirroring the boot service's `cp` into efivarfs.
func writeEfivarFile(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		_ = exec.Command("chattr", "-i", path).Run()
	}
	return os.WriteFile(path, data, 0o644)
}

func writeMarkerFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("arm attempted by wendy-agent\n"), 0o644)
}
