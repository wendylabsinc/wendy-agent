Output vhs.tape.mp4
Set Shell zsh
Set Width 1440
Set Height 900
Set FontSize 20
Set FontFamily "JetBrains Mono, JetBrainsMono, JetBrainsMono Nerd Font, JetBrainsMono Nerd Font Mono, monospace"
Set Framerate 10
Set CursorBlink false
Set TypingSpeed 16ms

Type "cd ../../../swift"
Enter
Sleep 400ms

Type "rm -rf Build/Reference && mkdir -p Build/Reference"
Enter
Type "cd WendyE2ETests"
Enter

Type "swift run swift-e2e-testing reference --format html --output ../Build/Reference Tests/WendyE2ETests"
Enter
Sleep 6s

Type "printf 'Generated pages: ' && find ../Build/Reference -maxdepth 1 -name '*.html' | wc -l"
Enter
Sleep 2s

Type "ls ../Build/Reference | head"
Enter
Sleep 4s
