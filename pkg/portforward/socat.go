package portforward

import (
	"fmt"
	"os"
	"os/exec"
)

// startSocat spawns a socat process that listens on listenPort (0.0.0.0) and
// forwards to targetPort on 127.0.0.1:
//
//	socat TCP-LISTEN:{listenPort},bind=0.0.0.0,reuseaddr,fork TCP:127.0.0.1:{targetPort}
//
// listenPort and targetPort may differ — they MUST differ when the service
// already owns targetPort on 127.0.0.1, because Linux will not allow a
// second listener on 0.0.0.0 for the same port number.
//
// The process runs in the background; callers must call stopSocat with the
// returned PID when done.
func startSocat(listenPort, targetPort int) (int, error) {
	listen := fmt.Sprintf("TCP-LISTEN:%d,bind=0.0.0.0,reuseaddr,fork", listenPort)
	target := fmt.Sprintf("TCP:127.0.0.1:%d", targetPort)

	cmd := exec.Command("socat", listen, target)
	// Discard socat's own stdout/stderr to avoid noise; errors surface through
	// the dial failing or the port becoming unreachable.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("socat: %w", err)
	}
	return cmd.Process.Pid, nil
}

// stopSocat sends SIGKILL to the socat process identified by pid.
func stopSocat(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
