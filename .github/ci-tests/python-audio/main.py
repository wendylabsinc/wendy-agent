#!/usr/bin/env python3
"""Audio entitlement test: ALSA devices, and a PipeWire socket that is usable.

applyAudio grants three things a container can observe: the audio group, a
/dev/snd bind mount, and — when the host has a PipeWire instance — its socket
at /run/pipewire/pipewire-0 with PIPEWIRE_RUNTIME_DIR and PULSE_SERVER set.

Two things that look like audio access are not evidence of it, and must not be
asserted on:

  - /sys/class/sound/* is visible in every container, entitled or not, because
    /sys is a plain read-only sysfs mount.
  - Actual playback needs hardware and a speaker, neither of which CI has.

The interesting assertion is the last one. The socket and PULSE_SERVER are both
derived from the same source directory, so a mounted socket without
PULSE_SERVER means the agent picked an instance with no PulseAudio-compatible
socket beside it. That is exactly what the system-wide pipewire.service looks
like: it always has a socket, but no WirePlumber and no pulse/ sibling, so a
container bound to it sees an empty graph and silently falls back to raw ALSA —
which cannot reach a Bluetooth speaker at all. Asserting the pairing catches
that regression without needing a sound card.

A host with no PipeWire at all mounts no socket; that is not a failure, so the
paired assertions are skipped rather than failed.
"""

import os
import stat
import sys

SOCKET = "/run/pipewire/pipewire-0"
PULSE_SOCKET = "/run/pipewire/pulse-native"

failures = []


def is_socket(path):
    try:
        return stat.S_ISSOCK(os.stat(path).st_mode)
    except OSError:
        return False


# /dev/snd is bind-mounted unconditionally by the entitlement.
if os.path.isdir("/dev/snd"):
    nodes = sorted(os.listdir("/dev/snd"))
    print(f"OK  /dev/snd present ({len(nodes)} node(s))")
else:
    failures.append("/dev/snd is not present; the ALSA bind mount is missing")

# The audio group must be among our supplementary groups.
if 29 in os.getgroups():
    print("OK  audio group (29) granted")
else:
    failures.append(f"audio group (29) not in supplementary groups {os.getgroups()}")

if is_socket(SOCKET):
    print(f"OK  {SOCKET} is mounted and is a socket")

    runtime_dir = os.environ.get("PIPEWIRE_RUNTIME_DIR")
    if runtime_dir == "/run/pipewire":
        print("OK  PIPEWIRE_RUNTIME_DIR=/run/pipewire")
    else:
        failures.append(f"PIPEWIRE_RUNTIME_DIR is {runtime_dir!r}, want '/run/pipewire'")

    # The regression this pairing catches: a socket taken from an instance with
    # no pulse/ sibling, i.e. one with no session manager behind it.
    pulse_server = os.environ.get("PULSE_SERVER")
    if not pulse_server:
        failures.append(
            "PULSE_SERVER is unset while a PipeWire socket is mounted; the agent "
            "chose an instance with no PulseAudio socket beside it (an empty graph)"
        )
    elif pulse_server != f"unix:{PULSE_SOCKET}":
        failures.append(f"PULSE_SERVER is {pulse_server!r}, want 'unix:{PULSE_SOCKET}'")
    elif not is_socket(PULSE_SOCKET):
        failures.append(f"PULSE_SERVER points at {PULSE_SOCKET}, which is not a socket")
    else:
        print(f"OK  PULSE_SERVER=unix:{PULSE_SOCKET} and the socket is mounted")
else:
    # No PipeWire on the host: ALSA-only access is all the entitlement can give.
    print(f"SKIP {SOCKET} not mounted; host has no PipeWire instance")

if failures:
    print("\nFAIL: audio entitlement did not grant usable audio access:")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)

print("\nPASS: audio entitlement grants ALSA devices and a usable PipeWire socket")
sys.exit(0)
