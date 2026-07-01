# logs.tape — `wendy device logs --app <app>`, the streaming app log view.
#
# Documented step (becomes <step>-{light,dark}.png):
#   logs
#
# Scoped to a single app (the HelloPython example) so the capture shows clean
# application output rather than the agent's internal/transport logs. Read-only;
# we let a few lines stream, then Ctrl+C. No theme/size/output here — see
# lib/common.tape + render.sh.

Type "wendy device logs --app sh.wendy.examples.hellopython"
Sleep 500ms
Enter

# Let the app's log lines stream in, then stop the follow.
Sleep 5s
Ctrl+C
Sleep 500ms
Screenshot logs.png
Sleep 1s
