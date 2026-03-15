package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"

	"github.com/urfave/cli/v3"

	"github.com/ekristen/guppi/pkg/common"
)

const systemdUnit = `[Unit]
Description=Guppi - Web dashboard for tmux sessions
After=default.target

[Service]
Type=simple
ExecStart={{.ExecStart}}
Restart=on-failure
RestartSec=5
Environment=PATH={{.Path}}

[Install]
WantedBy=default.target
`

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.guppi.server</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinaryPath}}</string>
		<string>server</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogDir}}/guppi.stdout.log</string>
	<key>StandardErrorPath</key>
	<string>{{.LogDir}}/guppi.stderr.log</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>{{.Path}}</string>
	</dict>
</dict>
</plist>
`

type serviceConfig struct {
	BinaryPath string
	ExecStart  string
	Path       string
	LogDir     string
}

func getBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not determine executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("could not resolve symlinks: %w", err)
	}
	return exe, nil
}

func installLinux(ctx context.Context, c *cli.Command) error {
	binPath, err := getBinaryPath()
	if err != nil {
		return err
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("could not determine config directory: %w", err)
	}

	unitDir := filepath.Join(configDir, "systemd", "user")
	unitPath := filepath.Join(unitDir, "guppi.service")

	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("could not create systemd user directory: %w", err)
	}

	cfg := serviceConfig{
		BinaryPath: binPath,
		ExecStart:  binPath + " server",
		Path:       os.Getenv("PATH"),
	}

	tmpl, err := template.New("systemd").Parse(systemdUnit)
	if err != nil {
		return fmt.Errorf("could not parse systemd template: %w", err)
	}

	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("could not create unit file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, cfg); err != nil {
		return fmt.Errorf("could not write unit file: %w", err)
	}

	fmt.Printf("Wrote %s\n", unitPath)

	// Reload and enable
	if err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %w", err)
	}

	if err := exec.CommandContext(ctx, "systemctl", "--user", "enable", "--now", "guppi.service").Run(); err != nil {
		return fmt.Errorf("systemctl enable failed: %w", err)
	}

	fmt.Println("Service enabled and started (systemctl --user)")
	fmt.Println()
	fmt.Println("  Status:   systemctl --user status guppi")
	fmt.Println("  Logs:     journalctl --user -u guppi -f")
	fmt.Println("  Restart:  systemctl --user restart guppi")
	fmt.Println("  Web UI:   https://localhost:7654")
	return nil
}

func installDarwin(ctx context.Context, c *cli.Command) error {
	binPath, err := getBinaryPath()
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(agentDir, "com.guppi.server.plist")
	logDir := filepath.Join(home, "Library", "Logs")

	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("could not create LaunchAgents directory: %w", err)
	}

	cfg := serviceConfig{
		BinaryPath: binPath,
		Path:       os.Getenv("PATH"),
		LogDir:     logDir,
	}

	tmpl, err := template.New("launchd").Parse(launchdPlist)
	if err != nil {
		return fmt.Errorf("could not parse plist template: %w", err)
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("could not create plist file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, cfg); err != nil {
		return fmt.Errorf("could not write plist file: %w", err)
	}

	fmt.Printf("Wrote %s\n", plistPath)

	// Load the agent
	if err := exec.CommandContext(ctx, "launchctl", "load", "-w", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load failed: %w", err)
	}

	fmt.Println("Service loaded and started (launchctl)")
	fmt.Println()
	fmt.Println("  Status:   launchctl list com.guppi.server")
	fmt.Printf("  Logs:     tail -f %s/guppi.stderr.log\n", cfg.LogDir)
	fmt.Printf("  Restart:  launchctl kickstart -k gui/$(id -u)/com.guppi.server\n")
	fmt.Println("  Web UI:   https://localhost:7654")
	return nil
}

func installExecute(ctx context.Context, c *cli.Command) error {
	switch runtime.GOOS {
	case "linux":
		return installLinux(ctx, c)
	case "darwin":
		return installDarwin(ctx, c)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func uninstallLinux(ctx context.Context, c *cli.Command) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("could not determine config directory: %w", err)
	}

	unitPath := filepath.Join(configDir, "systemd", "user", "guppi.service")

	// Disable and stop
	_ = exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", "guppi.service").Run()

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove unit file: %w", err)
	}

	_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()

	fmt.Printf("Removed %s\n", unitPath)
	fmt.Println("Service disabled and stopped")
	return nil
}

func uninstallDarwin(ctx context.Context, c *cli.Command) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.guppi.server.plist")

	// Unload the agent
	_ = exec.CommandContext(ctx, "launchctl", "unload", "-w", plistPath).Run()

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove plist file: %w", err)
	}

	fmt.Printf("Removed %s\n", plistPath)
	fmt.Println("Service unloaded and stopped")
	return nil
}

func uninstallExecute(ctx context.Context, c *cli.Command) error {
	switch runtime.GOOS {
	case "linux":
		return uninstallLinux(ctx, c)
	case "darwin":
		return uninstallDarwin(ctx, c)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func init() {
	cmd := &cli.Command{
		Name:  "install",
		Usage: "install guppi as a user service for auto-start",
		Description: `Install guppi to start automatically on login.

On Linux, installs a systemd user unit (~/.config/systemd/user/guppi.service).
On macOS, installs a launchd plist (~/Library/LaunchAgents/com.guppi.server.plist).

Use "guppi install" to install and enable, "guppi uninstall" to remove.`,
		Action: installExecute,
	}

	uninstallCmd := &cli.Command{
		Name:  "uninstall",
		Usage: "remove guppi user service",
		Description: `Remove the guppi auto-start service.

On Linux, disables and removes the systemd user unit.
On macOS, unloads and removes the launchd plist.`,
		Action: uninstallExecute,
	}

	common.RegisterCommand(cmd)
	common.RegisterCommand(uninstallCmd)
}
