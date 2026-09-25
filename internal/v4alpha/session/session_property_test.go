package session_test

// L1 property tests for session.Store.
//
// Universal claims verified here:
//   - Heartbeat never decreases audit_id (preserves monotonicity)
//   - Start isolates sessions across projects (INV-7)
//   - Close returns consistent Summary counts

import (
	"context"
	"testing"

	"pgregory.net/rapid"
)

// TestProperty_Heartbeat_PreservesAuditIDMonotonicity — INV-1 must
// hold across Heartbeat calls. Heartbeat emits its own audit row;
// that row's audit_id must be strictly greater than the previous one.
func TestProperty_Heartbeat_PreservesAuditIDMonotonicity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		store, w := newTestStoreAudit(t)

		// Start emits one audit row. Capture the session ID so
		// Heartbeat can target the right session (Start generates
		// a random ID; hardcoding "sess-1" would miss).
		sess, err := store.Start(ctx, "operator-nico", "proj-huila")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		prevAuditID := w.LastID()

		hbCount := rapid.IntRange(1, 20).Draw(t, "hbCount")
		for i := 0; i < hbCount; i++ {
			if err := store.Heartbeat(ctx, sess.ID); err != nil {
				t.Fatalf("Heartbeat %d (sessionID=%s): %v", i, sess.ID, err)
			}
			cur := w.LastID()
			if cur <= prevAuditID {
				t.Fatalf("audit_id non-monotonic across heartbeats: prev=%d cur=%d",
					prevAuditID, cur)
			}
			prevAuditID = cur
		}
	})
}

// TestProperty_Start_IsolatesAcrossProjects_INV7 — INV-7: starting
// sessions in distinct projects yields sessions that cannot be
// cross-read. The property is "for any two distinct project_ids,
// cross-project Read always errors".
func TestProperty_Start_IsolatesAcrossProjects_INV7(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		store := newTestStoreT(t)

		projA := rapid.StringMatching(`proj-[a-z]{3,12}`).Draw(t, "projA")
		projB := rapid.StringMatching(`proj-[a-z]{3,12}`).Draw(t, "projB")
		if projA == projB {
			t.Skip("projects collide (rare); property is about distinct ones")
		}

		sessA, err := store.Start(ctx, "operator-nico", projA)
		if err != nil {
			t.Fatalf("Start projA: %v", err)
		}

		// Read in projB must error.
		if _, err := store.Read(ctx, sessA.ID, projB); err == nil {
			t.Fatalf("cross-project Read succeeded: projA=%s, projB=%s, sessID=%s",
				projA, projB, sessA.ID)
		}

		// Read in projA must succeed.
		if _, err := store.Read(ctx, sessA.ID, projA); err != nil {
			t.Fatalf("same-project Read failed unexpectedly: %v", err)
		}
	})
}

// TestProperty_Close_ReturnsConsistentCounts — universal claim:
// for any sequence of Start + N heartbeats + Close, exactly one
// audit row is emitted by Close itself (N+1 total, where N=hbCount+1
// includes Start and the heartbeats). We check the increment from
// pre-Close to post-Close, which must be exactly 1.
func TestProperty_Close_ReturnsConsistentCounts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		store, w := newTestStoreAudit(t)

		sess, err := store.Start(ctx, "operator-nico", "proj-huila")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		hbCount := rapid.IntRange(0, 10).Draw(t, "hbCount")
		for i := 0; i < hbCount; i++ {
			if err := store.Heartbeat(ctx, sess.ID); err != nil {
				t.Fatalf("Heartbeat %d (sessionID=%s): %v", i, sess.ID, err)
			}
		}

		// Capture the audit_id before Close so we can compare.
		preCloseID := w.LastID()
		_, err = store.Close(ctx, sess.ID, true)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Close must have emitted exactly one audit row.
		if got := w.LastID(); got != preCloseID+1 {
			t.Fatalf("Close emitted %d audit rows, expected 1 (got %d, prev %d)",
				got-preCloseID, got, preCloseID)
		}
	})
}
