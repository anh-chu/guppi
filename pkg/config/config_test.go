package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirHonorsXDGDataHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "termyard")
	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestDataDirFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	want := filepath.Join(home, ".local", "share", "termyard")
	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
	// Must not contain .config -- it is a data dir, not a config dir.
	if strings.Contains(got, ".config") {
		t.Fatalf("DataDir() returned config path %q", got)
	}
}
