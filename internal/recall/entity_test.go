// Package recall — entity_test.go: EntityStore hermetic unit tests
// (Phase 9 alpha.20 Chunk 8.4, ADR-015). No Store or SQLite required;
// the index is built in-memory and tested directly.
//
// 6 tests:
//   1. Add + Lookup basic case (case-insensitive match).
//   2. Add idempotent (re-Add same memID + same values is no-op).
//   3. Add re-Add with different values replaces prior references.
//   4. Remove drops the row + decrements stats.
//   5. RowsContainingAnyEntity OR-semantics dedup + sort.
//   6. Concurrent reads safe after Close (sync.RWMutex).
package recall

import (
	"reflect"
	"sync"
	"testing"
)

func TestEntityStore_AddLookup_CaseInsensitive(t *testing.T) {
	s := NewEntityStore()
	s.Add(1, []string{"Dark", "Memory", "MCP"})
	s.Add(2, []string{"dark", "mcp", "alpha"})

	// "dark" should match both rows (case-insensitive).
	got := s.Lookup("DARK")
	want := []int64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(\"DARK\") = %v, want %v", got, want)
	}
	// "alpha" only row 2.
	got = s.Lookup("alpha")
	want = []int64{2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(\"alpha\") = %v, want %v", got, want)
	}
	// "mcp" both rows.
	got = s.Lookup("MCP")
	want = []int64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(\"MCP\") = %v, want %v", got, want)
	}
	// Unknown entity returns nil.
	got = s.Lookup("missing")
	if got != nil {
		t.Errorf("Lookup(\"missing\") = %v, want nil", got)
	}
	// Empty input returns nil.
	got = s.Lookup("")
	if got != nil {
		t.Errorf("Lookup(\"\") = %v, want nil", got)
	}
	// Whitespace input returns nil after trim.
	got = s.Lookup("   ")
	if got != nil {
		t.Errorf("Lookup(\"   \") = %v, want nil", got)
	}
}

func TestEntityStore_Add_Idempotent(t *testing.T) {
	s := NewEntityStore()
	s.Add(1, []string{"alpha", "beta", "gamma"})
	before := s.Stats()
	// Re-Add with the same values: no count drift.
	s.Add(1, []string{"alpha", "beta", "gamma"})
	after := s.Stats()
	if before != after {
		t.Errorf("re-Add same values drifted stats: before=%+v after=%+v", before, after)
	}
	// Re-Add with duplicate values within the same call: also no-op.
	s.Add(2, []string{"alpha", "alpha", "alpha"})
	got := s.Lookup("alpha")
	want := []int64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(\"alpha\") = %v, want %v", got, want)
	}
	// Distinct entity count should be 3 (alpha, beta, gamma).
	if got := s.Stats().DistinctCount; got != 3 {
		t.Errorf("DistinctCount = %d, want 3", got)
	}
	if got := s.Stats().RowCount; got != 2 {
		t.Errorf("RowCount = %d, want 2", got)
	}
}

func TestEntityStore_Add_ReplaceValuesForSameRow(t *testing.T) {
	s := NewEntityStore()
	s.Add(1, []string{"alpha", "beta", "gamma"})
	// Re-Add for row 1 with DIFFERENT values: replaces prior refs.
	s.Add(1, []string{"delta", "epsilon"})

	// "alpha" / "beta" / "gamma" should NOT be associated with row 1 anymore.
	for _, v := range []string{"alpha", "beta", "gamma"} {
		got := s.Lookup(v)
		if len(got) != 0 {
			t.Errorf("Lookup(%q) = %v after replace, want empty", v, got)
		}
	}
	// "delta" / "epsilon" should be only on row 1.
	if got := s.Lookup("delta"); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Lookup(\"delta\") = %v, want [1]", got)
	}
	if got := s.Lookup("epsilon"); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Lookup(\"epsilon\") = %v, want [1]", got)
	}
	// Stats: 1 row, 2 distinct entities, 2 total pairs.
	stats := s.Stats()
	if stats.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1", stats.RowCount)
	}
	if stats.DistinctCount != 2 {
		t.Errorf("DistinctCount = %d, want 2", stats.DistinctCount)
	}
	if stats.EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", stats.EntityCount)
	}
}

func TestEntityStore_Remove(t *testing.T) {
	s := NewEntityStore()
	s.Add(1, []string{"alpha", "beta"})
	s.Add(2, []string{"alpha", "gamma"})
	before := s.Stats()
	// "alpha" has 2 rows; remove row 1.
	s.Remove(1)
	// "alpha" should now have only row 2.
	if got := s.Lookup("alpha"); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("after Remove(1) Lookup(\"alpha\") = %v, want [2]", got)
	}
	// "beta" should be empty (only row 1 had it).
	if got := s.Lookup("beta"); len(got) != 0 {
		t.Errorf("after Remove(1) Lookup(\"beta\") = %v, want empty", got)
	}
	// "gamma" unchanged.
	if got := s.Lookup("gamma"); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("after Remove(1) Lookup(\"gamma\") = %v, want [2]", got)
	}
	// Stats: rowCount dropped by 1, entityCount dropped by 2 (alpha+beta for row 1).
	after := s.Stats()
	if after.RowCount != before.RowCount-1 {
		t.Errorf("RowCount = %d, want %d", after.RowCount, before.RowCount-1)
	}
	if after.EntityCount != before.EntityCount-2 {
		t.Errorf("EntityCount = %d, want %d", after.EntityCount, before.EntityCount-2)
	}
	// Remove a non-existent row: no-op, no panic.
	s.Remove(999)
	if after2 := s.Stats(); after2 != after {
		t.Errorf("Remove(999) drifted stats: %+v != %+v", after2, after)
	}
	// Remove row 0 / negative: no-op.
	s.Remove(0)
	s.Remove(-1)
}

func TestEntityStore_RowsContainingAnyEntity(t *testing.T) {
	s := NewEntityStore()
	s.Add(1, []string{"alpha", "beta"})
	s.Add(2, []string{"alpha", "gamma"})
	s.Add(3, []string{"delta"})

	// OR semantics: {alpha, delta} → rows {1, 2, 3}.
	got := s.RowsContainingAnyEntity([]string{"alpha", "delta"})
	want := []int64{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OR(alpha,delta) = %v, want %v", got, want)
	}
	// Single value: same as Lookup.
	got = s.RowsContainingAnyEntity([]string{"gamma"})
	want = []int64{2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OR(gamma) = %v, want %v", got, want)
	}
	// Empty input → nil.
	got = s.RowsContainingAnyEntity(nil)
	if got != nil {
		t.Errorf("OR(nil) = %v, want nil", got)
	}
	// Unknown entity only → nil.
	got = s.RowsContainingAnyEntity([]string{"unknown"})
	if got != nil {
		t.Errorf("OR(unknown) = %v, want nil", got)
	}
	// Mixed known + unknown: returns known only.
	got = s.RowsContainingAnyEntity([]string{"unknown", "beta"})
	want = []int64{1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OR(unknown,beta) = %v, want %v", got, want)
	}
	// Case-insensitive.
	got = s.RowsContainingAnyEntity([]string{"ALPHA", "DELTA"})
	want = []int64{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OR(ALPHA,DELTA) = %v, want %v", got, want)
	}
	// Dedup within the input slice.
	got = s.RowsContainingAnyEntity([]string{"alpha", "alpha", "alpha"})
	want = []int64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OR(alpha×3) = %v, want %v", got, want)
	}
}

func TestEntityStore_ConcurrentReads(t *testing.T) {
	s := NewEntityStore()
	// Build a non-trivial index.
	for id := int64(1); id <= 50; id++ {
		s.Add(id, []string{"common", "row" + string(rune('0'+int(id)%10))})
	}
	// Spawn 10 readers in parallel; all should see the same snapshot.
	var wg sync.WaitGroup
	for r := 0; r < 10; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = s.Lookup("common")
				_ = s.RowsContainingAnyEntity([]string{"common"})
				_ = s.EntitiesForRow(1)
				_ = s.Stats()
			}
		}()
	}
	// One writer that mutates the index (Add new row). This exercises
	// the RWMutex — readers should not race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for id := int64(100); id <= 110; id++ {
			s.Add(id, []string{"writer", "extra"})
		}
	}()
	wg.Wait()
	// Sanity: writer's rows visible after WaitGroup.
	if got := s.Lookup("writer"); len(got) != 11 {
		t.Errorf("after writer, Lookup(\"writer\") = %v (len=%d), want 11 rows", got, len(got))
	}
}