// Package sqlite — entity.go: agent_memory_entities helpers (PR-3 of
// v2.9.0 plan, agent_memory row 160).
//
// The entity side-table holds extracted noun phrases per
// agent_memory row. PR-3 ships a deterministic extractor
// (internal/entity); PR-3.1 will add a drift_judge bridge.
//
// # Backward compat (row 160 PR-3)
//   - extract_entities is OPT-IN. SaveAgentMemory only writes entity
//     rows when the caller populates m.Entities (transient field).
//   - Entity filter on search is OPT-IN. f.Entities empty → no
//     filter (pre-PR-3 behavior).
//
// # Cross-project isolation
//   - GetAgentMemoryEntities joins on agent_memory.project_id =
//     active_project. Cross-project reads return nil.
//   - SearchAgentMemory applies the entity filter after the BM25 /
//     vector / RRF arms produce their candidate set; the filter is
//     a post-rank prune that preserves the original ranking order.
package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
)

// GetAgentMemoryEntities returns the entity list for one row,
// sorted by entity ASC. Returns nil (not an error) when the row
// has no entities or doesn't exist in the active project (idempotent).
//
// INV-7 cross-project isolation: the SELECT joins agent_memory on
// project_id = active project. Cross-project reads return nil +
// nil; the operator cannot enumerate entities across tenants.
func (s *Store) GetAgentMemoryEntities(ctx context.Context, memID int64) ([]agentmemory.Entity, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if memID <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ent.entity, ent.source, ent.confidence, COALESCE(ent.model, '')
		  FROM agent_memory_entities ent
		  JOIN agent_memory        row ON row.id = ent.mem_id
		 WHERE ent.mem_id = ? AND row.project_id = ?
		 ORDER BY ent.entity ASC`,
		memID, s.ActiveProject())
	if err != nil {
		return nil, fmt.Errorf("agent_memory_entities: query: %w", err)
	}
	defer rows.Close()
	var out []agentmemory.Entity
	for rows.Next() {
		var e agentmemory.Entity
		if err := rows.Scan(&e.Value, &e.Source, &e.Confidence, &e.Model); err != nil {
			return nil, fmt.Errorf("agent_memory_entities: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_memory_entities: rows: %w", err)
	}
	return out, nil
}

// applyEntityFilter prunes hits whose row does NOT carry every
// entity in f.Entities (AND semantics, case-insensitive). The
// surviving hits keep their original order — entity filter is
// a post-rank prune, never a re-rank. f.Entities empty → returns
// hits unchanged (no-op).
//
// Used by searchByBM25, searchByVector, searchByRRF so the
// filter is uniform across the three modes (row 160 PR-3
// cross-cutting).
func (s *Store) applyEntityFilter(ctx context.Context, hits []agentmemory.SearchHit, entities []string) ([]agentmemory.SearchHit, error) {
	if len(entities) == 0 || len(hits) == 0 {
		return hits, nil
	}
	// Lowercase the filter set once; comparison against stored
	// values is also lowercase (store INSERT lowercases).
	wanted := make(map[string]struct{}, len(entities))
	for _, e := range entities {
		wanted[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}

	// Collect mem_ids we still need to consult.
	ids := make([]int64, 0, len(hits))
	seen := map[int64]struct{}{}
	for _, h := range hits {
		if _, ok := seen[h.ID]; ok {
			continue
		}
		seen[h.ID] = struct{}{}
		ids = append(ids, h.ID)
	}
	entitiesByMemID, err := s.loadEntitiesForMemIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]agentmemory.SearchHit, 0, len(hits))
	for _, h := range hits {
		rowEnts, ok := entitiesByMemID[h.ID]
		if !ok {
			// Row has zero entities → cannot match a non-empty filter.
			continue
		}
		allPresent := true
		for w := range wanted {
			if _, has := rowEnts[w]; !has {
				allPresent = false
				break
			}
		}
		if allPresent {
			out = append(out, h)
		}
	}
	return out, nil
}

// loadEntitiesForMemIDs returns a map[mem_id]map[entity]true for the
// given mem_ids. Used by applyEntityFilter to avoid N+1 queries.
//
// Cross-project guard: callers MUST filter the mem_ids to the
// active project's rows BEFORE calling this helper. The search
// dispatchers (searchByBM25 / searchByVector / searchByRRF) all
// restrict their candidate set by project_id = active_project,
// so the mem_ids here are always project-scoped.
func (s *Store) loadEntitiesForMemIDs(ctx context.Context, memIDs []int64) (map[int64]map[string]bool, error) {
	if len(memIDs) == 0 {
		return map[int64]map[string]bool{}, nil
	}
	out := make(map[int64]map[string]bool, len(memIDs))
	// Chunk to avoid sqlite's MAX_VARIABLE_NUMBER limit on huge
	// IN clauses (default 999 per SQLite; chunked 200 here for
	// safety on lower-bound drivers).
	const chunkSize = 200
	for start := 0; start < len(memIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(memIDs) {
			end = len(memIDs)
		}
		chunk := memIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		query := fmt.Sprintf(`
			SELECT mem_id, entity
			  FROM agent_memory_entities
			 WHERE mem_id IN (%s)`, placeholders)
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("agent_memory_entities: bulk query: %w", err)
		}
		for rows.Next() {
			var memID int64
			var entity string
			if err := rows.Scan(&memID, &entity); err != nil {
				rows.Close()
				return nil, fmt.Errorf("agent_memory_entities: bulk scan: %w", err)
			}
			if out[memID] == nil {
				out[memID] = map[string]bool{}
			}
			out[memID][entity] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("agent_memory_entities: bulk rows: %w", err)
		}
	}
	return out, nil
}

// ListAgentMemoryByAnyEntity returns the deduped mem_id list (in the
// active project) whose entity list contains at least one of the
// given values (OR semantics, case-insensitive — stored entities
// are already lowercase per the Save path at store.go:4392 which
// ToLower-insents before INSERT). The result is sorted by mem_id
// ASC for determinism (ProGraph BFS depends on a stable frontier
// expansion order — see internal/recall/prograph.go MultiHopRetrieve).
//
// Chunked at 200 placeholders per query to stay safely under SQLite's
// default MAX_VARIABLE_NUMBER (999).
//
// Phase 9 alpha.20 Chunk 8.4 (ProGraph 2-layer entity extraction,
// ADR-015). The complement to applyEntityFilter (AND semantics) for
// the ProGraph BFS frontier expansion. Returns (nil, nil) for empty
// input or no match — callers do not treat that as an error.
//
// Cross-project isolation: filters rows via JOIN on agent_memory
// project_id = active project. Rows from other projects never leak
// (INV-7).
func (s *Store) ListAgentMemoryByAnyEntity(ctx context.Context, entityValues []string) ([]int64, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	if len(entityValues) == 0 {
		return nil, nil
	}
	// Lowercase + dedup + trim the input. Stored entities are
	// already lowercase but the input may carry any case.
	seen := map[string]struct{}{}
	values := make([]string, 0, len(entityValues))
	for _, v := range entityValues {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		values = append(values, v)
	}
	if len(values) == 0 {
		return nil, nil
	}
	// sort for determinism (chunk iteration order must be stable)
	// and to keep the chunk composition reproducible across runs.
	sort.Strings(values)

	out := make([]int64, 0)
	seenRows := map[int64]struct{}{}
	const chunkSize = 200
	for start := 0; start < len(values); start += chunkSize {
		end := start + chunkSize
		if end > len(values) {
			end = len(values)
		}
		chunk := values[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		for _, v := range chunk {
			args = append(args, v)
		}
		args = append(args, s.ActiveProject())
		query := fmt.Sprintf(`
			SELECT DISTINCT ent.mem_id
			  FROM agent_memory_entities ent
			  JOIN agent_memory        row ON row.id = ent.mem_id
			 WHERE ent.entity IN (%s)
			   AND row.project_id = ?
			 ORDER BY ent.mem_id ASC`, placeholders)
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("agent_memory_entities by-any-entity: query: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("agent_memory_entities by-any-entity: scan: %w", err)
			}
			if _, ok := seenRows[id]; ok {
				continue
			}
			seenRows[id] = struct{}{}
			out = append(out, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("agent_memory_entities by-any-entity: rows: %w", err)
		}
	}
	return out, nil
}
