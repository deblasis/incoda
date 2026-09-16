Output "demo-watch.gif"

Require incoda

Set FontSize 18
Set Width 1050
Set Height 680
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
Env INCODA_DIR "/tmp/incoda-watch-gif/state"

Hide
Type "PROMPT='$ '; RPROMPT=''; rm -rf /tmp/incoda-watch-gif/state; mkdir -p /tmp/incoda-watch-gif/state" Enter
Type "incoda config builds --slots 2 --description 'CPU-heavy builds and link steps'" Enter
Type "incoda config gui-tests --description 'GUI and E2E runs that need the desktop'" Enter
Type "incoda config legacy --close 'retired: use builds or gui-tests'" Enter
Type "incoda run --queue builds --slots 1 --owner feature-x --reason 'zig build (LLVM)' -- sleep 120 &" Enter
Sleep 700ms
Type "incoda run --queue builds --owner main --reason 'dotnet test suite' -- sleep 120 &" Enter
Sleep 700ms
Type "incoda run --queue gui-tests --exclusive --owner desktop --reason 'playwright e2e' -- sleep 120 &" Enter
Sleep 1500ms
Show

Type "clear" Enter
Sleep 900ms

Type "incoda watch" Enter
Wait@20s /builds/
Sleep 5s
Enter
Sleep 4s
Escape
Sleep 2s
Type "?"
Sleep 3s
Type "?"
Sleep 800ms
Type "q"
Sleep 600ms
