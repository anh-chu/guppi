package ws

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type recordedFrame struct {
	messageType int
	payload     []byte
}

type recordingOutputWriter struct {
	frames []recordedFrame
	err    error
	wrote  chan struct{}
}

func newRecordingOutputWriter() *recordingOutputWriter {
	return &recordingOutputWriter{wrote: make(chan struct{}, 16)}
}

func (w *recordingOutputWriter) Write(messageType int, payload []byte) error {
	w.frames = append(w.frames, recordedFrame{
		messageType: messageType,
		payload:     bytes.Clone(payload),
	})
	w.wrote <- struct{}{}
	return w.err
}

type manualOutputTimer struct {
	ticks  chan time.Time
	resetC chan struct{}
	resets int
	stops  int
}

func newManualOutputTimer() *manualOutputTimer {
	return &manualOutputTimer{ticks: make(chan time.Time, 1), resetC: make(chan struct{}, 16)}
}

func (t *manualOutputTimer) Chan() <-chan time.Time {
	return t.ticks
}

func (t *manualOutputTimer) Reset(time.Duration) {
	t.resets++
	t.resetC <- struct{}{}
}

func (t *manualOutputTimer) Stop() {
	t.stops++
}

func (t *manualOutputTimer) Flush() {
	t.ticks <- time.Time{}
}

func TestOutputCoalescerPreservesOrderUntilTrailingFlush(t *testing.T) {
	writer := newRecordingOutputWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, timer)

	coalescer.Submit([]byte("first"))
	coalescer.Submit([]byte("-second"))
	coalescer.Submit([]byte("-third"))
	for range 3 {
		<-timer.resetC
	}
	timer.Flush()
	<-writer.wrote

	if len(writer.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(writer.frames))
	}
	frame := writer.frames[0]
	if frame.messageType != websocket.BinaryMessage {
		t.Fatalf("message type = %d, want binary", frame.messageType)
	}
	if got, want := string(frame.payload), "first-second-third"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if timer.resets != 3 {
		t.Fatalf("timer resets = %d, want 3", timer.resets)
	}

	coalescer.CloseAndFlush()
}

func TestOutputCoalescerFlushesAt64KiB(t *testing.T) {
	writer := newRecordingOutputWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, timer)

	coalescer.Submit(bytes.Repeat([]byte("a"), 32*1024))
	coalescer.Submit(bytes.Repeat([]byte("b"), 32*1024))
	<-writer.wrote

	if len(writer.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(writer.frames))
	}
	if got := len(writer.frames[0].payload); got != 64*1024 {
		t.Fatalf("frame size = %d, want %d", got, 64*1024)
	}
	if got := writer.frames[0].payload[:4]; !bytes.Equal(got, []byte("aaaa")) {
		t.Fatalf("frame starts with %q, want a bytes", got)
	}
	if got := writer.frames[0].payload[len(writer.frames[0].payload)-4:]; !bytes.Equal(got, []byte("bbbb")) {
		t.Fatalf("frame ends with %q, want b bytes", got)
	}
	if timer.stops == 0 {
		t.Fatal("expected timer stop for immediate flush")
	}

	coalescer.CloseAndFlush()
	stats := coalescer.Stats()
	if stats.frames != 1 || stats.bytes != 64*1024 || stats.maxFrame != 64*1024 {
		t.Fatalf("stats = %+v, want one 64KiB frame", stats)
	}
}

func TestOutputCoalescerSerializesPongWithOutput(t *testing.T) {
	writer := newRecordingOutputWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, timer)

	coalescer.Submit([]byte("output"))
	<-timer.resetC
	if !coalescer.RequestPong() {
		t.Fatal("pong request rejected")
	}
	<-writer.wrote
	timer.Flush()
	<-writer.wrote
	coalescer.CloseAndFlush()

	if len(writer.frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(writer.frames))
	}
	if got := writer.frames[0]; got.messageType != websocket.TextMessage || !bytes.Equal(got.payload, pongFrame) {
		t.Fatalf("first frame = (%d, %q), want pong", got.messageType, got.payload)
	}
	if got := writer.frames[1]; got.messageType != websocket.BinaryMessage || string(got.payload) != "output" {
		t.Fatalf("second frame = (%d, %q), want output", got.messageType, got.payload)
	}
}

func TestOutputCoalescerSignalsFirstWriteFailureAndStopsCleanly(t *testing.T) {
	writer := newRecordingOutputWriter()
	writer.err = errors.New("write failed")
	timer := newManualOutputTimer()
	var callbackCount int
	coalescer := newOutputCoalescer(writer.Write, func(error) {
		callbackCount++
	}, timer)

	coalescer.Submit([]byte("first"))
	<-timer.resetC
	timer.Flush()
	<-writer.wrote
	coalescer.Submit([]byte("second"))
	coalescer.CloseAndFlush()

	if callbackCount != 1 {
		t.Fatalf("write error callback count = %d, want 1", callbackCount)
	}
	if len(writer.frames) != 1 {
		t.Fatalf("write attempts = %d, want 1", len(writer.frames))
	}
	select {
	case <-coalescer.Done():
	default:
		t.Fatal("coalescer did not stop")
	}
}

func TestOutputCoalescerCloseFlushesAcceptedChunk(t *testing.T) {
	writer := newRecordingOutputWriter()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, newManualOutputTimer())

	coalescer.Submit([]byte("pending"))
	coalescer.CloseAndFlush()

	if len(writer.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(writer.frames))
	}
	if got := string(writer.frames[0].payload); got != "pending" {
		t.Fatalf("payload = %q, want pending", got)
	}
}

type blockingWriter struct {
	blocked chan struct{}
	unblock chan struct{}
	written chan struct{}
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		blocked: make(chan struct{}, 1),
		unblock: make(chan struct{}),
		written: make(chan struct{}, 16),
	}
}

func (w *blockingWriter) Write(mt int, payload []byte) error {
	select {
	case w.blocked <- struct{}{}:
	default:
	}
	<-w.unblock
	w.written <- struct{}{}
	return nil
}

func TestOutputCoalescerSerializesControlWithOutput(t *testing.T) {
	writer := newRecordingOutputWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, timer)

	if !coalescer.SubmitControl(replayStartJSON) {
		t.Fatal("control start rejected")
	}
	coalescer.Submit([]byte("replay"))
	<-timer.resetC
	if !coalescer.SubmitControl(replayEndJSON) {
		t.Fatal("control end rejected")
	}
	coalescer.Submit([]byte("live"))
	<-timer.resetC
	coalescer.CloseAndFlush()

	if len(writer.frames) != 4 {
		t.Fatalf("frames = %d, want 4", len(writer.frames))
	}
	want := []recordedFrame{
		{messageType: websocket.TextMessage, payload: replayStartJSON},
		{messageType: websocket.BinaryMessage, payload: []byte("replay")},
		{messageType: websocket.TextMessage, payload: replayEndJSON},
		{messageType: websocket.BinaryMessage, payload: []byte("live")},
	}
	for i, w := range want {
		got := writer.frames[i]
		if got.messageType != w.messageType || !bytes.Equal(got.payload, w.payload) {
			t.Fatalf("frame %d = (%d, %q), want (%d, %q)", i, got.messageType, got.payload, w.messageType, w.payload)
		}
	}
}

func TestOutputCoalescerControlFlushesImmediately(t *testing.T) {
	writer := newRecordingOutputWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, timer)

	coalescer.Submit([]byte("buffered"))
	<-timer.resetC
	coalescer.SubmitControl(replayStartJSON)
	<-writer.wrote

	// The control frame must flush any buffered binary out first so ordering
	// is preserved: buffered binary is emitted before the text control.
	if len(writer.frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(writer.frames))
	}
	if got := writer.frames[0]; got.messageType != websocket.BinaryMessage || !bytes.Equal(got.payload, []byte("buffered")) {
		t.Fatalf("first frame = (%d, %q), want buffered binary", got.messageType, got.payload)
	}
	if got := writer.frames[1]; got.messageType != websocket.TextMessage || !bytes.Equal(got.payload, replayStartJSON) {
		t.Fatalf("second frame = (%d, %q), want replay-start text", got.messageType, got.payload)
	}

	coalescer.CloseAndFlush()
}

func TestSubmitControlAfterClose(t *testing.T) {
	writer := newRecordingOutputWriter()
	coalescer := newOutputCoalescer(writer.Write, func(error) {}, newManualOutputTimer())

	coalescer.CloseAndFlush()
	if coalescer.SubmitControl(replayStartJSON) {
		t.Fatal("SubmitControl after CloseAndFlush returned true, want false")
	}
}

func TestRequestPongBlocksWhenQueueFull(t *testing.T) {
	bw := newBlockingWriter()
	timer := newManualOutputTimer()
	coalescer := newOutputCoalescer(bw.Write, func(error) {}, timer)

	// Submit a chunk that will eventually reach writeFrame and block the writer.
	coalescer.Submit(bytes.Repeat([]byte("x"), 64*1024))

	// Flush the timer so the chunk is written (writer blocks inside writeFrame).
	timer.Flush()
	// Wait until the writer is actually blocked.
	<-bw.blocked

	// Now the run loop is stalled inside writeFrame. Fill pong queue to capacity (4).
	for i := 0; i < 4; i++ {
		if !coalescer.RequestPong() {
			t.Fatalf("pong %d rejected", i)
		}
	}

	// The 5th pong must block because the run loop is stalled and queue is full.
	fifthDone := make(chan bool, 1)
	fifthStarted := make(chan struct{})
	go func() {
		close(fifthStarted)
		fifthDone <- coalescer.RequestPong()
	}()
	<-fifthStarted

	select {
	case <-fifthDone:
		t.Fatal("5th RequestPong returned without blocking — pong was dropped")
	default:
	}

	// Unblock the writer so the run loop can process pongs, freeing capacity.
	bw.unblock <- struct{}{}
	<-bw.written // wait for the frame to complete

	// The 5th RequestPong must now be unblocked and succeed.
	if !<-fifthDone {
		t.Fatal("5th RequestPong returned false after unblock")
	}

	// Let the coalescer drain and finish.
	close(bw.unblock)
	coalescer.CloseAndFlush()
}
