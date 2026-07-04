# discover.tape — the `wendy discover` device-listing flow.
#
# Documented step (becomes <step>-{light,dark}.png):
#   devices
#
# No theme/size/output/shell here — see lib/common.tape + render.sh.
#
# NOTE: discovery (mDNS + USB) can take a few seconds to populate the table;
# bump the Sleep below if the capture lands before your device appears.

Type "wendy discover"
Sleep 500ms
Enter

# Let mDNS/USB discovery populate the device table.
Sleep 5s
Screenshot devices.png
Sleep 1s
