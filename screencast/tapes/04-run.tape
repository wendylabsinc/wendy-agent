Output recordings/04-run.mp4
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
Set TypingSpeed 28ms

Hide
Type "export PATH=/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync-screencast/screencast/demo-bin:$PATH"
Enter
Type "reset-filesync-demo"
Enter
Type "cd demo"
Enter
Type "clear"
Enter
Show
Sleep 500ms
Type "wendy run --device lab-edge-01"
Enter
Sleep 8s
Type "printf 'Classify sensor events using the updated v2 prompt.\n' > prompts/system.txt"
Enter
Sleep 700ms
Type "wendy run --device lab-edge-01"
Enter
Sleep 8s
