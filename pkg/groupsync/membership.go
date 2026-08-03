package groupsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// MemberKeys extracts the unique set of leaf session keys from a pane tree.
//
// Only objects whose "type" field equals "leaf" are inspected; their
// "sessionKey" field must be a string. Duplicates are removed and the result
// is sorted lexicographically.
func MemberKeys(tree json.RawMessage) ([]string, error) {
	if len(tree) == 0 {
		return nil, nil
	}
	var root any
	if err := json.Unmarshal(tree, &root); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	if err := collectMemberKeys(root, seen); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// MembershipFingerprint returns a deterministic fingerprint for the
// membership of a pane tree plus the sorted list of keys used to compute it.
//
// The fingerprint is the lowercase hex encoding of the SHA-256 digest of the
// sorted member keys, each followed by a single newline byte ('\n'). Trees
// that differ only in split direction, ratio, nesting depth, or leaf order
// share the same fingerprint when their normalized leaf sets are identical.
func MembershipFingerprint(tree json.RawMessage) (string, []string, error) {
	keys, err := MemberKeys(tree)
	if err != nil {
		return "", nil, err
	}
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), keys, nil
}

func collectMemberKeys(v any, seen map[string]struct{}) error {
	switch x := v.(type) {
	case map[string]any:
		t, ok := x["type"].(string)
		if ok && t == "leaf" {
			sk, ok := x["sessionKey"].(string)
			if !ok {
				return errors.New("leaf node missing string sessionKey")
			}
			seen[sk] = struct{}{}
			return nil
		}
		for _, child := range x {
			if err := collectMemberKeys(child, seen); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := collectMemberKeys(child, seen); err != nil {
				return err
			}
		}
	}
	return nil
}
