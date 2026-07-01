# entitlements.tape — `wendy project entitlements add`, the entitlement picker.
#
# Documented step (becomes <step>-{light,dark}.png):
#   add  — the interactive "Select an entitlement to add" menu
#
# Runs inside a real project ($WENDY_SHOTS_RUN_DIR, default Examples/HelloPython)
# because the command loads wendy.json. It captures the picker, then aborts
# (Ctrl+C) BEFORE choosing anything — the write to wendy.json happens only after
# you pick a type and fill its fields, so the project file is never modified.
#
# No theme/size/output here — see lib/common.tape + render.sh.

# Enter the project dir (hidden so the capture opens on the command).
Hide
Type 'cd "$WENDY_SHOTS_RUN_DIR"'
Enter
Type "clear"
Enter
Show

Type "wendy project entitlements add"
Sleep 500ms
Enter

# Capture the entitlement picker, then bail out before selecting (no write).
Sleep 2s
Screenshot add.png
Sleep 500ms
Ctrl+C
Sleep 300ms
