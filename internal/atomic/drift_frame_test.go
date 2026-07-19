package atomic

import (
	"strings"
	"testing"
	"time"
)

func TestDriftFrame_HappyPath_Aligned(t *testing.T) {
	f, err := NewDriftFrame("sess-1", 42, "aligned", time.Now(), nil)
	if err != nil {
		t.Fatalf("NewDriftFrame: %v", err)
	}
	if !f.IsAligned() {
		t.Errorf("IsAligned should be true")
	}
	if f.HasPendingItems() {
		t.Errorf("HasPendingItems should be false for aligned")
	}
}

func TestDriftFrame_HappyPath_DriftDetected(t *testing.T) {
	f, err := NewDriftFrame("sess-1", 42, "drift_detected", time.Now(), []string{"missing_field_x", "wrong_typo"})
	if err != nil {
		t.Fatalf("NewDriftFrame: %v", err)
	}
	if f.IsAligned() {
		t.Errorf("IsAligned should be false")
	}
	if !f.HasPendingItems() {
		t.Errorf("HasPendingItems should be true")
	}
}

func TestDriftFrame_EmptySessionID(t *testing.T) {
	if _, err := NewDriftFrame("", 0, "", time.Time{}, nil); err != ErrDriftEmptySessionID {
		t.Errorf("want ErrDriftEmptySessionID, got %v", err)
	}
}

func TestDriftFrame_NegativeSpecID(t *testing.T) {
	if _, err := NewDriftFrame("s", -1, "", time.Time{}, nil); err != ErrDriftInvalidSpecID {
		t.Errorf("want ErrDriftInvalidSpecID, got %v", err)
	}
}

func TestDriftFrame_SpecWithoutVerdict(t *testing.T) {
	if _, err := NewDriftFrame("s", 42, "", time.Time{}, nil); err != ErrDriftSpecWithoutVerdict {
		t.Errorf("want ErrDriftSpecWithoutVerdict, got %v", err)
	}
}

func TestDriftFrame_VerdictUnknown(t *testing.T) {
	if _, err := NewDriftFrame("s", 42, "maybe", time.Now(), nil); err != ErrDriftVerdictUnknown {
		t.Errorf("want ErrDriftVerdictUnknown, got %v", err)
	}
}

func TestDriftFrame_AlignedWithItems(t *testing.T) {
	// Verdict=aligned but pending_items non-empty -> inconsistency.
	if _, err := NewDriftFrame("s", 42, "aligned", time.Now(), []string{"x"}); err != ErrDriftAlignedWithItems {
		t.Errorf("want ErrDriftAlignedWithItems, got %v", err)
	}
}

func TestDriftFrame_DriftWithoutItems(t *testing.T) {
	// Verdict=drift_detected but pending_items empty -> inconsistency.
	if _, err := NewDriftFrame("s", 42, "drift_detected", time.Now(), nil); err != ErrDriftDriftWithoutItems {
		t.Errorf("want ErrDriftDriftWithoutItems, got %v", err)
	}
}

func TestDriftFrame_NeedsHumanOK(t *testing.T) {
	// needs_human with no timestamp and no items is valid.
	f, err := NewDriftFrame("s", 42, "needs_human", time.Time{}, nil)
	if err != nil {
		t.Fatalf("needs_human with empty time + items: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestDriftFrame_AlignedWithoutTime(t *testing.T) {
	// Verdict=aligned requires non-zero last_reconciled_at.
	if _, err := NewDriftFrame("s", 42, "aligned", time.Time{}, nil); err != ErrDriftVerdictWithoutTime {
		t.Errorf("want ErrDriftVerdictWithoutTime, got %v", err)
	}
}

func TestDriftFrame_HashDeterminism(t *testing.T) {
	now := time.Now()
	f1, _ := NewDriftFrame("s", 42, "aligned", now, nil)
	f2, _ := NewDriftFrame("s", 42, "aligned", now, nil)
	// ComposedAtValue is set inside the constructor via time.Now(); sync
	// it explicitly because two distinct time.Now() calls produce different
	// nanosecond timestamps.
	f1.ComposedAtValue = f2.ComposedAtValue
	h1, _ := f1.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Errorf("hash determinism failed")
	}
}

func TestDriftFrame_Stale(t *testing.T) {
	f, _ := NewDriftFrame("s", 0, "", time.Time{}, nil)
	f.ComposedAtValue = time.Now().Add(-2 * MaxDriftFrameAge)
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("want stale, got %v", err)
	}
}
