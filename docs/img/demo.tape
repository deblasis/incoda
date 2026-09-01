Output "demo.gif"

Set FontSize 20
Set Width 1150
Set Height 640
Set Theme "Dracula"
Set Padding 24
Set BorderRadius 8
Set WindowBar Rings
Set Framerate 15
Set Shell zsh
Set TypingSpeed 40ms
Set CursorBlink false

# neutral prompt, no hostnames
Type "PROMPT='$ '; RPROMPT=''; export PATH=/tmp/incoda-demo/bin:$PATH; cd /tmp/incoda-demo && clear" Enter
Sleep 600ms

# 1. a heavy build takes the lane (backgrounded, keeps running)
Type "incoda run --queue builds --reason 'zig build (LLVM)' sleep 30 &" Enter
Sleep 1200ms

# 2. a second job arrives: it queues, and says so
Type "incoda run --queue builds --reason 'dotnet test suite' sleep 5 &" Enter
Sleep 1500ms

# 3. full visibility while both are in flight
Type "incoda status --queue builds" Enter
Sleep 4200ms

# 4. the holder dies hard: the kernel frees the lane, the waiter takes over
Type "kill -9 %1" Enter
Sleep 1800ms
Sleep 5000ms

# 5. and when everyone is done, the queue is simply free
Type "incoda status --queue builds" Enter
Sleep 3000ms

Sleep 800ms
