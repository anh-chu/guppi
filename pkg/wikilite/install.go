package wikilite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anh-chu/termyard/pkg/config"
)

// Dir returns the wiki-lite installation directory under termyard's data dir.
func Dir() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "wiki-lite"), nil
}

// BinPath returns the path to the wiki-viewer-lite entry point.
func BinPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bin", "wiki-viewer-lite.js"), nil
}

// Installed returns true when both the entry point and the vendored Next.js
// standalone server bundle exist on disk.
func Installed() bool {
	bp, err := BinPath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(bp); err != nil {
		return false
	}
	d, err := Dir()
	if err != nil {
		return false
	}
	serverPath := filepath.Join(d, ".next", "standalone", "server.js")
	_, err = os.Stat(serverPath)
	return err == nil
}

// InstalledVersion returns the version field from the installed package.json,
// or an empty string on any error.
func InstalledVersion() string {
	d, err := Dir()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(d, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}

// firstMissingTool checks that node, npm and tar are on PATH and returns the
// first missing tool, or an empty string when all are present.
func firstMissingTool() string {
	for _, tool := range []string{"node", "npm", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool
		}
	}
	return ""
}

// stagingPath and backupPath are siblings of the live directory on purpose.
// Staging must share a filesystem with the destination or the final rename
// fails with EXDEV, which is exactly what happens when staging is left in the
// system temp dir.
func stagingPath(destDir string) string { return destDir + ".new" }
func backupPath(destDir string) string  { return destDir + ".old" }

// RecoverInstall restores the previous install when a crash landed between the
// two renames of a swap, leaving no live directory. Without this the next
// install would delete the backup and destroy the last working copy.
func RecoverInstall() error {
	destDir, err := Dir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(destDir); err == nil {
		return nil
	}
	old := backupPath(destDir)
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	return os.Rename(old, destDir)
}

// Stage downloads and extracts the package into a staging directory beside the
// live install and returns its path. It does not touch the live directory, so
// it is safe to run while a child process is still serving.
//
// npm pack plus tar, deliberately not npm install, so there is no dependency
// resolution or native compilation on the user's machine. That only works
// because wiki-viewer's postbuild flattens its standalone node_modules: npm
// pack keeps a pnpm store's files but silently drops every symlink, so an
// unflattened tarball extracts to a tree where require("next") fails.
func Stage(ctx context.Context) (string, error) {
	if missing := firstMissingTool(); missing != "" {
		return "", fmt.Errorf("%s not found on PATH", missing)
	}

	destDir, err := Dir()
	if err != nil {
		return "", err
	}

	// The parent must exist before anything can be renamed into it. On a fresh
	// machine ~/.local/share/termyard does not exist yet.
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return "", fmt.Errorf("creating data directory: %w", err)
	}

	stagingDir := stagingPath(destDir)
	if err := os.RemoveAll(stagingDir); err != nil {
		return "", fmt.Errorf("clearing stale staging directory: %w", err)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", err
	}

	// Only the tarball lives in the system temp dir. Nothing is renamed out of
	// it, so a separate filesystem is fine here.
	tmpDir, err := os.MkdirTemp("", "termyard-wikilite-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	packDest := filepath.Join(tmpDir, "pack")
	if err := os.MkdirAll(packDest, 0o755); err != nil {
		return "", err
	}

	var packOut bytes.Buffer
	packCmd := exec.CommandContext(ctx, "npm", "pack", "wiki-viewer@latest", "--pack-destination", packDest)
	packCmd.Dir = packDest
	packCmd.Stdout = &packOut
	packCmd.Stderr = &packOut
	if err := packCmd.Run(); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", captureErr(err, packOut, "npm pack failed")
	}

	entries, err := os.ReadDir(packDest)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("reading pack destination: %w", err)
	}
	var tgz string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			tgz = filepath.Join(packDest, e.Name())
			break
		}
	}
	if tgz == "" {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("npm pack produced no tgz file")
	}

	var tarOut bytes.Buffer
	tarCmd := exec.CommandContext(ctx, "tar", "-xzf", tgz, "-C", stagingDir, "--strip-components=1")
	tarCmd.Stdout = &tarOut
	tarCmd.Stderr = &tarOut
	if err := tarCmd.Run(); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", captureErr(err, tarOut, "tar extract failed")
	}

	// Refuse to commit a package that cannot serve as lite. Without this an
	// older published version extracts fine, Installed() stays false, and the
	// panel sits on "not installed" while the user retries with no explanation.
	//
	// Both markers Installed() checks must be present, not just the entry
	// script: committing a package that satisfies one and not the other
	// replaces a working install with a broken one, and the old copy is
	// deleted straight after the swap.
	for _, marker := range []string{
		filepath.Join("bin", "wiki-viewer-lite.js"),
		filepath.Join(".next", "standalone", "server.js"),
	} {
		if _, err := os.Stat(filepath.Join(stagingDir, marker)); err == nil {
			continue
		}
		version := stagedVersion(stagingDir)
		_ = os.RemoveAll(stagingDir)
		if version == "" {
			return "", fmt.Errorf("the published wiki-viewer package does not provide %s", marker)
		}
		return "", fmt.Errorf("wiki-viewer %s does not provide %s, a newer release is required", version, marker)
	}

	return stagingDir, nil
}

// stagedVersion reads the version out of a staged package, best effort.
func stagedVersion(stagingDir string) string {
	raw, err := os.ReadFile(filepath.Join(stagingDir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}

// Commit swaps a staged directory over the live install. The caller must have
// stopped any child running from the live directory first.
func Commit(stagingDir string) error {
	destDir, err := Dir()
	if err != nil {
		return err
	}

	// Recover before touching the backup, or a previous half-finished swap
	// loses its only surviving copy here.
	if err := RecoverInstall(); err != nil {
		return fmt.Errorf("recovering previous install: %w", err)
	}

	oldDir := backupPath(destDir)

	// Move the live directory aside. Clearing any earlier backup is safe only
	// while the live directory still exists, which is the case here.
	renamed := false
	if _, err := os.Stat(destDir); err == nil {
		if err := os.RemoveAll(oldDir); err != nil {
			return fmt.Errorf("clearing previous backup: %w", err)
		}
		if err := os.Rename(destDir, oldDir); err != nil {
			return fmt.Errorf("renaming existing install: %w", err)
		}
		renamed = true
	}

	if err := os.Rename(stagingDir, destDir); err != nil {
		if renamed {
			_ = os.Rename(oldDir, destDir)
		}
		return fmt.Errorf("renaming staging to install: %w", err)
	}

	if renamed {
		go func() { _ = os.RemoveAll(oldDir) }()
	}

	return nil
}

// captureErr formats the last ~2000 bytes of combined output into the error.
func captureErr(cmdErr error, buf bytes.Buffer, prefix string) error {
	out := buf.Bytes()
	if len(out) > 2000 {
		out = out[len(out)-2000:]
	}
	errText := strings.TrimSpace(string(out))
	var exitErr *exec.ExitError
	if errors.As(cmdErr, &exitErr) {
		errText = strings.TrimSpace(string(exitErr.Stderr))
		if errText == "" {
			errText = strings.TrimSpace(string(out))
		}
	}
	if errText == "" {
		return fmt.Errorf("%s: %w", prefix, cmdErr)
	}
	return fmt.Errorf("%s: %s", prefix, errText)
}
