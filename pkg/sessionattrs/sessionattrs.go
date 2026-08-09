// Package sessionattrs is the server-authoritative store for SHARED, per-session
// UI attributes that are meaningful on every node in the mesh: whether a
// session is "backgrounded" (parked) and whether it is "hidden" from the list.
//
// WHY THIS EXISTS (and replaced the old layout-sync path):
//
// These two bits used to ride on the generic layout-sync channel: the browser
// owned the truth in localStorage, pushed a whole-blob snapshot to the server,
// and a frontend translation layer rewrote session keys between a device-local
// and a global namespace. That design produced a string of reset bugs:
//   - whole-blob last-write-wins stamped now() on every write, so a fresh
//     client's empty push always "won" and wiped everyone;
//   - the local<->global key translation was not the identity in multi-host
//     mode, so parked/hidden bits silently dropped on reload;
//   - client-side pruning fanned resets across the whole mesh.
//
// This store fixes the class of bug structurally:
//   - the SERVER owns the truth (no localStorage source of truth);
//   - keys are GLOBAL and host-qualified everywhere ("<owner-fp>/<name>"),
//     identical to sessionKey() on the frontend — no translation layer;
//   - updates are PER-KEY deltas with PER-KEY last-write-wins timestamps, so a
//     stale write on one key can never clobber another key, and there is no
//     "empty payload wipes everything" failure mode;
//   - garbage collection of dead sessions happens server-side (Prune), driven
//     by the authoritative mesh session list, so a client can't fan a reset.
package sessionattrs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anh-chu/termyard/pkg/config"
)

// Attr is the shared attribute set for one session key. A key is retained in
// the store as long as either bit is set OR it is acting as a recent tombstone
// (both bits false) guarding against a stale delta resurrecting it. UpdatedAt
// drives per-key last-write-wins reconciliation across the mesh.
type Attr struct {
	Background bool      `json:"background"`
	Hidden     bool      `json:"hidden"`
	ScheduleID string    `json:"schedule_id,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (a Attr) empty() bool { return !a.Background && !a.Hidden }

// tombstoneTTL bounds how long a cleared (both-false) entry is kept to suppress
// out-of-order resurrection. After this it is dropped on the next mutation.
const tombstoneTTL = 24 * time.Hour

// Store persists session attributes to disk. All keys are global
// ("<owner-fp>/<name>").
type Store struct {
	mu    sync.RWMutex
	path  string
	attrs map[string]Attr
}

// NewStore loads or creates the session-attrs store.
func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path:  filepath.Join(dir, "session-attrs.json"),
		attrs: map[string]Attr{},
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.attrs)
		if s.attrs == nil {
			s.attrs = map[string]Attr{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Path returns the file system path where the store is persisted.
func (s *Store) Path() string {
	return s.path
}

// Snapshot returns a copy of every retained attribute (including tombstones).
// Used to seed a freshly-connected peer so it can reconcile via per-key LWW.
func (s *Store) Snapshot() map[string]Attr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Attr, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// Sets is the flat, client-facing view: the set of keys currently backgrounded
// and the set currently hidden. Tombstones (both-false) are omitted.
type Sets struct {
	Background  []string          `json:"background"`
	Hidden      []string          `json:"hidden"`
	ScheduleIDs map[string]string `json:"schedule_ids,omitempty"`
}

// Sets returns the flat client-facing view.
func (s *Store) Sets() Sets {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Sets{Background: []string{}, Hidden: []string{}, ScheduleIDs: map[string]string{}}
	for k, v := range s.attrs {
		if v.Background {
			out.Background = append(out.Background, k)
		}
		if v.Hidden {
			out.Hidden = append(out.Hidden, k)
		}
		if v.ScheduleID != "" {
			out.ScheduleIDs[k] = v.ScheduleID
		}
	}
	return out
}

// Set applies a local (browser-originated) single-key update, stamping it with
// the current time. Returns the stored attr so the caller can fan it out.
func (s *Store) Set(key string, background, hidden bool) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, priorExists := s.attrs[key]

	a := Attr{Background: background, Hidden: hidden, ScheduleID: cur.ScheduleID, UpdatedAt: time.Now()}
	s.put(key, a)
	if err := s.save(); err != nil {
		// Rollback: restore prior state
		if priorExists {
			s.attrs[key] = cur
		} else {
			delete(s.attrs, key)
		}
		return Attr{}, err
	}
	return a, nil
}

// ApplyRemote merges a single-key delta received from a paired peer using
// per-key last-write-wins. Returns (attr, accepted): accepted=false means the
// local copy was newer-or-equal and the delta was ignored.
func (s *Store) ApplyRemote(key string, in Attr) (Attr, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.attrs[key]; ok && !in.UpdatedAt.After(cur.UpdatedAt) {
		return cur, false, nil
	} else if ok && in.ScheduleID == "" {
		in.ScheduleID = cur.ScheduleID
	}
	s.put(key, in)
	if err := s.save(); err != nil {
		return Attr{}, false, err
	}
	return in, true, nil
}

// ApplySnapshot merges a full peer snapshot via per-key LWW. Returns the keys
// that were accepted (changed locally) so the caller can notify browsers.
func (s *Store) ApplySnapshot(snap map[string]Attr) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []string
	for k, in := range snap {
		if cur, ok := s.attrs[k]; ok && !in.UpdatedAt.After(cur.UpdatedAt) {
			continue
		} else if ok && in.ScheduleID == "" {
			in.ScheduleID = cur.ScheduleID
		}
		s.put(k, in)
		changed = append(changed, k)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return changed, nil
}

// Prune garbage-collects keys whose owning host is known to be ONLINE but whose
// session is absent from liveKeys (the authoritative mesh session list), and
// drops expired tombstones. Pruned keys are turned into fresh tombstones so the
// removal propagates via the normal delta/LWW path. Returns the keys that were
// turned into tombstones (genuinely-gone sessions) for fan-out, plus whether
// anything changed on disk.
//
// onlineHosts is the set of host fingerprints currently connected/online; a key
// is only eligible for GC when its owner is in this set (otherwise its absence
// just means the owner is offline, not that the session is gone).
func (s *Store) Prune(liveKeys map[string]bool, onlineHosts map[string]bool) ([]string, bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	changed := false
	for k, v := range s.attrs {
		// Expired tombstone: drop silently.
		if v.empty() {
			if now.Sub(v.UpdatedAt) > tombstoneTTL {
				delete(s.attrs, k)
				changed = true
			}
			continue
		}
		if liveKeys[k] {
			continue
		}
		owner := ownerOf(k)
		if owner == "" || !onlineHosts[owner] {
			continue // owner offline/unknown -> can't prove the session is gone
		}
		// Owner online, session absent -> genuinely gone. Tombstone it.
		s.attrs[k] = Attr{UpdatedAt: now}
		gone = append(gone, k)
		changed = true
	}
	if changed {
		if err := s.save(); err != nil {
			return nil, false, err
		}
	}
	return gone, changed, nil
}

// Get returns the stored attr for a key (zero value if absent).
func (s *Store) Get(key string) Attr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attrs[key]
}

// SetScheduleID records owning schedule metadata for one session key.
func (s *Store) SetScheduleID(key, scheduleID string) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.attrs[key]
	cur.ScheduleID = scheduleID
	cur.UpdatedAt = time.Now()
	s.put(key, cur)
	if err := s.save(); err != nil {
		return Attr{}, err
	}
	return cur, nil
}

// MigrateKey moves all stored attributes (schedule_id, background, hidden) from
// a renamed session's old key to its new key, preserving the host prefix. A
// session rename (manual, AI auto-naming, or peer-driven) otherwise orphans
// these bits because every key is "<host>/<name>" or a bare "<name>".
//
// Only keys owned by this node are migrated: a bare key, or one host-qualified
// with localHost. A same-named session owned by a peer ("<peerfp>/<name>") is
// left untouched so a rename here never clobbers a peer's session. Returns the
// new keys that received migrated data so the caller can fan out updates.
func (s *Store) MigrateKey(localHost, oldName, newName string) ([]string, error) {
	if oldName == "" || newName == "" || oldName == newName {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Collect matches first; mutating the map while ranging it is legal but
	// makes visitation of freshly-inserted keys unspecified.
	type move struct{ oldKey, newKey string }
	var moves []move
	for key := range s.attrs {
		host, name := splitKey(key)
		if name != oldName || (host != "" && host != localHost) {
			continue
		}
		newKey := newName
		if host != "" {
			newKey = host + "/" + newName
		}
		if newKey != key {
			moves = append(moves, move{oldKey: key, newKey: newKey})
		}
	}
	if len(moves) == 0 {
		return nil, nil
	}
	migrated := make([]string, 0, len(moves))
	for _, mv := range moves {
		attr := s.attrs[mv.oldKey]
		attr.UpdatedAt = time.Now()
		delete(s.attrs, mv.oldKey)
		s.attrs[mv.newKey] = attr
		migrated = append(migrated, mv.newKey)
	}
	if err := s.save(); err != nil {
		return migrated, err
	}
	return migrated, nil
}

// put writes an entry, collapsing expired tombstones. Caller holds the lock.
func (s *Store) put(key string, a Attr) {
	if a.empty() && a.UpdatedAt.IsZero() {
		delete(s.attrs, key)
		return
	}
	s.attrs[key] = a
}

func (s *Store) save() error {
	return config.WriteJSON(s.path, s.attrs, 0o644)
}

// ownerOf returns the host-fingerprint prefix of a global session key
// ("<owner-fp>/<name>"). Mirrors parseSessionKey() on the frontend: the first
// '/' separates host from name.
func ownerOf(key string) string {
	host, _ := splitKey(key)
	return host
}

// splitKey divides a global session key into its host-fingerprint prefix and
// session name on the first '/'. A bare key (single-host) yields an empty host.
func splitKey(key string) (host, name string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}
