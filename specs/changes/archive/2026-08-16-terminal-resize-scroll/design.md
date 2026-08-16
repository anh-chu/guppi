# Design

`onWheel` will record the gesture timestamp but will not set `entry.userScrolled`. The existing viewport `scroll` listener already receives the resulting xterm movement and promotes the entry to user-scrolled only when the viewport is actually off the bottom. This prevents wheel events that do not move the viewport from poisoning resize anchor selection.

No xterm internals or PTY resize protocol changes are needed. The existing fit path will continue to preserve a real user anchor and pin follow-output terminals to the bottom.

Alternative rejected: infer scroll ownership from buffer geometry during fit. xterm updates viewport geometry asynchronously during writes and reflow, so gesture plus observed movement is the safer existing boundary.
