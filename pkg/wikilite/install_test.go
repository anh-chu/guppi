package wikilite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirHonorsXDGDataHome(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)
	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	// DataDir() returns <xdg>/termyard, Dir() returns <xdg>/termyard/wiki-lite.
	want := filepath.Join(d, "termyard", "wiki-lite")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestInstalledEmptyDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)

	if Installed() {
		t.Fatal("Installed() true for empty dir")
	}
}

func TestInstalledBothMarkers(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)

	// wikilite.Dir() is <xdg>/termyard/wiki-lite.
	wd := filepath.Join(d, "termyard", "wiki-lite")
	if err := os.MkdirAll(filepath.Join(wd, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wd, ".next", "standalone"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Missing entry point -> false.
	if Installed() {
		t.Fatal("Installed() true with only server.js")
	}

	// Add entry point.
	if err := os.WriteFile(filepath.Join(wd, "bin", "wiki-viewer-lite.js"), []byte("// stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Still missing server.js -> false.
	if Installed() {
		t.Fatal("Installed() true without server.js")
	}

	// Add server.js.
	if err := os.WriteFile(filepath.Join(wd, ".next", "standalone", "server.js"), []byte("// stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Installed() {
		t.Fatal("Installed() false with both markers present")
	}
}

func TestInstalledVersionParsesPackageJSON(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)

	wd := filepath.Join(d, "termyard", "wiki-lite")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}

	// No package.json -> empty.
	if v := InstalledVersion(); v != "" {
		t.Fatalf("InstalledVersion() = %q, want empty", v)
	}

	pkg := map[string]string{"version": "1.2.3"}
	raw, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(wd, "package.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if v := InstalledVersion(); v != "1.2.3" {
		t.Fatalf("InstalledVersion() = %q, want 1.2.3", v)
	}
}

func TestFirstMissingTool(t *testing.T) {
	// On a healthy dev machine all three exist.
	if missing := firstMissingTool(); missing != "" {
		t.Skipf("tool %q missing on test host; skipping", missing)
	}
}

// Commit must work when nothing under the data dir exists yet. The original
// code staged in the system temp dir and renamed into a parent it never
// created, so a fresh machine failed with ENOENT, or with EXDEV when temp sat
// on a different filesystem.
func TestCommitIntoFreshDataDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(d, "not-created-yet"))

	dest, err := Dir()
	if err != nil {
		t.Fatal(err)
	}

	// Stage() builds this path; construct it directly to avoid the network.
	staging := stagingPath(dest)
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "bin", "wiki-viewer-lite.js"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Commit(staging); err != nil {
		t.Fatalf("Commit into a fresh data dir failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "wiki-viewer-lite.js")); err != nil {
		t.Fatalf("committed tree missing: %v", err)
	}
}

// A crash between the two renames of a swap leaves no live directory. Recovery
// must restore the backup, and Commit must recover BEFORE clearing it, or the
// next install destroys the last working copy.
func TestRecoverInstallRestoresBackup(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)

	dest, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	old := backupPath(dest)
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "marker"), []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RecoverInstall(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "marker"))
	if err != nil {
		t.Fatalf("backup was not restored: %v", err)
	}
	if string(got) != "previous" {
		t.Fatalf("restored wrong content: %q", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("backup should be gone after a restore")
	}
}

// In the crash window (no live dir, only a backup) Commit must RESTORE the
// backup before it clears anything. Without that, the stale backup is left
// behind untouched and the next install deletes it, losing the last working
// copy. The observable difference is that a recovered backup gets consumed by
// the swap, whereas an unrecovered one persists.
func TestCommitConsumesBackupInCrashWindow(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_DATA_HOME", d)

	dest, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	old := backupPath(dest)
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "marker"), []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	staging := stagingPath(dest)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "marker"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Commit(staging); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Fatalf("live dir should hold the new install, got %q", got)
	}

	// The backup is cleared asynchronously once the swap succeeds. If Commit
	// never recovered, the stale backup is still sitting there instead.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stale backup survived Commit, so the crash window was not recovered")
}
