Output deck/public/videos/file-sync/07-developer-flow.mp4
Set Width 1440
Set Height 900
Set FontSize 21
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
Set TypingSpeed 25ms
Set Shell bash

Hide
Type "/Volumes/Projects/WendyLabs/wendy-agent/.worktrees/kb.wdy-1532-file-sync-screencast/screencast/scripts/prepare-filesync-vhs.sh >/dev/null"
Enter
Type "source /tmp/wendy-filesync-vhs/env"
Enter
Type "export PS1='wendy-demo % '"
Enter
Type "clear"
Enter
Show
Sleep 500ms

Type "cd Examples/HelloFileSync"
Enter
Sleep 300ms
Type "find . -maxdepth 3 -type f | sort"
Enter
Sleep 1700ms

Ctrl+L
Sleep 400ms
Type "cat .dockerignore"
Enter
Sleep 900ms
Type "jq '.files' wendy.json"
Enter
Sleep 2200ms

Ctrl+L
Sleep 400ms
Type "wendy discover"
Enter
Sleep 1200ms
Type "wendy run --device wendyos-jetson-orin-nano.local"
Enter
Sleep 4300ms

Ctrl+L
Sleep 400ms
Type "printf 'classifier-model-v2\npretend this classifier changed independently\n' > models/classifier/model.txt"
Enter
Sleep 500ms
Type "wendy run --device wendyos-jetson-orin-nano.local"
Enter
Sleep 4300ms

Ctrl+L
Sleep 400ms
Type "echo 'Only the classifier asset changed; app code and other synced files stayed independent.'"
Enter
Sleep 2200ms
