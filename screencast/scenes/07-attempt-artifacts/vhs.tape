Output vhs.tape.mp4
Set Shell zsh
Set Width 1440
Set Height 900
Set FontSize 18
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
Set TypingSpeed 12ms

Type "cd ../../.."
Enter
Sleep 400ms

Type "find Build/e2e-screencast/swift-e2e-tests.screencast.local.0001 -type f | sed 's|Build/e2e-screencast/swift-e2e-tests.screencast.local.0001/||' | sort | head -18"
Enter
Wait
Sleep 2s

Type "grep -E '^- Command:|^- Termination status:|^### stdout|^Wendy CLI|^  (Version|OS|Arch|Go Version):' Build/e2e-screencast/swift-e2e-tests.screencast.local.0001/observations/wendy-info/prints-cli-and-system-information/recording.md"
Enter
Wait
Sleep 3s
