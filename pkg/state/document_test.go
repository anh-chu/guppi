package state

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestPaneTreeJSONRoundTrip(t *testing.T) {
	tree := Split(
		DirectionHorizontal,
		Ratio(0.5),
		Leaf(SessionRef{Session: "abc"}),
		Split(
			DirectionVertical,
			Ratio(0.25),
			Leaf(SessionRef{Session: "abc", Pane: 1}),
			Leaf(SessionRef{Owner: "o1", Session: "def", Window: 2, Pane: 3}),
		),
	)

	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded PaneNode
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !treesEqual(decoded, tree) {
		t.Errorf("round-trip mismatch\n got  %#v\nwant %#v", decoded, tree)
	}
}

func treesEqual(a, b PaneNode) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}

func TestRatioFiniteValidation(t *testing.T) {
	cases := []struct {
		v    float64
		want bool
	}{
		{0.5, true},
		{0.001, true},
		{0.999, true},
		{0.0, false},
		{1.0, false},
		{-0.1, false},
		{1.1, false},
		{math.Inf(1), false},
		{math.Inf(-1), false},
		{math.NaN(), false},
	}
	for _, tc := range cases {
		if got := Ratio(tc.v).Valid(); got != tc.want {
			t.Errorf("Ratio(%v).Valid() = %v, want %v", tc.v, got, tc.want)
		}
	}

	var r Ratio
	if err := json.Unmarshal([]byte(`"bad"`), &r); err == nil {
		t.Error("expected JSON unmarshal to reject non-number ratio")
	}
	if err := json.Unmarshal([]byte(`1.0`), &r); err == nil {
		t.Error("expected JSON unmarshal to reject ratio 1.0")
	}
	if err := json.Unmarshal([]byte(`null`), &r); err == nil {
		t.Error("expected JSON unmarshal to reject null ratio")
	}
}

func TestAppDocumentSchemaValidation(t *testing.T) {
	owner := OwnerID("ownerdoc1234567890abcd")
	leaf := Leaf(SessionRef{Owner: owner, Session: SessionID("s1")})
	base := AppDocument{
		Schema:     SchemaVersion,
		Owner:      owner,
		Revision:   1,
		Sessions: []LocalSessionRecord{mkSession(owner, "sessdoc1234567890ab")},
		Workspace: &WorkspaceRecord{Revision: 0, Tree: &leaf},
	}

	if err := ValidateDocument(&base); err != nil {
		t.Fatalf("valid document failed: %v", err)
	}

	future := base
	future.Schema = SchemaVersion + 1
	if err := ValidateDocument(&future); err == nil {
		t.Errorf("expected schema %d to be rejected", SchemaVersion+1)
	}

	// Schema 2 -- the pre-canonical-schema transition layout (with
	// `_compat`-nested fields) -- is an old, unsupported schema under schema
	// 3 and must fail closed the same way any other unsupported schema does:
	// no migrator ever transforms or partially reads it.
	old2 := base
	old2.Schema = 2
	if err := ValidateDocument(&old2); err == nil {
		t.Error("expected schema 2 to be rejected")
	} else {
		var se StateError
		if !errors.As(err, &se) || se.Code != ErrBadSchema {
			t.Fatalf("expected ErrBadSchema, got %v", err)
		}
	}

	old := base
	old.Schema = 1
	if err := ValidateDocument(&old); err == nil {
		t.Error("expected schema 1 to be rejected")
	}
}

func mkSession(owner OwnerID, id string) LocalSessionRecord {
	return LocalSessionRecord{
		ID:         SessionID(id),
		Owner:      owner,
		Ref:        SessionRef{Owner: owner, Session: SessionID(id)},
		Phase:      SessionPhaseActive,
		Desired:    DesiredRun,
		Created:    time.Unix(0, 0).UTC(),
		Generation: "test-gen",
	}
}



func TestCommandReceiptBounds(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := CommandReceipt{
		ID:       NewCommandID(),
		IntentID: NewCommandID(),
		Seq:      1,
		Created:  now.Add(-MaxCommandReceiptAge + time.Second),
	}
	if err := ValidateCommandReceipt(fresh, now, MaxPendingCommands); err != nil {
		t.Errorf("fresh receipt should be valid: %v", err)
	}

	stale := CommandReceipt{
		ID:       NewCommandID(),
		IntentID: NewCommandID(),
		Seq:      2,
		Created:  now.Add(-MaxCommandReceiptAge - time.Second),
	}
	if err := ValidateCommandReceipt(stale, now, MaxPendingCommands); err == nil {
		t.Error("expected stale receipt to be rejected")
	}

	tooMany := CommandReceipt{
		ID:       NewCommandID(),
		IntentID: NewCommandID(),
		Seq:      3,
		Created:  now,
	}
	if err := ValidateCommandReceipt(tooMany, now, MaxPendingCommands+1); err == nil {
		t.Error("expected too many commands to be rejected")
	}
}

// TestFixtureSessionWithoutNewMetadataFieldsLoadsWithEmptyValues proves that
// the existing testdata/fixtures.json session fixture -- authored before
// LocalSessionRecord.AgentType/WorktreeBranch existed -- still unmarshals
// cleanly into LocalSessionRecord, with the new optional fields simply
// empty, instead of the fixture failing to load or the new fields being
// required.
func TestFixtureSessionWithoutNewMetadataFieldsLoadsWithEmptyValues(t *testing.T) {
	f := loadFixtures(t)
	var rec LocalSessionRecord
	if err := json.Unmarshal(f.Session, &rec); err != nil {
		t.Fatalf("unmarshal fixture session: %v", err)
	}
	if rec.ID != "sessionfixture1234567890" {
		t.Fatalf("unexpected fixture session id: %q", rec.ID)
	}
	if rec.AgentType != "" {
		t.Errorf("AgentType = %q, want empty for a fixture predating the field", rec.AgentType)
	}
	if rec.WorktreeBranch != "" {
		t.Errorf("WorktreeBranch = %q, want empty for a fixture predating the field", rec.WorktreeBranch)
	}
	if rec.ScheduleID != "" {
		t.Errorf("ScheduleID = %q, want empty for a fixture predating the field", rec.ScheduleID)
	}
}

// TestSchema4WorkspaceContract_FAILS proves the schema-4 target contract:
// AppDocument has a singleton Workspace with {Revision, Tree} only,
// and no Layouts or TmuxCatalogRevision fields.
func TestSchema4WorkspaceContract_FAILS(t *testing.T) {
	// This test documents the target schema-4 workspace shape.
	// It will FAIL against schema-3 HEAD because the contract does not yet exist.
	schema4Doc := AppDocument{
		Schema:   4,
		Owner:    OwnerID("owner1234567890abcd"),
		Revision: 1,
		Sessions: []LocalSessionRecord{mkSession(OwnerID("owner1234567890abcd"), "sess1234567890abcdef")},
		Workspace: &WorkspaceRecord{Revision: 1, Tree: nil}, // Empty workspace in schema 4
	}
	
	// Serialize and verify no "layouts" or "tmux_catalog_revision" appear in JSON.
	data, err := json.Marshal(schema4Doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	str := string(data)
	
	// These assertions FAIL now; they pass after Task 1.
	if containsLayoutsField := contains(str, "layouts"); containsLayoutsField {
		t.Error("schema 4 must not have 'layouts' field; found in JSON")
	}
	if containsTmuxRev := contains(str, "tmux_catalog_revision"); containsTmuxRev {
		t.Error("schema 4 must not have 'tmux_catalog_revision' field; found in JSON")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
