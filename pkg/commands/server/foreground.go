package server

// ForegroundProvider returns the foreground command running under a shell
// process. On unsupported platforms it returns false for every query.
type ForegroundProvider interface {
	Foreground(shellPid int) (command string, ok bool)
}

// shellNames lists foreground commands treated as "no meaningful process".
var shellNames = map[string]bool{
	"bash": true, "zsh": true, "sh": true, "fish": true,
	"-bash": true, "-zsh": true, "-sh": true, "login": true,
}

// trivialCmds are short-lived navigation/inspection commands that should never
// drive a session rename on their own -- they say nothing durable about the
// session's purpose.
var trivialCmds = map[string]bool{
	"ls": true, "cd": true, "pwd": true, "cat": true, "less": true,
	"more": true, "man": true, "clear": true, "echo": true, "which": true,
	"sleep": true, "watch": true, "top": true, "htop": true, "ps": true,
	"history": true, "env": true, "export": true, "head": true, "tail": true,
	"touch": true, "mkdir": true, "rm": true, "cp": true, "mv": true,
}
