# apps-list.tape — `wendy device apps list`, the deployed-app table.
#
# Documented step (becomes <step>-{light,dark}.png):
#   apps
#
# Read-only: just queries the device for its running apps. No theme/size/output
# here — see lib/common.tape + render.sh.

Type "wendy device apps list"
Sleep 500ms
Enter

# Let the device respond with its app table.
Sleep 4s
Screenshot apps.png
Sleep 1s
