// Package dual_driver_test — phase1_implementations_test.go: B2-D
// Phase 1 implementations integration tests (spec 2012).
//
// Phase 0 (commit 534d784) established a live-postgres CI job that
// only ran the boot smoke. Phase 1 fills 19 of the ~54 notImpl stubs
// in internal/store/postgres/store.go with real pgx-backed
// implementations (Constitution/Mod/ActiveSession/ActiveSubagent/
// BrandGuide). This file adds one happy-path round-trip test per
// surface so the postgres-smoke CI job can verify the new methods
// against a live PostgreSQL without waiting for the full
// runContract(t, ctx, s, "postgres") integration.
//
// Gating: every test t.Skip()s when DARK_TEST_POSTGRES_DSN is unset,
// matching the rest of the dual_driver package. On CI, the
// postgres-smoke job (added in ci.yml, extended in B2-D Phase 1)
// sets this env var after spinning up postgres:16-alpine.
//
// Pin lockstep UNCHANGED: the SQLite path is not exercised here.
// The existing SQLite-only tests in dual_driver cover that surface
// independently. Phase 1 only adds PG coverage; SQLite parity is
// preserved by mirroring the SQLite SQL 1:1 in pgx (with TIMESTAMP
// casts where columns differ).
package dual_driver_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/constitution"
	"github.com/dark-agents/dark-memory-mcp/internal/mods"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// openPhase1Store opens a fresh PostgreSQL-backed Store with the
// 'default' project created + active. Mirrors the SQLite-side helper
// freshActiveSessionStore (active_session_test.go:24) so the call
// sites read identically across drivers.
func openPhase1Store(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("DARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DARK_TEST_POSTGRES_DSN not set; skipping postgres phase1 implementation test (CI-only)")
	}
	ctx := context.Background()
	s, err := runtime.Open(ctx, store.Config{
		Driver: store.DriverPostgres,
		DSN:    dsn,
	})
	if err != nil {
		t.Fatalf("postgres open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProject(ctx, &project.Project{
		ProjectID:   "default",
		DisplayName: "Phase1 Test",
	}); err != nil && err.Error() != "project already exists" {
		// PG's CreateProject is idempotent on UNIQUE conflict;
		// accept any "already exists" message rather than failing
		// on shared CI DBs.
		if !isProjectExists(err) {
			t.Fatalf("create default: %v", err)
		}
	}
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set default active: %v", err)
	}
	return s
}

// isProjectExists is a small helper because the project-package
// stores ErrProjectExists as a typed error only — without dragging in
// that package's surface in this test file, we string-match instead.
func isProjectExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg != "" && (contains(msg, "project already exists") ||
		contains(msg, "duplicate key") ||
		contains(msg, "UNIQUE constraint failed"))
}

func contains(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

// indexOf avoids pulling strings into the file imports. Tiny
// private reimpl since strings.Contains is essentially equivalent;
// we keep this module lean on transitive deps.
func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// wcFor is a local WriteContext factory for Phase 1 tests. There is
// also one in tests/dual_driver/agent_memory_test.go:74 with the
// same signature, but it lives in a different (compiled) package —
// our `_test` package can't see it. So we redefine with identical
// shape: actor = op, writePath literal.
func wcFor(op, writePath string) store.WriteContext {
	return store.WriteContext{
		Actor:     op,
		WritePath: writePath,
	}
}

// TestPhase1_Constitution_Roundtrip covers Save / Get / List /
// VerifyConstitutionHash for the v2 constitution methods on Postgres.
// Mirror of sqlite/store.go:2842 (Save uses ON CONFLICT DO UPDATE on
// (constitution_id, version)) and sqlite/store.go:2888 (Get empty
// version = latest enabled).
func TestPhase1_Constitution_Roundtrip(t *testing.T) {
	s := openPhase1Store(t)
	ctx := context.Background()

	c := &constitution.Constitution{
		ConstitutionID: "phase1/test",
		Version:        "1.0.0",
		Label:          "phase1-fixture",
		Source:         "phase1",
		FilePath:       "/dev/null",
		ParsedJSON:     `{"identity":"phase1"}`,
		SHA256:         "phase1-fake-sha",
		Enabled:        true,
	}
	if err := s.SaveConstitution(ctx, wcFor("phase1", "phase1-test"), c); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Exact-version Get.
	got, err := s.GetConstitution(ctx, "phase1/test", "1.0.0")
	if err != nil {
		t.Fatalf("get exact: %v", err)
	}
	if got == nil || got.SHA256 != "phase1-fake-sha" {
		t.Errorf("get exact: nil=%v sha=%q", got == nil, gotOrEmpty(got))
	}

	// Latest-enabled Get (empty version).
	latest, err := s.GetConstitution(ctx, "phase1/test", "")
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil || latest.Version != "1.0.0" {
		t.Errorf("get latest: version got %v", latest)
	}

	// List.
	list, err := s.ListConstitutions(ctx, 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, c2 := range list {
		if c2.ConstitutionID == "phase1/test" && c2.Version == "1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("list did not include phase1/test@1.0.0; got %d entries", len(list))
	}

	// Verify.
	if ok, err := s.VerifyConstitutionHash(ctx, "phase1/test", "phase1-fake-sha"); err != nil || !ok {
		t.Errorf("verify: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.VerifyConstitutionHash(ctx, "phase1/test", "wrong"); ok {
		t.Error("verify with wrong sha: should be false")
	}
}

// TestPhase1_Mod_Roundtrip covers Save / Get / List / RecordModLoad /
// ListModLoads for the mods catalog (global) + mod_loads (project).
// Mirror of sqlite/store.go:3011 + 3056 + 3065 + 3134 + 3172.
func TestPhase1_Mod_Roundtrip(t *testing.T) {
	s := openPhase1Store(t)
	ctx := context.Background()

	m := &mods.Mod{
		ModID:        "phase1-mod",
		Name:         "phase1 mod",
		Version:      "1.0.0",
		Source:       "phase1",
		ManifestJSON: `{}`,
		SHA256:       "phase1-fake",
		RiskClass:    "governance",
		TargetScope:  "default",
		RequiresTor:  false,
	}
	if err := s.SaveMod(ctx, wcFor("phase1", "phase1-test"), m); err != nil {
		t.Fatalf("save mod: %v", err)
	}

	got, err := s.GetMod(ctx, "phase1-mod")
	if err != nil || got == nil {
		t.Fatalf("get mod: got=%v err=%v", got, err)
	}
	if got.Name != "phase1 mod" {
		t.Errorf("mod name: got %q", got.Name)
	}

	list, err := s.ListMods(ctx, 5)
	if err != nil {
		t.Fatalf("list mods: %v", err)
	}
	found := false
	for _, m2 := range list {
		if m2.ModID == "phase1-mod" {
			found = true
		}
	}
	if !found {
		t.Errorf("list mods: phase1-mod missing")
	}

	// Record a load.
	load := &mods.ModLoad{
		ModID:             "phase1-mod",
		SessionID:         "phase1-test-sess",
		LoadedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		DurationMs:        42,
		CapabilitiesCount: 5,
		ConstitutionID:    "phase1/test",
	}
	id, err := s.RecordModLoad(ctx, wcFor("phase1", "phase1-test-sess"), load)
	if err != nil || id <= 0 {
		t.Fatalf("record load: id=%d err=%v", id, err)
	}

	loads, err := s.ListModLoads(ctx, "phase1-mod", 10)
	if err != nil {
		t.Fatalf("list loads: %v", err)
	}
	if len(loads) < 1 {
		t.Errorf("list loads: empty, want >=1")
	} else {
		if loads[0].CapabilitiesCount != 5 {
			t.Errorf("load capabilities: got %d want 5", loads[0].CapabilitiesCount)
		}
	}
}

// TestPhase1_ActiveSession_Roundtrip covers Set / Get / Clear for
// projects.active_session_id with CAS semantics on Clear.
// Mirror of sqlite/store.go:4218 + 4270 + 4294 (and tests at
// active_session_test.go:53).
func TestPhase1_ActiveSession_Roundtrip(t *testing.T) {
	s := openPhase1Store(t)
	ctx := context.Background()

	if err := s.SetActiveSession(ctx, "default", "phase1-sess-A"); err != nil {
		t.Fatalf("set A: %v", err)
	}
	got, err := s.GetActiveSession(ctx, "default")
	if err != nil || got != "phase1-sess-A" {
		t.Errorf("get A: got=%q err=%v want phase1-sess-A", got, err)
	}

	// Bump to B.
	if err := s.SetActiveSession(ctx, "default", "phase1-sess-B"); err != nil {
		t.Fatalf("set B: %v", err)
	}
	if got, _ := s.GetActiveSession(ctx, "default"); got != "phase1-sess-B" {
		t.Errorf("get B: got %q want phase1-sess-B", got)
	}

	// Stale CAS — should be a no-op, active_session_id must still be B.
	if err := s.ClearActiveSession(ctx, "default", "phase1-sess-NONE"); err != nil {
		// Stale CAS may be silent in SQLite; in PG it returns no error
		// because RowsAffected=0 is silent. Accept either way.
		t.Logf("clear stale: %v (silent on PG)", err)
	}
	if got, _ := s.GetActiveSession(ctx, "default"); got != "phase1-sess-B" {
		t.Errorf("stale CAS: got %q want phase1-sess-B", got)
	}

	// Real CAS — clear B.
	if err := s.ClearActiveSession(ctx, "default", "phase1-sess-B"); err != nil {
		t.Fatalf("clear B: %v", err)
	}
	if got, _ := s.GetActiveSession(ctx, "default"); got != "" {
		t.Errorf("after clear: got %q want empty", got)
	}
}

// TestPhase1_ActiveSubagent_Roundtrip covers Set / Get / Clear / Sweep
// for active_subagents. Regression target: Set must return id > 0
// (per tests/dual_driver/agent_memory_v2_8_0_test.go:155). Sweep must
// not delete live rows that haven't expired.
func TestPhase1_ActiveSubagent_Roundtrip(t *testing.T) {
	s := openPhase1Store(t)
	ctx := context.Background()

	row := &store.ActiveSubagent{
		Operator:      "phase1-alice",
		SubagentID:    "phase1-sub-1",
		ParentAgentID: "phase1-parent",
		TTLSeconds:    3600,
	}
	id, err := s.SetActiveSubagent(ctx, wcFor("phase1", "phase1-test"), row)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if id <= 0 {
		t.Errorf("set: id=%d want >0", id)
	}
	if row.ID != id {
		t.Errorf("row.ID: got %d want %d", row.ID, id)
	}
	if row.ProjectID == "" {
		t.Error("row.ProjectID should be filled post-Set")
	}

	got, err := s.GetActiveSubagent(ctx, "phase1-alice")
	if err != nil || got == nil {
		t.Fatalf("get: got=%v err=%v", got, err)
	}
	if got.SubagentID != "phase1-sub-1" {
		t.Errorf("get subagent_id: got %q", got.SubagentID)
	}

	// Sweep live row — must NOT be deleted.
	n, err := s.SweepExpiredSubagents(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("sweep of live row: deleted=%d want=0", n)
	}

	// Refresh with negative-TTL-via-zero ttl: now() + 0 == expired.
	// The contract defaults ttl<=0 to 3600, so we can't actually set
	// a zero-TTL row via this entry point. We therefore force
	// expiry by using a row with TTLSeconds=1, sleeping 2 seconds,
	// and re-Sweep.
	row2 := &store.ActiveSubagent{
		Operator:      "phase1-alice",
		SubagentID:    "phase1-sub-ttl1",
		ParentAgentID: "phase1-parent",
		TTLSeconds:    1,
	}
	if _, err := s.SetActiveSubagent(ctx, wcFor("phase1", "phase1-test"), row2); err != nil {
		t.Fatalf("set ttl1: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // > 1s TTL
	n, err = s.SweepExpiredSubagents(ctx)
	if err != nil {
		t.Fatalf("sweep post-ttl: %v", err)
	}
	if n < 1 {
		t.Errorf("sweep should delete the expired ttl-1 row: deleted=%d want>=1", n)
	}

	// Clear the live one.
	if err := s.ClearActiveSubagent(ctx, wcFor("phase1", "phase1-test"), "phase1-alice", "phase1-sub-1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, err := s.GetActiveSubagent(ctx, "phase1-alice")
	if err != nil {
		t.Fatalf("get post-clear: %v", err)
	}
	if cleared != nil {
		t.Errorf("post-clear: got %+v want nil", cleared)
	}
}

// TestPhase1_BrandGuide_Roundtrip covers Save / Get / Delete / List
// for vibe_brands, exercising the new (project_id, brand_id)
// UNIQUE from migration v34. tenant isolation invariant:
// SaveBrandGuide for 'default' must NOT collide with a brand_id saved
// under a different project (handled separately by the
// contract tests in dual_driver/store_test.go; here we just verify
// basic round-trip in the active project).
func TestPhase1_BrandGuide_Roundtrip(t *testing.T) {
	s := openPhase1Store(t)
	ctx := context.Background()

	b := &vibeflow.BrandGuide{
		BrandID:    "phase1-brand-1",
		Voice:      "neutral",
		Visual:     "{}",
		Narrative:  "phase1",
		Compliance: "{}",
	}
	if err := s.SaveBrandGuide(ctx, wcFor("phase1", "phase1-test"), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetBrandGuide(ctx, "phase1-brand-1")
	if err != nil || got == nil {
		t.Fatalf("get: got=%v err=%v", got, err)
	}
	if got.Voice != "neutral" {
		t.Errorf("get voice: got %q want neutral", got.Voice)
	}

	list, err := s.ListBrandGuides(ctx, 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, b2 := range list {
		if b2.BrandID == "phase1-brand-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("list missing phase1-brand-1")
	}

	if err := s.DeleteBrandGuide(ctx, wcFor("phase1", "phase1-test"), "phase1-brand-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted, err := s.GetBrandGuide(ctx, "phase1-brand-1")
	if err != nil {
		t.Fatalf("get post-delete: %v", err)
	}
	if deleted != nil {
		t.Errorf("post-delete: got %+v want nil", deleted)
	}

	// Path-safety: invalid chars rejected.
	if err := s.DeleteBrandGuide(ctx, wcFor("phase1", "phase1-test"), "../etc"); err == nil {
		t.Error("delete with '/': should error")
	}
}

// gotOrEmpty dereferences a *constitution.Constitution for
// diagnostic logging without panicking on nil.
func gotOrEmpty(c *constitution.Constitution) string {
	if c == nil {
		return ""
	}
	return c.SHA256
}
