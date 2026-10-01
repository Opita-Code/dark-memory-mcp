// Package audit_test — Phase 6 alpha.18.1 ADR-019 tests (payload split).
package audit_test

import (
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// TestExtractPayloadFields_ValidJSON verifies JSON payloads parse
// correctly into structured fields.
func TestExtractPayloadFields_ValidJSON(t *testing.T) {
	payload := []byte(`{"event":"agent_memory.save","id":42,"operator":"nico","kind":"decision"}`)
	got := audit.ExtractPayloadFields(payload)

	if got.Event != "agent_memory.save" {
		t.Errorf("Event = %q, want agent_memory.save", got.Event)
	}
	if got.ID != 42 {
		t.Errorf("ID = %d, want 42", got.ID)
	}
	if got.Kind != "decision" {
		t.Errorf("Kind = %q, want decision", got.Kind)
	}
}

// TestExtractPayloadFields_Empty verifies empty payload returns zero.
func TestExtractPayloadFields_Empty(t *testing.T) {
	got := audit.ExtractPayloadFields(nil)
	if got.Event != "" || got.ID != 0 || got.Kind != "" {
		t.Errorf("expected zero PayloadFields, got %+v", got)
	}
}

// TestExtractPayloadFields_NotJSON verifies non-JSON payload returns zero.
func TestExtractPayloadFields_NotJSON(t *testing.T) {
	got := audit.ExtractPayloadFields([]byte("not json"))
	if got.Event != "" || got.ID != 0 || got.Kind != "" {
		t.Errorf("expected zero PayloadFields for non-JSON, got %+v", got)
	}
}

// TestExtractPayloadFields_PartialJSON verifies payloads with only some
// keys fill only the present fields.
func TestExtractPayloadFields_PartialJSON(t *testing.T) {
	payload := []byte(`{"event":"session.start"}`)
	got := audit.ExtractPayloadFields(payload)
	if got.Event != "session.start" {
		t.Errorf("Event = %q, want session.start", got.Event)
	}
	if got.ID != 0 {
		t.Errorf("ID = %d, want 0 (absent)", got.ID)
	}
	if got.Kind != "" {
		t.Errorf("Kind = %q, want empty (absent)", got.Kind)
	}
}

// TestExtractPayloadFields_IDAsInt verifies JSON int (not float) parses.
func TestExtractPayloadFields_IDAsInt(t *testing.T) {
	payload := []byte(`{"id":7}`)
	got := audit.ExtractPayloadFields(payload)
	if got.ID != 7 {
		t.Errorf("ID = %d, want 7", got.ID)
	}
}

// TestExtractPayloadFields_IDAsFloat verifies JSON float (the default
// for numbers in encoding/json Unmarshal) parses correctly.
func TestExtractPayloadFields_IDAsFloat(t *testing.T) {
	payload := []byte(`{"id":3.14}`)
	got := audit.ExtractPayloadFields(payload)
	if got.ID != 3 {
		t.Errorf("ID = %d, want 3 (truncated from 3.14)", got.ID)
	}
}

// TestApplyPayloadColumns_Idempotent verifies the migration is safe to
// apply multiple times.
func TestApplyPayloadColumns_Idempotent(t *testing.T) {
	ctx := context.Background()
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	if err := audit.ApplyPayloadColumns(ctx, db); err != nil {
		t.Fatalf("ApplyPayloadColumns (first): %v", err)
	}
	if err := audit.ApplyPayloadColumns(ctx, db); err != nil {
		t.Fatalf("ApplyPayloadColumns (second): %v", err)
	}

	for _, col := range []string{"payload_event", "payload_id", "payload_kind"} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name = ?",
			col,
		).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 1 {
			t.Errorf("column %s not present after ApplyPayloadColumns", col)
		}
	}
}

// TestAddPayloadIndex_Idempotent verifies the index is safe to add multiple times.
func TestAddPayloadIndex_Idempotent(t *testing.T) {
	ctx := context.Background()
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	if err := audit.AddPayloadIndex(ctx, db); err != nil {
		t.Fatalf("AddPayloadIndex (first): %v", err)
	}
	if err := audit.AddPayloadIndex(ctx, db); err != nil {
		t.Fatalf("AddPayloadIndex (second): %v", err)
	}
}

// TestWriter_FillsPayloadColumns verifies Write populates the
// structured payload columns when the payload is parseable JSON.
func TestWriter_FillsPayloadColumns(t *testing.T) {
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	ctx := context.Background()
	payload := []byte(`{"event":"agent_memory.save","id":99,"operator":"nico","kind":"observation"}`)
	id, err := w.Write(ctx, "test-actor", "", payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		gotEvent string
		gotID    *int64
		gotKind  string
	)
	if err := db.QueryRowContext(ctx,
		"SELECT payload_event, payload_id, payload_kind FROM audit_log WHERE audit_id = ?",
		id,
	).Scan(&gotEvent, &gotID, &gotKind); err != nil {
		t.Fatalf("query payload columns: %v", err)
	}
	if gotEvent != "agent_memory.save" {
		t.Errorf("payload_event = %q, want agent_memory.save", gotEvent)
	}
	if gotID == nil || *gotID != 99 {
		t.Errorf("payload_id = %v, want 99", gotID)
	}
	if gotKind != "observation" {
		t.Errorf("payload_kind = %q, want observation", gotKind)
	}
}

// TestWriter_NoPayloadFieldsForNonJSON verifies non-JSON payloads
// leave the structured columns NULL.
func TestWriter_NoPayloadFieldsForNonJSON(t *testing.T) {
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	ctx := context.Background()
	payload := []byte("legacy non-JSON payload")
	id, err := w.Write(ctx, "test-actor", "", payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		gotEvent *string
		gotID    *int64
		gotKind  *string
	)
	if err := db.QueryRowContext(ctx,
		"SELECT payload_event, payload_id, payload_kind FROM audit_log WHERE audit_id = ?",
		id,
	).Scan(&gotEvent, &gotID, &gotKind); err != nil {
		t.Fatalf("query payload columns: %v", err)
	}
	if gotEvent != nil {
		t.Errorf("payload_event should be NULL for non-JSON, got %q", *gotEvent)
	}
	if gotID != nil {
		t.Errorf("payload_id should be NULL for non-JSON, got %d", *gotID)
	}
	if gotKind != nil {
		t.Errorf("payload_kind should be NULL for non-JSON, got %q", *gotKind)
	}
}

// TestWriter_PayloadIDZeroStoredAsNULL verifies a JSON payload with id=0
// (the "absent" sentinel) is stored as SQL NULL in payload_id.
func TestWriter_PayloadIDZeroStoredAsNULL(t *testing.T) {
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	ctx := context.Background()
	payload := []byte(`{"event":"session.start"}`) // no id field → 0 → NULL
	id, err := w.Write(ctx, "test-actor", "", payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	var gotID *int64
	if err := db.QueryRowContext(ctx,
		"SELECT payload_id FROM audit_log WHERE audit_id = ?",
		id,
	).Scan(&gotID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotID != nil {
		t.Errorf("payload_id should be NULL for absent id, got %d", *gotID)
	}
}

// TestWriter_QueryByEvent_UsingPayloadEventIndex verifies the new
// idx_audit_payload_event index is queryable and returns the right rows.
func TestWriter_QueryByEvent_UsingPayloadEventIndex(t *testing.T) {
	db, _ := sqlOpenMemory()
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	ctx := context.Background()

	// Write 3 saves + 2 sessions.
	for i := 0; i < 3; i++ {
		payload := []byte(`{"event":"agent_memory.save","id":` + string(rune('0'+i)) + `,"kind":"decision"}`)
		if _, err := w.Write(ctx, "actor", "", payload); err != nil {
			t.Fatalf("Write save %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		payload := []byte(`{"event":"session.start"}`)
		if _, err := w.Write(ctx, "actor", "", payload); err != nil {
			t.Fatalf("Write session %d: %v", i, err)
		}
	}

	// Query: only agent_memory.save, only kind=decision.
	rows, err := db.QueryContext(ctx,
		"SELECT audit_id FROM audit_log WHERE payload_event = ? AND payload_kind = ?",
		"agent_memory.save", "decision",
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 rows with payload_event=agent_memory.save + payload_kind=decision, got %d", count)
	}
}
