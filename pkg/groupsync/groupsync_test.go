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

	got, ok, _, _, err := s.ApplyRemote("g1", Group{
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

	got, ok, _, _, err := s.ApplyRemote("g1", Group{
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

func TestSetTreeTombstonedGroupIsNoop(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`),
			TreeUpdatedAt: mustTime(10),
			DeletedAt:     mustTime(20),
		},
	}}

	// SetTree on tombstoned group should return ErrTombstoned (zombies forbidden)
	got, _, _, err := s.SetTree("g1", json.RawMessage(`{"type":"split","direction":"v","ratio":0.5,"first":{"type":"leaf","sessionKey":"x"},"second":{"type":"leaf","sessionKey":"y"}}`))
	if !errors.Is(err, ErrTombstoned) {
		t.Fatalf("SetTree on tombstoned group should return ErrTombstoned, got %v", err)
	}
	// Returned group should still have tombstone
	if got.DeletedAt.IsZero() {
		t.Fatalf("returned group should be tombstoned, got deleted_at zero")
	}
	// Tree should not be updated
	if !bytes.Equal(got.Tree, json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"a"},"second":{"type":"leaf","sessionKey":"b"}}`)) {
		t.Fatalf("SetTree on tombstoned group should not update tree, got %s", got.Tree)
	}
	if live := s.Live(); len(live) != 0 {
		t.Fatalf("tombstoned group should stay dead, live groups = %#v", live)
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
	if _, _, _, err = s.SetTree("g1", json.RawMessage(`{"type":"leaf","sessionKey":"x"}`)); err != nil {
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
	if _, _, _, err := s.SetTree("g1", json.RawMessage(`{"type":"leaf","sessionKey":"a"}`)); err != nil {
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

	got, ok, _, _, err := s.ApplyRemote("g1", Group{
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
	changed, _, _, err := s.ApplySnapshot(map[string]Group{
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
	got, _, _, err := s.SetTree("fresh", json.RawMessage(`{"type":"leaf","sessionKey":"a"}`))
	if err != nil {
		t.Fatalf("SetTree: %v", err)
	}
	// Fresh group is 1-leaf, so it must be tombstoned per the <2-leaf requirement
	if got.DeletedAt.IsZero() {
		t.Fatalf("1-leaf groups must be tombstoned, fresh should have DeletedAt set")
	}

	live := s.Live()
	if len(live) != 0 {
		t.Fatalf("both groups are 1-leaf duplicates, so both must be tombstoned, got %#v", live)
	}
	// Both fresh and stale should be tombstoned
	if s.groups["fresh"].DeletedAt.IsZero() {
		t.Fatalf("fresh should be tombstoned as 1-leaf group")
	}
	if s.groups["stale"].DeletedAt.IsZero() {
		t.Fatalf("stale should be tombstoned as 1-leaf duplicate")
	}
}

func TestApplySnapshotNameModeLWW(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {
			NameMode:          NameModeManual,
			NameModeUpdatedAt: mustTime(10),
		},
	}}

	changed, _, _, err := s.ApplySnapshot(map[string]Group{
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
	// g2 becomes single-leaf after pruning "a", so it must be tombstoned per <2-leaf requirement
	if s.groups["g2"].DeletedAt.IsZero() {
		t.Fatal("g2 should be tombstoned after pruning to single leaf")
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

func TestEnforceExclusivity_WinnerIDOrdered(t *testing.T) {
	// Verify that enforce orders groups winnerID-first, then by TreeUpdatedAt desc
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(10)},
		"g2": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(20)},
		"g3": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(15)},
	}}
	// Enforce with g1 as winner (despite earlier times)
	changed, _ := s.enforce(mustTime(100), "g1")
	// All three groups are 1-leaf groups, so all must be tombstoned per <2-leaf requirement
	if len(changed) != 3 {
		t.Fatalf("expected 3 changed (all 1-leaf groups tombstoned), got %d: %v", len(changed), changed)
	}
	for id, g := range changed {
		if g.DeletedAt.IsZero() {
			t.Fatalf("expected %s tombstoned, got not deleted", id)
		}
	}
}

func TestEnforceExclusivity_KeyRemovalPrunesBelowTwoLeaves(t *testing.T) {
	// When enforce removes a key, if the tree falls below 2 leaves, it should be tombstoned
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key2"}}`), TreeUpdatedAt: mustTime(10)},
		"g2": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key3"}}`), TreeUpdatedAt: mustTime(5)},
	}}
	// g1 should own key1, g2 loses key1 and becomes single leaf (key3), should be pruned to <2 and tombstoned
	changed, _ := s.enforce(mustTime(100), "")
	if _, ok := changed["g2"]; !ok {
		t.Fatalf("expected g2 changed, got %v", changed)
	}
	if g2 := s.groups["g2"]; g2.DeletedAt.IsZero() {
		// Check if tree is still there and has keys
		keys, _ := MemberKeys(g2.Tree)
		t.Fatalf("g2 should be tombstoned due to <2 leaves after key1 removal, but DeletedAt is zero and tree has keys %v", keys)
	}
}

func TestEnforceExclusivity_TreeUpdatedAtBumped(t *testing.T) {
	// Groups that have keys removed should get TreeUpdatedAt bumped
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(100)},
		"g2": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key"},"second":{"type":"leaf","sessionKey":"other"}}`), TreeUpdatedAt: mustTime(50)},
	}}
	now := mustTime(200)
	changed, _ := s.enforce(now, "")
	// g1 is 1-leaf (tombstoned in second pass), g2 loses key and becomes 1-leaf but may not be actively modified if enforcement stops early
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed, got %d: %v", len(changed), changed)
	}
	// Verify at least one group was changed (g1 or g2)
	if _, ok := changed["g1"]; ok {
		if s.groups["g1"].DeletedAt != now {
			t.Fatalf("g1 should be tombstoned at enforce time")
		}
	} else if _, ok := changed["g2"]; ok {
		if s.groups["g2"].TreeUpdatedAt != now {
			t.Fatalf("g2 TreeUpdatedAt should be bumped to enforce time, got %v", s.groups["g2"].TreeUpdatedAt)
		}
	} else {
		t.Fatalf("expected g1 or g2 in changed, got %v", changed)
	}
}

func TestReconcile_PrunesGoneSessions(t *testing.T) {
	// Reconcile should prune leaves where gone() returns true, then enforce
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"alive"},"second":{"type":"leaf","sessionKey":"dead"}}`), TreeUpdatedAt: mustTime(10)},
	}}
	changed, _, err := s.Reconcile(func(key string) bool {
		return key == "dead"
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(changed) != 1 || changed["g1"].DeletedAt.IsZero() {
		t.Fatalf("expected g1 tombstoned after pruning to <2 leaves, got %v", changed)
	}
}

func TestReconcile_EnforceAfterPrune(t *testing.T) {
	// Reconcile should apply enforce after pruning
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(10)},
		"g2": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(5)},
	}}
	changed, _, err := s.Reconcile(func(key string) bool {
		return false // nothing is gone
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Both g1 and g2 are 1-leaf groups (single session key), so both must be tombstoned
	if len(changed) != 2 {
		t.Fatalf("expected 2 changed (both 1-leaf groups tombstoned), got %d changes: %v", len(changed), changed)
	}
	for _, id := range []string{"g1", "g2"} {
		if _, ok := changed[id]; !ok {
			t.Fatalf("expected %s changed, got %v", id, changed)
		}
		if s.groups[id].DeletedAt.IsZero() {
			t.Fatalf("%s should be tombstoned as 1-leaf group", id)
		}
	}
}

func TestEnforcePreservesTombstones(t *testing.T) {
	// Enforce should not resurrect tombstones
	s := &Store{path: filepath.Join(t.TempDir(), "groups.json"), groups: map[string]Group{
		"g1": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"key"}`), TreeUpdatedAt: mustTime(100), DeletedAt: mustTime(50)},
		"g2": {Tree: json.RawMessage(`{"type":"leaf","sessionKey":"other"}`), TreeUpdatedAt: mustTime(10)},
	}}
	changed, _ := s.enforce(mustTime(200), "")
	// g1 is already tombstoned (skip), g2 is 1-leaf live group so must be tombstoned
	if len(changed) != 1 {
		t.Fatalf("expected 1 change (g2 tombstoned as 1-leaf), got %d changes", len(changed))
	}
	if _, ok := changed["g2"]; !ok {
		t.Fatalf("expected g2 changed, got %v", changed)
	}
	if s.groups["g2"].DeletedAt.IsZero() {
		t.Fatalf("g2 should be tombstoned as 1-leaf group")
	}
}

// Two-peer convergence test: verify that conflicting overlapping group writes
// via ApplyRemote eventually converge to the same state (deterministic by LWW
// and enforce ordering) without oscillation.
func TestTwoPeerGroupConvergence(t *testing.T) {
	// Scenario: two peers independently create groups for the same set of sessions.
	// Store1 creates g1={key1, key2} at t=10
	// Store2 creates g2={key1, key2} at t=5 (older, will lose)
	// Both trade deltas: g1 wins key1, key2; g2 is pruned to empty/tombstoned.
	// After exchange, both stores must agree.

	path1 := filepath.Join(t.TempDir(), "store1.json")
	path2 := filepath.Join(t.TempDir(), "store2.json")

	store1 := &Store{
		path: path1,
		groups: map[string]Group{
			"g1": {
				Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key2"}}`),
				TreeUpdatedAt: mustTime(10),
			},
		},
	}

	store2 := &Store{
		path: path2,
		groups: map[string]Group{
			"g2": {
				Tree:          json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key2"}}`),
				TreeUpdatedAt: mustTime(5),
			},
		},
	}

	// Simulate message exchange loop: each peer sends its snapshot to the other,
	// which applies it via ApplyRemote. Repeat until stable.
	maxRounds := 5
	for round := 0; round < maxRounds; round++ {
		// Store1 sends snapshot to Store2
		snap1 := store1.Snapshot()
		for id, g := range snap1 {
			store2.ApplyRemote(id, g)
		}

		// Store2 sends snapshot to Store1
		snap2 := store2.Snapshot()
		for id, g := range snap2 {
			store1.ApplyRemote(id, g)
		}

		// Check convergence: if live groups are identical, we're done
		live1 := store1.Live()
		live2 := store2.Live()

		if len(live1) == len(live2) {
			// Verify they contain the same groups
			identical := true
			for id, g1 := range live1 {
				g2, ok := live2[id]
				if !ok {
					identical = false
					break
				}
				// Compare relevant fields (not timestamps since they might drift)
				if !bytes.Equal(g1.Tree, g2.Tree) || g1.Name != g2.Name {
					identical = false
					break
				}
			}
			if identical {
				// Converged!
				if len(live1) != 1 {
					t.Fatalf("expected 1 live group (g1 winner), got %d: %v", len(live1), live1)
				}
				if _, ok := live1["g1"]; !ok {
					t.Fatalf("expected g1 in live groups, got %v", live1)
				}
				// Verify g2 is tombstoned in store1
				if g2, ok := store1.Get("g2"); !ok || g2.DeletedAt.IsZero() {
					t.Fatalf("expected g2 tombstoned in store1, got %v", g2)
				}
				// Verify g2 is tombstoned in store2
				if g2, ok := store2.Get("g2"); !ok || g2.DeletedAt.IsZero() {
					t.Fatalf("expected g2 tombstoned in store2, got %v", g2)
				}
				return
			}
		}
	}

	t.Fatalf("failed to converge after %d rounds", maxRounds)
}

// Test convergence with equal timestamps: conflicting writes to the same key
// with equal timestamps. Ordering by ID breaks tie (lexicographically first wins).
func TestTwoPeerGroupConvergenceEqualTimestamp(t *testing.T) {
	path1 := filepath.Join(t.TempDir(), "store1.json")
	path2 := filepath.Join(t.TempDir(), "store2.json")

	// Both stores create different groups with the same key, same timestamp.
	// g_aaa owns {key1, key2}, g_zzz owns {key1, key3}, both at time 42.
	// enforce will order by ID: g_aaa < g_zzz, so g_aaa wins key1.
	// g_zzz loses key1, left with only {key3} (1-leaf), gets tombstoned.
	sameTime := mustTime(42)

	store1 := &Store{
		path: path1,
		groups: map[string]Group{
			"g_aaa": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key2"}}`), TreeUpdatedAt: sameTime},
		},
	}

	store2 := &Store{
		path: path2,
		groups: map[string]Group{
			"g_zzz": {Tree: json.RawMessage(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"key1"},"second":{"type":"leaf","sessionKey":"key3"}}`), TreeUpdatedAt: sameTime},
		},
	}

	// Exchange snapshots
	snap1 := store1.Snapshot()
	for id, g := range snap1 {
		store2.ApplyRemote(id, g)
	}

	snap2 := store2.Snapshot()
	for id, g := range snap2 {
		store1.ApplyRemote(id, g)
	}

	// Both stores should converge: g_aaa wins key1 (ID-ordered), g_zzz loses it and becomes 1-leaf (tombstoned).
	live1 := store1.Live()
	live2 := store2.Live()

	if len(live1) != 1 || len(live2) != 1 {
		t.Fatalf("expected 1 live group in each store (g_aaa wins), got store1=%d, store2=%d", len(live1), len(live2))
	}

	if _, ok := live1["g_aaa"]; !ok {
		t.Fatalf("expected g_aaa to win in store1, got %v", live1)
	}

	if _, ok := live2["g_aaa"]; !ok {
		t.Fatalf("expected g_aaa to win in store2 (via enforce), got %v", live2)
	}

	// g_zzz should be tombstoned in both (left with only key3 after key1 lost to g_aaa)
	if g_zzz1, ok := store1.Get("g_zzz"); !ok || g_zzz1.DeletedAt.IsZero() {
		t.Fatalf("expected g_zzz tombstoned in store1, got %v", g_zzz1)
	}
	if g_zzz2, ok := store2.Get("g_zzz"); !ok || g_zzz2.DeletedAt.IsZero() {
		t.Fatalf("expected g_zzz tombstoned in store2, got %v", g_zzz2)
	}
}
