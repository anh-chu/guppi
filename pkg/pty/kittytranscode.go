package pty

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

const kittyMaxRead = 100 << 20

// kittyTranscoder turns kitty file-backed image transmissions into direct
// transmissions, which the browser terminal can consume.
type kittyTranscoder struct {
	carry []byte
	log   *logrus.Entry
}

func (t *kittyTranscoder) Feed(in []byte) []byte {
	if len(t.carry) == 0 && !bytesHasAPC(in) && !bytesHasPartialAPC(in) {
		return in
	}

	data := in
	if len(t.carry) != 0 {
		data = make([]byte, len(t.carry)+len(in))
		copy(data, t.carry)
		copy(data[len(t.carry):], in)
		t.carry = nil
	}

	out := make([]byte, 0, len(data))
	textStart := 0
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			i++
			continue
		}
		if i+1 >= len(data) {
			out = append(out, data[textStart:i]...)
			t.carry = append(t.carry[:0], data[i:]...)
			return out
		}
		if data[i+1] != '_' {
			i++
			continue
		}
		if i+2 >= len(data) {
			out = append(out, data[textStart:i]...)
			t.carry = append(t.carry[:0], data[i:]...)
			return out
		}
		if data[i+2] != 'G' {
			i += 2
			continue
		}

		term, termLen := kittyTerminator(data, i+3)
		if term < 0 {
			out = append(out, data[textStart:i]...)
			t.carry = append(t.carry[:0], data[i:]...)
			return out
		}
		out = append(out, data[textStart:i]...)
		command := data[i : term+termLen]
		out = append(out, t.transcode(command)...)
		i = term + termLen
		textStart = i
	}
	out = append(out, data[textStart:]...)
	return out
}

func bytesHasAPC(data []byte) bool {
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0x1b && data[i+1] == '_' && data[i+2] == 'G' {
			return true
		}
	}
	return false
}

func bytesHasPartialAPC(data []byte) bool {
	if len(data) == 0 || data[len(data)-1] != 0x1b {
		return len(data) >= 2 && data[len(data)-2] == 0x1b && data[len(data)-1] == '_'
	}
	return true
}

func kittyTerminator(data []byte, start int) (int, int) {
	for i := start; i < len(data); i++ {
		if data[i] == 0x07 {
			return i, 1
		}
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i, 2
		}
	}
	return -1, 0
}

func (t *kittyTranscoder) transcode(command []byte) []byte {
	if len(command) < 5 {
		return command
	}
	term, termLen := kittyTerminator(command, 3)
	if term < 0 {
		return command
	}
	body := command[3:term]
	semi := bytes.IndexByte(body, ';')
	if semi < 0 {
		return command
	}
	controlRaw := string(body[:semi])
	payload := body[semi+1:]
	medium := controlValue(controlRaw, "t")
	if medium == "" || medium == "d" {
		return command
	}
	if medium != "f" && medium != "t" && medium != "s" {
		return command
	}

	action := controlValue(controlRaw, "a")
	if action == "q" {
		control, ok := rewriteMediumControl(controlRaw)
		if !ok {
			return command
		}
		return kittyCommand(control, payload, command[term:term+termLen])
	}
	if action != "" && action != "t" && action != "T" && action != "f" {
		return command
	}

	name, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil {
		t.debug("decode kitty transmission path", err)
		return command
	}
	data, err := readKittyData(medium, string(name), controlRaw)
	if err != nil {
		t.debug("read kitty transmission data", err)
		return command
	}
	control, ok := rewriteControl(controlRaw)
	if !ok {
		return command
	}
	return kittyDataCommands(control, data)
}

func controlValue(raw, key string) string {
	for _, pair := range strings.Split(raw, ",") {
		if eq := strings.IndexByte(pair, '='); eq >= 0 && pair[:eq] == key {
			return pair[eq+1:]
		}
	}
	return ""
}

func rewriteMediumControl(raw string) (string, bool) {
	parts := strings.Split(raw, ",")
	found := false
	for i, pair := range parts {
		eq := strings.IndexByte(pair, '=')
		if eq >= 0 && pair[:eq] == "t" {
			parts[i] = "t=d"
			found = true
		}
	}
	return strings.Join(parts, ","), found
}

func rewriteControl(raw string) (string, bool) {
	parts := strings.Split(raw, ",")
	found := false
	kept := make([]string, 0, len(parts))
	for _, pair := range parts {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			kept = append(kept, pair)
			continue
		}
		key := pair[:eq]
		switch key {
		case "t":
			kept = append(kept, "t=d")
			found = true
		case "S", "O", "m":
			// Replaced or consumed by the direct transmission.
		default:
			kept = append(kept, pair)
		}
	}
	return strings.Join(kept, ","), found
}

func kittyCommand(control string, payload []byte, terminator []byte) []byte {
	out := make([]byte, 0, len(control)+len(payload)+8)
	out = append(out, 0x1b, '_', 'G')
	out = append(out, control...)
	out = append(out, ';')
	out = append(out, payload...)
	out = append(out, terminator...)
	return out
}

func kittyDataCommands(control string, data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	if len(encoded) <= 4096 {
		return kittyCommand(control, []byte(encoded), []byte{0x1b, '\\'})
	}

	out := make([]byte, 0, len(encoded)+len(encoded)/4096*16)
	first := control + ",m=1"
	for start := 0; start < len(encoded); start += 4096 {
		end := start + 4096
		if end > len(encoded) {
			end = len(encoded)
		}
		if start == 0 {
			out = append(out, kittyCommand(first, []byte(encoded[start:end]), []byte{0x1b, '\\'})...)
		} else if end == len(encoded) {
			out = append(out, kittyCommand("m=0", []byte(encoded[start:end]), []byte{0x1b, '\\'})...)
		} else {
			out = append(out, kittyCommand("m=1", []byte(encoded[start:end]), []byte{0x1b, '\\'})...)
		}
	}
	return out
}

func readKittyData(medium, name, control string) ([]byte, error) {
	path := name
	if medium == "s" {
		path = filepath.Join("/dev/shm", strings.TrimPrefix(name, "/"))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	offset, err := kittyNumber(control, "O", 0)
	if err != nil || offset < 0 {
		return nil, fmt.Errorf("invalid kitty offset")
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, err
	}

	size, err := kittySize(file, control, offset)
	if err != nil {
		return nil, err
	}
	if size > kittyMaxRead {
		return nil, fmt.Errorf("kitty transmission exceeds %d bytes", kittyMaxRead)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, err
	}
	if medium == "t" {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func kittyNumber(control, key string, dflt int64) (int64, error) {
	value := controlValue(control, key)
	if value == "" {
		return dflt, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func kittySize(file *os.File, control string, offset int64) (int, error) {
	value := controlValue(control, "S")
	if value != "" {
		size, err := strconv.ParseInt(value, 10, 64)
		if err != nil || size < 0 || size > kittyMaxRead {
			return 0, fmt.Errorf("invalid kitty size")
		}
		return int(size), nil
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	remaining := info.Size() - offset
	if remaining < 0 || remaining > kittyMaxRead {
		return 0, fmt.Errorf("invalid kitty file size")
	}
	return int(remaining), nil
}

func (t *kittyTranscoder) debug(message string, err error) {
	if t.log != nil {
		t.log.WithError(err).Debug(message)
	}
}
