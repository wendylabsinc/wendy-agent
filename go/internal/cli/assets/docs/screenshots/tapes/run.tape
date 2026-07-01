# run.tape — `wendy run`, building and deploying an app to the device.
#
# Documented step (becomes <step>-{light,dark}.png):
#   deploy
#
# Runs inside the project directory given by $WENDY_SHOTS_RUN_DIR, which
# render.sh defaults to the repo's Examples/HelloPython (fast to build). The
# command builds the image, pushes/diffs layers to the device, starts the app,
# then attaches to its logs — we capture that and stop the attached session.
#
# No theme/size/output here — see lib/common.tape + render.sh.

# Enter the project dir (hidden from the capture so it opens on the command).
Hide
Type 'cd "$WENDY_SHOTS_RUN_DIR"'
Enter
Type "clear"
Enter
Show

Type "wendy run"
Sleep 500ms
Enter

# Build → push/diff layers → start → "ready". Cached builds finish in seconds;
# allow margin for a cold build and the readiness probe.
Sleep 14s
Screenshot deploy.png
Sleep 500ms

# Stop the attached log session.
Ctrl+C
Sleep 500ms
