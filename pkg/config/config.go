package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Dir returns the termyard config directory (~/.config/termyard).
// It does not create the directory; callers create it with the perm
// they require.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "termyard"), nil
}

// DataDir returns the termyard data directory. The path respects
// $XDG_DATA_HOME when set; otherwise it falls back to ~/.local/share/termyard.
func DataDir() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "termyard"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "termyard"), nil
}

// V2StateDir returns the directory for the v2 atomic state store. The v2 store
// is dormant by default; callers open it only when they are ready to use it.
func V2StateDir() (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "v2"), nil
}

// WriteJSON marshals v with indentation and writes it to path with perm.
// It preserves existing non-atomic MarshalIndent + os.WriteFile behavior.
func WriteJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}
