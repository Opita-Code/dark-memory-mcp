// Package recall — entity.go: Entity + EntityStore for ProGraph
// 2-layer entity extraction (Phase 9 alpha.20 Chunk 8.4, ADR-015).
//
// # Scope
//
// This file is the v3 in-memory index that backs the ProGraph BFS.
// The on-disk side-table is agent_memory_entities (per
// internal/store/sqlite/entity.go); EntityStore here is a hot-path
// index built from Store queries during MultiHopRetrieve. It is
// NOT a write-through cache — every MultiHopRetrieve call rebuilds
// the index for the rows it visits, then drops it.
//
// # Why an in-memory index
//
// ProGraph BFS visits each row's entity list once per hop. A naive
// per-row lookup (call GetAgentMemoryEntities for each row) is
// O(rows × entities) queries. The index inverts the cost to
// O(rows + entities) by:
//
//	entity_value (lower-case)  ->  set<mem_id>
//	mem_id                      ->  []entity_value (reverse)
//
// The index is project-scoped at construction time — callers pass
// the Store and we use Store.ActiveProject() to filter. Cross-project
// reads return empty (INV-7).
//
// # Determinism
//
// BFS depends on a stable expansion order. EntityStore:
//
//   - Normalizes entity values to lower-case at Insert.
//   - Sorts the rows-per-entity set by mem_id ASC at insertion.
//   - Exposes RowsContainingAnyEntity([]string) which dedups input
//     and returns the union of matching row IDs sorted ASC.
//
// The deterministic ordering keeps MultiHopRetrieve reproducible
// across runs with the same input (testable without flakes).
//
// # Entity struct
//
// Entity is a SHALLOW shape distinct from internal/entity.Entity and
// internal/agentmemory.Entity: it carries an ID + Type (entity_kind)
// that the upstream types don't. We could have aliased instead, but
// ProGraph callers care about (ID, Type, Value, Confidence) and
// neither upstream carries all four. The shape is wire-compatible
// with internal/agentmemory.Entity via the JSON contract (Value,
// Source, Confidence, Model map cleanly onto ProGraph's (id, type,
// value, confidence) — Source doubles as Type when present).
package recall

import (
	"sort"
	"strings"
	"sync"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
)

// Entity is one extracted noun phrase from a row's content + title +
// tags, plus the type/kind classifier (alpha.21+ for LLM-extracted
// kinds; empty for v3 PR-3 deterministic).
//
// Fields:
//
//   - ID: optional row-side id (used by callers who want to dedup
//     against the parent row's id); 0 for entities not yet persisted.
//   - Type: entity_kind — "noun" | "verb" | "concept" | "" (untyped).
//     v3 PR-3 deterministic extractor leaves this empty; v4alpha
//         classifier (alpha.21+) will populate.
//   - Value: the lower-cased noun phrase (canonical for case-insensitive
//     matching).
//   - Confidence: 0..1 score. v3 PR-3 emits 1.0 (no model); drift_judge
//     extractor (alpha.21+) emits 0 < c < 1.
//
// JSON contract: Value is the primary identity field; the rest are
// metadata. Two Entities with the same Value collapse in the index
// (they originate from the same row + same noun phrase).
type Entity struct {
	ID         int64   `json:"id,omitempty"`
	Type       string  `json:"type,omitempty"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// EntityStore is the in-memory index backing ProGraph BFS. It is
// safe for concurrent reads after the building goroutine calls
// Close. Building is NOT thread-safe — callers must build the index
// (via Add/AddRow loop) before exposing the EntityStore to other
// goroutines.
//
// The index has two maps:
//
//	byEntity   map[entityValue] map[memID]struct{}
//	byRow      map[memID]        map[entityValue]struct{}
//
// Both are maintained together — Insert / Remove update both.
// The reverse map lets Remove drop a row's entity references
// atomically without scanning byEntity.
type EntityStore struct {
	mu sync.RWMutex // guards byEntity + byRow
	// byEntity maps lower-cased entity value to the set of row IDs
	// that mention it. Inner map[memID]struct{} is a set.
	byEntity map[string]map[int64]struct{}
	// byRow maps row ID to the set of entity values mentioned by
	// that row. Inner map[string]struct{} is a set.
	byRow map[int64]map[string]struct{}
	// stats counters (filled at Insert; reset at Remove).
	rowCount      int
	entityCount   int // total (value, row) pairs (after dedup at Insert)
	distinctCount int // number of distinct entity values
}

// NewEntityStore constructs an empty EntityStore.
func NewEntityStore() *EntityStore {
	return &EntityStore{
		byEntity: make(map[string]map[int64]struct{}),
		byRow:    make(map[int64]map[string]struct{}),
	}
}

// Add inserts one row's entity list into the index. memID is the
// parent row id (e.g. agent_memory.id). The values are normalized:
// trimmed, lower-cased, deduped within the input slice (the first
// occurrence wins).
//
// Idempotent: calling Add twice with the same (memID, values) is a
// no-op (no duplicate entries; no count drift). Re-calling Add with
// the same memID but a DIFFERENT values slice removes the prior
// entity references first (call Remove(memID) for explicit removal).
//
// Empty values, all-whitespace values, and values shorter than 3
// characters are skipped (matches internal/entity.Entity's
// minTokenLen = 3 policy; deterministic across the stack).
func (s *EntityStore) Add(memID int64, values []string) {
	if memID <= 0 || len(values) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addLocked(memID, values)
}

// addLocked is Add without the lock. Caller MUST hold s.mu.
func (s *EntityStore) addLocked(memID int64, values []string) {
	// If memID already in the index, remove the prior entity
	// references first so Add is idempotent under re-extraction.
	if _, exists := s.byRow[memID]; exists {
		s.removeLocked(memID)
	}
	// Dedupe within the input slice (case-insensitive).
	added := 0
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if len(v) < 3 {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		if s.byEntity[v] == nil {
			s.byEntity[v] = make(map[int64]struct{})
			s.distinctCount++
		}
		if _, ok := s.byEntity[v][memID]; !ok {
			s.byEntity[v][memID] = struct{}{}
			added++
		}
		if s.byRow[memID] == nil {
			s.byRow[memID] = make(map[string]struct{})
			s.rowCount++
		}
		s.byRow[memID][v] = struct{}{}
	}
	s.entityCount += added
}

// AddEntity is the variant for callers who have the typed Entity
// struct (carries Type + Confidence). The same normalization applies.
// Type + Confidence are NOT indexed (only Value matters for graph
// traversal); they are passed through to MultiHopRetrieve consumers
// via Entity in the result.
func (s *EntityStore) AddEntity(memID int64, entities []Entity) {
	if memID <= 0 || len(entities) == 0 {
		return
	}
	values := make([]string, 0, len(entities))
	for _, e := range entities {
		values = append(values, e.Value)
	}
	s.Add(memID, values)
}

// AddFromAgentMemory is the convenience bridge for v3 PR-3 callers
// that already have internal/agentmemory.Entity (which is an alias
// of internal/entity.Entity). Extracts the Value field.
func (s *EntityStore) AddFromAgentMemory(memID int64, entities []entity.Entity) {
	if memID <= 0 || len(entities) == 0 {
		return
	}
	values := make([]string, 0, len(entities))
	for _, e := range entities {
		values = append(values, e.Value)
	}
	s.Add(memID, values)
}

// AddFromInternalEntity is the same as AddFromAgentMemory but for
// internal/entity.Entity (used by tests and orchestrator code).
// Both shapes carry the same field (Value); this is just an alias
// for clarity at call sites.
func (s *EntityStore) AddFromInternalEntity(memID int64, entities []entity.Entity) {
	s.AddFromAgentMemory(memID, entities)
}

// ToAgentMemory maps the EntityStore's view back to the canonical
// internal/agentmemory.Entity shape — used by consumers who need
// the wire type. Lossy: Entity.Type and Entity.Confidence collapse
// into Source + Confidence respectively (since the canonical wire
// shape doesn't carry a Type field).
func (s *EntityStore) ToAgentMemory(memID int64) []agentmemory.Entity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals, ok := s.byRow[memID]
	if !ok {
		return nil
	}
	out := make([]agentmemory.Entity, 0, len(vals))
	for v := range vals {
		out = append(out, agentmemory.Entity{
			Value:      v,
			Source:     entity.SourceDeterministic,
			Confidence: 1.0,
			Model:      "",
		})
	}
	// Sort by value ASC for determinism (mirrors the Store contract).
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// Remove drops a row's entity references from the index. Safe to
// call on a non-existent memID (no-op). Updates the stats counters.
func (s *EntityStore) Remove(memID int64) {
	if memID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(memID)
}

// removeLocked is Remove without the lock. Caller MUST hold s.mu.
func (s *EntityStore) removeLocked(memID int64) {
	vals, ok := s.byRow[memID]
	if !ok {
		return
	}
	for v := range vals {
		if rs, ok := s.byEntity[v]; ok {
			delete(rs, memID)
			if len(rs) == 0 {
				delete(s.byEntity, v)
				s.distinctCount--
			}
			s.entityCount--
		}
	}
	delete(s.byRow, memID)
	s.rowCount--
}

// Lookup returns the sorted list of mem_ids that contain the given
// entity value (case-insensitive — stored values are lower-cased).
// Returns nil for unknown entity. Empty result slice means "no row
// mentions this entity", which is distinct from "unknown entity"
// only when you care about the difference (most callers don't).
func (s *EntityStore) Lookup(value string) []int64 {
	if value == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := strings.ToLower(strings.TrimSpace(value))
	rs, ok := s.byEntity[v]
	if !ok || len(rs) == 0 {
		return nil
	}
	out := make([]int64, 0, len(rs))
	for id := range rs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RowsContainingAnyEntity returns the deduped, sorted mem_id list
// of rows that mention at least one of the given values (OR
// semantics, case-insensitive). Empty input or no match → returns
// nil (not an empty slice — caller checks with len() == 0).
//
// This is the BFS frontier expansion primitive for ProGraph.
func (s *EntityStore) RowsContainingAnyEntity(values []string) []int64 {
	if len(values) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]struct{})
	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "" {
			continue
		}
		for id := range s.byEntity[v] {
			seen[id] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// EntitiesForRow returns the sorted list of entity values mentioned
// by the given row. Nil if the row is unknown.
func (s *EntityStore) EntitiesForRow(memID int64) []string {
	if memID <= 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals, ok := s.byRow[memID]
	if !ok || len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for v := range vals {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// EntityStoreStats is a snapshot of the index for diagnostics +
// observability. Cheap to compute.
type EntityStoreStats struct {
	RowCount      int // distinct row IDs indexed
	DistinctCount int // distinct entity values indexed
	EntityCount   int // total (value, row) pairs (after dedup)
}

// Stats returns the current index counters.
func (s *EntityStore) Stats() EntityStoreStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return EntityStoreStats{
		RowCount:      s.rowCount,
		DistinctCount: s.distinctCount,
		EntityCount:   s.entityCount,
	}
}

// Size is a synonym for Stats().RowCount for quick length checks.
func (s *EntityStore) Size() int { return s.Stats().RowCount }