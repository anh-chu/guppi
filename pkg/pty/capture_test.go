package pty

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureServer returns a connected net.Pipe pair and a goroutine that reads
// FrameQueryBuffer and replies with a FrameReplay containing payload.
func captureServer(t *testing.T, payload []byte) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		header := make([]byte, 5)
		if _, err := io.ReadFull(server, header); err != nil {
			return
		}
		if header[0] != FrameQueryBuffer {
			return
		}
		frame := encodeFrame(FrameReplay, payload)
		_, _ = server.Write(frame)
	}()
	return client
}

// captureServerSlow returns a server that replies with payload one byte per
// interval after the header, to exercise read deadlines.
func captureServerSlow(t *testing.T, payload []byte, interval time.Duration) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		header := make([]byte, 5)
		if _, err := io.ReadFull(server, header); err != nil {
			return
		}
		if header[0] != FrameQueryBuffer {
			return
		}
		h := make([]byte, 5)
		h[0] = FrameReplay
		binary.BigEndian.PutUint32(h[1:5], uint32(len(payload)))
		if _, err := server.Write(h); err != nil {
			return
		}
		for _, b := range payload {
			if _, err := server.Write([]byte{b}); err != nil {
				return
			}
			time.Sleep(interval)
		}
	}()
	return client
}

// TestCapture_FullOutputUnchanged verifies Capture still returns the original
// cleaned payload after the refactor.
func TestCapture_FullOutputUnchanged(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := "hello world\n\x1b[31mprompt\x1b[0m\rmore"
	payloadBytes := []byte(payload)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		frame := encodeFrame(FrameReplay, payloadBytes)
		_, _ = conn.Write(frame)
	}()

	reg := NewRegistry(dir)
	got, err := reg.Capture("test")
	if err != nil {
		t.Fatalf("Capture error: %v", err)
	}

	want := "hello world\nprompt\rmore"
	if got != want {
		t.Fatalf("Capture = %q, want %q", got, want)
	}
}

// TestCaptureTail_SmallTailEqualsFull verifies that when maxBytes covers the
// payload CaptureTail returns the same content as Capture.
func TestCaptureTail_SmallTailEqualsFull(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "small.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := "line one\nline two\nline three"
	payloadBytes := []byte(payload)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			header := make([]byte, 5)
			_, _ = io.ReadFull(conn, header)
			frame := encodeFrame(FrameReplay, payloadBytes)
			_, _ = conn.Write(frame)
			_ = conn.Close()
		}
	}()

	reg := NewRegistry(dir)
	full, err := reg.Capture("small")
	if err != nil {
		t.Fatalf("Capture error: %v", err)
	}
	tail, err := reg.CaptureTail("small", len(payload)*2)
	if err != nil {
		t.Fatalf("CaptureTail error: %v", err)
	}
	if tail != full {
		t.Fatalf("CaptureTail != Capture: tail=%q, full=%q", tail, full)
	}
}

// TestCaptureTail_TailExcludesEarlyData uses a real unix socket to send a
// known multi-line payload and confirms CaptureTail returns only the trailing
// portion plus still ends with the final prompt line.
func TestCaptureTail_TailExcludesEarlyData(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "tail.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	lines := []string{}
	for i := 0; i < 20; i++ {
		if i == 0 {
			lines = append(lines, "EARLY-MARKER-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		} else {
			lines = append(lines, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		}
	}
	lines = append(lines, "final prompt> ")
	payload := []byte(strings.Join(lines, "\n"))

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		frame := encodeFrame(FrameReplay, payload)
		_, _ = conn.Write(frame)
	}()

	reg := NewRegistry(dir)
	got, err := reg.CaptureTail("tail", 80)
	if err != nil {
		t.Fatalf("CaptureTail error: %v", err)
	}
	if !strings.HasSuffix(got, "final prompt> ") {
		t.Fatalf("CaptureTail missing final prompt: %q", got)
	}
	if strings.Contains(got, lines[0]) {
		t.Fatalf("CaptureTail included early data: %q", got)
	}
}

// TestCaptureTail_UnexpectedFrame verifies CaptureTail rejects non-replay frames.
func TestCaptureTail_UnexpectedFrame(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bad.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		frame := encodeFrame(FrameOutput, []byte("live"))
		_, _ = conn.Write(frame)
	}()

	reg := NewRegistry(dir)
	_, err = reg.CaptureTail("bad", 100)
	if err == nil {
		t.Fatal("expected error for unexpected frame")
	}
	if !strings.Contains(err.Error(), "unexpected frame type") {
		t.Fatalf("expected unexpected frame error, got: %v", err)
	}
}

// TestCaptureTail_DialError verifies a missing socket returns an error.
func TestCaptureTail_DialError(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.CaptureTail("missing", 100)
	if err == nil {
		t.Fatal("expected dial error")
	}
	if !strings.Contains(err.Error(), "dial daemon socket") {
		t.Fatalf("expected dial error, got: %v", err)
	}
}

// TestCapture_TimeoutOnSilentDaemon verifies Capture returns ErrCaptureTimeout
// when the daemon never finishes sending the payload.
func TestCapture_TimeoutOnSilentDaemon(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "slow.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := []byte(strings.Repeat("x", 1000))
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		h := make([]byte, 5)
		h[0] = FrameReplay
		binary.BigEndian.PutUint32(h[1:5], uint32(len(payload)))
		_, _ = conn.Write(h)
		for _, b := range payload {
			if _, err := conn.Write([]byte{b}); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	reg := NewRegistry(dir)
	start := time.Now()
	_, err = reg.Capture("slow")
	dur := time.Since(start)
	if !errors.Is(err, ErrCaptureTimeout) {
		t.Fatalf("expected ErrCaptureTimeout, got: %v", err)
	}
	if dur < 5*time.Second || dur > 12*time.Second {
		t.Fatalf("timeout duration out of range: %v", dur)
	}
}

// TestCaptureTail_DropsPartialANSIAndUTF8 verifies that a tail cut mid-ANSI
// and mid-UTF8 multibyte sequence is cleaned safely.
func TestCaptureTail_DropsPartialANSIAndUTF8(t *testing.T) {
	// Using a small tail window should cut the payload in the middle of both
	// an ANSI sequence and a UTF-8 continuation region.
	dir := t.TempDir()
	sock := filepath.Join(dir, "cut.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	prefix := strings.Repeat("a", 200)
	sequence := []byte("\n" + prefix + "\x1b[31m" + strings.Repeat("é", 40) + "\x1b[0m\n")
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		frame := encodeFrame(FrameReplay, sequence)
		_, _ = conn.Write(frame)
	}()

	reg := NewRegistry(dir)
	got, err := reg.CaptureTail("cut", 30)
	if err != nil {
		t.Fatalf("CaptureTail error: %v", err)
	}
	// Should not panic, should not contain raw ANSI bytes.
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI byte leaked into tail: %q", got)
	}
	if strings.Contains(got, "\x80") || strings.Contains(got, "\xc3") {
		// UTF-8 continuation bytes leaked.
		t.Fatalf("raw UTF-8 continuation leaked: %q", got)
	}
}

// TestCaptureTail_EmptyPayload verifies tail capture handles zero-length replay.
func TestCaptureTail_EmptyPayload(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "empty.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		_, _ = io.ReadFull(conn, header)
		frame := encodeFrame(FrameReplay, nil)
		_, _ = conn.Write(frame)
	}()

	reg := NewRegistry(dir)
	got, err := reg.CaptureTail("empty", 100)
	if err != nil {
		t.Fatalf("CaptureTail error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// TestCleanCapture_DropsMidLineStart verifies cleanCapture removes the first
// partial line from a tail cut.
func TestCleanCapture_DropsMidLineStart(t *testing.T) {
	reg := &Registry{}
	payload := []byte("rld\nfull line one\nfull line two")
	got := reg.cleanCapture(payload)
	want := "full line one\nfull line two"
	if got != want {
		t.Fatalf("cleanCapture = %q, want %q", got, want)
	}
}

// TestCleanCapture_DropsPartialUTF8 verifies cleanCapture drops continuation bytes.
func TestCleanCapture_DropsPartialUTF8(t *testing.T) {
	reg := &Registry{}
	// UTF-8 for é is 0xc3 0xa9. Cut after 0xc3 leaves 0xa9 0xa9 ...
	payload := []byte{0xa9, 0xa9, '\n', 'a', 'b', 'c'}
	got := reg.cleanCapture(payload)
	if got != "\nabc" {
		t.Fatalf("cleanCapture = %q, want %q", got, "\nabc")
	}
}

// TestCapture_LegacyCleaningUnchanged confirms Capture still does not drop the
// first line, preserving historical behavior for full captures.
func TestCapture_LegacyCleaningUnchanged(t *testing.T) {
	reg := &Registry{}
	payload := []byte("rld\nfull line")
	got := reg.legacyCleanCapture(payload)
	want := "rld\nfull line"
	if got != want {
		t.Fatalf("legacyCleanCapture = %q, want %q", got, want)
	}
}

// Ensure no conflict with os import from unused.
var _ = os.IsNotExist
