package recovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForgetSession_removesNamedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := &manifest{
		Version:    currentVersion,
		UpdatedAt:  time.Unix(123, 0).UTC(),
		Generation: 1,
		Sessions: []sessionSnapshot{
			{Name: "alpha"},
			{Name: "beta"},
			{Name: "gamma"},
		},
	}
	if err := m.save(); err != nil {
		t.Fatalf("save() failed: %v", err)
	}

	if err := ForgetSession("beta"); err != nil {
		t.Fatalf("ForgetSession() failed: %v", err)
	}

	got, err := load()
	if err != nil {
		t.Fatalf("load() failed: %v", err)
	}
	if len(got.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got.Sessions))
	}
	for _, s := range got.Sessions {
		if s.Name == "beta" {
			t.Fatalf("beta still present")
		}
	}
	if got.Generation <= 1 {
		t.Fatalf("generation not bumped: %d", got.Generation)
	}
	if got.Sessions[0].Name != "alpha" || got.Sessions[1].Name != "gamma" {
		t.Fatalf("unexpected order: %v", sessionNames(got.Sessions))
	}
}

func TestForgetSession_missingSessionNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := &manifest{Version: currentVersion, Sessions: []sessionSnapshot{{Name: "alpha"}}}
	if err := m.save(); err != nil {
		t.Fatalf("save() failed: %v", err)
	}

	if err := ForgetSession("missing"); err != nil {
		t.Fatalf("ForgetSession() failed: %v", err)
	}
	got, err := load()
	if err != nil {
		t.Fatalf("load() failed: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Name != "alpha" {
		t.Fatalf("sessions modified unexpectedly: %v", sessionNames(got.Sessions))
	}
}

func TestForgetSession_emptyNameNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := &manifest{Version: currentVersion, Sessions: []sessionSnapshot{{Name: "alpha"}}}
	if err := m.save(); err != nil {
		t.Fatalf("save() failed: %v", err)
	}
	if err := ForgetSession(""); err != nil {
		t.Fatalf("ForgetSession('') failed: %v", err)
	}
	got, err := load()
	if err != nil {
		t.Fatalf("load() failed: %v", err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions modified unexpectedly: %d", len(got.Sessions))
	}
}

func TestForgetSession_missingFileIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := manifestPath()
	if err != nil {
		t.Fatalf("manifestPath() failed: %v", err)
	}
	_ = os.Remove(path)
	_ = os.Remove(filepath.Dir(path))
	if err := ForgetSession("alpha"); err != nil {
		t.Fatalf("ForgetSession() failed on missing manifest: %v", err)
	}
}

func sessionNames(sessions []sessionSnapshot) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.Name
	}
	return out
}
