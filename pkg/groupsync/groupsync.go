package groupsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/anh-chu/termyard/pkg/config"
)

// NameMode indicates how a group's name was chosen.
type NameMode string

const (
	// NameModeAuto means the group's name was generated automatically.
	NameModeAuto NameMode = "auto"
	// NameModeManual means the group's name was explicitly set by a user.
	NameModeManual NameMode = "manual"
)

// EffectiveNameMode resolves the provenance of a group's name. Legacy records
// without an explicit NameMode default to manual when a name exists and auto
// when the name is empty.
func EffectiveNameMode(g Group) NameMode {
	if g.NameMode != "" {
		return g.NameMode
	}
	if g.Name != "" {
		return NameModeManual
	}
	return NameModeAuto
}

// Group is one synced saved-layout record.
type Group struct {
	Tree              json.RawMessage `json:"tree"`
	TreeUpdatedAt     time.Time       `json:"tree_updated_at"`
	Name              string          `json:"name"`
	NameUpdatedAt     time.Time       `json:"name_updated_at"`
	NameMode          NameMode        `json:"name_mode,omitempty"`
	NameModeUpdatedAt time.Time       `json:"name_mode_updated_at,omitempty"`
	Rank              string          `json:"rank"`
	RankUpdatedAt     time.Time       `json:"rank_updated_at"`
	DeletedAt         time.Time       `json:"deleted_at"`
}

// Store persists group records to disk.
type Store struct {
	mu     sync.RWMutex
	path   string
	groups map[string]Group
}

// NewStore loads or creates the group store.
func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path:   filepath.Join(dir, "groups.json"),
		groups: map[string]Group{},
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.groups)
		if s.groups == nil {
			s.groups = map[string]Group{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Snapshot returns all retained records, including tombstones.
func (s *Store) Snapshot() map[string]Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Group, len(s.groups))
	for id, g := range s.groups {
		out[id] = g
	}
	return out
}

// Live returns only non-tombstoned groups.
func (s *Store) Live() map[string]Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Group, len(s.groups))
	for id, g := range s.groups {
		if g.DeletedAt.IsZero() {
			out[id] = g
		}
	}
	return out
}

// Get returns one stored group.
func (s *Store) Get(id string) (Group, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	return g, ok
}

// SetTree applies a local tree update.
func (s *Store) SetTree(id string, tree json.RawMessage) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	g.Tree = append(json.RawMessage(nil), tree...)
	g.TreeUpdatedAt = time.Now()
	// A local edit resurrects a tombstoned group: without clearing DeletedAt a
	// re-created id stays invisible because Live() filters non-zero DeletedAt.
	g.DeletedAt = time.Time{}
	s.groups[id] = g
	if err := s.save(); err != nil {
		return Group{}, err
	}
	return g, nil
}

// SetName applies a local name update.
func (s *Store) SetName(id, name string, mode NameMode) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	g.Name = name
	g.NameUpdatedAt = time.Now()
	g.NameMode = mode
	g.NameModeUpdatedAt = time.Now()
	s.groups[id] = g
	if err := s.save(); err != nil {
		return Group{}, err
	}
	return g, nil
}

// SetRank applies a local rank update.
func (s *Store) SetRank(id, rank string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	g.Rank = rank
	g.RankUpdatedAt = time.Now()
	s.groups[id] = g
	if err := s.save(); err != nil {
		return Group{}, err
	}
	return g, nil
}

// Delete marks a group deleted and keeps its tombstone.
func (s *Store) Delete(id string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	g.DeletedAt = time.Now()
	s.groups[id] = g
	if err := s.save(); err != nil {
		return Group{}, err
	}
	return g, nil
}

// ApplyRemote merges one remote group using field-level LWW.
func (s *Store) ApplyRemote(id string, in Group) (Group, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.groups[id]
	merged := cur
	accepted := false

	if in.TreeUpdatedAt.After(cur.TreeUpdatedAt) {
		merged.Tree = append(json.RawMessage(nil), in.Tree...)
		merged.TreeUpdatedAt = in.TreeUpdatedAt
		accepted = true
	}
	if in.NameUpdatedAt.After(cur.NameUpdatedAt) {
		merged.Name = in.Name
		merged.NameUpdatedAt = in.NameUpdatedAt
		accepted = true
	}
	if in.NameModeUpdatedAt.After(cur.NameModeUpdatedAt) {
		merged.NameMode = in.NameMode
		merged.NameModeUpdatedAt = in.NameModeUpdatedAt
		accepted = true
	}
	if in.RankUpdatedAt.After(cur.RankUpdatedAt) {
		merged.Rank = in.Rank
		merged.RankUpdatedAt = in.RankUpdatedAt
		accepted = true
	}
	if in.DeletedAt.After(cur.DeletedAt) {
		merged.DeletedAt = in.DeletedAt
		accepted = true
	}

	if !accepted {
		return cur, false, nil
	}
	s.groups[id] = merged
	if err := s.save(); err != nil {
		return Group{}, false, err
	}
	return merged, true, nil
}

// ApplySnapshot merges a remote snapshot using field-level LWW.
func (s *Store) ApplySnapshot(snap map[string]Group) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := make([]string, 0, len(snap))
	for id, in := range snap {
		cur := s.groups[id]
		merged := cur
		accepted := false

		if in.TreeUpdatedAt.After(cur.TreeUpdatedAt) {
			merged.Tree = append(json.RawMessage(nil), in.Tree...)
			merged.TreeUpdatedAt = in.TreeUpdatedAt
			accepted = true
		}
		if in.NameUpdatedAt.After(cur.NameUpdatedAt) {
			merged.Name = in.Name
			merged.NameUpdatedAt = in.NameUpdatedAt
			accepted = true
		}
		if in.NameModeUpdatedAt.After(cur.NameModeUpdatedAt) {
			merged.NameMode = in.NameMode
			merged.NameModeUpdatedAt = in.NameModeUpdatedAt
			accepted = true
		}
		if in.RankUpdatedAt.After(cur.RankUpdatedAt) {
			merged.Rank = in.Rank
			merged.RankUpdatedAt = in.RankUpdatedAt
			accepted = true
		}
		if in.DeletedAt.After(cur.DeletedAt) {
			merged.DeletedAt = in.DeletedAt
			accepted = true
		}
		if !accepted {
			continue
		}
		s.groups[id] = merged
		changed = append(changed, id)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	sort.Strings(changed)
	if err := s.save(); err != nil {
		return nil, err
	}
	return changed, nil
}

// MigrateKey rewrites owned session-key leaves inside every tree blob.
func (s *Store) MigrateKey(localHost, oldName, newName string) ([]string, error) {
	if oldName == "" || newName == "" || oldName == newName {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	oldKeys := []string{oldName}
	newKeys := []string{newName}
	if localHost != "" {
		oldKeys = append(oldKeys, localHost+"/"+oldName)
		newKeys = append(newKeys, localHost+"/"+newName)
	}

	changed := make([]string, 0)
	now := time.Now()
	for id, g := range s.groups {
		if len(g.Tree) == 0 {
			continue
		}
		var tree any
		if err := json.Unmarshal(g.Tree, &tree); err != nil {
			return nil, err
		}
		updated, ok := replaceStrings(tree, oldKeys, newKeys)
		if !ok {
			continue
		}
		raw, err := json.Marshal(updated)
		if err != nil {
			return nil, err
		}
		g.Tree = raw
		g.TreeUpdatedAt = now
		s.groups[id] = g
		changed = append(changed, id)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	sort.Strings(changed)
	if err := s.save(); err != nil {
		return changed, err
	}
	return changed, nil
}

func replaceStrings(v any, olds, news []string) (any, bool) {
	switch x := v.(type) {
	case string:
		for i, old := range olds {
			if x == old {
				return news[i], true
			}
		}
		return v, false
	case []any:
		changed := false
		for i := range x {
			updated, ok := replaceStrings(x[i], olds, news)
			if ok {
				x[i] = updated
				changed = true
			}
		}
		return x, changed
	case map[string]any:
		changed := false
		for k := range x {
			updated, ok := replaceStrings(x[k], olds, news)
			if ok {
				x[k] = updated
				changed = true
			}
		}
		return x, changed
	default:
		return v, false
	}
}

func (s *Store) save() error {
	return config.WriteJSON(s.path, s.groups, 0o644)
}
