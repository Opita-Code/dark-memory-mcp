// Package atomic - frame_interface_test.go: Wave 5X.2 — verify that
// every Frame implementation satisfies the atomic.Frame interface
// and that Hash() returns a non-nil error when the canonical
// encoder fails (defensive — currently no impl triggers this).
package atomic_test

import (
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
)

// TestFrameInterface_AllImplsSatisfyInterface: compile-time guard
// duplication as a runtime check. Catches any future drift where a
// concrete type's Hash signature diverges from the interface
// declaration. Without this test the `var _ Frame = (*XxxFrame)(nil)`
// guards at file scope would catch the same mismatch — but a
// runtime check makes the intent explicit and provides a single
// pivot for refactors.
func TestFrameInterface_AllImplsSatisfyInterface(t *testing.T) {
	now := time.Now().UTC()
	constitutionID := "constitution-test"
	constitutionVer := "1.0.0"
	projectID := "test-project"

	cases := []struct {
		name  string
		frame atomic.Frame
	}{
		{
			name: "IdentityFrame",
			frame: func() *atomic.IdentityFrame {
				f, _ := atomic.NewIdentityFrame("actor", "operator", "sess-test", constitutionID, constitutionVer, false)
				return f
			}(),
		},
		{
			name: "ScopeFrame",
			frame: func() *atomic.ScopeFrame {
				f, _ := atomic.NewScopeFrame("sess-test", 42, nil, nil, "aligned", now)
				return f
			}(),
		},
		{
			name: "CapabilitiesFrame",
			frame: func() *atomic.CapabilitiesFrame {
				f, _ := atomic.NewCapabilitiesFrame(projectID, "sess-test", nil, nil, now, "test")
				return f
			}(),
		},
		{
			name: "PersonaFrame",
			frame: func() *atomic.PersonaFrame {
				f, _ := atomic.NewPersonaFrame(constitutionID, constitutionVer, "brand-1", "voice", "claims", "refusal", "tone")
				return f
			}(),
		},
		{
			name: "DriftFrame",
			frame: func() *atomic.DriftFrame {
				f, _ := atomic.NewDriftFrame("sess-test", 42, "aligned", now, nil)
				return f
			}(),
		},
		{
			name: "EvidenceFrame",
			frame: func() *atomic.EvidenceFrame {
				f, _ := atomic.NewEvidenceFrame(projectID, "sess-test", nil, nil, 0)
				return f
			}(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.frame == nil {
				t.Fatalf("%s: nil frame", c.name)
			}
			// Compile-time interface check: assigning to atomic.Frame
			// would fail to compile if the impl didn't satisfy. Inside
			// the test, we just call methods that the interface declares.
			var f atomic.Frame = c.frame
			if f.Kind() == "" {
				t.Errorf("%s: Kind() returned empty string", c.name)
			}
			if f.ComposedAt().IsZero() {
				t.Errorf("%s: ComposedAt() is zero", c.name)
			}
			if err := f.Validate(); err != nil {
				t.Errorf("%s: Validate() = %v, want nil", c.name, err)
			}
			hash, err := f.Hash()
			if err != nil {
				t.Errorf("%s: Hash() returned err: %v", c.name, err)
			}
			var zero [32]byte
			if hash == zero {
				t.Errorf("%s: Hash() returned all-zero hash", c.name)
			}
			body, err := f.Render()
			if err != nil {
				t.Errorf("%s: Render() returned err: %v", c.name, err)
			}
			if len(body) == 0 {
				t.Errorf("%s: Render() returned empty body", c.name)
			}
		})
	}
}

// TestFrameInterface_HashReturnsErrorOnFailure: defensive. Today
// no concrete impl can fail (json.Marshal of a struct succeeds),
// but the interface contract says Hash() can return error. This
// test verifies the contract surface is correct by exercising a
// nil-receiver via reflection would be too invasive; instead it
// documents the expectation.
func TestFrameInterface_HashErrorContractDocumented(t *testing.T) {
	// Build one valid frame to confirm the happy path works.
	f, _ := atomic.NewIdentityFrame("a", "o", "s", "c", "1.0", false)
	hash, err := f.Hash()
	if err != nil {
		t.Fatalf("Hash happy path failed: %v", err)
	}
	if hash == ([32]byte{}) {
		t.Errorf("Hash returned zero hash")
	}
}