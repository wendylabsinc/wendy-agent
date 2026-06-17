Output recordings/02-config.mp4
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
Type "reset-filesync-demo"
Enter
Sleep 300ms
Type "cd /Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync-screencast/screencast/demo-project"
Enter
Sleep 400ms
Type "find . -maxdepth 3 -type f | sort"
Enter
Sleep 2s
Type "cat wendy.json"
Enter
Sleep 6s
Type "sed -n '1,140p' main.py"
Enter
Sleep 7s
