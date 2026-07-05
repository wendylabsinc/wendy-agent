package services

import (
	"os"
	"os/exec"
)

// rootfsRedundancyEfivar is the NVIDIA UEFI variable that gates Jetson A/B rootfs
// switching. A missing or zero level means the firmware runs single-slot: an OTA
// writes the inactive slot, the slot switch is ignored, and the device boots back
// into the old slot (a phantom rollback).
const rootfsRedundancyEfivar = "/sys/firmware/efi/efivars/RootfsRedundancyLevel-781e084c-a330-417c-b678-38e696380cb9"

// redundancyNotEnabledMessage is the honest failure for a Jetson whose firmware
// has A/B rootfs redundancy disabled. Redundancy is a flash-time (device-tree)
// setting NVIDIA firmware owns; the OS cannot enable it at runtime
// (RootfsRedundancyLevel is firmware-locked — efivarfs writes return EINVAL), so
// the only fix is a reflash with an A/B-enabled image.
const redundancyNotEnabledMessage = "cannot update: this Jetson's firmware does not have A/B rootfs redundancy enabled, so an OS update would install and then roll back. A/B redundancy is set at flash time and cannot be enabled from the running OS — reflash with an A/B-enabled WendyOS image, then retry."

// redundancyGate decides whether an OS update must be refused because Jetson A/B
// rootfs redundancy is not enabled in firmware. OS interactions are seams so the
// decision is unit-testable.
type redundancyGate struct {
	isJetson   func() bool
	readEfivar func(path string) ([]byte, error)
}

func newRedundancyGate() redundancyGate {
	return redundancyGate{isJetson: jetsonDetected, readEfivar: os.ReadFile}
}

// makeRedundancyGate is an indirection so UpdateOS handler tests can inject a
// stubbed gate without real OS access.
var makeRedundancyGate = newRedundancyGate

// blocksUpdate reports true when the OS update must be refused: a Jetson whose
// firmware A/B rootfs redundancy is not armed. Non-Jetson devices and armed
// Jetsons return false (the update proceeds normally).
func (g redundancyGate) blocksUpdate() bool {
	if !g.isJetson() {
		return false
	}
	return !g.armed()
}

func (g redundancyGate) armed() bool {
	raw, err := g.readEfivar(rootfsRedundancyEfivar)
	if err != nil {
		return false
	}
	return len(raw) >= 8 && (raw[4]|raw[5]|raw[6]|raw[7]) != 0
}

func jetsonDetected() bool {
	_, err := exec.LookPath("nvbootctrl")
	return err == nil
}
