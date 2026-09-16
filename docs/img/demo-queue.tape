Output "demo-queue.gif"

Require incoda

Set FontSize 18
Set Width 1050
Set Height 620
Set Theme "Catppuccin Mocha"
Set Padding 24
Set BorderRadius 8
Set WindowBar Rings
Set Framerate 10
Set Shell zsh
Set TypingSpeed 50ms
Set CursorBlink false

Env TERM "xterm-256color"
Env CLICOLOR_FORCE "1"
Env INCODA_DIR "/tmp/incoda-cli-demo/state"

Hide
Type "PROMPT='$ '; RPROMPT=''; rm -rf /tmp/incoda-cli-demo/state; mkdir -p /tmp/incoda-cli-demo/state" Enter
Type "incoda config builds --slots 2 --description 'CPU-heavy builds'" Enter
Type "incoda run --queue builds --owner feature-x --reason 'zig build (LLVM)' -- sleep 120 &" Enter
Sleep 800ms
Type "incoda run --queue builds --owner main --reason 'cargo build' -- sleep 120 &" Enter
Sleep 800ms
Type "incoda run --queue builds --owner agents --reason 'dotnet test suite' -- sleep 60 &" Enter
Sleep 1500ms
Show

Type "clear" Enter
Sleep 900ms

Type "incoda status --queue builds" Enter
Sleep 5s

Type "kill -9 %1" Enter
Sleep 2800ms

Type "incoda status --queue builds" Enter
Sleep 5s
