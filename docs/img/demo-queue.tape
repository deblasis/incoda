Output "demo-queue.gif"

Require incoda

Set FontSize 18
Set Width 1050
Set Height 620
Set Theme "Dracula"
Set Padding 24
Set BorderRadius 8
Set WindowBar Rings
Set Framerate 8
Set Shell zsh
Set TypingSpeed 45ms
Set CursorBlink false

Type "PROMPT='$ '; RPROMPT=''; export INCODA_DIR=/tmp/incoda-cli-demo/state; rm -rf /tmp/incoda-cli-demo/state; mkdir -p /tmp/incoda-cli-demo/state; clear" Enter
Sleep 700ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# queue demo — builds allows 2 heavy jobs at once (--slots 2)'`
Enter
Type@80ms `echo ''`
Enter
Sleep 1800ms

Type "incoda config builds --slots 2 --description 'CPU-heavy builds'" Enter
Sleep 1200ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# job 1 and 2 take both slots — they run in parallel'`
Enter
Type@80ms `echo ''`
Enter
Sleep 900ms

Type "incoda run --queue builds --owner feature-x --reason 'zig build (LLVM)' -- sleep 120 &" Enter
Sleep 1400ms

Type "incoda run --queue builds --owner main --reason 'cargo build' -- sleep 120 &" Enter
Sleep 1600ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# job 3 waits — queue is full, FIFO order'`
Enter
Type@80ms `echo ''`
Enter
Sleep 900ms

Type "incoda run --queue builds --owner agents --reason 'dotnet test suite' -- sleep 60 &" Enter
Sleep 1800ms

Type "incoda status --queue builds" Enter
Sleep 5s

Type@80ms `echo ''`
Enter
Type@80ms `echo '# kill -9 the first holder — kernel frees the lock'`
Enter
Type@80ms `echo ''`
Enter
Sleep 1200ms

Type "kill -9 %1" Enter
Sleep 2500ms

Type@80ms `echo ''`
Enter
Type@80ms `echo '# the waiter acquires — no stale pid file'`
Enter
Type@80ms `echo ''`
Enter
Sleep 1200ms

Type "incoda status --queue builds" Enter
Sleep 5s
