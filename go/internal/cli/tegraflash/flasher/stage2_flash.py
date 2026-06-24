#!/usr/bin/env python3
# wendy stage-2 flasher driver.
#
# Runs NVIDIA bootburn's FlashImages() over our ADB transport, WITHOUT editing any
# bundled NVIDIA script. We import bootburn's modules and monkeypatch the
# device-touching pre-flash steps to no-ops, because our Go stage-1 already RCM-
# booted the device to the initrd-flash ADB gadget — and those steps shell out to
# the i386 flash tools (tegrarcm_v2 etc.) that do not run on macOS arm64.
# bootburn's actual partition flashing (FlashImages) runs unmodified; on the host
# it only invokes `adb`, which thor-flash points at our wendy-shim via PATH/the bundle adb swap.
#
# Run from the bundle's .../unified_flash/tools/flashtools/bootburn directory, the
# same way flash_bsp_images.py is normally run; pass the usual bootburn flash args
# (e.g. -b jetson-t264 --l4t -P <flash_workspace>).
#
# Note: class/method names below match the T264 BSP inspected (bootburn_t264_py).
# If a different BSP changes them, adjust the monkeypatch targets accordingly.

import sys
import os

here = os.getcwd()                          # .../flashtools/bootburn
flashtools = os.path.dirname(here)
sys.path.insert(0, here)                    # bootburn/  -> select_socgrp

from select_socgrp import select_socgrp

soc = select_socgrp()
if not soc.isSocGrpFound():
    print("wendy stage2: SOC group not found (run from flashtools/bootburn)", file=sys.stderr)
    sys.exit(1)

# Put the SOC scripts dir first on sys.path and import its modules by their TOP-LEVEL
# names. This matters: flash_bsp_images.py uses `import bootburn_lib` /
# `from bootburn_thor import ...` (top-level), so we must patch the same module
# objects it resolves — not the package-qualified bootburn_<soc>_py.bootburn_lib,
# which is a distinct entry in sys.modules and would leave the real methods in place.
sys.path.insert(0, os.path.join(flashtools, soc.soc_scripts_dir))

import bootburn_lib
import bootburn_thor
import flash_utilities


def _noop(self, *args, **kwargs):
    return 0


# CheckUSBServiceInit greps /etc/udev/rules.d for a 0955 udev rule (informational on
# a Linux host). That folder doesn't exist on macOS, and grepInFolder aborts on a
# missing path. Treat a missing folder as "no matches" so it doesn't abort; the
# result is unused by the caller.
_orig_grep_in_folder = flash_utilities.shell_utilities.grepInFolder


def _grep_in_folder_safe(self, path, searchString, printOut=False):
    if not os.path.isdir(path):
        return []
    return _orig_grep_in_folder(self, path, searchString, printOut)


flash_utilities.shell_utilities.grepInFolder = _grep_in_folder_safe


# Our Go stage-1 already booted the device; skip bootburn's own boot/probe (it uses
# i386 host tools and expects a recovery-mode device on the bus). FlashImages() —
# the real adb-driven flashing — is left intact.
bootburn_lib.bootburn_lib.CheckRecoveryTargets = _noop  # looks for a 0955: recovery device via lsusb
bootburn_lib.bootburn_lib.SetUsbAutoSuspend = _noop      # host USB power management, not applicable
bootburn_lib.bootburn_lib.StartNewSession_t264 = _noop
bootburn_lib.bootburn_lib.GetTargetECID = _noop
bootburn_lib.bootburn_lib.CheckFuseForAuthentication = _noop
bootburn_lib.bootburn_lib.CheckFuseForEncryption = _noop
# These classify the board (INT/PROD) and validate options against device-read state
# (chip id / fuses) that only the skipped session/ECID steps would populate. They
# affect image *generation* (already done on the Linux builder), not the adb push of
# the pre-built FileToFlash.txt, so they are not needed here.
bootburn_lib.bootburn_lib.CheckForDeviceType = _noop
bootburn_lib.bootburn_lib.ValidateUserOptionAndDeviceType = _noop
bootburn_lib.bootburn_lib.ValidateFindBoardForFused = _noop
bootburn_thor.bootburn_thor.BootRCM = _noop
bootburn_thor.bootburn_thor.runPlatformDetection = _noop


# Pin the adb serial wendy-shim reports, so `adb -s <serial>` / `adb devices` match.
def _gen_serial(self, *args, **kwargs):
    self.targetConfig.s_AdbSerialNum = "wendythor"


bootburn_lib.bootburn_lib.GenAdbSerialNum = _gen_serial

import flash_bsp_images
sys.exit(flash_bsp_images.flash_bsp(sys.argv))
