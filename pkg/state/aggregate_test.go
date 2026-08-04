package state

import "testing"

// fakeRemoteCatalogSource is a minimal, injectable RemoteCatalogSource used
// to prove AggregateCatalog merges a remote-owner cache with the local
// catalog without needing a live peer.Manager or a real network connection.
type fakeRemoteCatalogSource struct {
	snapshots []OwnerCatalogSnapshot
}

func (f *fakeRemoteCatalogSource) AllRemoteCatalogSnapshots() []OwnerCatalogSnapshot {
	return f.snapshots
}

func TestAggregateCatalog_MergesLocalAndRemote(t *testing.T) {
	localOwner := NewOwnerID()
	local := NewCatalog(localOwner, nil)
	if err := local.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	localSessionID := NewSessionID()
	if err := local.PutSession(LocalSessionRecord{
		ID:    localSessionID,
		Owner: localOwner,
		Ref:   SessionRef{Owner: localOwner, Session: localSessionID},
		Phase: SessionPhaseActive,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	remoteOwner := NewOwnerID()
	remoteSessionID := NewSessionID()
	remoteSnap := OwnerCatalogSnapshot{
		Owner:    remoteOwner,
		Revision: 42,
		Sessions: []LocalSessionRecord{
			{ID: remoteSessionID, Owner: remoteOwner, Ref: SessionRef{Owner: remoteOwner, Session: remoteSessionID}, Phase: SessionPhaseActive},
		},
	}
	source := &fakeRemoteCatalogSource{snapshots: []OwnerCatalogSnapshot{remoteSnap}}

	agg := AggregateCatalog(local, source)

	if agg.Local.Owner != localOwner {
		t.Fatalf("expected local owner %q, got %q", localOwner, agg.Local.Owner)
	}
	if len(agg.Local.Sessions) != 1 || agg.Local.Sessions[0].ID != localSessionID {
		t.Fatalf("expected local snapshot to contain only the local session, got %+v", agg.Local.Sessions)
	}
	if len(agg.Remote) != 1 {
		t.Fatalf("expected exactly one remote owner snapshot, got %d", len(agg.Remote))
	}
	if agg.Remote[0].Owner != remoteOwner {
		t.Fatalf("expected remote owner %q, got %q", remoteOwner, agg.Remote[0].Owner)
	}
	// Each owner's revision must remain independent: the remote owner's
	// revision (42) must never be conflated with the local catalog's own
	// revision (which is unrelated and much smaller after one mutation).
	if agg.Remote[0].Revision != 42 {
		t.Fatalf("expected remote revision to be preserved untouched, got %d", agg.Remote[0].Revision)
	}
	// The remote owner's revision (42) is far ahead of the local catalog's
	// own revision (1, after a single mutation) and must stay that way --
	// proving the two are tracked independently, not conflated into one
	// number.
	if agg.Local.Revision != 1 {
		t.Fatalf("expected local revision to be 1 after one mutation, got %d", agg.Local.Revision)
	}
	if len(agg.Remote[0].Sessions) != 1 || agg.Remote[0].Sessions[0].ID != remoteSessionID {
		t.Fatalf("expected remote snapshot to contain only the remote session, got %+v", agg.Remote[0].Sessions)
	}
}

func TestAggregateCatalog_NilSourceYieldsLocalOnly(t *testing.T) {
	owner := NewOwnerID()
	local := NewCatalog(owner, nil)
	if err := local.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	agg := AggregateCatalog(local, nil)
	if agg.Remote != nil {
		t.Fatalf("expected nil Remote with a nil source, got %+v", agg.Remote)
	}
	if agg.Local.Owner != owner {
		t.Fatalf("expected local owner %q, got %q", owner, agg.Local.Owner)
	}
}
