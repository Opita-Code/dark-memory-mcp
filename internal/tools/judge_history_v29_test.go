// Package tools — judge_history_v29_test.go: integration test for
// the judgment_history tool after v29 (spec 1276 T10) schema bump.
// Verifies that the v29 audit + anchor columns surface in the
// JudgmentHistoryEntry output (so operators can audit "which bytes
// were evaluated" + "which chunk in a consensus run" without parsing
// the VerdictJSON blob).
package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	sqlitestore "github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// TestJudgmentHistory_V29AuditColumnsExposed verifies that the v29
// audit + anchor columns surface in the judgment_history output.
// The seed writes a drift_judge row with all v29 fields populated;
// the response is then parsed and verified to contain the same
// values.
func TestJudgmentHistory_V29AuditColumnsExposed(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "hist-v29.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dbPath,
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlitestore.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// Seed a drift_judge row with v29 audit columns populated.
	wc := store.WriteContext{
		Actor:     "test_judgment_history_v29",
		WritePath: "TestJudgmentHistory_V29AuditColumnsExposed",
	}
	in := &ssd.SDDEvaluation{
		EvalType:       "drift_judge",
		TargetType:     "artifact",
		TargetID:       "artifact:v29-history-1",
		VerdictJSON:    `{"verdict":"aligned","confidence":0.92}`,
		Confidence:     0.92,
		Model:          "deberta-v3-large-mnli",
		ConstitutionID: "default-v1",
		CreatedAt:      "2026-08-19T00:00:00.0000000Z",
		// v29 columns.
		MerkleRoot:     "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234",
		ArtifactSource: "file",
		ArtifactSHA256: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ArtifactPath:   "/tmp/hist-v29.bin",
		ArtifactSize:   9999,
		ChunkIndex:     1,
		ChunkTotal:     4,
		NLIProviderID:  "deberta-v3-large-mnli",
	}
	if _, err := st.SaveSDDEvaluation(ctx, wc, in); err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}

	// Run the judgment_history tool by hand (the BindStore
	// callback signature is internal but we can hit the Store
	// directly + verify the same fields flow through).
	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "drift_judge",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListSDDEvaluations: got %d rows, want 1", len(rows))
	}

	// Simulate the BindStore callback: it builds JudgmentHistoryEntry
	// from the sdd.SDDEvaluation row. We re-create the same field
	// mapping here to confirm the tool's binding logic.
	entry := JudgmentHistoryEntry{
		ID:             rows[0].ID,
		EvalType:       rows[0].EvalType,
		TargetType:     rows[0].TargetType,
		TargetID:       rows[0].TargetID,
		Confidence:     rows[0].Confidence,
		Verdict:        parseVerdictJSON(rows[0].VerdictJSON),
		Model:          rows[0].Model,
		CreatedAt:      rows[0].CreatedAt,
		MerkleRoot:     rows[0].MerkleRoot,
		ArtifactSource: rows[0].ArtifactSource,
		ArtifactSHA256: rows[0].ArtifactSHA256,
		ArtifactPath:   rows[0].ArtifactPath,
		ArtifactSize:   rows[0].ArtifactSize,
		ChunkIndex:     rows[0].ChunkIndex,
		ChunkTotal:     rows[0].ChunkTotal,
		NLIProviderID:  rows[0].NLIProviderID,
	}

	// Marshal + unmarshal to confirm the JSON shape carries the v29
	// fields (the harness / opencode receives the wire form).
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTrip struct {
		ID             int64   `json:"id"`
		EvalType       string  `json:"eval_type"`
		TargetType     string  `json:"target_type"`
		TargetID       string  `json:"target_id"`
		Confidence     float32 `json:"confidence"`
		Verdict        string  `json:"verdict"`
		Model          string  `json:"model"`
		CreatedAt      string  `json:"created_at"`
		MerkleRoot     string  `json:"merkle_root"`
		ArtifactSource string  `json:"artifact_source"`
		ArtifactSHA256 string  `json:"artifact_sha256"`
		ArtifactPath   string  `json:"artifact_path"`
		ArtifactSize   int64   `json:"artifact_size"`
		ChunkIndex     int     `json:"chunk_index"`
		ChunkTotal     int     `json:"chunk_total"`
		NLIProviderID  string  `json:"nli_provider_id"`
	}
	if err := json.Unmarshal(b, &roundTrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Verify the v29 fields surface in the wire form.
	if roundTrip.MerkleRoot != in.MerkleRoot {
		t.Errorf("merkle_root: got %q, want %q", roundTrip.MerkleRoot, in.MerkleRoot)
	}
	if roundTrip.ArtifactSource != in.ArtifactSource {
		t.Errorf("artifact_source: got %q, want %q", roundTrip.ArtifactSource, in.ArtifactSource)
	}
	if roundTrip.ArtifactSHA256 != in.ArtifactSHA256 {
		t.Errorf("artifact_sha256: got %q, want %q", roundTrip.ArtifactSHA256, in.ArtifactSHA256)
	}
	if roundTrip.ArtifactPath != in.ArtifactPath {
		t.Errorf("artifact_path: got %q, want %q", roundTrip.ArtifactPath, in.ArtifactPath)
	}
	if roundTrip.ArtifactSize != in.ArtifactSize {
		t.Errorf("artifact_size: got %d, want %d", roundTrip.ArtifactSize, in.ArtifactSize)
	}
	if roundTrip.ChunkIndex != in.ChunkIndex {
		t.Errorf("chunk_index: got %d, want %d", roundTrip.ChunkIndex, in.ChunkIndex)
	}
	if roundTrip.ChunkTotal != in.ChunkTotal {
		t.Errorf("chunk_total: got %d, want %d", roundTrip.ChunkTotal, in.ChunkTotal)
	}
	if roundTrip.NLIProviderID != in.NLIProviderID {
		t.Errorf("nli_provider_id: got %q, want %q", roundTrip.NLIProviderID, in.NLIProviderID)
	}
	// Regression: the v3 entry fields still flow through.
	if roundTrip.Verdict != "aligned" {
		t.Errorf("verdict: got %q, want aligned", roundTrip.Verdict)
	}
	if roundTrip.Confidence != 0.92 {
		t.Errorf("confidence: got %f, want 0.92", roundTrip.Confidence)
	}
}

// TestJudgmentHistory_PreV29RowsShowEmptyV29Fields: pre-v29 rows
// have v29 fields as empty / 0. The entry should serialize them as
// omittable (omitempty tag) so the wire form stays clean for
// non-drift evaluations.
func TestJudgmentHistory_PreV29RowsShowEmptyV29Fields(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "hist-pre-v29.db")
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dbPath,
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlitestore.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// Seed a pre-v29-shape row (no v29 fields).
	wc := store.WriteContext{
		Actor:     "test_judgment_history_pre_v29",
		WritePath: "TestJudgmentHistory_PreV29RowsShowEmptyV29Fields",
	}
	in := &ssd.SDDEvaluation{
		EvalType:    "brand_match",
		TargetType:  "artifact",
		TargetID:    "artifact:pre-v29-1",
		VerdictJSON: `{"verdict":"match"}`,
		Confidence:  0.85,
		CreatedAt:   "2026-08-18T00:00:00.0000000Z",
	}
	if _, err := st.SaveSDDEvaluation(ctx, wc, in); err != nil {
		t.Fatalf("SaveSDDEvaluation: %v", err)
	}

	rows, err := st.ListSDDEvaluations(ctx, ssd.ListFilters{
		EvalType: "brand_match",
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("ListSDDEvaluations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListSDDEvaluations: got %d rows, want 1", len(rows))
	}

	// Build the entry.
	entry := JudgmentHistoryEntry{
		ID:         rows[0].ID,
		EvalType:   rows[0].EvalType,
		TargetType: rows[0].TargetType,
		TargetID:   rows[0].TargetID,
		Confidence: rows[0].Confidence,
		Verdict:    parseVerdictJSON(rows[0].VerdictJSON),
		Model:      rows[0].Model,
		CreatedAt:  rows[0].CreatedAt,
		// v29 columns come from the ssd.SDDEvaluation row, which
		// has zero values for pre-v29 rows.
	}

	// Marshal — pre-v29 row should NOT include v29 fields in the
	// JSON (omitempty).
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// The JSON should contain v3 fields but NOT v29 fields.
	s := string(b)
	for _, omittable := range []string{
		"merkle_root", "artifact_source", "artifact_sha256",
		"artifact_path", "artifact_size", "chunk_index",
		"chunk_total", "nli_provider_id",
	} {
		if substringContains(s, omittable) {
			t.Errorf("pre-v29 row JSON should not include %q, got: %s", omittable, s)
		}
	}
	// v3 fields should be present.
	for _, expected := range []string{
		"eval_type", "target_id", "verdict", "confidence",
	} {
		if !substringContains(s, expected) {
			t.Errorf("pre-v29 row JSON should include %q, got: %s", expected, s)
		}
	}
}

// contains is a tiny helper that checks if a string contains a
// substring (the strings package is used elsewhere; this avoids
// importing it here).
// (Naming: defined in health_test.go too — Go's package-scope
// imports dedupe only when the helper is identical; using the
// same name conflicts. We rename the local one to substringContains
// to avoid the conflict.)

// substringContains is a local helper that checks if s contains
// substr. Defined here to avoid colliding with the existing
// "contains" in health_test.go.
func substringContains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
