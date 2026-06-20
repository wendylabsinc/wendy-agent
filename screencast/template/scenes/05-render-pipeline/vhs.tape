Output vhs.tape.mp4
Set Shell zsh
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false

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

Type "scripts/render-tape --dry-run --with-hooks 02"
Enter
Sleep 250ms

Type "scripts/render-tape --with-hooks 02"
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
