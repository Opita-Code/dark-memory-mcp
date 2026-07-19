package atomic

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScopeFrame_HappyPath(t *testing.T) {
	f, err := NewScopeFrame("sess-1", 42, []TaskRef{
		{TaskID: "t1", SpecID: 42, Owner: "dark-agent", Status: "open"},
	}, []EvidenceRef{
		{ArtifactID: 100, Kind: "text"},
	}, "aligned", time.Now())
	if err != nil {
		t.Fatalf("NewScopeFrame: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !f.HasOpenSpec() {
		t.Errorf("HasOpenSpec should be true")
	}
	if f.TaskCount() != 1 || f.EvidenceCount() != 1 {
		t.Errorf("counts wrong: tasks=%d evidence=%d", f.TaskCount(), f.EvidenceCount())
	}
	// Hash determinism: same content -> same hash.
	f2, _ := NewScopeFrame("sess-1", 42, []TaskRef{
		{TaskID: "t1", SpecID: 42, Owner: "dark-agent", Status: "open"},
	}, []EvidenceRef{{ArtifactID: 100, Kind: "text"}}, "aligned", f.LastDriftAt)
	f2.ComposedAtValue = f.ComposedAtValue
	h1, _ := f.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Errorf("hash determinism failed")
	}
}

func TestScopeFrame_EmptySessionID(t *testing.T) {
	if _, err := NewScopeFrame("", 0, nil, nil, "", time.Time{}); err != ErrScopeEmptySessionID {
		t.Errorf("want ErrScopeEmptySessionID, got %v", err)
	}
}

func TestScopeFrame_NegativeSpecID(t *testing.T) {
	if _, err := NewScopeFrame("sess-1", -1, nil, nil, "", time.Time{}); err != ErrScopeInvalidSpecID {
		t.Errorf("want ErrScopeInvalidSpecID, got %v", err)
	}
}

func TestScopeFrame_VerdictWithoutTime(t *testing.T) {
	// Verdict set, time zero -> cross-consistency error.
	if _, err := NewScopeFrame("sess-1", 0, nil, nil, "aligned", time.Time{}); err != ErrScopeVerdictWithoutTime {
		t.Errorf("want ErrScopeVerdictWithoutTime, got %v", err)
	}
	// Time set, verdict empty -> cross-consistency error.
	if _, err := NewScopeFrame("sess-1", 0, nil, nil, "", time.Now()); err != ErrScopeVerdictWithoutTime {
		t.Errorf("want ErrScopeVerdictWithoutTime, got %v", err)
	}
}

func TestScopeFrame_VerdictUnknown(t *testing.T) {
	if _, err := NewScopeFrame("sess-1", 0, nil, nil, "maybe", time.Now()); err != ErrScopeVerdictUnknown {
		t.Errorf("want ErrScopeVerdictUnknown, got %v", err)
	}
}

func TestScopeFrame_TaskMissingTaskID(t *testing.T) {
	tasks := []TaskRef{{TaskID: "", SpecID: 42}}
	if _, err := NewScopeFrame("sess-1", 42, tasks, nil, "", time.Time{}); err == nil {
		t.Fatalf("expected error for empty task_id")
	} else if !strings.Contains(err.Error(), "task_id") {
		t.Errorf("expected task_id in error, got %v", err)
	}
}

func TestScopeFrame_TaskMissingSpecID(t *testing.T) {
	tasks := []TaskRef{{TaskID: "t1", SpecID: 0}}
	if _, err := NewScopeFrame("sess-1", 42, tasks, nil, "", time.Time{}); err == nil {
		t.Fatalf("expected error for task without spec_id (open spec set)")
	} else if !strings.Contains(err.Error(), "spec_id") {
		t.Errorf("expected spec_id in error, got %v", err)
	}
}

func TestScopeFrame_StaleRejected(t *testing.T) {
	f, _ := NewScopeFrame("sess-1", 0, nil, nil, "", time.Time{})
	f.ComposedAtValue = time.Now().Add(-2 * MaxScopeFrameAge)
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("want stale error, got %v", err)
	}
}

func TestScopeFrame_VerifyAgainstIdentityFrame_OK(t *testing.T) {
	f, _ := NewScopeFrame("sess-1", 0, nil, nil, "", time.Time{})
	id, _ := NewIdentityFrame("a", "op", "sess-1", "c", "v", true)
	if err := f.VerifyAgainstIdentityFrame(id); err != nil {
		t.Errorf("VerifyAgainstIdentityFrame: %v", err)
	}
}

func TestScopeFrame_VerifyAgainstIdentityFrame_Mismatch(t *testing.T) {
	f, _ := NewScopeFrame("sess-1", 0, nil, nil, "", time.Time{})
	id, _ := NewIdentityFrame("a", "op", "sess-OTHER", "c", "v", true)
	err := f.VerifyAgainstIdentityFrame(id)
	if !errors.Is(err, ErrScopeIdentityMismatch) {
		t.Errorf("want ErrScopeIdentityMismatch, got %v", err)
	}
}

func TestScopeFrame_RenderCanonicalJSON(t *testing.T) {
	f, _ := NewScopeFrame("sess-1", 0, nil, nil, "", time.Time{})
	b, err := f.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(b), "  ") {
		t.Errorf("canonical JSON should have no double spaces: %s", string(b))
	}
	if !strings.Contains(string(b), `"session_id":"sess-1"`) {
		t.Errorf("expected session_id in rendered output: %s", string(b))
	}
}
