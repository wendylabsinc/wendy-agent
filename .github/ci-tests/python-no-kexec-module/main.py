#!/usr/bin/env python3
"""Negative test: verify the kernel-module and kexec syscalls are blocked by the
default seccomp profile (WDY-1012).

The baseline seccomp profile denies init_module, finit_module, delete_module,
create_module, kexec_load, and kexec_file_load with EPERM — these are pure
host-escape primitives a normal application container never needs. A successful
call (return 0) means the filter was not applied and is a hard failure.

create_module is deliberately omitted: it was removed from Linux long ago and
has no syscall number on arm64, so it can't be exercised portably."""

import ctypes
import ctypes.util
import platform
import sys

# Syscall numbers per architecture. WendyOS devices are arm64; x86_64 is
# included so the test is meaningful on dev machines / amd64 runners too.
SYSCALLS = {
    "aarch64": {
        "init_module": 105,
        "finit_module": 273,
        "delete_module": 106,
        "kexec_load": 104,
        "kexec_file_load": 294,
    },
    "x86_64": {
        "init_module": 175,
        "finit_module": 313,
        "delete_module": 176,
        "kexec_load": 246,
        "kexec_file_load": 320,
    },
}

arch = platform.machine()
table = SYSCALLS.get(arch)
if table is None:
    print(f"FAIL: unsupported architecture {arch!r}; cannot resolve syscall numbers")
    sys.exit(1)

libc = ctypes.CDLL(ctypes.util.find_library("c") or "libc.so.6", use_errno=True)

failures = []
for name, nr in table.items():
    ctypes.set_errno(0)
    # Benign/invalid args: if the syscall were permitted it would still fail
    # (EBADF/EFAULT/EPERM from the capability check), so a -1 return is expected.
    # The only unacceptable outcome is a successful (0) call.
    ret = libc.syscall(nr, 0, 0, 0, 0, 0)
    err = ctypes.get_errno()
    if ret == 0:
        failures.append(f"{name} (nr {nr}) SUCCEEDED — seccomp profile not applied")
    else:
        print(f"  {name} (nr {nr}) blocked: ret={ret} errno={err}")

if failures:
    for f in failures:
        print(f"FAIL: {f}")
    sys.exit(1)

print(f"PASS: all kernel-module / kexec syscalls blocked on {arch}")
sys.exit(0)
