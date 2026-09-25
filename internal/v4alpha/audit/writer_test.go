package audit_test

// L2 example tests for audit.Writer.
//
// These tests cover specific cases (boundary values, named scenarios)
// per dark-testing skill §3.4.1 (execution-based verification — the
// strongest oracle). They run alongside the L1 property tests in
// writer_property_test.go.
//
// A14 defense: every assertion uses NON-default values. First audit_id
// is asserted as exactly 1 (not "is non-zero"), which is the strictest
// claim the property allows.

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// newTestWriter is a *testing.T-specific facade for newTestWriterAny.
func newTestWriter(t *testing.T) *audit.Writer {
	t.Helper()
	return newTestWriterAny(t)
}

// TestExample_Write_FirstAuditIDIsOne — A14 defense: the assertion
// "audit_id == 1" would FAIL if the implementation returned 0 (Go
// default int) or any other value. This catches the
// default-value-coincidence anti-pattern.
func TestExample_Write_FirstAuditIDIsOne(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	id, err := w.Write(ctx, "operator-nico", "", []byte("payload-1"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id != 1 {
		t.Fatalf("first audit_id: expected exactly 1 (A14 defense), got %d", id)
	}
}

// TestExample_Write_MonotonicAcrossActors — invariant: different
// actors do not break the monotonic counter. The Writer is
// process-singleton per session; actor differentiation is metadata,
// not sequencing.
func TestExample_Write_MonotonicAcrossActors(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	id1, err := w.Write(ctx, "operator-nico", "sess-a", []byte("save-1"))
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	id2, err := w.Write(ctx, "operator-maria", "sess-b", []byte("save-2"))
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	id3, err := w.Write(ctx, "operator-nico", "sess-a", []byte("save-3"))
	if err != nil {
		t.Fatalf("Write 3: %v", err)
	}

	if !(id1 < id2 && id2 < id3) {
		t.Fatalf("monotonic across actors: got %d, %d, %d", id1, id2, id3)
	}
}

// TestExample_Write_EmptyActorRejected — INV-1 requires that every
// audit row is tagged with an operator id; empty actor is a hard
// invariant violation.
func TestExample_Write_EmptyActorRejected(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	_, err := w.Write(ctx, "", "", []byte("payload"))
	if err == nil {
		t.Fatal("Write with empty actor: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("error message should mention non-empty, got: %v", err)
	}
}

// TestExample_Write_PayloadMayBeEmpty — payload is metadata, may be
// empty. This documents that the empty-payload path is intentional,
// not a bug.
func TestExample_Write_PayloadMayBeEmpty(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	id, err := w.Write(ctx, "operator-nico", "sess-x", []byte{})
	if err != nil {
		t.Fatalf("Write empty payload: %v", err)
	}
	if id != 1 {
		t.Fatalf("audit_id: expected 1, got %d", id)
	}
}

// TestExample_Write_SessionIDOptional — sessionID may be empty (for
// non-session writes). Empty sessionID is stored as NULL.
func TestExample_Write_SessionIDOptional(t *testing.T) {
	w := newTestWriter(t)
	ctx := context.Background()

	id, err := w.Write(ctx, "operator-nico", "", []byte("standalone"))
	if err != nil {
		t.Fatalf("Write without sessionID: %v", err)
	}
	if id != 1 {
		t.Fatalf("audit_id: expected 1, got %d", id)
	}
}
