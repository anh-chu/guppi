package notify

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/common"
)

func TestNotify_HTTPFallback_AttachesBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tool-event" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".config", "termyard")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	token := "notify-token-from-test"
	if err := os.WriteFile(filepath.Join(cfgDir, "notify.token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write notify token: %v", err)
	}

	app := &cli.Command{
		Commands: common.GetCommands(),
	}
	args := []string{
		"termyard", "notify",
		"--tool", "claude",
		"--status", "active",
		"--session", "test-session",
		"--pane", "test-pane",
		"--server", srv.URL,
	}
	if err := app.Run(t.Context(), args); err != nil {
		t.Fatalf("app.Run error: %v", err)
	}

	want := "Bearer " + token
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}
