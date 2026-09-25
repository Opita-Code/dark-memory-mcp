package audit_test

// L1 property tests for audit.Writer.
//
// These tests verify universal claims using rapid (pgregory.net/rapid).
// Per dark-testing skill §3.2 (Property Pattern Catalog), the
// relevant patterns here are:
//   - Invariant: monotonicity (audit_id strictly increases)
//   - No-crash: Write must succeed for any non-empty actor + payload
//
// Per dark-testing skill §5.3 (AI-generated code), property tests are
// the highest-value discipline when AI is generating the
// implementation: "AI can churn out code that passes a handful of
// examples all day long. The problem is the empty space between the
// examples."

import (
	"context"
	"testing"

	"pgregory.net/rapid"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// TestProperty_Write_AuditIDMonotonic — universal claim:
// for any N Writes (N in [1, 1000]), audit_ids are strictly monotonic.
//
// Generator discipline (§3.3): non-empty strings, non-zero-length
// payloads (when present). A14 defense: would FAIL if implementation
// returned a constant or the Go zero value (0).
func TestProperty_Write_AuditIDMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		w := newTestWriterT(t)

		n := rapid.IntRange(1, 1000).Draw(t, "n")
		prevID := int64(0)
		for i := 0; i < n; i++ {
			actor := rapid.StringMatching(`[a-z][a-z0-9_-]{2,31}`).Draw(t, "actor")
			payloadLen := rapid.IntRange(0, 64).Draw(t, "payloadLen")
			payload := make([]byte, payloadLen)
			for j := range payload {
				payload[j] = byte(rapid.IntRange(1, 255).Draw(t, "byte")) // non-zero bytes
			}
			sessionID := rapid.StringMatching(`sess-[a-z0-9]{8,32}`).Draw(t, "sessionID")

			id, err := w.Write(ctx, actor, sessionID, payload)
			if err != nil {
				t.Fatalf("Write %d (actor=%s): %v", i, actor, err)
			}
			if id <= prevID {
				t.Fatalf("monotonicity violated at iter %d: prev=%d, current=%d",
					i, prevID, id)
			}
			prevID = id
		}
	})
}

// TestProperty_Write_NoCrash — universal claim:
// for any non-empty actor + any sessionID (incl. empty) + any payload
// (length 0..128), Write returns no error. The no-crash property is
// the minimum of every public API (youngju.dev PBT practical 2026).
func TestProperty_Write_NoCrash(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		w := newTestWriterT(t)

		actor := rapid.StringMatching(`[a-z][a-z0-9_-]{2,31}`).Draw(t, "actor")
		payloadLen := rapid.IntRange(0, 128).Draw(t, "payloadLen")
		payload := make([]byte, payloadLen)
		for j := range payload {
			payload[j] = byte(rapid.IntRange(0, 255).Draw(t, "byte"))
		}
		// 50% chance of empty sessionID (standalone write)
		sessionID := ""
		if rapid.Bool().Draw(t, "hasSession") {
			sessionID = rapid.StringMatching(`sess-[a-z0-9]{8,32}`).Draw(t, "sessionID")
		}

		_, err := w.Write(ctx, actor, sessionID, payload)
		if err != nil {
			t.Fatalf("Write unexpectedly crashed for valid input: %v", err)
		}
	})
}

// TestProperty_Write_EmptyActorAlwaysRejected — universal claim:
// for any empty string, Write returns an error. INV-1 enforcement
// must hold for the entire input space, not just one example.
func TestProperty_Write_EmptyActorAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		w := newTestWriterT(t)

		// Always empty (no Draw) — the property is "for empty actor, error".
		_, err := w.Write(ctx, "", "", []byte("payload"))
		if err == nil {
			t.Fatal("empty actor should always be rejected (INV-1)")
		}
	})
}

// newTestWriterT is a *rapid.T-specific facade for newTestWriterAny.
func newTestWriterT(t *rapid.T) *audit.Writer {
	return newTestWriterAny(t)
}
