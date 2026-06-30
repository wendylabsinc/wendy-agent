# init.tape — `wendy init`, the new-project scaffolding wizard.
#
# Documented step (becomes <step>-{light,dark}.png):
#   wizard
#
# Self-contained: runs in render.sh's throwaway temp working dir, and we abort
# the wizard (Ctrl+C) right after its first screen, so no project files are
# actually written. No theme/size/output here — see lib/common.tape + render.sh.

Type "wendy init"
Sleep 500ms
Enter

# Let the wizard's first prompt render, capture it, then bail before it writes.
Sleep 2s
Screenshot wizard.png
Sleep 500ms
Ctrl+C
