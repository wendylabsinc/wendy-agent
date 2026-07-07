#!/bin/sh
# Reload udev so the Jetson rules (70-wendy-jetson.rules) apply without a reboot.
# Best-effort (|| true): containers, chroots and minimal systems have no running
# udev, and package installation must not fail there. The trigger is scoped to
# NVIDIA (0955) devices so unrelated USB devices are not re-processed.
if command -v udevadm >/dev/null 2>&1; then
    udevadm control --reload-rules 2>/dev/null || true
    udevadm trigger --subsystem-match=usb --attr-match=idVendor=0955 2>/dev/null || true
fi
# An /etc rule with the same filename overrides the packaged /usr/lib one (e.g.
# from following the wendy CLI's hint before installing the package); warn if it
# has diverged so a stale copy doesn't mask future packaged updates.
etc_rule=/etc/udev/rules.d/70-wendy-jetson.rules
usr_rule=/usr/lib/udev/rules.d/70-wendy-jetson.rules
if [ -f "$etc_rule" ] && ! cmp -s "$etc_rule" "$usr_rule"; then
    echo "wendy: $etc_rule differs from the packaged $usr_rule and overrides it;" >&2
    echo "wendy: consider removing the /etc copy (sudo rm $etc_rule) to use the packaged rule." >&2
fi
exit 0
