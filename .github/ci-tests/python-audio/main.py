#!/usr/bin/env python3
"""Audio entitlement test: ALSA devices, and a PipeWire socket that is usable.

applyAudio grants three things a container can observe: the audio group, a
/dev/snd bind mount, and — when the host has a user PipeWire session — its
socket at /run/pipewire/pipewire-0 with PIPEWIRE_RUNTIME_DIR and PULSE_SERVER
set.

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

import glob
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
    print(f"OK  /dev/snd present ({len(os.listdir('/dev/snd'))} node(s))")
else:
    failures.append("/dev/snd is not present; the ALSA bind mount is missing")

# The granted group must be the one that owns the sound nodes.
#
# Comparing os.getgroups() against a hardcoded 29 only works while the host and
# this image happen to agree: the supplementary GIDs are the *host's*, whereas
# any name lookup in here resolves this image's /etc/group. Both are Debian-ish
# today, so both say 29 — but a host whose audio group differs would fail a test
# that is working correctly.
#
# The node's own st_gid is the host GID, seen through the same numeric space as
# getgroups(), so comparing the two needs no group database on either side and
# catches an agent that grants a GID which does not own this host's nodes.
#
# Opening the node would not test anything: the container runs as root with
# CAP_DAC_OVERRIDE, so the open succeeds whether or not the group was granted.
control_nodes = sorted(glob.glob("/dev/snd/controlC*"))
if control_nodes:
    node = control_nodes[0]
    node_gid = os.stat(node).st_gid
    groups = os.getgroups()
    if node_gid == 0:
        print(f"SKIP {node} is root-owned; no group gates it on this host")
    elif node_gid in groups:
        print(f"OK  {node} is owned by gid {node_gid}, which is granted to us")
    else:
        failures.append(
            f"{node} is owned by gid {node_gid}, which is not among our "
            f"supplementary groups {groups}; the audio group grant does not "
            f"match this host"
        )
else:
    # No sound card on this host (CI runners, headless VMs). The bind mount is
    # still asserted above; there is simply no node to check the grant against.
    print("SKIP no /dev/snd/controlC* node to verify the group grant against")

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
