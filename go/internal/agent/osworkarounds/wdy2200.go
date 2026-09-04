package osworkarounds

// WDY-2200 — non-durable UEFI capsule staging strands Jetson OTA.
//
// wendyos-update's SwapSlot stages the bootloader capsule onto the vfat ESP and
// fsyncs only the capsule file, not the filesystem. The agent then restarts the
// kernel immediately, without a userspace sync or an unmount, so the capsule
// never reaches disk and the firmware sees no capsule at all — distinguishable
// from a rejected one by ESRT last_attempt_version staying 0.
//
// Because the capsule branch deliberately skips `nvbootctrl set-active-boot-slot`
// (it expects UEFI to switch the boot chain when it processes the capsule), no
// capsule means the rootfs slot never moves either. The device reboots onto the
// old slot, the boot verifier detects `running != target`, marks the deployment
// failed, and the agent rolls back. Every OTA, forever.
//
// Fixed in wendyos-update cb2c7b5 with a Syncfs of the ESP, first shipped in
// WendyOS 0.18.1. That fix cannot deliver itself: an OTA is performed by the
// updater binary on the currently running slot, so an affected device always uses
// the broken one to perform its own upgrade. The agent is the way out, because
// the agent — not the updater — is what reboots.
//
// Applies to the tegrauefi connector (Jetson). The workaround is gated on OS
// version alone rather than also probing for a Jetson, because flushing before a
// reboot is correct everywhere and not worth a platform probe to avoid.
const capsuleDurabilityFixedIn = "0.18.1"
