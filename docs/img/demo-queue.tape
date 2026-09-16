Output "demo-queue.gif"
Output "demo-queue.mp4"

Require incoda

Set FontSize 15
Set Width 1050
Set Height 820
Set Theme "Catppuccin Mocha"
Set Padding 24
Set BorderRadius 8
Set WindowBar Colorful
Set Framerate 12
Set Shell zsh
Set TypingSpeed 50ms
Set CursorBlink false

Env TERM "xterm-256color"
Env CLICOLOR_FORCE "1"
Env INCODA_DIR "/tmp/incoda-cli-demo/state"

Type "PROMPT='$ '; RPROMPT=''; clear" Enter
Sleep 900ms

Type "incoda watch" Enter
Wait@20s /feature-x/
Sleep 4.5s
Enter
Sleep 4s
Escape
Sleep 1s
Type "q"
Sleep 800ms

Type "kill -9 $(cat /tmp/incoda-cli-demo/state/.holder.pid)" Enter
Sleep 2800ms

Type "incoda watch" Enter
Wait@20s /agents/
Sleep 4.5s
Type "q"
Sleep 600ms
