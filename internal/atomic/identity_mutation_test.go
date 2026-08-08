package atomic

import (
	"bytes"
	"testing"
	"time"
)

// Wave 3 mutation-killing coverage for IdentityFrame. The original
// suite tested the constructor paths but never called Validate() on
// hand-built frames per field, never exercised Equal() field-by-field,
// and never exercised EqualBytes() nil/kind-mismatch paths. Every
// surviving mutant from the 2026-08-07 run is a REAL branch, not an
// equivalent — killing them required asserting exact sentinel errors
// and exact equality semantics.

// --- Validate per-field ---

func TestIdentityFrame_Validate_Nil(t *testing.T) {
	var f *IdentityFrame
	if err := f.Validate(); err != ErrZeroComposedAt {
		t.Errorf("want ErrZeroComposedAt, got %v", err)
	}
}

func TestIdentityFrame_Validate_EmptyActor(t *testing.T) {
	f := &IdentityFrame{Operator: "op", SessionID: "s", ConstitutionID: "c", ConstitutionVer: "v", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEmptyActor {
		t.Errorf("want ErrEmptyActor, got %v", err)
	}
}

func TestIdentityFrame_Validate_EmptyOperator(t *testing.T) {
	f := &IdentityFrame{Actor: "a", SessionID: "s", ConstitutionID: "c", ConstitutionVer: "v", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEmptyOperator {
		t.Errorf("want ErrEmptyOperator, got %v", err)
	}
}

func TestIdentityFrame_Validate_EmptySessionID(t *testing.T) {
	f := &IdentityFrame{Actor: "a", Operator: "op", ConstitutionID: "c", ConstitutionVer: "v", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEmptySessionID {
		t.Errorf("want ErrEmptySessionID, got %v", err)
	}
}

func TestIdentityFrame_Validate_EmptyConstitutionID(t *testing.T) {
	f := &IdentityFrame{Actor: "a", Operator: "op", SessionID: "s", ConstitutionVer: "v", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEmptyConstitutionID {
		t.Errorf("want ErrEmptyConstitutionID, got %v", err)
	}
}

func TestIdentityFrame_Validate_EmptyConstitutionVer(t *testing.T) {
	f := &IdentityFrame{Actor: "a", Operator: "op", SessionID: "s", ConstitutionID: "c", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEmptyConstitutionVer {
		t.Errorf("want ErrEmptyConstitutionVer, got %v", err)
	}
}

func TestIdentityFrame_Validate_ZeroComposed(t *testing.T) {
	f := &IdentityFrame{Actor: "a", Operator: "op", SessionID: "s", ConstitutionID: "c", ConstitutionVer: "v"}
	if err := f.Validate(); err != ErrZeroComposedAt {
		t.Errorf("want ErrZeroComposedAt, got %v", err)
	}
}

func TestIdentityFrame_Validate_OK(t *testing.T) {
	f := &IdentityFrame{Actor: "a", Operator: "op", SessionID: "s", ConstitutionID: "c", ConstitutionVer: "v", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// --- Equal semantics ---

func identityFrame() *IdentityFrame {
	f, _ := NewIdentityFrame("a", "op", "s", "c", "v", true)
	return f
}

func TestIdentityFrame_Equal_Identical(t *testing.T) {
	a, b := identityFrame(), identityFrame()
	if !a.Equal(b) {
		t.Error("identical frames must be equal")
	}
}

func TestIdentityFrame_Equal_AllFieldDifferences(t *testing.T) {
	base := identityFrame()
	cases := []struct {
		name   string
		mutate func(f *IdentityFrame)
	}{
		{"actor", func(f *IdentityFrame) { f.Actor = "other" }},
		{"operator", func(f *IdentityFrame) { f.Operator = "other" }},
		{"session", func(f *IdentityFrame) { f.SessionID = "other" }},
		{"constitution_id", func(f *IdentityFrame) { f.ConstitutionID = "other" }},
		{"constitution_ver", func(f *IdentityFrame) { f.ConstitutionVer = "other" }},
		{"canary", func(f *IdentityFrame) { f.CanaryActive = !f.CanaryActive }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := identityFrame()
			tc.mutate(other)
			if base.Equal(other) {
				t.Errorf("Equal must be false when %s differs", tc.name)
			}
		})
	}
}

func TestIdentityFrame_Equal_NilCombos(t *testing.T) {
	f := identityFrame()
	var nilFrame *IdentityFrame
	if f.Equal(nilFrame) {
		t.Error("f.Equal(nil) must be false")
	}
	if !nilFrame.Equal(nilFrame) {
		t.Error("nil.Equal(nil) must be true (both nil)")
	}
	if nilFrame.Equal(f) {
		t.Error("nil.Equal(f) must be false")
	}
}

// --- EqualBytes ---

func TestEqualBytes_NilCombos(t *testing.T) {
	var a, b Frame
	if ok, err := EqualBytes(a, b); err != nil || !ok {
		t.Errorf("EqualBytes(nil,nil) = (%v,%v), want (true,nil)", ok, err)
	}
	f := identityFrame()
	if ok, err := EqualBytes(a, f); err != nil || ok {
		t.Errorf("EqualBytes(nil,f) = (%v,%v), want (false,nil)", ok, err)
	}
}

func TestEqualBytes_KindMismatch(t *testing.T) {
	id := identityFrame()
	cap, _ := NewCapabilitiesFrame("p", "s", nil, nil, time.Time{}, "src")
	if ok, err := EqualBytes(id, cap); err != nil || ok {
		t.Errorf("EqualBytes(identity, capabilities) = (%v,%v), want (false,nil)", ok, err)
	}
}

func TestEqualBytes_SameContent(t *testing.T) {
	a, b := identityFrame(), identityFrame()
	b.ComposedAtValue = a.ComposedAtValue
	ok, err := EqualBytes(a, b)
	if err != nil || !ok {
		t.Errorf("EqualBytes identical = (%v,%v), want (true,nil)", ok, err)
	}
}

func TestEqualBytes_DifferentContent(t *testing.T) {
	a := identityFrame()
	b := identityFrame()
	b.Actor = "other"
	ok, err := EqualBytes(a, b)
	if err != nil || ok {
		t.Errorf("EqualBytes different = (%v,%v), want (false,nil)", ok, err)
	}
}

// --- VerifyAgainstWriteAudit ---

func TestMT_IdentityFrame_VerifyAgainstWriteAudit_OK(t *testing.T) {
	f := identityFrame()
	if err := f.VerifyAgainstWriteAudit(WriteAuditRef{SessionID: "s", ConstitutionID: "c", ConstitutionVer: "v"}); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestMT_IdentityFrame_VerifyAgainstWriteAudit_SessionMismatch(t *testing.T) {
	f := identityFrame()
	err := f.VerifyAgainstWriteAudit(WriteAuditRef{SessionID: "other", ConstitutionID: "c", ConstitutionVer: "v"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("session")) {
		t.Errorf("want cross-session binding error, got %v", err)
	}
}

func TestIdentityFrame_VerifyAgainstWriteAudit_ConstitutionIDMismatch(t *testing.T) {
	f := identityFrame()
	err := f.VerifyAgainstWriteAudit(WriteAuditRef{SessionID: "s", ConstitutionID: "other", ConstitutionVer: "v"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("constitution")) {
		t.Errorf("want constitution mismatch error, got %v", err)
	}
}

func TestIdentityFrame_VerifyAgainstWriteAudit_ConstitutionVerMismatch(t *testing.T) {
	f := identityFrame()
	// Kills mutation identity_frame.go.23: `|| false` replacing the
	// ConstitutionVer comparison. Session and ConstitutionID match but
	// Ver differs — original errors, mutant falls through to nil.
	err := f.VerifyAgainstWriteAudit(WriteAuditRef{SessionID: "s", ConstitutionID: "c", ConstitutionVer: "other"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("constitution")) {
		t.Errorf("want constitution mismatch error, got %v", err)
	}
}
