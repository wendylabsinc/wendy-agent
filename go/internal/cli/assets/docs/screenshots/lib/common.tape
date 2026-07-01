# common.tape — shared visual chrome for every Wendy CLI screenshot.
#
# Single source of truth for font, size, padding, window chrome, and shell.
# It contains NO theme, output, or per-flow keystrokes: render.sh injects the
# theme + output and then Sources the per-flow tape AFTER this file. Keeping all
# styling here is what guarantees every capture shares identical dimensions,
# font, padding, and chrome (see success criterion #2 in the design).

# Fixed terminal + frame dimensions → every capture is identically sized.
# FontFamily is the CSS generic "monospace" → each machine's native monospace
# (Menlo on macOS, etc.). No font to install, and it avoids the broken letter
# spacing VHS's bundled fallback produces when a named font is missing.
Set Width 1200
Set Height 720
Set FontSize 18
Set FontFamily "monospace"
Set Padding 30
Set Margin 24

# Square corners, matching the docs site (its CSS forces border-radius: 0
# everywhere). MarginFill is set per-theme by render.sh so the window blends
# into the page.
#
# macOS-style traffic-light title bar is disabled for now. To re-enable,
# uncomment both lines (WindowBarSize 28 keeps the dots right-sized; default 40
# is too chunky):
# Set WindowBar Colorful
# Set WindowBarSize 28
Set BorderRadius 0

# Deterministic shell + typing cadence. CursorBlink off so a still grabbed at
# any moment looks the same (no half-blinked cursor).
Set Shell "bash"
Set TypingSpeed 90ms
Set CursorBlink false

# Clean, uniform prompt: a bare "$ " with no host/path noise, on a cleared
# screen. Hidden from the recording so every flow opens on an empty prompt
# rather than on this setup.
Hide
Type "export PS1='$ '"
Enter
Type "clear"
Enter
Show
