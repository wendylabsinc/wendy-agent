#!/usr/bin/env python3
# wendy stage-2 flasher driver.
#
# Runs NVIDIA bootburn's FlashImages() over our ADB transport, WITHOUT editing any
# bundled NVIDIA script. We import bootburn's modules and monkeypatch the
# device-touching pre-flash steps to no-ops, because our Go stage-1 already RCM-
# booted the device to the initrd-flash ADB gadget — and those steps shell out to
# the i386 flash tools (tegrarcm_v2 etc.) that do not run on macOS arm64.
# bootburn's actual partition flashing (FlashImages) runs unmodified; on the host
# it only invokes `adb`, which thor-flash points at our wendy-adb shim via PATH.
#
# Run from the bundle's .../unified_flash/tools/flashtools/bootburn directory, the
# same way flash_bsp_images.py is normally run; pass the usual bootburn flash args
# (e.g. -b jetson-t264 --l4t -P <flash_workspace>).
#
# Note: class/method names below match the T264 BSP inspected (bootburn_t264_py).
# If a different BSP changes them, adjust the monkeypatch targets accordingly.

import sys
import os
import importlib

here = os.getcwd()
sys.path.insert(0, here)                    # bootburn/  -> select_socgrp
sys.path.insert(0, os.path.dirname(here))   # flashtools/ -> bootburn_<soc>_py package

from select_socgrp import select_socgrp

soc = select_socgrp()
if not soc.isSocGrpFound():
    print("wendy stage2: SOC group not found (run from flashtools/bootburn)", file=sys.stderr)
    sys.exit(1)
soc_dir = soc.soc_scripts_dir

lib = importlib.import_module(f"{soc_dir}.bootburn_lib")
thor = importlib.import_module(f"{soc_dir}.bootburn_thor")


def _noop(self, *args, **kwargs):
    return 0


# Our Go stage-1 already booted the device; skip bootburn's own boot/probe (it uses
# i386 host tools). FlashImages() — the real adb-driven flashing — is left intact.
lib.bootburn_lib.StartNewSession_t264 = _noop
lib.bootburn_lib.GetTargetECID = _noop
lib.bootburn_lib.CheckFuseForAuthentication = _noop
lib.bootburn_lib.CheckFuseForEncryption = _noop
thor.bootburn_thor.BootRCM = _noop
thor.bootburn_thor.runPlatformDetection = _noop


# Pin the adb serial wendy-adb reports, so `adb -s <serial>` / `adb devices` match.
def _gen_serial(self, *args, **kwargs):
    self.targetConfig.s_AdbSerialNum = "wendythor"


lib.bootburn_lib.GenAdbSerialNum = _gen_serial

flash_bsp_images = importlib.import_module(f"{soc_dir}.flash_bsp_images")
sys.exit(flash_bsp_images.flash_bsp(sys.argv))
