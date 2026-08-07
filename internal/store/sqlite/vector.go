// Package sqlite — vector.go: dense-vector encode, decode, cosine, RRF
// for PR-2 of the v2.9.0 plan (agent_memory row 160).
//
// The store keeps brute-force in-process cosine for v2.9.0; sqlite-vec
// (and pgvector on the Postgres path) are deliberately deferred to
// v2.9.1+ as drop-in acceleration. For typical vibe-coder row counts
// (hundreds to low thousands) brute force is well under a frame budget
// and adds zero binary footprint. If operators later complain about
// latency at scale, the dispatch in SearchAgentMemory can be flipped
// to use sqlite-vec behind the same SearchFilters signature.
//
// # Encoding
//
// One float32 → 4 bytes, little-endian, contiguous. Length derives
// from the embedder's Dim() at decode time — callers verify the
// length matches their active embedder or fall back to BM25 if the
// column was written by a different provider (mismatched Dim can
// still cosine-rank but the result is meaningless).
//
// # RRF
//
// Reciprocal Rank Fusion (Cormack et al., 2009): for each row, sum
// the rank contributions across arms, weighted. Empty arms
// contribute 0 (so a row only present in BM25 still gets a rank,
// proportional to its BM25 position). RowMap is keyed by row id.
package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// encodeVec serializes v as little-endian float32 contiguous bytes.
// Returns nil for nil input. Encoding is stable across processes
// (same v → same []byte on linux/amd64 vs mac/arm64) so a stored
// embedding can be decoded into a stable len() regardless of host
// architecture.
func encodeVec(v embedder.Vec) []byte {
	if len(v) == 0 {
		return nil
	}
	out := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:i*4+4], math.Float32bits(x))
	}
	return out
}

// decodeVec inverts encodeVec. If raw is nil/empty, returns nil. If
// raw length is not a multiple of 4, returns ErrInvalidEmbedding so
// callers can drop the row rather than rank garbage.
//
// The expected dim is informational — if raw is 4 * expected bytes,
// decode succeeds. Otherwise the caller decides whether to skip
// (different provider wrote the row, or migration incomplete).
func decodeVec(raw []byte, expectedDim int) (embedder.Vec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw)%4 != 0 {
		return nil, ErrInvalidEmbedding
	}
	n := len(raw) / 4
	if expectedDim > 0 && n != expectedDim {
		return nil, ErrInvalidEmbedding
	}
	out := make(embedder.Vec, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}

// ErrInvalidEmbedding is returned by decodeVec when the BLOB cannot
// be deserialized (wrong length / non-multiple-of-4). Stored along
// with embedder.ErrDisabled in the dispatch path's errors.Is check.
var ErrInvalidEmbedding = errors.New("sqlite: invalid embedding blob (wrong length or non-aligned)")

// cosine returns the cosine similarity between a and b. Returns 0
// for zero-length inputs OR for mismatched dimensions (caller should
// drop mismatched rows before calling cosine). The result is in
// [-1, +1] for unit vectors; the brute-force loop in the store
// normalizes vectors at write time (mock.go's contract) so the
// cosine is just a dot product for the deterministic test path;
// production vectors from OpenAI / ONNX are not unit-length, so we
// compute the full cosine.
//
// NaN-safe: zero norms return 0 (don't pollute ranking with NaN).
func cosine(a, b embedder.Vec) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < len(a); i++ {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	denom := math.Sqrt(na * nb)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// rrfHit is one row's RRF score + axis ranks. The store consumes
// this to assemble SearchHit values for Mode="rrf".
type rrfHit struct {
	ID         int64
	Score      float64
	BM25Rank   int // 0 = not present in BM25 axis
	VectorRank int // 0 = not present in vector axis
}

// rrfRank fuses bm25 and vector rank lists into one. Each axis
// contributes weight / (k + rank_pos). rank_pos is 1-based; the
// 0 entry means "absent from this axis" and contributes nothing.
//
// k=60 is the value Cormack et al. recommend as a reasonable
// default; row 160 adopts it without further tuning. Weights
// default to 1.0 each.
//
// bm25Ranks / vectorRanks: map of row id -> 1-based rank position.
// Rows in only one axis are included (with rank 0 in the absent
// axis, contributing weight/(k+0)=weight/k — so a row ranked #1
// in BM25 alone contributes 1/60, same magnitude as #59 in the
// other axis). This is intentional RRF behavior.
func rrfRank(bm25Ranks, vectorRanks map[int64]int, k int, wBM25, wVector float64) []rrfHit {
	if k <= 0 {
		k = 60
	}
	if wBM25 == 0 {
		wBM25 = 1.0
	}
	if wVector == 0 {
		wVector = 1.0
	}

	seen := map[int64]struct{}{}
	for id := range bm25Ranks {
		seen[id] = struct{}{}
	}
	for id := range vectorRanks {
		seen[id] = struct{}{}
	}

	out := make([]rrfHit, 0, len(seen))
	for id := range seen {
		h := rrfHit{ID: id}
		if r, ok := bm25Ranks[id]; ok && r > 0 {
			h.BM25Rank = r
			h.Score += wBM25 / float64(k+r)
		}
		if r, ok := vectorRanks[id]; ok && r > 0 {
			h.VectorRank = r
			h.Score += wVector / float64(k+r)
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		// Primary: score DESC. Tie-break: lower row id ASC for
		// determinism (test reproducibility).
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// assembleVectorHits converts a ranked vec+id slice into SearchHit
// values, applying the AgentMemory fields. The store's load function
// pre-fetches the AgentMemory rows in scope, so this helper does
// pure joins.
func assembleVectorHits(rows map[int64]agentmemory.AgentMemory, ranked []idScore, limit int) []agentmemory.SearchHit {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]agentmemory.SearchHit, 0, len(ranked))
	for i, r := range ranked {
		row, ok := rows[r.ID]
		if !ok {
			continue
		}
		h := agentmemory.SearchHit{
			AgentMemory: row,
			Rank:        r.Score,
			VectorRank:  i + 1,
		}
		out = append(out, h)
	}
	return out
}

// idScore pairs a row id with its cosine rank position (or RRF
// score — see caller). Used as the wire between rankArm() and the
// SearchHit assembly.
type idScore struct {
	ID    int64
	Score float64
}

// searchByVector embeds f.Query, loads every agent_memory row in the
// active project with a non-NULL embedding, ranks by cosine
// similarity, and returns the top f.Limit as SearchHits. Rows
// without an embedding are silently skipped (their vector axis
// contribution is 0); Mode="vector" by definition has no BM25 arm.
//
// Implementation note: loads ALL embedding-bearing rows for the
// active project, NOT just the top-N candidates. This is the
// v2.9.0 brute-force design (row 160 PR-2 cross-cutting).
// Operators with hundreds of thousands of rows should opt into
// v2.9.1's sqlite-vec acceleration; for the vibe-coder scale
// (hundreds to low thousands of rows), brute force is well under
// a frame budget on commodity hardware.
//
// Active embedder requirement: returns embedder.ErrDisabled wrapped
// when Store.Embedder() is the disabled stub. Callers can fall
// back to Mode="bm25" on that error.
func (s *Store) searchByVector(ctx context.Context, f agentmemory.SearchFilters) ([]agentmemory.SearchHit, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	e := s.Embedder()
	if e == nil || e.Kind() == embedder.KindNone {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory mode=vector: %w (set Mode=\"bm25\" or configure an embedder)", embedder.ErrDisabled)
	}
	qvecs, err := e.Embed(ctx, []string{f.Query})
	if err != nil {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory: embed query: %w", err)
	}
	if len(qvecs) == 0 || len(qvecs[0]) == 0 {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory: embedder returned empty vector")
	}
	qvec := qvecs[0]

	activeProject := s.ActiveProject()
	rows, err := s.loadAgentMemoryWithEmbedding(ctx, f, activeProject)
	if err != nil {
		return nil, err
	}

	type scored struct {
		ID    int64
		Score float64
	}
	ranked := make([]scored, 0, len(rows.embeddings))
	for id, vec := range rows.embeddings {
		ranked = append(ranked, scored{ID: id, Score: cosine(qvec, vec)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ID < ranked[j].ID
	})

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	out := make([]agentmemory.SearchHit, 0, len(ranked))
	for i, r := range ranked {
		row, ok := rows.rows[r.ID]
		if !ok {
			continue
		}
		hit := agentmemory.SearchHit{
			AgentMemory: row,
			Rank:        r.Score,
			VectorRank:  i + 1,
		}
		out = append(out, hit)
	}
	// v2.9.0 PR-3: optional entity-axis filter (AND semantics).
	return s.applyEntityFilter(ctx, out, f.Entities)
}

// searchByRRF runs BM25 + vector arms in parallel, fuses via RRF.
// Both arms are run unconditionally; rows in only one arm still
// contribute (RRF semantics from Cormack et al., 2009). Active
// embedder requirement is the same as Mode="vector".
func (s *Store) searchByRRF(ctx context.Context, f agentmemory.SearchFilters) ([]agentmemory.SearchHit, error) {
	if err := s.requireProject(); err != nil {
		return nil, err
	}
	e := s.Embedder()
	if e == nil || e.Kind() == embedder.KindNone {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory mode=rrf: %w (set Mode=\"bm25\" or configure an embedder)", embedder.ErrDisabled)
	}
	qvecs, err := e.Embed(ctx, []string{f.Query})
	if err != nil {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory: embed query: %w", err)
	}
	if len(qvecs) == 0 || len(qvecs[0]) == 0 {
		return nil, fmt.Errorf("sqlite: SearchAgentMemory: embedder returned empty vector")
	}
	qvec := qvecs[0]

	// BM25 arm: existing path.
	bm25Hits, err := s.searchByBM25(ctx, f)
	if err != nil {
		return nil, err
	}
	bm25Ranks := map[int64]int{}
	for i, h := range bm25Hits {
		bm25Ranks[h.ID] = i + 1
	}

	// Vector arm: identical to searchByVector, but we capture the
	// rank map before the limit-cap so the RRF fusion sees every
	// rank the embedder gave us (capping BEFORE fusion would
	// silently bias results toward the dominant arm).
	rows, err := s.loadAgentMemoryWithEmbedding(ctx, f, s.ActiveProject())
	if err != nil {
		return nil, err
	}
	type scored struct {
		ID    int64
		Score float64
	}
	scoredVec := make([]scored, 0, len(rows.embeddings))
	for id, vec := range rows.embeddings {
		scoredVec = append(scoredVec, scored{ID: id, Score: cosine(qvec, vec)})
	}
	sort.Slice(scoredVec, func(i, j int) bool {
		if scoredVec[i].Score != scoredVec[j].Score {
			return scoredVec[i].Score > scoredVec[j].Score
		}
		return scoredVec[i].ID < scoredVec[j].ID
	})
	vecRanks := map[int64]int{}
	for i, s := range scoredVec {
		vecRanks[s.ID] = i + 1
	}

	fused := rrfRank(bm25Ranks, vecRanks, f.RRFK, f.RRFWeightBM25, f.RRFWeightVector)

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(fused) > limit {
		fused = fused[:limit]
	}

	// Pull AgentMemory rows for every id in fused (some may be
	// BM25-only, some vector-only — easier to load by id set).
	ids := make([]int64, 0, len(fused))
	for _, h := range fused {
		ids = append(ids, h.ID)
	}
	rowMap, err := s.loadAgentMemoryByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]agentmemory.SearchHit, 0, len(fused))
	for _, h := range fused {
		row, ok := rowMap[h.ID]
		if !ok {
			continue
		}
		hit := agentmemory.SearchHit{
			AgentMemory: row,
			Rank:        h.Score,
			BM25Rank:    h.BM25Rank,
			VectorRank:  h.VectorRank,
			RRFScore:    h.Score,
		}
		// If the row came from BM25 only, surface its BM25 rank as
		// the canonical Rank too (avoids "0" confusing callers).
		if h.BM25Rank > 0 && h.VectorRank == 0 {
			hit.Rank = float64(h.BM25Rank) * -1 // mirror FTS5 sign
		} else if h.VectorRank > 0 && h.BM25Rank == 0 {
			hit.Rank = scoredVec[h.VectorRank-1].Score
		}
		out = append(out, hit)
	}
	// v2.9.0 PR-3: optional entity-axis filter (AND semantics). The
	// RRF score is preserved through the prune.
	return s.applyEntityFilter(ctx, out, f.Entities)
}

// embeddingRows is the pre-loaded pair of AgentMemory metadata plus
// per-row embeddings. Avoids the N+1 query problem: vector + rrf
// arms both reach for the same rows in the same scan.
type embeddingRows struct {
	rows       map[int64]agentmemory.AgentMemory
	embeddings map[int64]embedder.Vec
}

// loadAgentMemoryWithEmbedding runs one SELECT per the agent_memory
// rows in scope (INV-7 project_id + Optional filters inherited from
// f), reading the embedding BLOB column directly. Decoding errors
// are SKIPPED (the row is dropped from the result set rather than
// failing the whole query — operators with a half-migrated DB
// still get useful hits from rows with intact embeddings).
//
// The filter chain (f.Kind, f.MemoryType, f.AgentID, f.Operator)
// is shared with the BM25 path; only the FTS5 MATCH and archived_at
// filter differ.
func (s *Store) loadAgentMemoryWithEmbedding(ctx context.Context, f agentmemory.SearchFilters, activeProject string) (*embeddingRows, error) {
	where := []string{"row.project_id = ?", "row.embedding IS NOT NULL"}
	args := []any{activeProject}
	if f.Kind != "" {
		where = append(where, "row.kind = ?")
		args = append(args, f.Kind)
	}
	if f.MemoryType != "" {
		where = append(where, "row.memory_type = ?")
		args = append(args, f.MemoryType)
	}
	if f.AgentID != "" {
		where = append(where, "row.agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.Operator != "" {
		where = append(where, "row.operator = ?")
		args = append(args, f.Operator)
	}

	query := `
		SELECT row.id, row.project_id, COALESCE(row.session_id, ''), row.operator,
		       COALESCE(row.agent_id, ''), row.kind, COALESCE(row.memory_type, ''),
		       COALESCE(row.title, ''), row.content, COALESCE(row.tags, ''),
		       row.pinned, row.created_at, row.updated_at,
		       COALESCE(row.archived_at, ''), COALESCE(row.expires_at, ''),
		       row.embedding
		  FROM agent_memory AS row
		 WHERE ` + strings.Join(where, " AND ") + `
		   AND row.archived_at IS NULL`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("agent_memory: hybrid rows: %w", err)
	}
	defer rows.Close()

	out := &embeddingRows{
		rows:       map[int64]agentmemory.AgentMemory{},
		embeddings: map[int64]embedder.Vec{},
	}
	for rows.Next() {
		var m agentmemory.AgentMemory
		var pinned int
		var raw []byte
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.Operator,
			&m.AgentID, &m.Kind, &m.MemoryType,
			&m.Title, &m.Content, &m.Tags, &pinned,
			&m.CreatedAt, &m.UpdatedAt, &m.ArchivedAt, &m.ExpiresAt,
			&raw); err != nil {
			return nil, fmt.Errorf("agent_memory: hybrid scan: %w", err)
		}
		m.Pinned = pinned != 0
		vec, err := decodeVec(raw, 0)
		if err != nil {
			// Drop corrupted row rather than fail the whole query.
			continue
		}
		out.rows[m.ID] = m
		out.embeddings[m.ID] = vec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_memory: hybrid rows iter: %w", err)
	}
	return out, nil
}

// loadAgentMemoryByIDs pulls AgentMemory rows by primary key. Used
// by the RRF fusion path to assemble the final SearchHit list from
// the (perhaps BM25-only, perhaps vector-only) id set.
func (s *Store) loadAgentMemoryByIDs(ctx context.Context, ids []int64) (map[int64]agentmemory.AgentMemory, error) {
	if len(ids) == 0 {
		return map[int64]agentmemory.AgentMemory{}, nil
	}
	out := make(map[int64]agentmemory.AgentMemory, len(ids))
	// Chunk to avoid sqlite's MAX_VARIABLE_NUMBER limit on huge
	// IN clauses (default 999 per SQLite; chunked 200 here for
	// safety on lower-bound drivers).
	const chunkSize = 200
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, s.ActiveProject())
		q := `
			SELECT id, project_id, COALESCE(session_id, ''), operator,
			       COALESCE(agent_id, ''), kind, COALESCE(memory_type, ''),
			       COALESCE(title, ''), content, COALESCE(tags, ''),
			       pinned, created_at, updated_at,
			       COALESCE(archived_at, ''), COALESCE(expires_at, '')
			  FROM agent_memory
			 WHERE project_id = ? AND id IN (` + ph + `)`
		args = append(args, chunkAsAny(chunk)...)
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("agent_memory: lookup ids: %w", err)
		}
		for rows.Next() {
			var m agentmemory.AgentMemory
			var pinned int
			if err := rows.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.Operator,
				&m.AgentID, &m.Kind, &m.MemoryType,
				&m.Title, &m.Content, &m.Tags, &pinned,
				&m.CreatedAt, &m.UpdatedAt, &m.ArchivedAt, &m.ExpiresAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("agent_memory: lookup scan: %w", err)
			}
			m.Pinned = pinned != 0
			out[m.ID] = m
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("agent_memory: lookup rows: %w", err)
		}
	}
	return out, nil
}

// chunkAsAny is a tiny helper to convert []int64 → []any in one
// place. Saves repeating the loop at call sites; trivial cost.
func chunkAsAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, x := range ids {
		out[i] = x
	}
	return out
}
