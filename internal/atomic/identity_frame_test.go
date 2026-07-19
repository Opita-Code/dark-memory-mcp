package atomic

import (
	"strings"
	"testing"
	"time"
)

// TestIdentityFrame_HappyPath verifies that a fully-populated frame
// passes Validate, hashes deterministically, and renders to canonical JSON.
func TestIdentityFrame_HappyPath(t *testing.T) {
	f, err := NewIdentityFrame(
		"orchestrator_publish_vibe", // actor
		"dark-agent",                 // operator
		"sess-test-001",              // session_id
		"dark-agents/dark-memory-mcp-cerebro", // constitution_id
		"1.0.0",                      // constitution_ver
		true,                         // canary_active
	)
	if err != nil {
		t.Fatalf("NewIdentityFrame: unexpected error: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error on healthy frame: %v", err)
	}

	// Hash determinism: same content (different pointer) => same hash.
	f2, _ := NewIdentityFrame(
		"orchestrator_publish_vibe",
		"dark-agent",
		"sess-test-001",
		"dark-agents/dark-memory-mcp-cerebro",
		"1.0.0",
		true,
	)
	// Different ComposedAt may differ slightly; advance f2's clock back
	// to f's wall-clock-equivalent moment to ensure same hash.
	f2.ComposedAtValue = f.ComposedAtValue

	h1, err := f.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	h2, err := f2.Hash()
	if err != nil {
		t.Fatalf("Hash (f2): %v", err)
	}
	if h1 != h2 {
		t.Errorf("Hash determinism failed: h1=%x h2=%x", h1, h2)
	}

	// Render to canonical JSON
	b, err := f.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(b) == 0 {
		t.Errorf("Render returned empty bytes")
	}
	// Confirm canonical JSON is sorted (no whitespace, deterministic)
	if strings.Contains(string(b), "  ") {
		t.Errorf("Render produced whitespace; canonical JSON should be compact: %s", string(b))
	}
}

// TestIdentityFrame_EmptyActorRejected ensures the constructor refuses
// when Actor is empty.
func TestIdentityFrame_EmptyActorRejected(t *testing.T) {
	_, err := NewIdentityFrame("", "op", "sess", "dark-agents/x", "1.0.0", true)
	if err == nil {
		t.Fatalf("expected error for empty actor, got nil")
	}
	if err != ErrEmptyActor {
		t.Errorf("expected ErrEmptyActor, got %v", err)
	}
}

// TestIdentityFrame_EmptyOperatorRejected ensures the constructor refuses
// when Operator is empty.
func TestIdentityFrame_EmptyOperatorRejected(t *testing.T) {
	_, err := NewIdentityFrame("actor", "", "sess", "dark-agents/x", "1.0.0", true)
	if err == nil {
		t.Fatalf("expected error for empty operator, got nil")
	}
	if err != ErrEmptyOperator {
		t.Errorf("expected ErrEmptyOperator, got %v", err)
	}
}

// TestIdentityFrame_EmptySessionIDRejected tests INV-2 implication: a
// session frame must carry a session_id for cross-session scoping.
func TestIdentityFrame_EmptySessionIDRejected(t *testing.T) {
	_, err := NewIdentityFrame("actor", "op", "", "dark-agents/x", "1.0.0", true)
	if err == nil {
		t.Fatalf("expected error for empty session_id, got nil")
	}
	if err != ErrEmptySessionID {
		t.Errorf("expected ErrEmptySessionID, got %v", err)
	}
}

// TestIdentityFrame_EmptyConstitutionIDRejected tests INV-4: a frame
// must declare which constitution it is bound to.
func TestIdentityFrame_EmptyConstitutionIDRejected(t *testing.T) {
	_, err := NewIdentityFrame("actor", "op", "sess", "", "1.0.0", true)
	if err == nil {
		t.Fatalf("expected error for empty constitution_id, got nil")
	}
	if err != ErrEmptyConstitutionID {
		t.Errorf("expected ErrEmptyConstitutionID, got %v", err)
	}
}

// TestIdentityFrame_EmptyConstitutionVerRejected.
func TestIdentityFrame_EmptyConstitutionVerRejected(t *testing.T) {
	_, err := NewIdentityFrame("actor", "op", "sess", "dark-agents/x", "", true)
	if err == nil {
		t.Fatalf("expected error for empty constitution_ver, got nil")
	}
	if err != ErrEmptyConstitutionVer {
		t.Errorf("expected ErrEmptyConstitutionVer, got %v", err)
	}
}

// TestIdentityFrame_StaleRejectedBackdated verifies that an IdentityFrame
// with a back-dated ComposedAt fails Validate with ErrStaleFrame.
//
// We construct a valid frame, then manually backdate ComposedAtValue
// past MaxIdentityFrameAge, then expect rejection.
func TestIdentityFrame_StaleRejectedBackdated(t *testing.T) {
	f, err := NewIdentityFrame("actor", "op", "sess", "dark-agents/x", "1.0.0", true)
	if err != nil {
		t.Fatalf("NewIdentityFrame: %v", err)
	}
	f.ComposedAtValue = time.Now().Add(-2 * MaxIdentityFrameAge)
	if err := f.Validate(); err == nil {
		t.Fatalf("expected stale-frame error, got nil")
	} else if !strings.Contains(err.Error(), "stale") {
		t.Errorf("expected error to mention staleness, got: %v", err)
	}
}

// TestIdentityFrame_VerifyAgainstWriteAudit_SessionMismatch tests the
// cross-session binding check (INV-7).
func TestIdentityFrame_VerifyAgainstWriteAudit_SessionMismatch(t *testing.T) {
	f, _ := NewIdentityFrame("actor", "op", "sess-A", "dark-agents/x", "1.0.0", true)
	ref := WriteAuditRef{
		SessionID:       "sess-B", // different
		ConstitutionID:  "dark-agents/x",
		ConstitutionVer: "1.0.0",
	}
	err := f.VerifyAgainstWriteAudit(ref)
	if err == nil {
		t.Fatalf("expected session-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("expected error to mention session, got: %v", err)
	}
}

// TestIdentityFrame_VerifyAgainstWriteAudit_ConstitutionMismatch tests
// the cross-constitution binding check (INV-4).
func TestIdentityFrame_VerifyAgainstWriteAudit_ConstitutionMismatch(t *testing.T) {
	f, _ := NewIdentityFrame("actor", "op", "sess-A", "dark-agents/x", "1.0.0", true)
	ref := WriteAuditRef{
		SessionID:       "sess-A",
		ConstitutionID:  "dark-agents/y", // different
		ConstitutionVer: "1.0.0",
	}
	err := f.VerifyAgainstWriteAudit(ref)
	if err == nil {
		t.Fatalf("expected constitution-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "constitution") {
		t.Errorf("expected error to mention constitution, got: %v", err)
	}
}

// TestIdentityFrame_VerifyAgainstWriteAudit_OK tests the happy-path
// cross-check.
func TestIdentityFrame_VerifyAgainstWriteAudit_OK(t *testing.T) {
	f, _ := NewIdentityFrame("actor", "op", "sess-A", "dark-agents/x", "1.0.0", true)
	ref := WriteAuditRef{
		SessionID:       "sess-A",
		ConstitutionID:  "dark-agents/x",
		ConstitutionVer: "1.0.0",
	}
	if err := f.VerifyAgainstWriteAudit(ref); err != nil {
		t.Errorf("VerifyAgainstWriteAudit on matching ref: %v", err)
	}
}

// TestIdentityFrame_Equal confirms structural equality (ignoring ComposedAt).
func TestIdentityFrame_Equal(t *testing.T) {
	f1, _ := NewIdentityFrame("a", "o", "s", "c", "v", true)
	f2, _ := NewIdentityFrame("a", "o", "s", "c", "v", true)
	// Different ComposedAt (timestamps differ); still equal structurally.
	if !f1.Equal(f2) {
		t.Errorf("Equal should be true for matching fields regardless of ComposedAt")
	}
	// Change a field; no longer equal.
	f2.CanaryActive = false
	if f1.Equal(f2) {
		t.Errorf("Equal should be false when CanaryActive differs")
	}
}

// TestParseFrameKind covers the enum parser.
func TestParseFrameKind(t *testing.T) {
	for _, k := range AllFrameKinds() {
		got, err := ParseFrameKind(string(k))
		if err != nil {
			t.Errorf("ParseFrameKind(%q): unexpected err %v", string(k), err)
		}
		if got != k {
			t.Errorf("ParseFrameKind(%q) roundtrip = %q, want %q", string(k), got, k)
		}
	}
	if _, err := ParseFrameKind("bogus"); err == nil {
		t.Errorf("expected error for unknown kind, got nil")
	}
	if _, err := ParseFrameKind(""); err == nil {
		t.Errorf("expected error for empty kind, got nil")
	}
}

// TestParseScopeLevel covers the scope-level parser.
func TestParseScopeLevel(t *testing.T) {
	for _, l := range AllScopeLevels() {
		got, err := ParseScopeLevel(string(l))
		if err != nil {
			t.Errorf("ParseScopeLevel(%q): unexpected err %v", string(l), err)
		}
		if got != l {
			t.Errorf("ParseScopeLevel(%q) roundtrip = %q, want %q", string(l), got, l)
		}
	}
	if _, err := ParseScopeLevel("nope"); err == nil {
		t.Errorf("expected error for unknown level, got nil")
	}
}
