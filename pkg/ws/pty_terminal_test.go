package ws

import (
	"bytes"
	"compress/flate"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/pty"
)

type fakeFramedSession struct {
	steps []struct {
		n    int
		kind pty.ChunkKind
		err  error
	}
	pos int
}

func (f *fakeFramedSession) Read(p []byte) (int, error)     { return 0, io.EOF }
func (f *fakeFramedSession) Write(p []byte) (int, error)    { return len(p), nil }
func (f *fakeFramedSession) Close()                         {}
func (f *fakeFramedSession) Resize(cols, rows uint16) error { return nil }

func (f *fakeFramedSession) ReadFramed(p []byte) (int, pty.ChunkKind, error) {
	if f.pos >= len(f.steps) {
		return 0, pty.ChunkLive, io.EOF
	}
	s := f.steps[f.pos]
	f.pos++
	n := s.n
	if n > len(p) {
		n = len(p)
	}
	if n > 0 {
		for i := range p[:n] {
			p[i] = byte('0' + f.pos%10)
		}
	}
	return n, s.kind, s.err
}

func readWSFrames(t *testing.T, client *websocket.Conn, n int) []recordedFrame {
	t.Helper()
	frames := make([]recordedFrame, 0, n)
	for i := 0; i < n; i++ {
		mt, payload := mustReadWSMessage(t, client)
		frames = append(frames, recordedFrame{messageType: mt, payload: bytes.Clone(payload)})
	}
	return frames
}

func TestBridgeDirectPTYReplayGatedFramed(t *testing.T) {
	client, server := mustWSConnPair(t)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	log := logrus.NewEntry(logger)

	fs := &fakeFramedSession{
		steps: []struct {
			n    int
			kind pty.ChunkKind
			err  error
		}{
			{n: 6, kind: pty.ChunkReplay},
			{n: 0, kind: pty.ChunkReplayBoundary},
			{n: 0, kind: pty.ChunkLive, err: io.EOF},
		},
	}

	done := make(chan struct{})
	go func() {
		BridgeDirectPTY(server, fs, "test", nil, log, true)
		close(done)
	}()

	frames := readWSFrames(t, client, 3)
	_ = client.Close()
	<-done

	want := []recordedFrame{
		{messageType: websocket.TextMessage, payload: replayStartJSON},
		{messageType: websocket.BinaryMessage, payload: []byte("111111")},
		{messageType: websocket.TextMessage, payload: replayEndJSON},
	}
	for i, w := range want {
		got := frames[i]
		if got.messageType != w.messageType || !bytes.Equal(got.payload, w.payload) {
			t.Fatalf("frame %d = (%d, %q), want (%d, %q)", i, got.messageType, got.payload, w.messageType, w.payload)
		}
	}
}

func TestBridgeDirectPTYReplayGatedFalseNoControlFrames(t *testing.T) {
	client, server := mustWSConnPair(t)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	log := logrus.NewEntry(logger)

	fake := &fakeReadSession{data: []byte("replaylive")}

	done := make(chan struct{})
	go func() {
		BridgeDirectPTY(server, fake, "test", nil, log, false)
		close(done)
	}()

	frames := readWSFrames(t, client, 1)
	_ = client.Close()
	<-done

	if len(frames) != 1 || frames[0].messageType != websocket.BinaryMessage || !bytes.Equal(frames[0].payload, []byte("replaylive")) {
		t.Fatalf("got frames %+v, want one binary frame", frames)
	}
}

func TestBridgeDirectPTYNonFramedFallback(t *testing.T) {
	client, server := mustWSConnPair(t)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	log := logrus.NewEntry(logger)

	fake := &fakeReadSession{data: []byte("hello")}

	done := make(chan struct{})
	go func() {
		BridgeDirectPTY(server, fake, "test", nil, log, true)
		close(done)
	}()

	frames := readWSFrames(t, client, 1)
	_ = client.Close()
	<-done

	if len(frames) != 1 || frames[0].messageType != websocket.BinaryMessage || !bytes.Equal(frames[0].payload, []byte("hello")) {
		t.Fatalf("got frames %+v, want one binary hello", frames)
	}
}

type fakeReadSession struct {
	data []byte
	done bool
}

func (f *fakeReadSession) Read(p []byte) (int, error) {
	if f.done {
		return 0, io.EOF
	}
	f.done = true
	n := copy(p, f.data)
	return n, nil
}
func (f *fakeReadSession) Write(p []byte) (int, error)    { return len(p), nil }
func (f *fakeReadSession) Close()                         {}
func (f *fakeReadSession) Resize(cols, rows uint16) error { return nil }

func TestIsPing(t *testing.T) {
	if !isPing([]byte(`{"type":"ping"}`)) {
		t.Fatal("expected ping control message")
	}
	if isPing([]byte(`{"type":"paste-file","filename":"ping"}`)) {
		t.Fatal("paste-file message misidentified as ping")
	}
}

func mustSpliceDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for splice shutdown")
	}
}

func TestSpliceConns(t *testing.T) {
	t.Run("binary and text", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer := mustWSConnPair(t)
		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		if err := dataClient.WriteMessage(websocket.BinaryMessage, []byte("from-data")); err != nil {
			t.Fatal(err)
		}
		mt, got := mustReadWSMessage(t, browserClient)
		if mt != websocket.BinaryMessage || !bytes.Equal(got, []byte("from-data")) {
			t.Fatalf("browser got %d %q", mt, got)
		}

		if err := browserClient.WriteMessage(websocket.TextMessage, []byte("from-browser")); err != nil {
			t.Fatal(err)
		}
		mt, got = mustReadWSMessage(t, dataClient)
		if mt != websocket.TextMessage || !bytes.Equal(got, []byte("from-browser")) {
			t.Fatalf("data got %d %q", mt, got)
		}

		_ = browserClient.Close()
		mustSpliceDone(t, done)
		_ = dataClient.Close()
	})

	t.Run("data close unwinds browser loop", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer := mustWSConnPair(t)
		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		_ = dataClient.Close()
		mustSpliceDone(t, done)
		_ = browserClient.Close()
	})

	t.Run("ping answered locally and forwarded", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer := mustWSConnPair(t)
		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		if err := browserClient.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
			t.Fatal(err)
		}
		// Fast local ack reaches the browser immediately.
		mt, got := mustReadWSMessage(t, browserClient)
		if mt != websocket.TextMessage || !bytes.Equal(got, pongFrame) {
			t.Fatalf("browser got %d %q, want pong", mt, got)
		}
		// The ping is also forwarded to the peer data conn so the host echoes
		// a pong back, keeping the hub<->host link alive on idle terminals.
		mt, got = mustReadWSMessage(t, dataClient)
		if mt != websocket.TextMessage || !bytes.Equal(got, []byte(`{"type":"ping"}`)) {
			t.Fatalf("data conn got %d %q, want forwarded ping", mt, got)
		}

		_ = browserClient.Close()
		mustSpliceDone(t, done)
		_ = dataClient.Close()
	})
}

func mustWSConnPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ch <- conn
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http://", "ws://", 1)+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	server := <-ch
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func mustReadWSMessage(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	mt, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return mt, got
}

// compressedUpgrader is a WebSocket upgrader with EnableCompression.
var compressedUpgrader = websocket.Upgrader{
	CheckOrigin:       CheckSameOrigin,
	ReadBufferSize:    1024,
	WriteBufferSize:   1024 * 16,
	EnableCompression: true,
}

// mustWSConnPairCompressed returns a client/server pair where both sides
// negotiate permessage-deflate. The server enables write compression; the
// client leaves it disabled (viewer role). The returned response is from the
// client dial handshake and can be used to verify compression negotiation.
func mustWSConnPairCompressed(t *testing.T) (*websocket.Conn, *websocket.Conn, *http.Response) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := compressedUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.EnableWriteCompression(false)
		ch <- conn
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dialer := &websocket.Dialer{HandshakeTimeout: 5 * time.Second, EnableCompression: true}
	client, resp, err := dialer.Dial(strings.Replace(srv.URL, "http://", "ws://", 1)+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	client.EnableWriteCompression(false)
	server := <-ch
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server, resp
}

func TestSpliceConnsCompression(t *testing.T) {
	t.Run("large compressed frame traverses splice losslessly", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer, resp := mustWSConnPairCompressed(t)

		// Prove permessage-deflate was negotiated in the handshake response.
		if ext := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(ext, "permessage-deflate") {
			t.Fatalf("Sec-WebSocket-Extensions missing permessage-deflate: %q", ext)
		}

		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		// Host side (dataClient) enables write compression with BestSpeed.
		// PTY output flows host→viewer, so this is the compressed direction.
		// Set compression level before EnableWriteCompression per gorilla/websocket API.
		if err := dataClient.SetCompressionLevel(flate.BestSpeed); err != nil {
			t.Fatalf("set compression level: %v", err)
		}
		dataClient.EnableWriteCompression(true)

		// Send a large repetitive frame from the host through the compressed channel.
		payload := bytes.Repeat([]byte("X"), 65536)
		if err := dataClient.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			t.Fatal(err)
		}
		mt, got := mustReadWSMessage(t, browserClient)
		if mt != websocket.BinaryMessage || !bytes.Equal(got, payload) {
			t.Fatalf("browser got mt=%d len=%d", mt, len(got))
		}

		// Browser reply (keystrokes) flows uncompressed through the viewer.
		reply := []byte("keystroke")
		if err := browserClient.WriteMessage(websocket.TextMessage, reply); err != nil {
			t.Fatal(err)
		}
		mt, got = mustReadWSMessage(t, dataClient)
		if mt != websocket.TextMessage || !bytes.Equal(got, reply) {
			t.Fatalf("data client got mt=%d len=%d", mt, len(got))
		}

		_ = browserClient.Close()
		mustSpliceDone(t, done)
	})

	t.Run("viewer writes stay uncompressed", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer, _ := mustWSConnPairCompressed(t)
		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		// DataServer (viewer) explicitly disables write compression,
		// matching serveViewerPerStream behavior.
		dataServer.EnableWriteCompression(false)

		// Browser input flows through to host uncompressed.
		reply := []byte("ls -la\n")
		if err := browserClient.WriteMessage(websocket.TextMessage, reply); err != nil {
			t.Fatal(err)
		}
		mt, got := mustReadWSMessage(t, dataClient)
		if mt != websocket.TextMessage || !bytes.Equal(got, reply) {
			t.Fatalf("data client got mt=%d len=%d", mt, len(got))
		}

		_ = browserClient.Close()
		mustSpliceDone(t, done)
	})

	t.Run("compression-negotiated data close unwinds browser loop", func(t *testing.T) {
		browserClient, browserServer := mustWSConnPair(t)
		dataClient, dataServer, _ := mustWSConnPairCompressed(t)
		done := make(chan struct{})
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		go func() {
			SpliceConns(browserServer, dataServer, logrus.NewEntry(logger))
			close(done)
		}()

		_ = dataClient.Close()
		mustSpliceDone(t, done)
		_ = browserClient.Close()
	})
}
