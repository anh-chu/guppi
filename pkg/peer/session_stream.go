package peer

import (
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
	ws "github.com/anh-chu/termyard/pkg/ws"
)

// handleStreamMessage routes terminal and file-stream control messages.
func handleStreamMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgOpenTerminal:
		var p OpenTerminalPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid open-terminal")
			return
		}
		go handleOpenTerminal(p, pc, deps, log)

	case MsgCapturePane:
		var p CapturePanePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid capture-pane")
			return
		}
		go handleCapturePane(p, pc, deps, log)

	case MsgCapturePaneResult:
		var p CapturePaneResultPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if deps.CaptureReg != nil {
			deps.CaptureReg.Deliver(p.Token, CaptureResult{Text: p.Text, Error: p.Error})
		}

	case MsgFileRead:
		var p FileReadPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid file-read")
			return
		}
		go handleFileRead(p, pc, deps, log)

	case MsgOpenUpload:
		var p OpenUploadPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid open-upload")
			return
		}
		go handleOpenUpload(p, pc, deps, log)

	case MsgFileReadResult:
		var p FileReadResultPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if deps.FileReadReg != nil {
			deps.FileReadReg.Deliver(p.Token, FileReadResult{
				Data: p.Data, ContentType: p.ContentType,
				FileName: p.FileName, Error: p.Error,
			})
		}
	}
}

// handleOpenTerminal is the host end of a per-terminal data connection.
func handleOpenTerminal(p OpenTerminalPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	log = log.WithFields(logrus.Fields{"stream": p.StreamID, "session": p.Session})
	dial := pc.Role == RoleDialer
	var conn *websocket.Conn
	if deps.Manager == nil || deps.DaemonReg == nil || deps.Identity == nil {
		return
	}
	if dial {
		addr := deps.Manager.GetPeerAddress(pc.HostID)
		c, err := DialPeerStream(context.Background(), addr, deps.Identity, p.Token)
		if err != nil {
			log.WithError(err).Debug("host data-conn dial failed")
			return
		}
		conn = c
	} else {
		if deps.StreamReg == nil || deps.Manager == nil || deps.DaemonReg == nil {
			return
		}
		ps := NewPendingStream(p.StreamID, p.Session, p.Cols, p.Rows, deps.Manager.LocalID(), p.ViewerHostID, pc.HostID)
		deps.StreamReg.Register(p.Token, ps)
		c, ok := ps.WaitResolved(streamSetupTimeout)
		if !ok {
			return
		}
		conn = c
	}
	defer conn.Close()
	// Enable write compression with fastest level so PTY host→viewer output
	// (the bulk direction) is compressed on the wire.
	conn.EnableWriteCompression(true)
	if err := conn.SetCompressionLevel(flate.BestSpeed); err != nil {
		log.WithError(err).Debug("set compression level ignored")
	}

	socketPath := deps.DaemonReg.SocketPath(p.Session)
	ds, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		log.WithError(err).Debug("failed to connect to daemon socket")
		return
	}
	defer ds.Close()
	_ = ds.Resize(p.Cols, p.Rows)
	ws.BridgeDirectPTY(conn, ds, p.Session, deps.ActTracker, log, false)
}

// handleCapturePane captures the daemon ring buffer for the requested session.
func handleCapturePane(p CapturePanePayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	res := CapturePaneResultPayload{Token: p.Token}
	if deps.DaemonReg == nil {
		res.Error = "daemon registry unavailable"
	} else if text, err := deps.DaemonReg.Capture(p.Session); err != nil {
		res.Error = err.Error()
	} else {
		res.Text = model.LastLines(text, p.Lines)
	}
	msg, err := NewMessage(MsgCapturePaneResult, res)
	if err != nil {
		log.WithError(err).Debug("capture-pane result marshal failed")
		return
	}
	pc.Enqueue(msg)
}

// maxFileReadSize caps the file content sent over peer relay (10 MB).
const maxFileReadSize = 10 << 20

// handleFileRead reads a local file and sends its content back over the
// control link. Relative paths resolve against the session's active pane CWD.
func handleFileRead(p FileReadPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	res := FileReadResultPayload{Token: p.Token}

	path := p.Path
	if !filepath.IsAbs(path) {
		base := ""
		if p.Session != "" && deps.DaemonReg != nil {
			for _, s := range deps.DaemonReg.List() {
				if s.ID == p.Session {
					base = s.Cwd
					break
				}
			}
		}
		if base == "" {
			res.Error = "cannot resolve relative path: no active pane cwd"
			if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
				pc.Enqueue(msg)
			}
			return
		}
		path = filepath.Clean(filepath.Join(base, path))
	} else {
		path = filepath.Clean(path)
	}

	info, err := os.Stat(path)
	if err != nil {
		res.Error = "not found"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	if info.IsDir() {
		res.Error = "path is a directory"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	if info.Size() > maxFileReadSize {
		res.Error = "file too large"
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}

	f, err := os.Open(path)
	if err != nil {
		res.Error = err.Error()
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		res.Error = err.Error()
		if msg, err := NewMessage(MsgFileReadResult, res); err == nil {
			pc.Enqueue(msg)
		}
		return
	}

	res.Data = base64.StdEncoding.EncodeToString(data)
	res.FileName = filepath.Base(path)

	// Detect content type.
	ext := filepath.Ext(path)
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	res.ContentType = ct

	msg, err := NewMessage(MsgFileReadResult, res)
	if err != nil {
		log.WithError(err).Debug("file-read result marshal failed")
		return
	}
	pc.Enqueue(msg)
}
