package groupsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mustTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func TestApplyRemoteFieldLWW(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
			TreeUpdatedAt: mustTime(10),
			Name:          "old",
			NameUpdatedAt: mustTime(10),
			Rank:          "r1",
			RankUpdatedAt: mustTime(10),
		},
	}}

	got, ok, err := s.ApplyRemote("g1", Group{
		Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"b"}`),
		TreeUpdatedAt: mustTime(5),
		Name:          "new",
		NameUpdatedAt: mustTime(20),
		Rank:          "r2",
		RankUpdatedAt: mustTime(5),
	})
	if err != nil {
		t.Fatalf("ApplyRemote: %v", err)
	}
	if !ok {
		t.Fatal("expected accepted")
	}
	if !bytes.Equal(got.Tree, []byte(`{"type":"leaf","sessionKey":"a"}`)) {
		t.Fatalf("tree = %s", got.Tree)
	}
	if got.Name != "new" || got.Rank != "r1" {
		t.Fatalf("merged = %#v", got)
	}
	if got.NameUpdatedAt != mustTime(20) || got.TreeUpdatedAt != mustTime(10) || got.RankUpdatedAt != mustTime(10) {
		t.Fatalf("clocks = %#v", got)
	}
}

func TestDeleteTombstoneStaysDeletedAgainstStaleSnapshot(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
			TreeUpdatedAt: mustTime(10),
			Name:          "gone",
			NameUpdatedAt: mustTime(10),
			Rank:          "r1",
			RankUpdatedAt: mustTime(10),
			DeletedAt:     mustTime(20),
		},
	}}

	got, ok, err := s.ApplyRemote("g1", Group{
		Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
		TreeUpdatedAt: mustTime(5),
		Name:          "old",
		NameUpdatedAt: mustTime(5),
		Rank:          "r0",
		RankUpdatedAt: mustTime(5),
		DeletedAt:     time.Time{},
	})
	if err != nil {
		t.Fatalf("ApplyRemote: %v", err)
	}
	if ok {
		t.Fatal("stale live snapshot should not win")
	}
	if got.DeletedAt != mustTime(20) {
		t.Fatalf("deleted_at = %v", got.DeletedAt)
	}
	if live := s.Live(); len(live) != 0 {
		t.Fatalf("live = %#v", live)
	}
}

func TestSetTreeResurrectsTombstonedGroup(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
			TreeUpdatedAt: mustTime(10),
			DeletedAt:     mustTime(20),
		},
	}}

	got, err := s.SetTree("g1", json.RawMessage(`{"type":"leaf","sessionKey":"b"}`))
	if err != nil {
		t.Fatalf("SetTree: %v", err)
	}
	if !got.DeletedAt.IsZero() {
		t.Fatalf("local SetTree should clear tombstone, got deleted_at = %v", got.DeletedAt)
	}
	if live := s.Live(); len(live) != 1 {
		t.Fatalf("resurrected group should be live, got %#v", live)
	}
}

func TestMigrateKeyRewritesOwnedLeavesOnly(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"local-fp/old"},"second":{"type":"split","direction":"v","ratio":0.5,"first":{"type":"leaf","sessionKey":"peer-fp/old"},"second":{"type":"leaf","sessionKey":"old"}}}`),
			TreeUpdatedAt: mustTime(10),
		},
	}}

	changed, err := s.MigrateKey("local-fp", "old", "new")
	if err != nil {
		t.Fatalf("MigrateKey: %v", err)
	}
	if len(changed) != 1 || changed[0] != "g1" {
		t.Fatalf("changed = %#v", changed)
	}
	got := s.groups["g1"]
	want := `{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"local-fp/new"},"second":{"type":"split","direction":"v","ratio":0.5,"first":{"type":"leaf","sessionKey":"peer-fp/old"},"second":{"type":"leaf","sessionKey":"new"}}}`
	var gotTree any
	var wantTree any
	if err := json.Unmarshal(got.Tree, &gotTree); err != nil {
		t.Fatalf("got tree unmarshal: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantTree); err != nil {
		t.Fatalf("want tree unmarshal: %v", err)
	}
	if !reflect.DeepEqual(gotTree, wantTree) {
		t.Fatalf("tree = %#v", gotTree)
	}
	if !got.TreeUpdatedAt.After(mustTime(10)) {
		t.Fatalf("tree clock not bumped: %v", got.TreeUpdatedAt)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	var err error
	if _, err = s.SetTree("g1", json.RawMessage(`{"type":"leaf","sessionKey":"x"}`)); err != nil {
		t.Fatalf("SetTree: %v", err)
	}
	if _, err = s.SetName("g1", "name", NameModeManual); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if _, err = s.SetRank("g1", "rank"); err != nil {
		t.Fatalf("SetRank: %v", err)
	}
	if _, err = s.Delete("g1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	reloaded, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}
	got, ok := reloaded.Get("g1")
	if !ok {
		t.Fatal("missing reloaded group")
	}
	if got.Name != "name" || got.Rank != "rank" || got.DeletedAt.IsZero() {
		t.Fatalf("reloaded = %#v", got)
	}
	if len(reloaded.Live()) != 0 {
		t.Fatalf("live = %#v", reloaded.Live())
	}
}

func TestEffectiveNameMode(t *testing.T) {
	cases := []struct {
		name string
		g    Group
		want NameMode
	}{
		{"explicit auto", Group{NameMode: NameModeAuto, Name: "foo"}, NameModeAuto},
		{"explicit manual", Group{NameMode: NameModeManual, Name: "foo"}, NameModeManual},
		{"legacy named", Group{Name: "foo"}, NameModeManual},
		{"legacy unnamed", Group{}, NameModeAuto},
		{"legacy empty-name", Group{Name: ""}, NameModeAuto},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveNameMode(c.g); got != c.want {
				t.Fatalf("EffectiveNameMode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSetNamePersistsMode(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetTree("g1", json.RawMessage(`{"type":"leaf","sessionKey":"a"}`)); err != nil {
		t.Fatalf("SetTree: %v", err)
	}
	g, err := s.SetName("g1", "AI-chat", NameModeAuto)
	if err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if g.Name != "AI-chat" || g.NameMode != NameModeAuto {
		t.Fatalf("group = %#v", g)
	}
	if g.NameModeUpdatedAt.IsZero() {
		t.Fatal("NameModeUpdatedAt not set")
	}

	reloaded, err := NewStore()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Get("g1")
	if !ok {
		t.Fatal("missing reloaded group")
	}
	if got.Name != "AI-chat" || got.NameMode != NameModeAuto {
		t.Fatalf("reloaded = %#v", got)
	}
	if EffectiveNameMode(got) != NameModeAuto {
		t.Fatalf("effective mode = %q", EffectiveNameMode(got))
	}
}

func TestApplyRemoteNameModeLWW(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Name:              "old",
			NameUpdatedAt:     mustTime(10),
			NameMode:          NameModeManual,
			NameModeUpdatedAt: mustTime(10),
		},
	}}

	got, ok, err := s.ApplyRemote("g1", Group{
		Name:              "old",
		NameUpdatedAt:     mustTime(5),
		NameMode:          NameModeAuto,
		NameModeUpdatedAt: mustTime(20),
	})
	if err != nil {
		t.Fatalf("ApplyRemote: %v", err)
	}
	if !ok {
		t.Fatal("expected accepted")
	}
	if got.Name != "old" {
		t.Fatalf("name should stay local, got %q", got.Name)
	}
	if got.NameMode != NameModeAuto {
		t.Fatalf("mode = %q, want auto", got.NameMode)
	}
	if got.NameModeUpdatedAt != mustTime(20) {
		t.Fatalf("mode clock = %v", got.NameModeUpdatedAt)
	}
}

func TestApplySnapshotDedupesDuplicateContentGroups(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"local": {
			Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`),
			TreeUpdatedAt: mustTime(10),
		},
	}}

	// A peer independently minted its own id for the exact same set of
	// session leaves (different tree shape, same membership), with a newer
	// tree timestamp.
	changed, err := s.ApplySnapshot(map[string]Group{
		"remote": {
			Tree:          json.RawMessage(`{"type":"split","direction":"v","ratio":0.5,"first":{"type":"leaf","sessionKey":"b"},"second":{"type":"leaf","sessionKey":"a"}}`),
			TreeUpdatedAt: mustTime(20),
		},
	})
	if err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}

	live := s.Live()
	if len(live) != 1 {
		t.Fatalf("expected exactly one live group after dedup, got %#v", live)
	}
	if _, ok := live["remote"]; !ok {
		t.Fatalf("expected the more recently updated group (\"remote\") to survive, got %#v", live)
	}

	changedSet := map[string]bool{}
	for _, id := range changed {
		changedSet[id] = true
	}
	if !changedSet["remote"] || !changedSet["local"] {
		t.Fatalf("changed should report both the winner and the tombstoned loser, got %#v", changed)
	}

	if got := s.groups["local"]; got.DeletedAt.IsZero() {
		t.Fatalf("older duplicate should be tombstoned, got %#v", got)
	}
}

func TestSetTreeDedupesAgainstExistingDuplicateContentGroup(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"stale": {
			Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
			TreeUpdatedAt: mustTime(10),
		},
	}}

	// A local action (drag-drop, pop-out) mints a brand new id for a tree
	// that ends up containing the same single session as an existing group.
	got, err := s.SetTree("fresh", json.RawMessage(`{"type":"leaf","sessionKey":"a"}`))
	if err != nil {
		t.Fatalf("SetTree: %v", err)
	}
	if !got.DeletedAt.IsZero() {
		t.Fatalf("the just-edited group itself should not be tombstoned, got %#v", got)
	}

	live := s.Live()
	if len(live) != 1 {
		t.Fatalf("expected the duplicate to collapse to one live group, got %#v", live)
	}
	if _, ok := live["fresh"]; !ok {
		t.Fatalf("the just-edited group should win (most recent tree), got %#v", live)
	}
	if stale := s.groups["stale"]; stale.DeletedAt.IsZero() {
		t.Fatalf("stale duplicate should be tombstoned, got %#v", stale)
	}
}

func TestApplySnapshotNameModeLWW(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			NameMode:          NameModeManual,
			NameModeUpdatedAt: mustTime(10),
		},
	}}

	changed, err := s.ApplySnapshot(map[string]Group{
		"g1": {
			NameMode:          NameModeAuto,
			NameModeUpdatedAt: mustTime(20),
		},
	})
	if err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if len(changed) != 1 || changed[0] != "g1" {
		t.Fatalf("changed = %#v", changed)
	}
	got := s.groups["g1"]
	if got.NameMode != NameModeAuto || got.NameModeUpdatedAt != mustTime(20) {
		t.Fatalf("merged = %#v", got)
	}
}

func TestRemoveSessionKey(t *testing.T) {
	tests := []struct {
		name          string
		groups        map[string]Group
		keyToRemove   string
		wantChanged   []string
		wantTombstone bool                                                   // if true, group should be deleted (DeletedAt set)
		checkTree     func(t *testing.T, s *Store, groupID string, tree any) // optional: verify resulting tree structure
	}{
		{
			name: "key in 2-leaf split collapses to sibling",
			groups: map[string]Group{
				"g1": {
					Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`),
				},
			},
			keyToRemove: "a",
			wantChanged: []string{"g1"},
			checkTree: func(t *testing.T, s *Store, groupID string, tree any) {
				treeMap, ok := tree.(map[string]any)
				if !ok {
					t.Fatalf("collapsed tree is not map: %T", tree)
				}
				if typ, ok := treeMap["type"].(string); !ok || typ != "leaf" {
					t.Fatalf("collapsed tree type = %q, want leaf", typ)
				}
				if key, ok := treeMap["sessionKey"].(string); !ok || key != "b" {
					t.Fatalf("collapsed tree sessionKey = %q, want b", key)
				}
			},
		},
		{
			name: "key as sole leaf tombstones group",
			groups: map[string]Group{
				"g1": {
					Tree: json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
				},
			},
			keyToRemove:   "a",
			wantChanged:   []string{"g1"},
			wantTombstone: true,
		},
		{
			name: "key absent returns no-op",
			groups: map[string]Group{
				"g1": {
					Tree: json.RawMessage(`{"type":"leaf","sessionKey":"x"}`),
				},
			},
			keyToRemove: "a",
			wantChanged: nil,
			checkTree: func(t *testing.T, s *Store, groupID string, tree any) {
				treeMap, ok := tree.(map[string]any)
				if !ok {
					t.Fatalf("tree is not map: %T", tree)
				}
				if key, ok := treeMap["sessionKey"].(string); !ok || key != "x" {
					t.Fatalf("tree sessionKey = %q, want x (unchanged)", key)
				}
			},
		},
		{
			name: "key in two groups both pruned",
			groups: map[string]Group{
				"g1": {
					Tree: json.RawMessage(`{"type":"leaf","sessionKey":"a"}`),
				},
				"g2": {
					Tree: json.RawMessage(`{"type":"split","direction":"v","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"c"}}`),
				},
			},
			keyToRemove: "a",
			wantChanged: []string{"g1", "g2"},
			checkTree: func(t *testing.T, s *Store, groupID string, tree any) {
				treeMap, ok := tree.(map[string]any)
				if !ok {
					t.Fatalf("tree is not map: %T", tree)
				}
				// g1 should be tombstoned (no tree check)
				// g2 should have collapsed to just the "c" leaf
				if groupID == "g2" {
					if typ, ok := treeMap["type"].(string); !ok || typ != "leaf" {
						t.Fatalf("g2 collapsed tree type = %q, want leaf", typ)
					}
					if key, ok := treeMap["sessionKey"].(string); !ok || key != "c" {
						t.Fatalf("g2 collapsed tree sessionKey = %q, want c", key)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "groups.json")
			s := &Store{path: path, groups: tt.groups}

			changed, prior, err := s.RemoveSessionKey(tt.keyToRemove)
			if err != nil {
				t.Fatalf("RemoveSessionKey: %v", err)
			}

			var gotChanged []string
			for id := range changed {
				gotChanged = append(gotChanged, id)
			}
			sort.Strings(gotChanged)

			if !reflect.DeepEqual(gotChanged, tt.wantChanged) {
				t.Fatalf("changed = %#v, want %#v", gotChanged, tt.wantChanged)
			}

			if len(changed) != len(prior) {
				t.Fatalf("changed/prior mismatch: len(changed)=%d len(prior)=%d", len(changed), len(prior))
			}

			// Verify tombstone if expected
			if tt.wantTombstone && len(changed) > 0 {
				for id := range changed {
					if s.groups[id].DeletedAt.IsZero() {
						t.Fatalf("group %s should be tombstoned", id)
					}
				}
			}

			// Verify resulting tree structure if checkTree provided
			if tt.checkTree != nil {
				for groupID := range tt.groups {
					g, ok := s.groups[groupID]
					if !ok {
						continue
					}
					if g.DeletedAt.IsZero() && len(g.Tree) > 0 {
						var tree any
						if err := json.Unmarshal(g.Tree, &tree); err != nil {
							t.Fatalf("unmarshal tree for %s: %v", groupID, err)
						}
						tt.checkTree(t, s, groupID, tree)
					}
				}
			}
		})
	}
}

func TestRemoveSessionKeyDedupesDuplicates(t *testing.T) {
	// Pruning "a" from g2 leaves g1 and g2 with identical membership {b};
	// the older duplicate must be tombstoned so the sidebar shows it once.
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"leaf","sessionKey":"b"}`),
			TreeUpdatedAt: mustTime(10),
		},
		"g2": {
			Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`),
			TreeUpdatedAt: mustTime(20),
		},
	}}

	changed, prior, err := s.RemoveSessionKey("a")
	if err != nil {
		t.Fatalf("RemoveSessionKey: %v", err)
	}
	if len(changed) != 2 || len(prior) != 2 {
		t.Fatalf("changed=%d prior=%d, want 2/2", len(changed), len(prior))
	}
	if s.groups["g1"].DeletedAt.IsZero() {
		t.Fatal("g1 should be tombstoned as older duplicate")
	}
	if !s.groups["g2"].DeletedAt.IsZero() {
		t.Fatal("g2 should stay live")
	}
	if !prior["g1"].DeletedAt.IsZero() {
		t.Fatal("prior[g1] must capture pre-dedupe live state")
	}
}

func TestSetNameUnknownGroup(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetName("ghost", "AI Name", NameModeAuto); !errors.Is(err, ErrUnknownGroup) {
		t.Fatalf("SetName unknown id: want ErrUnknownGroup, got %v", err)
	}
	if _, err := s.SetRank("ghost", "a0"); !errors.Is(err, ErrUnknownGroup) {
		t.Fatalf("SetRank unknown id: want ErrUnknownGroup, got %v", err)
	}
	if len(s.Live()) != 0 {
		t.Fatalf("phantom group materialized: %v", s.Live())
	}
}
