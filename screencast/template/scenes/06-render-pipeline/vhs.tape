Output vhs.tape.mp4
Set Shell zsh
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "Geist Mono, monospace"
Set Theme { "name": "Wendy Dark", "black": "#171C23", "red": "#F1EEE7", "green": "#9FE2BF", "yellow": "#E6E2D8", "blue": "#CBCBCB", "magenta": "#DEDEDE", "cyan": "#86D3A8", "white": "#F1EEE7", "brightBlack": "#5B5A56", "brightRed": "#F1EEE7", "brightGreen": "#9FE2BF", "brightYellow": "#E6E2D8", "brightBlue": "#CBCBCB", "brightMagenta": "#DEDEDE", "brightCyan": "#9FE2BF", "brightWhite": "#F1EEE7", "background": "#171C23", "foreground": "#F1EEE7", "selection": "#242B35", "cursor": "#9FE2BF" }
Set Framerate 10
Set CursorBlink false

Hide
Type "export PS1='$ '"
Enter
Type "clear"
Enter
Show

Sleep 1s

Type "cat > /tmp/screencast-render.sh <<'COMMANDS'"
Enter
Sleep 300ms

Type "cd screencast"
Enter
Sleep 250ms

Type "scripts/render-slide 01"
Enter
Sleep 250ms

Type "scripts/render-voice 01"
Enter
Sleep 250ms

Type "scripts/render-tape --dry-run --with-hooks 06"
Enter
Sleep 250ms

Type "scripts/render-tape --with-hooks 06"
Enter
Sleep 250ms

Type "scripts/stitch scenes/* --output output/feature-name.mp4"
Enter
Sleep 250ms

Type "COMMANDS"
Enter
Sleep 500ms

Type "bash -n /tmp/screencast-render.sh && echo 'verified before recording'"
Enter
Sleep 2s
