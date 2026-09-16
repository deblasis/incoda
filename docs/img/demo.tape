Output "demo.gif"

Set FontSize 20
Set Width 1000
Set Height 580
Set Theme "Dracula"
Set Padding 24
Set BorderRadius 8
Set WindowBar Rings
Set Framerate 8
Set Shell zsh
Set TypingSpeed 35ms
Set CursorBlink false

Type "PROMPT='$ '; RPROMPT=''; export INCODA_DIR=/tmp/incoda-cli-demo/state; rm -rf /tmp/incoda-cli-demo/state; mkdir -p /tmp/incoda-cli-demo/state; clear" Enter
Sleep 400ms

Type "incoda config builds --description 'CPU-heavy builds'" Enter
Sleep 300ms

Type "incoda run --queue builds --owner feature-x --reason 'zig build (LLVM)' -- sleep 15 &" Enter
Sleep 700ms

Type "incoda run --queue builds --owner main --reason 'dotnet test suite' -- sleep 6 &" Enter
Sleep 900ms

Type "incoda status --queue builds" Enter
Sleep 2200ms

Type "kill -9 %1" Enter
Sleep 900ms
Sleep 2800ms

Type "incoda status --queue builds" Enter
Sleep 1800ms

Sleep 400ms
