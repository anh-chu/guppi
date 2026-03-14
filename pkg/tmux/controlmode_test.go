package tmux

import (
	"testing"
)

func TestParseNotification(t *testing.T) {
	tests := []struct {
		line     string
		wantNil  bool
		wantType string
		wantArgs []string
	}{
		{
			line:     "%sessions-changed",
			wantType: "sessions-changed",
			wantArgs: nil,
		},
		{
			line:     "%window-add @1",
			wantType: "window-add",
			wantArgs: []string{"@1"},
		},
		{
			line:     "%window-renamed @1 my-window",
			wantType: "window-renamed",
			wantArgs: []string{"@1", "my-window"},
		},
		{
			line:     "%session-window-changed $1 @3",
			wantType: "session-window-changed",
			wantArgs: []string{"$1", "@3"},
		},
		{
			line:     "%layout-change @1 abcd,80x24,0,0,1 abcd,80x24,0,0,1 *",
			wantType: "layout-change",
			wantArgs: []string{"@1", "abcd,80x24,0,0,1", "abcd,80x24,0,0,1", "*"},
		},
		{
			line:     "%exit server exited",
			wantType: "exit",
			wantArgs: []string{"server", "exited"},
		},
		{
			line:     "%client-session-changed /dev/pts/1 $2 my-session",
			wantType: "client-session-changed",
			wantArgs: []string{"/dev/pts/1", "$2", "my-session"},
		},
		{
			line:    "%",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			n := parseNotification(tt.line)
			if tt.wantNil {
				if n != nil {
					t.Fatalf("expected nil, got %+v", n)
				}
				return
			}
			if n == nil {
				t.Fatal("expected notification, got nil")
			}
			if n.Type != tt.wantType {
				t.Errorf("type: got %q, want %q", n.Type, tt.wantType)
			}
			if len(n.Args) != len(tt.wantArgs) {
				t.Errorf("args length: got %d, want %d", len(n.Args), len(tt.wantArgs))
				return
			}
			for i, arg := range n.Args {
				if arg != tt.wantArgs[i] {
					t.Errorf("args[%d]: got %q, want %q", i, arg, tt.wantArgs[i])
				}
			}
		})
	}
}

func TestParseNotificationIgnoresNonNotifications(t *testing.T) {
	// Lines not starting with % should not be passed to parseNotification,
	// but test that the function handles edge cases
	n := parseNotification("%")
	if n != nil {
		t.Errorf("expected nil for bare %%, got %+v", n)
	}
}
