package peer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/pty"
)

type fakeDaemonReg struct {
	captureText string
	captureErr  error
	killErr     error
	list        []pty.SessionInfo
}

func (f *fakeDaemonReg) Create(name, shell, cwd string, cols, rows uint16) (pty.SessionInfo, error) { return pty.SessionInfo{}, nil }
func (f *fakeDaemonReg) Kill(name string) error                                  { return f.killErr }
func (f *fakeDaemonReg) Capture(name string) (string, error)                     { return f.captureText, f.captureErr }
func (f *fakeDaemonReg) SocketPath(name string) string                            { return "" }
func (f *fakeDaemonReg) List() []pty.SessionInfo                                  { return f.list }

func TestHandleFileReadNotFound(t *testing.T) {
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{}
	msg, err := NewMessage(MsgFileRead, FileReadPayload{
		Token:   "t",
		Path:    "/no/such/file",
		Session: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	handleStreamMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgFileReadResult {
			t.Fatalf("expected %s, got %s", MsgFileReadResult, msg.Type)
		}
		var res FileReadResultPayload
		if err := json.Unmarshal(msg.Payload, &res); err != nil {
			t.Fatal(err)
		}
		if res.Error == "" {
			t.Fatal("expected error for missing file")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for file-read result")
	}
}

func TestHandleCapturePane(t *testing.T) {
	pc := NewPeerConnection("peer", 1)
	reg := &fakeDaemonReg{captureText: "line1\nline2\nline3\n"}
	deps := SessionDeps{DaemonReg: reg}
	msg, err := NewMessage(MsgCapturePane, CapturePanePayload{
		Token:   "t",
		Session: "s",
		Lines:   2,
	})
	if err != nil {
		t.Fatal(err)
	}

	handleStreamMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgCapturePaneResult {
			t.Fatalf("expected %s, got %s", MsgCapturePaneResult, msg.Type)
		}
		var res CapturePaneResultPayload
		if err := json.Unmarshal(msg.Payload, &res); err != nil {
			t.Fatal(err)
		}
		if res.Error != "" {
			t.Fatalf("unexpected error: %s", res.Error)
		}
		if res.Text == "" {
			t.Fatal("expected captured text")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capture-pane result")
	}
}
