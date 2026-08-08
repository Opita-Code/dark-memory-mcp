package atomic

import (
	"strings"
	"testing"
	"time"
)

// Wave 3 mutation-killing coverage for ScopeFrame. The original suite
// covered constructor errors and the stale path but missed:
//   - Validate() on hand-built frames per branch (VerifyAgainstIdentityFrame nil,
//     verdict-unknown via Validate, needs_human via Validate — regression for
//     the `&& true` bug at commit 7e88406)
//   - constructor verdict cross-consistency for needs_human/drift_detected
//   - HasOpenSpec boundary (OpenSpecID == 1)
//   - task validation boundary (openSpecID == 1)
//   - MaxScopeFrameAge +/- 1s constant thresholds

func TestScopeFrame_VerifyAgainstIdentityFrame_Nil(t *testing.T) {
	f, _ := NewScopeFrame("sess-1", 0, nil, nil, "", time.Time{})
	if err := f.VerifyAgainstIdentityFrame(nil); err != ErrScopeIdentityMismatch {
		t.Errorf("want ErrScopeIdentityMismatch, got %v", err)
	}
}

func TestScopeFrame_Constructor_NeedsHumanWithTime(t *testing.T) {
	// Kills mutation scope_frame.go.23 (`&& true` replacing the
	// needs_human check in the constructor verdict validation).
	// needs_human is a canonical verdict and must construct cleanly.
	f, err := NewScopeFrame("sess-1", 42, nil, nil, "needs_human", time.Now())
	if err != nil {
		t.Fatalf("NewScopeFrame needs_human: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate needs_human: %v", err)
	}
}

func TestScopeFrame_Constructor_DriftDetectedWithTime(t *testing.T) {
	// Kills mutation scope_frame.go.25 (`true &&` replacing the
	// drift_detected check). drift_detected is canonical.
	f, err := NewScopeFrame("sess-1", 42, nil, nil, "drift_detected", time.Now())
	if err != nil {
		t.Fatalf("NewScopeFrame drift_detected: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate drift_detected: %v", err)
	}
}

func TestScopeFrame_Validate_DriftDetected(t *testing.T) {
	// Kills mutation scope_frame.go.31 (`true &&` replacing the
	// drift_detected check inside Validate()).
	f := &ScopeFrame{
		SessionID:        "s",
		LastDriftVerdict: "drift_detected",
		LastDriftAt:      time.Now(),
		ComposedAtValue:  time.Now(),
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate drift_detected: %v", err)
	}
}

func TestScopeFrame_HasOpenSpec_OpenSpecIDOne(t *testing.T) {
	// Kills mutations scope_frame.go.21/.41/.48 (HasOpenSpec boundary
	// shifted: `> 0` -> `>= 0`, `> -1`, `> 1`). OpenSpecID==1 must be
	// "has open spec"; OpenSpecID==0 must not.
	f, _ := NewScopeFrame("s", 1, nil, nil, "", time.Time{})
	if !f.HasOpenSpec() {
		t.Error("HasOpenSpec must be true for OpenSpecID=1")
	}
	f0, _ := NewScopeFrame("s", 0, nil, nil, "", time.Time{})
	if f0.HasOpenSpec() {
		t.Error("HasOpenSpec must be false for OpenSpecID=0")
	}
}

func TestScopeFrame_Constructor_TaskValidation_OpenSpecIDOne(t *testing.T) {
	// Kills mutations scope_frame.go.18/.37/.44 (task validation
	// boundary shifted). With OpenSpecID==1 the task loop MUST run:
	// a task missing task_id must be rejected.
	tasks := []TaskRef{{TaskID: "", SpecID: 1}}
	_, err := NewScopeFrame("s", 1, tasks, nil, "", time.Time{})
	if err == nil {
		t.Fatal("expected task_id error with OpenSpecID=1 and empty task_id")
	}
	if !strings.Contains(err.Error(), "task_id") {
		t.Errorf("expected task_id in error, got %v", err)
	}
}

func TestScopeFrame_Constructor_TaskValidation_OpenSpecIDZero(t *testing.T) {
	// With OpenSpecID==0 the task loop must NOT run (no open spec).
	// Mutations shifting `> 0` -> `>= 0` / `> -1` would wrongly
	// validate tasks here.
	tasks := []TaskRef{{TaskID: "", SpecID: 0}}
	if _, err := NewScopeFrame("s", 0, tasks, nil, "", time.Time{}); err != nil {
		t.Errorf("OpenSpecID=0 must skip task validation, got %v", err)
	}
}

// --- MaxScopeFrameAge +/- 1s constant thresholds ---

func TestScopeFrame_Validate_Age29s_NotStale(t *testing.T) {
	f := &ScopeFrame{SessionID: "s", ComposedAtValue: time.Now().Add(-29 * time.Second)}
	if err := f.Validate(); err != nil {
		t.Errorf("29s age must be fresh under 30s budget; got %v", err)
	}
}

func TestScopeFrame_Validate_Age31s_Stale(t *testing.T) {
	f := &ScopeFrame{SessionID: "s", ComposedAtValue: time.Now().Add(-31 * time.Second)}
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("31s age must be stale under 30s budget; got %v", err)
	}
}

// --- Validate() verdict-with-time XOR (scope_frame.go:159) ---
// The constructor (line 103) and Validate (line 159) BOTH enforce
// `(LastDriftVerdict == "") != LastDriftAt.IsZero()`. The mutation
// run killed the constructor copy but LIVED the Validate copy because
// the original suite only drove the constructor. These hand-built
// frames drive Validate directly:
//   - "" + zero time     → valid
//   - "aligned" + time   → valid
//   - "" + non-zero time → ErrScopeVerdictWithoutTime
//   - "aligned" + zero   → ErrScopeVerdictWithoutTime
func TestScopeFrame_Validate_VerdictTimeXOR(t *testing.T) {
	valid := []*ScopeFrame{
		{SessionID: "s", ComposedAtValue: time.Now()},
		{SessionID: "s", LastDriftVerdict: "aligned", LastDriftAt: time.Now(), ComposedAtValue: time.Now()},
		{SessionID: "s", LastDriftVerdict: "needs_human", LastDriftAt: time.Now(), ComposedAtValue: time.Now()},
	}
	for i, f := range valid {
		if err := f.Validate(); err != nil {
			t.Errorf("valid[%d]: expected nil, got %v", i, err)
		}
	}
	invalid := []*ScopeFrame{
		{SessionID: "s", LastDriftVerdict: "aligned", ComposedAtValue: time.Now()},                   // verdict w/o time
		{SessionID: "s", LastDriftVerdict: "", LastDriftAt: time.Now(), ComposedAtValue: time.Now()}, // time w/o verdict
	}
	for i, f := range invalid {
		if err := f.Validate(); err == nil {
			t.Errorf("invalid[%d]: expected ErrScopeVerdictWithoutTime, got nil", i)
		} else if err != ErrScopeVerdictWithoutTime {
			t.Errorf("invalid[%d]: expected ErrScopeVerdictWithoutTime, got %v", i, err)
		}
	}
}
