// Phase 4 Chunk 4.3 — vibe.Artifact + ArtifactStore project_id tests.
//
// Three tests cover the project_id column in vibe_artifacts:
//
//  1. TestArtifactInsert_ProjectIDPersisted — Insert with
//     ProjectID stamps the column; Get returns it.
//  2. TestArtifactInsert_EmptyProjectIDDefaults — Insert with
//     empty ProjectID falls back to 'default'.
//  3. TestPipelinePublish_AuditStampedWithProjectID — Publish
//     threads art.ProjectID into the audit_log row.
package vibe

import (
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
)

// TestArtifactInsert_ProjectIDPersisted verifies the column is
// stamped when the caller sets ProjectID.
func TestArtifactInsert_ProjectIDPersisted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	specID := insertFakeSpec(t, db)

	art := &Artifact{
		SpecID:    specID,
		ProjectID: "proj-huila",
		Type:      ArtifactTypeCode,
		URL:       "file:///tmp/foo.go",
	}
	store := NewArtifactStore(db)
	id, err := store.Insert(ctx, art)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// SELECT directly to verify the column was stamped.
	var got string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM vibe_artifacts WHERE id = ?", id,
	).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "proj-huila" {
		t.Errorf("project_id = %q; want proj-huila", got)
	}

	// Get should also surface it.
	roundtrip, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if roundtrip.ProjectID != "proj-huila" {
		t.Errorf("Get.ProjectID = %q; want proj-huila", roundtrip.ProjectID)
	}
}

// TestArtifactInsert_EmptyProjectIDDefaults verifies the
// pre-Phase-4 contract (empty ProjectID → 'default' via DEFAULT).
func TestArtifactInsert_EmptyProjectIDDefaults(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	specID := insertFakeSpec(t, db)

	art := &Artifact{
		SpecID: specID,
		Type:   ArtifactTypeCode,
		URL:    "file:///tmp/foo.go",
	}
	store := NewArtifactStore(db)
	id, err := store.Insert(ctx, art)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var got string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM vibe_artifacts WHERE id = ?", id,
	).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if got != "default" {
		t.Errorf("project_id = %q; want default (column DEFAULT)", got)
	}
}

// TestPipelinePublish_AuditStampedWithProjectID verifies the
// audit_log row emitted by Publish carries art.ProjectID.
func TestPipelinePublish_AuditStampedWithProjectID(t *testing.T) {
	db := newTestDB(t)
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project.ApplyProjectIDColumns: %v", err)
	}
	p, _, _ := newTestPipeline(t, db)

	ctx := context.Background()
	specID := insertFakeSpec(t, db)
	art := &Artifact{
		SpecID:    specID,
		ProjectID: "proj-huila",
		Type:      ArtifactTypeCode,
		URL:       "file:///tmp/foo.go",
	}
	if _, err := p.Publish(ctx, art, "test intent"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Look at the last audit_log row; the Publish audit emits
	// event=vibe.publish with operator="pipeline". Find by
	// payload substring (use the v4alpha audit_log schema where
	// payload is stored as BLOB; LIKE works on the textual form).
	var projectID string
	if err := db.QueryRowContext(ctx,
		"SELECT project_id FROM audit_log WHERE actor = 'pipeline' ORDER BY audit_id DESC LIMIT 1",
	).Scan(&projectID); err != nil {
		t.Fatalf("SELECT audit_log: %v", err)
	}
	if projectID != "proj-huila" {
		t.Errorf("audit_log.project_id = %q; want proj-huila", projectID)
	}
}
