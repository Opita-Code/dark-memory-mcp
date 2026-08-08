package atomic

import (
	"strings"
	"testing"
	"time"
)

// This file targets mutation survivors found in the 2026-08-07 wave 3
// run. Root cause: tests covered the CONSTRUCTOR error paths but never
// called Validate() on hand-built frames for each branch, so every
// Validate() mutation escaped. Each test below builds the frame
// directly (bypassing the constructor) and asserts Validate() behavior
// branch-by-branch.
//
// Regression note: TestScopeFrame_Validate_NeedsHuman is a real-bug
// regression — Validate() had `&& true` instead of `!= "needs_human"`
// (commit 7e88406, 2026-08-06), rejecting the canonical needs_human
// verdict.

// --- CapabilitiesFrame.Validate branches ---

func TestCapabilitiesFrame_Validate_Nil(t *testing.T) {
	var f *CapabilitiesFrame
	if err := f.Validate(); err != ErrCapabilitiesZeroComposed {
		t.Errorf("want ErrCapabilitiesZeroComposed, got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_EmptyProjectID(t *testing.T) {
	f := &CapabilitiesFrame{SessionID: "s", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrCapabilitiesEmptyProjectID {
		t.Errorf("want ErrCapabilitiesEmptyProjectID, got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_EmptySessionID(t *testing.T) {
	f := &CapabilitiesFrame{ProjectID: "p", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrCapabilitiesEmptySessionID {
		t.Errorf("want ErrCapabilitiesEmptySessionID, got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_ZeroComposed(t *testing.T) {
	f := &CapabilitiesFrame{ProjectID: "p", SessionID: "s"}
	if err := f.Validate(); err != ErrCapabilitiesZeroComposed {
		t.Errorf("want ErrCapabilitiesZeroComposed, got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_Stale(t *testing.T) {
	f := &CapabilitiesFrame{
		ProjectID:        "p",
		SessionID:        "s",
		ComposedAtValue:  time.Now().Add(-2 * MaxCapabilitiesFrameAge),
	}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("want stale, got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_OK(t *testing.T) {
	f := &CapabilitiesFrame{ProjectID: "p", SessionID: "s", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// --- CapabilitiesFrame accessor branches ---

func TestCapabilitiesFrame_HasGrant_CaseSensitive(t *testing.T) {
	f := &CapabilitiesFrame{GrantedTools: []ToolGrant{{ToolName: "Recall"}}}
	if f.HasGrant("recall") {
		t.Errorf("HasGrant must be case-sensitive exact match")
	}
	if !f.HasGrant("Recall") {
		t.Errorf("HasGrant exact match failed")
	}
}

func TestCapabilitiesFrame_HasProjectAccess_False(t *testing.T) {
	f := &CapabilitiesFrame{GrantedScopes: []ScopeGrant{{ProjectID: "proj-a"}}}
	if f.HasProjectAccess("proj-b") {
		t.Errorf("HasProjectAccess proj-b should be false")
	}
	if !f.HasProjectAccess("proj-a") {
		t.Errorf("HasProjectAccess proj-a should be true")
	}
}

func TestCapabilitiesFrame_IsExpired_Future(t *testing.T) {
	f := &CapabilitiesFrame{GrantedExpiresAt: time.Now().Add(time.Hour)}
	if f.IsExpired(time.Now()) {
		t.Errorf("future expiry should not be expired")
	}
	if !f.IsExpired(time.Now().Add(2 * time.Hour)) {
		t.Errorf("past expiry should be expired")
	}
}

// --- DriftFrame.Validate branches ---

func TestDriftFrame_Validate_Nil(t *testing.T) {
	var f *DriftFrame
	if err := f.Validate(); err != ErrDriftZeroComposed {
		t.Errorf("want ErrDriftZeroComposed, got %v", err)
	}
}

func TestDriftFrame_Validate_EmptySessionID(t *testing.T) {
	f := &DriftFrame{ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftEmptySessionID {
		t.Errorf("want ErrDriftEmptySessionID, got %v", err)
	}
}

func TestDriftFrame_Validate_NegativeSpecID(t *testing.T) {
	f := &DriftFrame{SessionID: "s", SpecID: -1, ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftInvalidSpecID {
		t.Errorf("want ErrDriftInvalidSpecID, got %v", err)
	}
}

func TestDriftFrame_Validate_SpecWithoutVerdict(t *testing.T) {
	f := &DriftFrame{SessionID: "s", SpecID: 42, ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftSpecWithoutVerdict {
		t.Errorf("want ErrDriftSpecWithoutVerdict, got %v", err)
	}
}

func TestDriftFrame_Validate_VerdictUnknown(t *testing.T) {
	f := &DriftFrame{SessionID: "s", LastVerdict: "maybe", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftVerdictUnknown {
		t.Errorf("want ErrDriftVerdictUnknown, got %v", err)
	}
}

func TestDriftFrame_Validate_AlignedWithoutTime(t *testing.T) {
	f := &DriftFrame{SessionID: "s", LastVerdict: "aligned", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftVerdictWithoutTime {
		t.Errorf("want ErrDriftVerdictWithoutTime, got %v", err)
	}
}

func TestDriftFrame_Validate_AlignedWithItems(t *testing.T) {
	f := &DriftFrame{
		SessionID:        "s",
		LastVerdict:      "aligned",
		LastReconciledAt: time.Now(),
		PendingItems:     []string{"x"},
		ComposedAtValue:  time.Now(),
	}
	if err := f.Validate(); err != ErrDriftAlignedWithItems {
		t.Errorf("want ErrDriftAlignedWithItems, got %v", err)
	}
}

func TestDriftFrame_Validate_DriftWithoutItems(t *testing.T) {
	f := &DriftFrame{
		SessionID:        "s",
		LastVerdict:      "drift_detected",
		LastReconciledAt: time.Now(),
		ComposedAtValue:  time.Now(),
	}
	if err := f.Validate(); err != ErrDriftDriftWithoutItems {
		t.Errorf("want ErrDriftDriftWithoutItems, got %v", err)
	}
}

func TestDriftFrame_Validate_ZeroComposed(t *testing.T) {
	f := &DriftFrame{SessionID: "s"}
	if err := f.Validate(); err != ErrDriftZeroComposed {
		t.Errorf("want ErrDriftZeroComposed, got %v", err)
	}
}

func TestDriftFrame_Validate_OK(t *testing.T) {
	f := &DriftFrame{
		SessionID:        "s",
		LastVerdict:      "aligned",
		LastReconciledAt: time.Now(),
		ComposedAtValue:  time.Now(),
	}
	if err := f.Validate(); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

// --- DriftFrame accessor branches ---

func TestDriftFrame_HasPendingItems_DriftNoItems(t *testing.T) {
	f := &DriftFrame{LastVerdict: "drift_detected"}
	if f.HasPendingItems() {
		t.Errorf("drift_detected with empty items must return false")
	}
}

// --- EvidenceFrame.Validate branches ---

func TestEvidenceFrame_Validate_Nil(t *testing.T) {
	var f *EvidenceFrame
	if err := f.Validate(); err != ErrEvidenceZeroComposed {
		t.Errorf("want ErrEvidenceZeroComposed, got %v", err)
	}
}

// --- EvidenceFrame constant thresholds (evidence_frame.go.13/18) ---

func TestEvidenceFrame_Validate_Age9s_NotStale(t *testing.T) {
	f := &EvidenceFrame{ProjectID: "p", SessionID: "s", ComposedAtValue: time.Now().Add(-9 * time.Second)}
	if err := f.Validate(); err != nil {
		t.Errorf("9s age must be fresh under 10s budget; got %v", err)
	}
}

func TestEvidenceFrame_Validate_Age11s_Stale(t *testing.T) {
	f := &EvidenceFrame{ProjectID: "p", SessionID: "s", ComposedAtValue: time.Now().Add(-11 * time.Second)}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("11s age must be stale under 10s budget; got %v", err)
	}
}

func TestEvidenceFrame_Validate_EmptyProjectID(t *testing.T) {
	f := &EvidenceFrame{SessionID: "s", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEvidenceEmptyProjectID {
		t.Errorf("want ErrEvidenceEmptyProjectID, got %v", err)
	}
}

func TestEvidenceFrame_Validate_EmptySessionID(t *testing.T) {
	f := &EvidenceFrame{ProjectID: "p", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEvidenceEmptySessionID {
		t.Errorf("want ErrEvidenceEmptySessionID, got %v", err)
	}
}

func TestEvidenceFrame_Validate_NegativeToken(t *testing.T) {
	f := &EvidenceFrame{ProjectID: "p", SessionID: "s", LastSeenToken: -1, ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrEvidenceNegativeToken {
		t.Errorf("want ErrEvidenceNegativeToken, got %v", err)
	}
}

func TestEvidenceFrame_Validate_ZeroComposed(t *testing.T) {
	f := &EvidenceFrame{ProjectID: "p", SessionID: "s"}
	if err := f.Validate(); err != ErrEvidenceZeroComposed {
		t.Errorf("want ErrEvidenceZeroComposed, got %v", err)
	}
}

// --- ScopeFrame.Validate branches ---

func TestScopeFrame_Validate_Nil(t *testing.T) {
	var f *ScopeFrame
	if err := f.Validate(); err != ErrScopeZeroComposed {
		t.Errorf("want ErrScopeZeroComposed, got %v", err)
	}
}

func TestScopeFrame_Validate_EmptySessionID(t *testing.T) {
	f := &ScopeFrame{ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrScopeEmptySessionID {
		t.Errorf("want ErrScopeEmptySessionID, got %v", err)
	}
}

func TestScopeFrame_Validate_NegativeSpecID(t *testing.T) {
	f := &ScopeFrame{SessionID: "s", OpenSpecID: -1, ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrScopeInvalidSpecID {
		t.Errorf("want ErrScopeInvalidSpecID, got %v", err)
	}
}

func TestScopeFrame_Validate_VerdictWithoutTime(t *testing.T) {
	f := &ScopeFrame{SessionID: "s", LastDriftVerdict: "aligned", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrScopeVerdictWithoutTime {
		t.Errorf("want ErrScopeVerdictWithoutTime, got %v", err)
	}
}

func TestScopeFrame_Validate_VerdictUnknown(t *testing.T) {
	f := &ScopeFrame{SessionID: "s", LastDriftVerdict: "maybe", LastDriftAt: time.Now(), ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrScopeVerdictUnknown {
		t.Errorf("want ErrScopeVerdictUnknown, got %v", err)
	}
}

func TestScopeFrame_Validate_NeedsHuman(t *testing.T) {
	// Regression: commit 7e88406 replaced `!= "needs_human"` with
	// `&& true` in Validate(), rejecting the canonical needs_human
	// verdict. Constructor accepted it; Validate() did not.
	f := &ScopeFrame{SessionID: "s", LastDriftVerdict: "needs_human", LastDriftAt: time.Now(), ComposedAtValue: time.Now()}
	if err := f.Validate(); err != nil {
		t.Errorf("needs_human is a canonical verdict; want nil, got %v", err)
	}
}

func TestScopeFrame_Validate_ZeroComposed(t *testing.T) {
	f := &ScopeFrame{SessionID: "s"}
	if err := f.Validate(); err != ErrScopeZeroComposed {
		t.Errorf("want ErrScopeZeroComposed, got %v", err)
	}
}

// --- Threshold (constant) mutations: MaxXFrameAge +/- 1 minute ---
//
// go-mutesting mutates the staleness constant (15*time.Minute -> 14/16).
// A frame whose age falls BETWEEN the mutated and real threshold only
// validates under one of them, so these tests kill the constant
// mutation deterministically without sleeping (ComposedAtValue is
// fabricated into the past).

func TestCapabilitiesFrame_Validate_Age14m30s_NotStale(t *testing.T) {
	// 14m30s age: stale under a 14-minute constant (mutant), not under 15m.
	f := &CapabilitiesFrame{
		ProjectID:       "p",
		SessionID:       "s",
		ComposedAtValue: time.Now().Add(-14*time.Minute - 30*time.Second),
	}
	if err := f.Validate(); err != nil {
		t.Errorf("14m30s age must be fresh under 15m budget; got %v", err)
	}
}

func TestCapabilitiesFrame_Validate_Age15m30s_Stale(t *testing.T) {
	// 15m30s age: stale under 15m (real), fresh under a 16-minute mutant.
	f := &CapabilitiesFrame{
		ProjectID:       "p",
		SessionID:       "s",
		ComposedAtValue: time.Now().Add(-15*time.Minute - 30*time.Second),
	}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("15m30s age must be stale under 15m budget; got %v", err)
	}
}

func TestDriftFrame_Constructor_DriftDetectedZeroTime(t *testing.T) {
	// Kills the `|| false` boolean-term mutation (drift_frame.go.36 in
	// the 2026-08-07 run, which also HUNG the run when left mutated).
	// Original: (lastVerdict=="aligned" || lastVerdict=="drift_detected")
	//           && lastReconciledAt.IsZero() -> ErrDriftVerdictWithoutTime.
	// Mutant:   (lastVerdict=="aligned" || false) -> falls through to
	//           ErrDriftDriftWithoutItems instead. Different error.
	_, err := NewDriftFrame("s", 42, "drift_detected", time.Time{}, nil)
	if err != ErrDriftVerdictWithoutTime {
		t.Errorf("want ErrDriftVerdictWithoutTime, got %v", err)
	}
}

// --- DriftFrame Validate() boolean-term mutants (drift_frame.go.53/54/57) ---

func TestDriftFrame_Validate_DriftDetectedZeroTime(t *testing.T) {
	// Kills drift_frame.go.53: `|| false` in Validate() — same shape
	// as the constructor mutant. drift_detected + zero time must yield
	// ErrDriftVerdictWithoutTime, not fall through.
	f := &DriftFrame{SessionID: "s", LastVerdict: "drift_detected", ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftVerdictWithoutTime {
		t.Errorf("want ErrDriftVerdictWithoutTime, got %v", err)
	}
}

func TestDriftFrame_Validate_DriftDetectedWithItems(t *testing.T) {
	// Kills drift_frame.go.57: `&& true` in the drift-without-items
	// branch. A drift_detected frame WITH items must validate cleanly
	// (the mutant would reject it as ErrDriftDriftWithoutItems).
	f := &DriftFrame{
		SessionID:        "s",
		LastVerdict:      "drift_detected",
		LastReconciledAt: time.Now(),
		PendingItems:     []string{"x"},
		ComposedAtValue:  time.Now(),
	}
	if err := f.Validate(); err != nil {
		t.Errorf("drift_detected with items must validate; got %v", err)
	}
}

func TestDriftFrame_Validate_NeedsHumanWithItems(t *testing.T) {
	// Kills drift_frame.go.54: `true &&` in the aligned-with-items
	// branch. A needs_human frame with items must validate cleanly
	// (the mutant would reject it as ErrDriftAlignedWithItems).
	f := &DriftFrame{
		SessionID:       "s",
		LastVerdict:     "needs_human",
		PendingItems:    []string{"x"},
		ComposedAtValue: time.Now(),
	}
	if err := f.Validate(); err != nil {
		t.Errorf("needs_human with items must validate; got %v", err)
	}
}

// --- DriftFrame HasPendingItems mutants (drift_frame.go.58/81) ---

func TestDriftFrame_HasPendingItems_NeedsHumanWithItems(t *testing.T) {
	// Kills drift_frame.go.58: `true &&` — only drift_detected with
	// items counts as pending, needs_human must return false.
	f := &DriftFrame{LastVerdict: "needs_human", PendingItems: []string{"x"}}
	if f.HasPendingItems() {
		t.Error("needs_human with items must NOT report pending items")
	}
}

func TestDriftFrame_HasPendingItems_ExactlyOneItem(t *testing.T) {
	// Kills drift_frame.go.81: `len > 0` -> `len > 1`. One pending item
	// must still report true.
	f := &DriftFrame{LastVerdict: "drift_detected", PendingItems: []string{"x"}}
	if !f.HasPendingItems() {
		t.Error("drift_detected with 1 item must report pending items")
	}
}

// --- DriftFrame specID boundary mutants (drift_frame.go.73/77) ---

func TestDriftFrame_Constructor_SpecIDOne(t *testing.T) {
	// Kills drift_frame.go.73: `specID > 0` -> `specID > 1`.
	// SpecID==1 with empty verdict must still be rejected.
	_, err := NewDriftFrame("s", 1, "", time.Time{}, nil)
	if err != ErrDriftSpecWithoutVerdict {
		t.Errorf("want ErrDriftSpecWithoutVerdict, got %v", err)
	}
}

func TestDriftFrame_Validate_SpecIDOne(t *testing.T) {
	// Kills drift_frame.go.77: `f.SpecID > 0` -> `f.SpecID > 1` in
	// Validate(). SpecID==1 with empty verdict must be rejected.
	f := &DriftFrame{SessionID: "s", SpecID: 1, ComposedAtValue: time.Now()}
	if err := f.Validate(); err != ErrDriftSpecWithoutVerdict {
		t.Errorf("want ErrDriftSpecWithoutVerdict, got %v", err)
	}
}

// --- DriftFrame constant thresholds (drift_frame.go.60/71) ---

func TestDriftFrame_Validate_Age29s_NotStale(t *testing.T) {
	f := &DriftFrame{SessionID: "s", ComposedAtValue: time.Now().Add(-29 * time.Second)}
	if err := f.Validate(); err != nil {
		t.Errorf("29s age must be fresh under 30s budget; got %v", err)
	}
}

func TestDriftFrame_Validate_Age31s_Stale(t *testing.T) {
	f := &DriftFrame{SessionID: "s", ComposedAtValue: time.Now().Add(-31 * time.Second)}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("31s age must be stale under 30s budget; got %v", err)
	}
}
