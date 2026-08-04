package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/model"
)

const uploadFrameTimeout = 60 * time.Second

// handleOpenUpload is the host end of a dedicated upload data connection.
// It receives binary frames (file content) from the hub, streams them to disk
// via model.StoreUploadedFile, and replies with the stored path.
func handleOpenUpload(p OpenUploadPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	log = log.WithFields(logrus.Fields{"stream": p.StreamID, "file": p.Filename})
	dial := pc.Role == RoleDialer
	var conn *websocket.Conn
	if deps.Manager == nil || deps.Identity == nil {
		return
	}
	if dial {
		addr := deps.Manager.GetPeerAddress(pc.HostID)
		c, err := DialPeerStream(context.Background(), addr, deps.Identity, p.Token)
		if err != nil {
			log.WithError(err).Debug("upload data-conn dial failed")
			return
		}
		conn = c
	} else {
		if deps.StreamReg == nil || deps.Manager == nil {
			return
		}
		ps := NewPendingStream(p.StreamID, "", 0, 0, deps.Manager.LocalID(), p.ViewerHostID, pc.HostID)
		deps.StreamReg.Register(p.Token, ps)
		c, ok := ps.WaitResolved(streamSetupTimeout)
		if !ok {
			return
		}
		conn = c
	}
	defer conn.Close()
	// No write compression for upload content (may be binaries, symmetric
	// with viewer writes which are also uncompressed).
	conn.EnableWriteCompression(false)

	pr, pw := io.Pipe()
	storeDone := make(chan storeUploadResult, 1)

	go func() {
		path, err := model.StoreUploadedFile(pr, p.Filename)
		storeDone <- storeUploadResult{path: path, err: err}
	}()

	var storeRes storeUploadResult
Loop:
	for {
		conn.SetReadDeadline(time.Now().Add(uploadFrameTimeout))
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			pw.CloseWithError(fmt.Errorf("connection error: %w", err))
			storeRes = <-storeDone
			break
		}
		switch msgType {
		case websocket.BinaryMessage:
			if _, err := pw.Write(msg); err != nil {
				storeRes = <-storeDone
				break Loop
			}
		case websocket.TextMessage:
			var frame uploadControlFrame
			if err := json.Unmarshal(msg, &frame); err != nil {
				pw.CloseWithError(fmt.Errorf("invalid control frame: %w", err))
				storeRes = <-storeDone
				break Loop
			}
			switch frame.Type {
			case "upload-eof":
				pw.Close()
				storeRes = <-storeDone
				break Loop
			case "upload-abort":
				pw.CloseWithError(fmt.Errorf("upload aborted"))
				storeRes = <-storeDone
				// don't reply on abort — hub already disconnected
				return
			default:
				pw.CloseWithError(fmt.Errorf("unknown upload frame type %q", frame.Type))
				storeRes = <-storeDone
				break Loop
			}
		default:
			pw.CloseWithError(fmt.Errorf("unexpected websocket message type %d", msgType))
			storeRes = <-storeDone
			break Loop
		}
	}

	if storeRes.err != nil {
		log.WithError(storeRes.err).Debug("upload store failed")
		// If client disconnected (abort), the hub is gone; skip reply.
		if strings.Contains(storeRes.err.Error(), "upload aborted") {
			return
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(map[string]string{
			"error": storeRes.err.Error(),
		}))
		return
	}

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(map[string]string{
		"path":       storeRes.path,
		"quotedPath": model.ShellQuote(storeRes.path),
	}))
}

type uploadControlFrame struct {
	Type string `json:"type"`
}

type storeUploadResult struct {
	path string
	err  error
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
