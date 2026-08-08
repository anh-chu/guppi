package peer

import (
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// TestReconnectPreservesSessions verifies that Sessions, Stats, Activity,
// ToolEvents, and Version are preserved across peer disconnect/reconnect.
func TestReconnectPreservesSessions(t *testing.T) {
	id, err := identity.Generate("local")
	if err != nil {
		t.Fatalf("Generate identity: %v", err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	localMgr := state.NewManager()
	m := NewManager(id, peerStore, localMgr)

	peerID := "peer-fingerprint-123"
	peerName := "peer-host"
	peerPubKey := "peer-public-key"
	peerAddr := "peer.local:7654"

	// Register the peer with initial state
	conn1 := NewPeerConnection(peerID, 128)
	m.RegisterPeerWithAddress(peerID, peerName, peerPubKey, peerAddr, conn1)

	// Manually set accumulated state on the registered peer
	m.mu.Lock()
	h, _ := m.hosts[peerID]
	h.Sessions = []*model.Session{
		{
			ID:   "session-1",
			Name: "test-session",
		},
	}
	h.Stats = map[string]interface{}{
		"load": 0.5,
	}
	h.Activity = []*activity.Snapshot{
		{
			Host:        "test-host",
			SessionName: "test-session",
			IdleSeconds: 1.5,
			TotalBytes:  1024,
		},
	}
	h.ToolEvents = []*toolevents.Event{
		{
			Tool:    toolevents.ToolPi,
			Status:  toolevents.StatusActive,
			Session: "test-session",
		},
	}
	h.Version = "v1.0.0"
	m.mu.Unlock()

	// Simulate disconnect
	m.UnregisterPeer(peerID)

	// Verify state is preserved after disconnect
	m.mu.RLock()
	h, _ = m.hosts[peerID]
	if h.Connected {
		t.Errorf("after UnregisterPeer: Connected=%v, want false", h.Connected)
	}
	if h.Conn != nil {
		t.Errorf("after UnregisterPeer: Conn=%v, want nil", h.Conn)
	}
	// Sessions, Stats, Activity, ToolEvents, Version should still be present
	if len(h.Sessions) != 1 || h.Sessions[0].ID != "session-1" {
		t.Errorf("after UnregisterPeer: Sessions=%v, want 1 session with ID=session-1", h.Sessions)
	}
	if h.Stats["load"] != 0.5 {
		t.Errorf("after UnregisterPeer: Stats=%v, want load=0.5", h.Stats)
	}
	if len(h.Activity) != 1 {
		t.Errorf("after UnregisterPeer: Activity=%v, want 1 snapshot", h.Activity)
	}
	if len(h.ToolEvents) != 1 || h.ToolEvents[0].Tool != toolevents.ToolPi {
		t.Errorf("after UnregisterPeer: ToolEvents=%v, want 1 event with Tool=ToolPi", h.ToolEvents)
	}
	if h.Version != "v1.0.0" {
		t.Errorf("after UnregisterPeer: Version=%q, want v1.0.0", h.Version)
	}
	m.mu.RUnlock()

	// Reconnect with a new connection
	conn2 := NewPeerConnection(peerID, 128)
	m.RegisterPeerWithAddress(peerID, peerName, peerPubKey, peerAddr, conn2)

	// Verify state is still preserved after reconnect, and connection is updated
	m.mu.RLock()
	h, _ = m.hosts[peerID]
	if !h.Connected {
		t.Errorf("after reconnect: Connected=%v, want true", h.Connected)
	}
	if h.Conn != conn2 {
		t.Errorf("after reconnect: Conn=%v, want %v", h.Conn, conn2)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != "session-1" {
		t.Errorf("after reconnect: Sessions=%v, want 1 session with ID=session-1", h.Sessions)
	}
	if h.Stats["load"] != 0.5 {
		t.Errorf("after reconnect: Stats=%v, want load=0.5", h.Stats)
	}
	if len(h.Activity) != 1 {
		t.Errorf("after reconnect: Activity=%v, want 1 snapshot", h.Activity)
	}
	if len(h.ToolEvents) != 1 || h.ToolEvents[0].Tool != toolevents.ToolPi {
		t.Errorf("after reconnect: ToolEvents=%v, want 1 event with Tool=ToolPi", h.ToolEvents)
	}
	if h.Version != "v1.0.0" {
		t.Errorf("after reconnect: Version=%q, want v1.0.0", h.Version)
	}
	m.mu.RUnlock()
}

// TestTryRegisterPeerPreservesState verifies that TryRegisterPeer also
// preserves accumulated state when it succeeds.
func TestTryRegisterPeerPreservesState(t *testing.T) {
	id, err := identity.Generate("local")
	if err != nil {
		t.Fatalf("Generate identity: %v", err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	localMgr := state.NewManager()
	m := NewManager(id, peerStore, localMgr)

	peerID := "peer-fp-456"
	peerName := "peer-2"
	peerPubKey := "peer-pk-2"
	peerAddr := "peer2.local:7654"

	// Register with TryRegisterPeer
	conn1 := NewPeerConnection(peerID, 128)
	ok := m.TryRegisterPeer(peerID, peerName, peerPubKey, peerAddr, conn1)
	if !ok {
		t.Fatalf("TryRegisterPeer returned false, want true")
	}

	// Set accumulated state
	m.mu.Lock()
	h, _ := m.hosts[peerID]
	h.Sessions = []*model.Session{
		{
			ID:   "session-2",
			Name: "another-session",
		},
	}
	h.Stats = map[string]interface{}{
		"memory": 2048,
	}
	h.Activity = []*activity.Snapshot{
		{
			Host:        "peer-2-host",
			SessionName: "another-session",
			IdleSeconds: 2.5,
			TotalBytes:  2048,
		},
	}
	h.ToolEvents = []*toolevents.Event{
		{
			Tool:    toolevents.ToolClaude,
			Status:  toolevents.StatusWaiting,
			Session: "another-session",
		},
	}
	h.Version = "v2.0.0"
	m.mu.Unlock()

	// Disconnect
	m.UnregisterPeer(peerID)

	// Try to reconnect
	conn2 := NewPeerConnection(peerID, 128)
	ok = m.TryRegisterPeer(peerID, peerName, peerPubKey, peerAddr, conn2)
	if !ok {
		t.Fatalf("TryRegisterPeer on reconnect returned false, want true")
	}

	// Verify state is preserved
	m.mu.RLock()
	h, _ = m.hosts[peerID]
	if !h.Connected {
		t.Errorf("after reconnect: Connected=%v, want true", h.Connected)
	}
	if h.Conn != conn2 {
		t.Errorf("after reconnect: Conn=%v, want %v", h.Conn, conn2)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != "session-2" {
		t.Errorf("after reconnect: Sessions=%v, want 1 session with ID=session-2", h.Sessions)
	}
	if h.Stats["memory"] != 2048 {
		t.Errorf("after reconnect: Stats=%v, want memory=2048", h.Stats)
	}
	if len(h.Activity) != 1 {
		t.Errorf("after reconnect: Activity=%v, want 1 snapshot", h.Activity)
	}
	if len(h.ToolEvents) != 1 || h.ToolEvents[0].Tool != toolevents.ToolClaude {
		t.Errorf("after reconnect: ToolEvents=%v, want 1 event with Tool=ToolClaude", h.ToolEvents)
	}
	if h.Version != "v2.0.0" {
		t.Errorf("after reconnect: Version=%q, want v2.0.0", h.Version)
	}
	m.mu.RUnlock()
}

// TestTryRegisterPeerRejectsLiveConnection verifies that TryRegisterPeer
// correctly rejects registration when a live connection already exists,
// and that no mutation occurs (Name/PublicKey/Address/Connected/LastSeen stay unchanged).
func TestTryRegisterPeerRejectsLiveConnection(t *testing.T) {
	id, err := identity.Generate("local")
	if err != nil {
		t.Fatalf("Generate identity: %v", err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	localMgr := state.NewManager()
	m := NewManager(id, peerStore, localMgr)

	peerID := "peer-fp-789"
	peerName := "peer-3"
	peerPubKey := "peer-pk-3"
	peerAddr := "peer3.local:7654"

	// Register initial connection
	conn1 := NewPeerConnection(peerID, 128)
	ok := m.TryRegisterPeer(peerID, peerName, peerPubKey, peerAddr, conn1)
	if !ok {
		t.Fatalf("first TryRegisterPeer returned false, want true")
	}

	// Try to register another connection with DIFFERENT name/publicKey/address while first is live
	conn2 := NewPeerConnection(peerID, 128)
	newName := "different-name"
	newPubKey := "different-pubkey"
	newAddr := "different-address:9999"
	ok = m.TryRegisterPeer(peerID, newName, newPubKey, newAddr, conn2)
	if ok {
		t.Errorf("second TryRegisterPeer returned true, want false (live connection exists)")
	}

	// Verify original connection is still intact and Name/PublicKey/Address are UNCHANGED
	// (proving guard rejected before any mutation)
	m.mu.RLock()
	h, _ := m.hosts[peerID]
	if h.Conn != conn1 {
		t.Errorf("after rejecting second TryRegisterPeer: Conn=%v, want %v", h.Conn, conn1)
	}
	if h.Name != peerName {
		t.Errorf("after rejecting second TryRegisterPeer: Name=%q, want %q (should be unchanged)", h.Name, peerName)
	}
	if h.PublicKey != peerPubKey {
		t.Errorf("after rejecting second TryRegisterPeer: PublicKey=%q, want %q (should be unchanged)", h.PublicKey, peerPubKey)
	}
	if h.Address != peerAddr {
		t.Errorf("after rejecting second TryRegisterPeer: Address=%q, want %q (should be unchanged)", h.Address, peerAddr)
	}
	if !h.Connected {
		t.Errorf("after rejecting second TryRegisterPeer: Connected=%v, want true (should be unchanged)", h.Connected)
	}
	oldLastSeen := h.LastSeen
	m.mu.RUnlock()

	// Verify LastSeen was not modified by the rejected call
	if !oldLastSeen.Equal(h.LastSeen) {
		t.Errorf("after rejecting second TryRegisterPeer: LastSeen was modified, want unchanged")
	}
}

// TestLastSeenUpdatedOnReconnect verifies that LastSeen is updated when
// reconnecting, even though we preserve other state.
func TestLastSeenUpdatedOnReconnect(t *testing.T) {
	id, err := identity.Generate("local")
	if err != nil {
		t.Fatalf("Generate identity: %v", err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatalf("NewPeerStore: %v", err)
	}
	localMgr := state.NewManager()
	m := NewManager(id, peerStore, localMgr)

	peerID := "peer-fp-lastseen"
	peerName := "peer-lastseen"
	peerPubKey := "peer-pk-lastseen"
	peerAddr := "peer-lastseen.local:7654"

	// Register and set an old LastSeen time
	conn1 := NewPeerConnection(peerID, 128)
	m.RegisterPeerWithAddress(peerID, peerName, peerPubKey, peerAddr, conn1)

	m.mu.Lock()
	oldTime := time.Now().Add(-10 * time.Minute)
	h, _ := m.hosts[peerID]
	h.LastSeen = oldTime
	m.mu.Unlock()

	// Wait a tiny bit to ensure time difference is measurable
	time.Sleep(10 * time.Millisecond)

	// Disconnect
	m.UnregisterPeer(peerID)

	// Reconnect
	beforeReconnect := time.Now()
	conn2 := NewPeerConnection(peerID, 128)
	m.RegisterPeerWithAddress(peerID, peerName, peerPubKey, peerAddr, conn2)
	afterReconnect := time.Now()

	// Verify LastSeen was updated to near-current time
	m.mu.RLock()
	h, _ = m.hosts[peerID]
	if h.LastSeen.Before(beforeReconnect) || h.LastSeen.After(afterReconnect.Add(100*time.Millisecond)) {
		t.Errorf("after reconnect: LastSeen=%v, want between %v and %v", h.LastSeen, beforeReconnect, afterReconnect)
	}
	m.mu.RUnlock()
}
