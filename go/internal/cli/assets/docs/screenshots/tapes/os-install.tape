# os-install.tape — `wendy os install`, the OS-flashing wizard.
#
# Documented step (becomes <step>-{light,dark}.png):
#   select-device-type
#
# ⚠ SAFETY: `wendy os install` downloads an OS image and writes it to a drive —
# destructive and irreversible. This tape captures ONLY the first interactive
# screen (the device-type picker) and then quits (Ctrl+C) BEFORE any image
# download or drive write. It never reaches the confirmation gate, so nothing
# is ever flashed. Do not add keystrokes that advance past the picker.
#
# No theme/size/output here — see lib/common.tape + render.sh.

Type "wendy os install"
Sleep 500ms
Enter

# Wait for the device-type list to fetch, capture the picker, then bail out
# before any image download or drive write.
Sleep 6s
Screenshot select-device-type.png
Sleep 500ms
Ctrl+C
