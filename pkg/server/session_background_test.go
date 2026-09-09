package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/go-chi/chi/v5"
)

func TestSessionBackgroundingHappyPath(t *testing.T) {
	// Setup: real stores in temp directory
	home := t.TempDir()
	t.Setenv("HOME", home)

	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("NewGroupStore: %v", err)
	}
	attrsStore, err := sessionattrs.NewStore()
	if err != nil {
		t.Fatalf("NewAttrsStore: %v", err)
	}

	// Create a group with 2-leaf split containing session "sessionA"
	tree := []byte(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"sessionA"},"second":{"type":"leaf","sessionKey":"sessionB"}}`)
	if _, _, _, err := groupStore.SetTree("g1", tree); err != nil {
		t.Fatalf("SetTree: %v", err)
	}

	// Setup handler
	opts := &Options{
		AttrsStore: attrsStore,
		GroupStore: groupStore,
	}

	router := chi.NewRouter()
	registerSessionsRoutes(router, opts, nil)

	// POST /session-attrs with background=true
	body := map[string]interface{}{
		"key":        "sessionA",
		"background": true,
		"hidden":     false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/session-attrs", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify background bit set in attrs
	if sets := attrsStore.Sets(); len(sets.Background) != 1 || sets.Background[0] != "sessionA" {
		t.Fatalf("background sets = %#v", sets)
	}

	// Verify session removed from group tree
	g1, ok := groupStore.Get("g1")
	if !ok {
		t.Fatal("group g1 missing")
	}
	var updatedTree any
	if err := json.Unmarshal(g1.Tree, &updatedTree); err != nil {
		t.Fatalf("unmarshal tree: %v", err)
	}
	// After removing "sessionA" from the split, tree should collapse to just the "sessionB" leaf
	tree2, ok := updatedTree.(map[string]any)
	if !ok {
		t.Fatalf("tree is not map: %T", updatedTree)
	}
	if typ, ok := tree2["type"].(string); !ok || typ != "leaf" {
		t.Fatalf("collapsed tree type = %q, want leaf", typ)
	}
	if key, ok := tree2["sessionKey"].(string); !ok || key != "sessionB" {
		t.Fatalf("collapsed tree sessionKey = %q, want sessionB", key)
	}

	// Verify persistence: reload stores and check both trees and attrs intact
	groupStore2, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("reload groupStore: %v", err)
	}
	attrsStore2, err := sessionattrs.NewStore()
	if err != nil {
		t.Fatalf("reload attrsStore: %v", err)
	}

	if g1reloaded, ok := groupStore2.Get("g1"); !ok {
		t.Fatal("reloaded group missing")
	} else if len(g1reloaded.Tree) == 0 {
		t.Fatal("reloaded group tree is empty")
	}

	if sets2 := attrsStore2.Sets(); len(sets2.Background) != 1 || sets2.Background[0] != "sessionA" {
		t.Fatalf("reloaded background sets = %#v", sets2)
	}
}

func TestSessionBackgroundingAtomicityFault(t *testing.T) {
	// Setup: real stores in temp directory
	home := t.TempDir()
	t.Setenv("HOME", home)

	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("NewGroupStore: %v", err)
	}
	attrsStore, err := sessionattrs.NewStore()
	if err != nil {
		t.Fatalf("NewAttrsStore: %v", err)
	}

	// Create a group with 2-leaf split containing session "sessionA" and "sessionB"
	tree := []byte(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"sessionA"},"second":{"type":"leaf","sessionKey":"sessionB"}}`)
	if _, _, _, err := groupStore.SetTree("g1", tree); err != nil {
		t.Fatalf("SetTree: %v", err)
	}

	// Sabotage attrs persistence: replace session-attrs.json with a directory
	// so that os.WriteFile fails when attrsStore tries to save
	attrPath := attrsStore.Path()
	if err := os.RemoveAll(attrPath); err != nil {
		t.Fatalf("remove attrs file: %v", err)
	}
	if err := os.Mkdir(attrPath, 0o755); err != nil {
		t.Fatalf("mkdir attrs: %v", err)
	}

	// Setup handler
	opts := &Options{
		AttrsStore: attrsStore,
		GroupStore: groupStore,
	}

	router := chi.NewRouter()
	registerSessionsRoutes(router, opts, nil)

	// POST /session-attrs with background=true
	// This should fail on attrs save, triggering rollback
	body := map[string]interface{}{
		"key":        "sessionA",
		"background": true,
		"hidden":     false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/session-attrs", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify 500 response (save failed)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	// Verify background bit NOT set in attrs
	if sets := attrsStore.Sets(); len(sets.Background) != 0 {
		t.Fatalf("background sets = %#v, want empty", sets)
	}

	// Verify session NOT removed from group tree (rollback re-saved the group)
	g1, ok := groupStore.Get("g1")
	if !ok {
		t.Fatal("group g1 missing")
	}
	var updatedTree any
	if err := json.Unmarshal(g1.Tree, &updatedTree); err != nil {
		t.Fatalf("unmarshal tree: %v", err)
	}
	// Tree should still be a split (not collapsed)
	tree2, ok := updatedTree.(map[string]any)
	if !ok {
		t.Fatalf("tree is not map: %T", updatedTree)
	}
	if typ, ok := tree2["type"].(string); !ok || typ != "split" {
		t.Fatalf("tree type = %q, want split (rollback failed)", typ)
	}

	// Verify persistence: reload from disk with fresh store
	// The group should still have the original split tree
	groupStore2, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("reload groupStore: %v", err)
	}

	if g1reloaded, ok := groupStore2.Get("g1"); !ok {
		t.Fatal("reloaded group missing")
	} else {
		var reloadedTree any
		if err := json.Unmarshal(g1reloaded.Tree, &reloadedTree); err != nil {
			t.Fatalf("unmarshal reloaded tree: %v", err)
		}
		tree3, ok := reloadedTree.(map[string]any)
		if !ok {
			t.Fatalf("reloaded tree is not map: %T", reloadedTree)
		}
		if typ, ok := tree3["type"].(string); !ok || typ != "split" {
			t.Fatalf("reloaded tree type = %q, want split", typ)
		}
	}
}
