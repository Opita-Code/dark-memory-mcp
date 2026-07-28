// Package migrate — current_schema_version_test.go: unit tests for the
// CurrentSchemaVersion helper.
//
// What we verify:
//
//   - Returns 0 for an empty slice (defensive — production never
//     hits this but the contract should be explicit).
//   - Returns the single version for a single-element slice.
//   - Returns the maximum when versions are not strictly ascending
//     (defensive — production orders ascending but the helper
//     should not rely on it).
//   - Returns the last version for a strictly-ascending slice (the
//     common production shape).
//
// We do NOT test integration with the live sqlite.Migrations slice
// here — that lives in internal/migrate/sqlite/current_schema_version_test.go
// and asserts a specific numeric value (>=20 today). Keeping the two
// tests separate means changes to the migration count don't break
// the unit logic tests, and changes to the helper logic don't break
// the integration test.
package migrate

import "testing"

func TestCurrentSchemaVersion_EmptySlice(t *testing.T) {
	if got := CurrentSchemaVersion(nil); got != 0 {
		t.Errorf("CurrentSchemaVersion(nil) = %d, want 0", got)
	}
	if got := CurrentSchemaVersion([]Migration{}); got != 0 {
		t.Errorf("CurrentSchemaVersion([]Migration{}) = %d, want 0", got)
	}
}

func TestCurrentSchemaVersion_SingleMigration(t *testing.T) {
	migs := []Migration{{Version: 7, Name: "lone"}}
	if got := CurrentSchemaVersion(migs); got != 7 {
		t.Errorf("CurrentSchemaVersion single = %d, want 7", got)
	}
}

func TestCurrentSchemaVersion_MaxNotLast(t *testing.T) {
	// Out-of-order slice: the helper must find the max, not blindly
	// return the last element.
	migs := []Migration{
		{Version: 3},
		{Version: 10},
		{Version: 7},
		{Version: 2},
	}
	if got := CurrentSchemaVersion(migs); got != 10 {
		t.Errorf("CurrentSchemaVersion out-of-order = %d, want 10", got)
	}
}

func TestCurrentSchemaVersion_StrictlyAscending(t *testing.T) {
	// The production shape: Migrations slice is ordered ascending by
	// Version. Last element is the max.
	migs := []Migration{
		{Version: 1},
		{Version: 2},
		{Version: 5},
		{Version: 9},
	}
	if got := CurrentSchemaVersion(migs); got != 9 {
		t.Errorf("CurrentSchemaVersion ascending = %d, want 9", got)
	}
}

func TestCurrentSchemaVersion_DuplicatesTakesLarger(t *testing.T) {
	// Defensive: even if a slice contains two entries with the same
	// Version, CurrentSchemaVersion should still report that version
	// (not panic, not double-count).
	migs := []Migration{
		{Version: 5, Name: "first"},
		{Version: 5, Name: "second"}, // duplicate Version, different Name
		{Version: 3},
	}
	if got := CurrentSchemaVersion(migs); got != 5 {
		t.Errorf("CurrentSchemaVersion duplicates = %d, want 5", got)
	}
}
