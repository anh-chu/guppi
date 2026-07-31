//go:build linux

package wikilite

import "syscall"

// childSysProcAttr configures the wiki-viewer-lite child process group.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
}
