package pty

import (
	"bytes"
	"testing"
)

// naiveRingBuffer implements the original per-byte ring algorithm for
// differential verification.
type naiveRingBuffer struct {
	buf  []byte
	head int
	tail int
	size int
}

func newNaiveRingBuffer(capacity int) *naiveRingBuffer {
	return &naiveRingBuffer{buf: make([]byte, capacity)}
}

func (r *naiveRingBuffer) Write(data []byte) {
	for _, b := range data {
		r.buf[r.head] = b
		r.head = (r.head + 1) % len(r.buf)
		if r.size < len(r.buf) {
			r.size++
		} else {
			r.tail = (r.tail + 1) % len(r.buf)
		}
	}
}

func (r *naiveRingBuffer) Snapshot() []byte {
	if r.size == 0 {
		return nil
	}
	out := make([]byte, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(r.tail+i)%len(r.buf)]
	}
	return out
}

func TestRingBuffer_Differential(t *testing.T) {
	const cap = 64
	n := newNaiveRingBuffer(cap)
	r := newRingBuffer(cap)

	inputs := [][]byte{
		[]byte("hello world"),
		[]byte("\x1b[31mred\x1b[0m"),
		[]byte("this is a longer string that should wrap around the buffer boundary just fine"),
		bytes.Repeat([]byte("a"), cap*3),
		[]byte("tail"),
		bytes.Repeat([]byte("b"), cap),
		bytes.Repeat([]byte("c"), cap-1),
		bytes.Repeat([]byte("d"), cap+1),
	}

	for i, in := range inputs {
		n.Write(in)
		r.Write(in)

		want := n.Snapshot()
		got := r.Snapshot()
		if !bytes.Equal(got, want) {
			t.Fatalf("input %d: snapshot mismatch\ngot (%d): %q\nwant (%d): %q", i, len(got), got, len(want), want)
		}
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	r := newRingBuffer(16)
	if got := r.Snapshot(); got != nil {
		t.Fatalf("empty snapshot should be nil, got %q", got)
	}
}

func TestRingBuffer_ExactFill(t *testing.T) {
	const cap = 8
	r := newRingBuffer(cap)
	data := []byte("abcdefgh")
	r.Write(data)
	if got := string(r.Snapshot()); got != string(data) {
		t.Fatalf("exact fill snapshot = %q, want %q", got, data)
	}

	// Overwrite exactly one byte around the boundary.
	r.Write([]byte("X"))
	want := "bcdefghX"
	if got := string(r.Snapshot()); got != want {
		t.Fatalf("overwrite snapshot = %q, want %q", got, want)
	}
}

func TestRingBuffer_OversizedWrite(t *testing.T) {
	const cap = 8
	r := newRingBuffer(cap)
	r.Write([]byte("12345678"))
	r.Write([]byte("abcdefghi"))
	want := "bcdefghi"
	if got := string(r.Snapshot()); got != want {
		t.Fatalf("oversized write snapshot = %q, want %q", got, want)
	}
}
