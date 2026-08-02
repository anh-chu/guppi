//go:build linux

package server

import (
	"fmt"
	"os"
	"strings"
)

// newForegroundProvider returns the Linux /proc-based foreground provider.
func newForegroundProvider() ForegroundProvider {
	return linuxForegroundProvider{}
}

type linuxForegroundProvider struct{}

// Foreground returns the name of the foreground process running under the
// given shell PID, or false if the shell has no children / is idle.
func (linuxForegroundProvider) Foreground(shellPid int) (string, bool) {
	childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", shellPid, shellPid)
	data, err := os.ReadFile(childrenPath)
	if err != nil {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) == 0 {
		return "", false // shell is idle, no foreground process
	}
	// Use the first child (most likely the foreground process).
	childPid := fields[0]
	commPath := fmt.Sprintf("/proc/%s/comm", childPid)
	comm, err := os.ReadFile(commPath)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(comm)), true
}
