package ws

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/anh-chu/termyard/pkg/state"
)

// fakeRemoteCatalogNotifier is a minimal, injectable RemoteCatalogNotifier.
// pkg/ws cannot import pkg/peer (pkg/peer already imports pkg/ws), so this
// stands in for peer.Manager at the hub-logic unit-test level; the real
// peer.Manager -> StateStreamHub wiring is proven end-to-end by
// pkg/server's TestBootstrapIncludesRemoteOwnerCatalog and by
// pkg/peer's own AllRemoteCatalogSnapshots/SubscribeRemoteCatalogs tests.
type fakeRemoteCatalogNotifier struct {
	mu        sync.Mutex
	snapshots []state.OwnerCatalogSnapshot
	subs      []func(state.OwnerID, state.OwnerCatalogSnapshot, bool)
}

func (f *fakeRemoteCatalogNotifier) AllRemoteCatalogSnapshots() []state.OwnerCatalogSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]state.OwnerCatalogSnapshot, len(f.snapshots))
	copy(out, f.snapshots)
	return out
}

func (f *fakeRemoteCatalogNotifier) SubscribeRemoteCatalogs(fn func(state.OwnerID, state.OwnerCatalogSnapshot, bool)) func() {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	f.mu.Unlock()
	return func() {}
}

func (f *fakeRemoteCatalogNotifier) publishUpdate(snap state.OwnerCatalogSnapshot) {
	f.mu.Lock()
	f.snapshots = append(f.snapshots, snap)
	subs := make([]func(state.OwnerID, state.OwnerCatalogSnapshot, bool), len(f.subs))
	copy(subs, f.subs)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(snap.Owner, snap, false)
	}
}

func (f *fakeRemoteCatalogNotifier) publishRemoval(owner state.OwnerID) {
	f.mu.Lock()
	filtered := f.snapshots[:0]
	for _, s := range f.snapshots {
		if s.Owner != owner {
			filtered = append(filtered, s)
		}
	}
	f.snapshots = filtered
	subs := make([]func(state.OwnerID, state.OwnerCatalogSnapshot, bool), len(f.subs))
	copy(subs, f.subs)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(owner, state.OwnerCatalogSnapshot{}, true)
	}
}

// TestStateStreamIncludesRemoteCatalogOnConnect proves a client connecting
// AFTER a remote owner's catalog is already cached receives it immediately,
// tagged is_local=false and keyed to its own owner -- distinguishable from
// the local catalog_snapshot.
func TestStateStreamIncludesRemoteCatalogOnConnect(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog)
	defer hub.Close()

	remoteOwner := state.NewOwnerID()
	remoteSessionID := state.NewSessionID()
	notifier := &fakeRemoteCatalogNotifier{snapshots: []state.OwnerCatalogSnapshot{{
		Owner:    remoteOwner,
		Revision: 9,
		Sessions: []state.LocalSessionRecord{
			{ID: remoteSessionID, Owner: remoteOwner, Ref: state.SessionRef{Owner: remoteOwner, Session: remoteSessionID}},
		},
	}}}
	hub.AttachRemoteCatalogSource(notifier)

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	// First frame: the local catalog (key "local" sorts first).
	first := readTyped(t, conn)
	if first["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot first, got %v", first["type"])
	}
	if isLocal, _ := first["is_local"].(bool); !isLocal {
		t.Fatalf("expected first frame is_local=true, got %v", first["is_local"])
	}

	// Second frame: the remote owner's cached catalog.
	second := readTyped(t, conn)
	if second["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot second, got %v", second["type"])
	}
	if isLocal, _ := second["is_local"].(bool); isLocal {
		t.Fatal("expected second frame is_local=false")
	}
	snap, ok := second["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("expected snapshot object, got %v", second["snapshot"])
	}
	if snap["owner"] != string(remoteOwner) {
		t.Fatalf("expected remote owner %q, got %v", remoteOwner, snap["owner"])
	}
}

// TestStateStreamRemoteCatalogUpdateAndRemoval proves a LIVE connection
// receives both a later update to a remote owner's catalog and an explicit
// removal signal (distinct from silence) when that owner is forgotten.
func TestStateStreamRemoteCatalogUpdateAndRemoval(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog)
	defer hub.Close()

	notifier := &fakeRemoteCatalogNotifier{}
	hub.AttachRemoteCatalogSource(notifier)

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	// Drain the initial (local-only, no remote owners yet) snapshot (catalog + workspace).
	drainInitialBootstrap(t, conn)

	remoteOwner := state.NewOwnerID()
	notifier.publishUpdate(state.OwnerCatalogSnapshot{Owner: remoteOwner, Revision: 1})

	msg := readTyped(t, conn)
	if msg["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot for the update, got %v", msg["type"])
	}
	if isLocal, _ := msg["is_local"].(bool); isLocal {
		t.Fatal("expected is_local=false for a remote update")
	}

	notifier.publishRemoval(remoteOwner)

	removedMsg := readTyped(t, conn)
	if removedMsg["type"] != "catalog_owner_removed" {
		t.Fatalf("expected an explicit catalog_owner_removed message, got %v", removedMsg["type"])
	}
	if removedMsg["owner"] != string(remoteOwner) {
		t.Fatalf("expected removal for owner %q, got %v", remoteOwner, removedMsg["owner"])
	}
}
