package ws

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// pongFrame is the canonical reply to a browser heartbeat ping.
var pongFrame = []byte(`{"type":"pong"}`)

// replayStartJSON and replayEndJSON delimit the initial daemon ring-buffer
// replay on sessions that opt into replay gating.
var replayStartJSON = []byte(`{"type":"replay-start"}`)
var replayEndJSON = []byte(`{"type":"replay-end"}`)

// isPing reports whether a text control frame is a heartbeat ping.
func isPing(msg []byte) bool {
	var control struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(msg, &control) == nil && control.Type == "ping"
}

// PTYTerminalHandler handles WebSocket connections backed by daemon PTY sessions.
type PTYTerminalHandler struct {
	activityTracker *activity.Tracker
	tracker         *toolevents.Tracker
}

// NewPTYTerminalHandler creates a new PTY-based terminal handler.
func NewPTYTerminalHandler(activityTracker *activity.Tracker, tracker *toolevents.Tracker) *PTYTerminalHandler {
	return &PTYTerminalHandler{
		activityTracker: activityTracker,
		tracker:         tracker,
	}
}

func bridgeSessionWithCB(conn *websocket.Conn, sess pty.Session, session string, act *activity.Tracker, onOutput func(), log *logrus.Entry, replayGated bool) {
	conn.SetReadLimit(model.MaxPTYControlMessageBytes)

	output := newOutputCoalescer(func(mt int, payload []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := conn.WriteMessage(mt, payload)
		_ = conn.SetWriteDeadline(time.Time{})
		return err
	}, func(error) {
		_ = conn.SetReadDeadline(time.Now())
	}, newResettableOutputTimer())
	ptyDone := make(chan struct{})
	var ptyReads int64

	go func() {
		defer close(ptyDone)
		defer output.CloseAndFlush()

		buffer := make([]byte, 32*1024)

		if fr, isFramed := sess.(pty.FramedReader); replayGated && isFramed {
			var firstChunkDone, inReplay bool
			for {
				n, kind, readErr := fr.ReadFramed(buffer)
				switch kind {
				case pty.ChunkReplay:
					if n == 0 {
						continue
					}
					if !firstChunkDone {
						output.SubmitControl(replayStartJSON)
						firstChunkDone = true
						inReplay = true
					}
					ptyReads++
					if act != nil {
						act.Record(session, n)
					}
					if onOutput != nil {
						onOutput()
					}
					output.Submit(append([]byte(nil), buffer[:n]...))
				case pty.ChunkReplayBoundary:
					if inReplay {
						output.SubmitControl(replayEndJSON)
						inReplay = false
					}
				case pty.ChunkLive:
					if n == 0 && readErr == nil {
						continue
					}
					if inReplay {
						output.SubmitControl(replayEndJSON)
						inReplay = false
					}
					if n > 0 {
						ptyReads++
						if act != nil {
							act.Record(session, n)
						}
						if onOutput != nil {
							onOutput()
						}
						output.Submit(append([]byte(nil), buffer[:n]...))
					}
				}
				if readErr != nil {
					if inReplay {
						output.SubmitControl(replayEndJSON)
					}
					_ = conn.SetReadDeadline(time.Now())
					return
				}
			}
		}

		for {
			n, readErr := sess.Read(buffer)
			if n > 0 {
				ptyReads++
				if act != nil {
					act.Record(session, n)
				}
				if onOutput != nil {
					onOutput()
				}
				chunk := append([]byte(nil), buffer[:n]...)
				output.Submit(chunk)
			}
			if readErr != nil {
				_ = conn.SetReadDeadline(time.Now())
				return
			}
		}
	}()

outer:
	for {
		msgType, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Debug("session ws read error")
			}
			break
		}

		switch msgType {
		case websocket.TextMessage:
			if isPing(message) {
				if !output.RequestPong() {
					break outer
				}
				continue
			}
			if err := model.HandlePTYControlMessage(sess, message); err != nil {
				log.WithError(err).Debug("control message failed")
			}

		case websocket.BinaryMessage:
			if _, err := sess.Write(message); err != nil {
				log.WithError(err).Debug("PTY write failed")
				break outer
			}
		}
	}

	sess.Close()
	<-ptyDone
	stats := output.Stats()
	log.WithFields(logrus.Fields{
		"pty_reads":        ptyReads,
		"output_bytes":     stats.bytes,
		"output_frames":    stats.frames,
		"max_output_frame": stats.maxFrame,
		"slow_writes":      stats.slowWrites,
	}).Info("session ws client disconnected")
}

// BridgeDirectPTY pumps a direct PTY session over an already-open WebSocket
// connection. An optional onOutput callback is invoked on every PTY read so
// the silence monitor can track daemon output activity.
func BridgeDirectPTY(conn *websocket.Conn, sess pty.Session, session string, act *activity.Tracker, log *logrus.Entry, replayGated bool, onOutput ...func()) {
	var cb func()
	if len(onOutput) > 0 {
		cb = onOutput[0]
	}
	bridgeSessionWithCB(conn, sess, session, act, cb, log, replayGated)
}

// SpliceConns pumps bytes between an upgraded browser WS and a peer data conn.
// Teardown nudges both read deadlines and callers own Close.
//
// Heartbeat: the browser sends an app-level {"type":"ping"} every 10s. We
// answer locally for a fast ack AND forward the ping to the peer data conn
// so the host echoes a pong back through this splice. The round trip keeps
// the hub<->host data connection bidirectionally busy, defeating NAT/proxy
// idle timeouts that would otherwise silently kill an idle remote terminal
// and force a visible reconnect flap (the browser watchdog then never gets a
// host pong and tears down). Without the forward, a half-open data conn is
// only detected when output stops flowing, by which point the tab is stuck.
func SpliceConns(browser, data *websocket.Conn, log *logrus.Entry) {
	browser.SetReadLimit(model.MaxPTYControlMessageBytes)
	data.SetReadLimit(model.MaxPTYControlMessageBytes)

	var browserMu sync.Mutex
	var once sync.Once
	done := make(chan struct{})
	closeBoth := func() {
		once.Do(func() {
			_ = browser.SetReadDeadline(time.Now())
			_ = data.SetReadDeadline(time.Now())
		})
	}

	go func() {
		defer close(done)
		for {
			mt, msg, err := data.ReadMessage()
			if err != nil {
				closeBoth()
				return
			}
			browserMu.Lock()
			_ = browser.SetWriteDeadline(time.Now().Add(10 * time.Second))
			werr := browser.WriteMessage(mt, msg)
			_ = browser.SetWriteDeadline(time.Time{})
			browserMu.Unlock()
			if werr != nil {
				closeBoth()
				return
			}
		}
	}()

	for {
		mt, msg, err := browser.ReadMessage()
		if err != nil {
			closeBoth()
			break
		}
		if mt == websocket.TextMessage && isPing(msg) {
			// Fast local ack so the browser watchdog never false-fires.
			browserMu.Lock()
			_ = browser.SetWriteDeadline(time.Now().Add(10 * time.Second))
			werr := browser.WriteMessage(websocket.TextMessage, pongFrame)
			_ = browser.SetWriteDeadline(time.Time{})
			browserMu.Unlock()
			if werr != nil {
				closeBoth()
				break
			}
			// Forward the ping to the peer data conn so the host echoes a
			// pong back through the data->browser pump. This keeps the
			// hub<->host link alive on idle terminals and surfaces
			// half-open peer connections as write errors here.
			_ = data.SetWriteDeadline(time.Now().Add(10 * time.Second))
			derr := data.WriteMessage(websocket.TextMessage, msg)
			_ = data.SetWriteDeadline(time.Time{})
			if derr != nil {
				closeBoth()
				break
			}
			continue
		}
		_ = data.SetWriteDeadline(time.Now().Add(10 * time.Second))
		werr := data.WriteMessage(mt, msg)
		_ = data.SetWriteDeadline(time.Time{})
		if werr != nil {
			closeBoth()
			break
		}
	}
	closeBoth()
	<-done
	log.Info("peer-stream splice ended")
}

// HandleDirectSession handles a WebSocket connection for a direct PTY session
// Query params: cols=<cols>, rows=<rows>, cwd=<dir>
func (h *PTYTerminalHandler) HandleDirectSession(w http.ResponseWriter, r *http.Request) {
	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	cwd := r.URL.Query().Get("cwd")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("direct session ws upgrade failed")
		return
	}
	defer conn.Close()

	sess, err := pty.NewDirectPTYSession("", uint16(cols), uint16(rows), cwd)
	if err != nil {
		logrus.WithError(err).Error("failed to start direct PTY session")
		conn.WriteMessage(websocket.TextMessage, []byte("\r\n[termyard: failed to create direct PTY session]\r\n"))
		return
	}

	log := logrus.WithFields(logrus.Fields{
		"cols": cols,
		"rows": rows,
		"cwd":  cwd,
	})
	log.Info("direct session ws client connected")

	BridgeDirectPTY(conn, sess, cwd, h.activityTracker, log, false)
}
