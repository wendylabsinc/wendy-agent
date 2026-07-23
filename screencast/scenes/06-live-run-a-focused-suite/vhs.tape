Output vhs.tape.mp4
Set Shell zsh
Set Width 1440
Set Height 900
Set FontSize 19
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
Set TypingSpeed 16ms

Type "cd ../../../swift"
Enter
Sleep 500ms

Type "export WENDY_E2E_AGENT_OS=Linux"
Enter
Type "export WENDY_E2E_RUN_ID=swift-e2e-tests.screencast.local.0001"
Enter

Type "bash Scripts/E2ETest.sh --output-dir ../Build/e2e-screencast --no-managed-agent --filter 'wendy info'"
Enter
Sleep 10s
