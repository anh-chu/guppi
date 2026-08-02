//go:build darwin

package server

import (
	"os/exec"
	"strconv"
	"strings"
)

// newForegroundProvider returns the Darwin ps-snapshot-based foreground provider.
func newForegroundProvider() ForegroundProvider {
	return darwinForegroundProvider{}
}

type darwinForegroundProvider struct{}

type darwinProc struct {
	ppid int
	comm string
}

// Foreground returns the command name of the first direct child of the given
// shell PID by parsing a single `ps -axo pid=,ppid=,stat=,comm=` snapshot.
// It returns false when no child is found or the process table is unavailable.
func (darwinForegroundProvider) Foreground(shellPid int) (string, bool) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,stat=,comm=").Output()
	if err != nil {
		return "", false
	}

	procs := make(map[int]darwinProc)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, errPid := strconv.Atoi(fields[0])
		ppid, errPpid := strconv.Atoi(fields[1])
		if errPid != nil || errPpid != nil {
			continue
		}
		procs[pid] = darwinProc{ppid: ppid, comm: fields[3]}
	}

	for pid, p := range procs {
		if p.ppid == shellPid {
			return procs[pid].comm, true
		}
	}
	return "", false
}
