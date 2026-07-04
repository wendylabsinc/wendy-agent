# wifi-connect.tape — `wendy device wifi`, the interactive WiFi TUI.
#
# Documented step (becomes <step>-{light,dark}.png):
#   networks  — the network table, FILTERED to "Wendy" so neighbouring SSIDs
#               aren't shown (sanitised for public docs)
#
# This tape never commits a real connection: it just filters the list and
# quits, so no device WiFi state is changed. No theme/size/output here — see
# lib/common.tape + render.sh.
#
# Keys (from the TUI footer): ↑/↓ move · enter connect · / filter · n new · q quit

# Hide the launch + filtering from the animated clip so the unfiltered table
# (with neighbouring SSIDs) is never recorded — only the filtered view is shown.
Hide
Type "wendy device wifi"
Sleep 500ms
Enter

# Let the network table stream in, then filter to our own networks so
# neighbouring SSIDs stay out of both the still and the clip.
Sleep 4s
Type "/"
Sleep 400ms
Type "Wendy"
Sleep 800ms
Show

Sleep 800ms
Screenshot networks.png
Sleep 800ms

# Bail out without connecting to anything.
Escape
Sleep 300ms
Type "q"
Sleep 500ms
