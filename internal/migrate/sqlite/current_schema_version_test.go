// Package sqlite — current_schema_version_test.go: integration test
// that the package-level CurrentVersion() helper returns the highest
// VERSION in the compiled-in Migrations slice.
//
// This test guards against two regressions:
//
//  1. Adding a migration to ddl.go but forgetting to update some
//     hardcoded version number elsewhere in the codebase (the original
//     v2.6.0 concern that motivated this helper). If you bump the
//     migration count, this test still passes; if you REMOVE migrations,
//     this test fails with a clear "expected N got M" message.
//
//  2. Someone bypassing CurrentVersion() and re-introducing a hardcoded
//     SchemaVersion constant somewhere (this test would fail indirectly
//     if internal/tools/agent_bootstrap.go stops reading from here —
//     see TestDetectEnvironment_ReportsCurrentVersion in tests/tools/).
//
// The expected value >= 20 reflects the v2.4.1+v2.6.0 state. When a
// new migration lands (v21), this test must be updated to reflect
// the new floor. We don't pin to an exact number to keep this test
// resilient to new migrations that don't change the helper contract.
package sqlite

import "testing"

func TestCurrentVersion_AtLeastV20(t *testing.T) {
	got := CurrentVersion()
	if got < 20 {
		t.Errorf("CurrentVersion() = %d, want >= 20 (the v2.4.1 schema version). "+
			"If you bumped the migration count down, restore v20. "+
			"If you added v21+, update this floor to the new minimum.", got)
	}
}

func TestCurrentVersion_MatchesMaxMigration(t *testing.T) {
	// Cross-check: the function's output equals the max version
	// found by iterating the slice directly. Catches the case where
	// someone adds a migration but accidentally slices the wrong
	// range or shadows Migrations with a smaller local var.
	got := CurrentVersion()
	var direct int
	for _, m := range Migrations {
		if m.Version > direct {
			direct = m.Version
		}
	}
	if got != direct {
		t.Errorf("CurrentVersion() = %d, direct max = %d — they must agree", got, direct)
	}
}
