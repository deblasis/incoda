Output "demo-watch.gif"

Require incoda

Set FontSize 18
Set Width 1050
Set Height 680
Set Theme "Dracula"
Set Padding 24
Set BorderRadius 8
Set WindowBar Rings
Set Framerate 8
Set Shell zsh
Set TypingSpeed 45ms
Set CursorBlink false

Type "PROMPT='$ '; RPROMPT=''; export INCODA_DIR=/tmp/incoda-watch-gif/state; rm -rf /tmp/incoda-watch-gif/state; mkdir -p /tmp/incoda-watch-gif/state; clear" Enter
Sleep 700ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# watch demo — three queues: busy, exclusive, closed'`
Enter
Type@80ms `echo ''`
Enter
Sleep 1500ms

Type "incoda config builds --slots 2 --description 'CPU-heavy builds and link steps'" Enter
Sleep 500ms
Type "incoda config gui-tests --description 'GUI and E2E runs that need the desktop'" Enter
Sleep 500ms
Type "incoda config legacy --close 'retired: use builds or gui-tests'" Enter
Sleep 500ms

Type "incoda run --queue builds --slots 1 --owner feature-x --reason 'zig build (LLVM)' -- sleep 120 &" Enter
Sleep 900ms
Type "incoda run --queue builds --owner main --reason 'dotnet test suite' -- sleep 120 &" Enter
Sleep 900ms
Type "incoda run --queue gui-tests --exclusive --owner desktop --reason 'playwright e2e' -- sleep 120 &" Enter
Sleep 1400ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# incoda watch — overview, drill-down, help'`
Enter
Type@80ms `echo ''`
Enter
Sleep 1200ms

Type "incoda watch" Enter
Wait@20s /builds/
Sleep 4.5s
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
