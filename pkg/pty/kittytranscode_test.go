package pty

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kittyTestCommand(control, payload string) []byte {
	return kittyCommand(control, []byte(payload), []byte{0x1b, '\\'})
}

func kittyPathCommand(control string, path string) []byte {
	return kittyTestCommand(control, base64.StdEncoding.EncodeToString([]byte(path)))
}

func TestKittyTranscoderFastPath(t *testing.T) {
	input := []byte("plain output")
	tr := &kittyTranscoder{}
	if got := tr.Feed(input); &got[0] != &input[0] || !bytes.Equal(got, input) {
		t.Fatal("plain output was not returned unchanged")
	}
}

func TestKittyTranscoderFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.bin")
	want := []byte("image bytes")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	input := append([]byte("before"), kittyPathCommand("a=T,f=100,t=f", path)...)
	input = append(input, []byte("after")...)
	got := (&kittyTranscoder{}).Feed(input)
	if !bytes.HasPrefix(got, []byte("before")) || !bytes.HasSuffix(got, []byte("after")) {
		t.Fatalf("surrounding output lost: %q", got)
	}
	if bytes.Contains(got, []byte("t=f")) || !bytes.Contains(got, []byte("t=d")) {
		t.Fatalf("medium not rewritten: %q", got)
	}
	payload := kittyPayloads(got)
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || !bytes.Equal(decoded, want) {
		t.Fatalf("payload = %q, want %q (err %v)", decoded, want, err)
	}
}

func TestKittyTranscoderTempOffsetAndSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "temp.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	control := "a=T,f=32,t=t,O=3,S=4"
	got := (&kittyTranscoder{}).Feed(kittyPathCommand(control, path))
	decoded, err := base64.StdEncoding.DecodeString(kittyPayloads(got))
	if err != nil || string(decoded) != "3456" {
		t.Fatalf("decoded = %q, err %v", decoded, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists, stat error %v", err)
	}
}

func TestKittyTranscoderSplitAndReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "split.bin")
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	command := kittyPathCommand("a=T,t=f", path)
	tr := &kittyTranscoder{}
	mid := len(command) - 1
	if got := tr.Feed(command[:mid]); len(got) != 0 {
		t.Fatalf("split first output = %q", got)
	}
	if got := tr.Feed(command[mid:]); !bytes.Contains(got, []byte("t=d")) {
		t.Fatalf("split command was not transcoded: %q", got)
	}
	missing := kittyPathCommand("a=T,t=f", filepath.Join(dir, "missing"))
	if got := (&kittyTranscoder{}).Feed(missing); !bytes.Equal(got, missing) {
		t.Fatalf("read error changed command: %q", got)
	}
}

func TestKittyTranscoderQuery(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("not-a-path"))
	input := kittyTestCommand("a=q,t=f", payload)
	got := (&kittyTranscoder{}).Feed(input)
	if !bytes.Equal(got, kittyTestCommand("a=q,t=d", payload)) {
		t.Fatalf("query = %q", got)
	}
}

func TestKittyTranscoderChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")
	want := bytes.Repeat([]byte("x"), 4097)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got := (&kittyTranscoder{}).Feed(kittyPathCommand("a=T,t=f", path))
	if strings.Count(string(got), "\x1b_G") < 2 || !bytes.Contains(got, []byte("m=1")) || !bytes.Contains(got, []byte("m=0")) {
		t.Fatalf("expected chunked output: %q", got[:min(len(got), 80)])
	}
	decoded, err := base64.StdEncoding.DecodeString(kittyPayloads(got))
	if err != nil || !bytes.Equal(decoded, want) {
		t.Fatalf("chunked payload mismatch: %v", err)
	}
}

func kittyPayloads(data []byte) string {
	var payload strings.Builder
	for i := 0; i+3 < len(data); {
		if data[i] != 0x1b || data[i+1] != '_' || data[i+2] != 'G' {
			i++
			continue
		}
		term, size := kittyTerminator(data, i+3)
		if term < 0 {
			break
		}
		body := data[i+3 : term]
		semi := bytes.IndexByte(body, ';')
		if semi >= 0 {
			payload.Write(body[semi+1:])
		}
		i = term + size
	}
	return payload.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
