Output recordings/04-lifecycle.mp4
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false

Sleep 500ms
Type "export PATH=/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync-screencast/screencast/demo-bin:$PATH"
Enter
Sleep 300ms
Type "cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync-screencast/screencast/demo-project"
Enter
Sleep 300ms
Type "touch .wendy-demo-first-run"
Enter
Sleep 300ms
Type "printf 'Classify sensor events using the updated v2 prompt.\n' > prompts/system.txt"
Enter
Sleep 800ms
Type "wendy run --device lab-edge-01"
Enter
Sleep 8s
Type "wendy --device lab-edge-01 device apps remove --force sh.wendy.demo.filesync"
Enter
Sleep 5s
