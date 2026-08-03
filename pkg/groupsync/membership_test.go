package groupsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func fp(keys []string) string {
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestMemberKeysSimpleSplit(t *testing.T) {
	tree := json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`)
	keys, err := MemberKeys(tree)
	if err != nil {
		t.Fatalf("MemberKeys: %v", err)
	}
	want := []string{"a", "b"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
	fpOut, keys2, err := MembershipFingerprint(tree)
	if err != nil {
		t.Fatalf("MembershipFingerprint: %v", err)
	}
	if fpOut != fp(want) {
		t.Fatalf("fingerprint = %s, want %s", fpOut, fp(want))
	}
	if len(keys2) != len(want) {
		t.Fatalf("returned keys = %#v", keys2)
	}
}

func TestMemberKeysDedupeAndSort(t *testing.T) {
	tree := json.RawMessage(`[
		{"type":"leaf","sessionKey":"c"},
		{"type":"leaf","sessionKey":"a"},
		{"type":"leaf","sessionKey":"b"},
		{"type":"leaf","sessionKey":"a"}
	]`)
	keys, err := MemberKeys(tree)
	if err != nil {
		t.Fatalf("MemberKeys: %v", err)
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if i >= len(keys) || keys[i] != want[i] {
			t.Fatalf("keys = %#v, want %#v", keys, want)
		}
	}
}

func TestMembershipFingerprintStructureInvariance(t *testing.T) {
	// Same four leaves arranged with different directions, ratios, and nesting.
	treeA := json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"x"},"second":{"type":"split","direction":"v","ratio":0.3,"first":{"type":"leaf","sessionKey":"y"},"second":{"type":"split","direction":"h","ratio":0.7,"first":{"type":"leaf","sessionKey":"z"},"second":{"type":"leaf","sessionKey":"w"}}}}`)
	treeB := json.RawMessage(`{"type":"split","direction":"v","ratio":0.9,"first":{"type":"split","direction":"h","ratio":0.2,"first":{"type":"leaf","sessionKey":"z"},"second":{"type":"leaf","sessionKey":"w"}},"second":{"type":"split","direction":"v","ratio":0.4,"first":{"type":"leaf","sessionKey":"y"},"second":{"type":"leaf","sessionKey":"x"}}}`)

	fpA, keysA, err := MembershipFingerprint(treeA)
	if err != nil {
		t.Fatalf("treeA: %v", err)
	}
	fpB, keysB, err := MembershipFingerprint(treeB)
	if err != nil {
		t.Fatalf("treeB: %v", err)
	}
	if len(keysA) != 4 || len(keysB) != 4 {
		t.Fatalf("unexpected key counts: %d, %d", len(keysA), len(keysB))
	}
	if fpA != fpB {
		t.Fatalf("fingerprint mismatch: %s vs %s", fpA, fpB)
	}
}

func TestMemberKeysMalformedJSON(t *testing.T) {
	_, err := MemberKeys(json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestMemberKeysMalformedLeaf(t *testing.T) {
	cases := []json.RawMessage{
		[]byte(`{"type":"leaf"}`),
		[]byte(`{"type":"leaf","sessionKey":123}`),
		[]byte(`{"type":"split","first":{"type":"leaf"}}`),
	}
	for _, c := range cases {
		_, err := MemberKeys(c)
		if err == nil {
			t.Fatalf("expected error for %s", c)
		}
		if !strings.Contains(err.Error(), "sessionKey") {
			t.Fatalf("error should mention sessionKey, got: %v", err)
		}
	}
}
