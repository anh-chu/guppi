package pty

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// maxClientBuffer caps the internal read buffer to prevent the server
	// from growing unbounded memory when a daemon produces output faster
	// than the WebSocket can drain it.
	maxClientBuffer = 4 * 1024 * 1024 // 4 MiB
)

// DaemonSession connects to a running session daemon via Unix socket
// and implements the Session interface (Read/Write/Resize/Close).
type DaemonSession struct {
	conn net.Conn

	// Internal buffering for Read() calls.
	mu   sync.Mutex
	cond *sync.Cond

	// Replay and live output are kept in separate buffers so that a framed
	// reader can emit boundary signals and guarantee a single ReadFramed
	// call never mixes replay bytes with live bytes.
	replayBuf bytes.Buffer
	liveBuf   bytes.Buffer

	// replaySeen is set when a FrameReplay frame arrives. It is used to
	// emit the ChunkReplayBoundary sentinel exactly once after the replay
	// buffer has been drained. Connections with no initial replay do not
	// emit the boundary.
	replaySeen      bool
	boundaryEmitted bool

	done bool // set when connection is closed or daemon exits
}

// NewDaemonSession connects to a session daemon at the given socket path.
// It receives the initial Replay message and makes it available via Read().
// daemonDialTimeout caps how long NewDaemonSession waits for a just-spawned
// daemon to bind its socket. Registry.Create returns immediately after
// starting the daemon process (which may still be cold-starting or waiting
// on the systemd-run DBus round-trip), so the terminal's WS connect lands
// here and retries until the socket is ready.
const daemonDialTimeout = 2 * time.Second

func NewDaemonSession(socketPath string) (*DaemonSession, error) {
	var conn net.Conn
	var err error
	deadline := time.Now().Add(daemonDialTimeout)
	for attempt := 0; ; attempt++ {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("dial daemon socket %s: %w (after %d attempts)", socketPath, err, attempt+1)
		}
		time.Sleep(20 * time.Millisecond)
	}

	d := &DaemonSession{conn: conn}
	d.cond = sync.NewCond(&d.mu)

	// Start goroutine reading frames from the daemon.
	go d.readFrames()

	return d, nil
}

// readFrames reads frames from the socket and appends Output/Replay data
// to the internal buffer, signaling any blocked Read() callers.
func (d *DaemonSession) readFrames() {
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(d.conn, header); err != nil {
			d.signalDone()
			return
		}
		ftype := header[0]
		plen := binary.BigEndian.Uint32(header[1:5])

		if plen > MaxFramePayload { // sanity
			d.signalDone()
			return
		}

		var payload []byte
		if plen > 0 {
			payload = make([]byte, plen)
			if _, err := io.ReadFull(d.conn, payload); err != nil {
				d.signalDone()
				return
			}
		}

		switch ftype {
		case FrameReplay:
			d.appendReplay(payload)
		case FrameOutput:
			d.appendLive(payload)
		case FrameClose:
			// Daemon is shutting down.
			d.signalDone()
			return
		}
	}
}

// appendReplay adds data to the replay buffer and wakes blocked readers.
// If the buffer would exceed maxClientBuffer, the oldest data is discarded.
func (d *DaemonSession) appendReplay(data []byte) {
	d.mu.Lock()
	if d.replayBuf.Len()+len(data) > maxClientBuffer {
		// Discard the oldest buffered data to make room.
		// If the incoming data alone exceeds the cap, keep only the tail.
		d.replayBuf.Reset()
		if len(data) > maxClientBuffer {
			data = data[len(data)-maxClientBuffer:]
		}
	}
	d.replayBuf.Write(data)
	d.replaySeen = true
	d.mu.Unlock()
	d.cond.Broadcast()
}

// appendLive adds data to the live buffer and wakes blocked readers.
// If the buffer would exceed maxClientBuffer, the oldest data is discarded.
func (d *DaemonSession) appendLive(data []byte) {
	d.mu.Lock()
	if d.liveBuf.Len()+len(data) > maxClientBuffer {
		// Discard the oldest buffered data to make room.
		// If the incoming data alone exceeds the cap, keep only the tail.
		d.liveBuf.Reset()
		if len(data) > maxClientBuffer {
			data = data[len(data)-maxClientBuffer:]
		}
	}
	d.liveBuf.Write(data)
	d.mu.Unlock()
	d.cond.Broadcast()
}

// signalDone marks the session as done and wakes all readers.
func (d *DaemonSession) signalDone() {
	d.mu.Lock()
	d.done = true
	d.mu.Unlock()
	d.cond.Broadcast()
}

// Read implements Session — returns PTY output bytes (replay first, then live),
// flattened for legacy callers that do not understand framing.
// Blocks until data is available or the session is closed.
func (d *DaemonSession) Read(buf []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Wait until there is data or we're done.
	for d.replayBuf.Len() == 0 && d.liveBuf.Len() == 0 && !d.done {
		d.cond.Wait()
	}

	if d.replayBuf.Len() > 0 {
		n, _ := d.replayBuf.Read(buf)
		return n, nil
	}

	if d.liveBuf.Len() == 0 && d.done {
		return 0, io.EOF
	}

	// liveBuf has data.
	n, err := d.liveBuf.Read(buf)
	// If we drained the buffer but there's more data coming, return what we have.
	// Only signal EOF when both buffer is empty AND done.
	if err == io.EOF {
		err = nil // buffer EOF is not a real error
	}
	return n, err
}

// ReadFramed reads output with explicit framing. It satisfies the
// FramedReader interface. A single call never returns bytes from both
// replay and live buffers.
//
// The order of returns is: all ChunkReplay bytes, exactly one
// ChunkReplayBoundary with n == 0, then all ChunkLive bytes. If no replay
// frame is received on this connection, the boundary is never emitted.
func (d *DaemonSession) ReadFramed(buf []byte) (int, ChunkKind, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.replayBuf.Len() > 0 {
		n, _ := d.replayBuf.Read(buf)
		return n, ChunkReplay, nil
	}

	if d.replaySeen && !d.boundaryEmitted {
		d.boundaryEmitted = true
		return 0, ChunkReplayBoundary, nil
	}

	// Replay drained; wait for live data.
	for d.liveBuf.Len() == 0 && !d.done {
		d.cond.Wait()
	}

	if d.liveBuf.Len() > 0 {
		n, err := d.liveBuf.Read(buf)
		if err == io.EOF {
			err = nil
		}
		return n, ChunkLive, err
	}

	return 0, ChunkLive, io.EOF
}

// Write implements Session — sends an Input frame to the daemon.
func (d *DaemonSession) Write(data []byte) (int, error) {
	frame := encodeFrame(FrameInput, data)
	_, err := d.conn.Write(frame)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

// Resize implements Session — sends a Resize frame to the daemon.
func (d *DaemonSession) Resize(cols, rows uint16) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], cols)
	binary.BigEndian.PutUint16(payload[2:4], rows)
	frame := encodeFrame(FrameResize, payload)
	_, err := d.conn.Write(frame)
	return err
}

// Close implements Session — disconnects from the daemon WITHOUT
// killing it.  The daemon keeps running so other clients (or a
// later reconnect) can attach.  To actually terminate the daemon,
// use Registry.Kill() which sends FrameClose explicitly.
func (d *DaemonSession) Close() {
	// Do NOT send FrameClose — that would shut down the daemon.
	// Just close the local socket connection.
	d.conn.Close()
	d.signalDone()
	logrus.Debug("daemon session client disconnected (daemon still running)")
}

// Kill sends a FrameClose frame to the daemon, instructing it to
// terminate the shell and exit, then closes the local connection.
// Unlike Close, this actually shuts down the daemon process.
func (d *DaemonSession) Kill() {
	frame := encodeFrame(FrameClose, nil)
	d.conn.Write(frame)
	d.conn.Close()
	d.signalDone()
}
