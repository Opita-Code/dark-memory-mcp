package atomic

import (
	"strings"
	"testing"
	"time"
)

func TestEvidenceFrame_HappyPath(t *testing.T) {
	writes := []WriteRef{
		{WriteID: 1, SessionID: "s1", WritePath: "SaveFrame"},
		{WriteID: 2, SessionID: "s1", WritePath: "SaveArtifact"},
	}
	research := []ResearchRef{
		{ItemID: 10, URL: "https://nvd", Title: "CVE-2026-XXXX", Confidence: 0.92},
	}
	f, err := NewEvidenceFrame("proj-x", "sess-1", writes, research, 42)
	if err != nil {
		t.Fatalf("NewEvidenceFrame: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.WriteCount() != 2 || f.ResearchCount() != 1 {
		t.Errorf("counts wrong")
	}
}

func TestEvidenceFrame_EmptyProjectID(t *testing.T) {
	if _, err := NewEvidenceFrame("", "s1", nil, nil, 0); err != ErrEvidenceEmptyProjectID {
		t.Errorf("want ErrEvidenceEmptyProjectID, got %v", err)
	}
}

func TestEvidenceFrame_EmptySessionID(t *testing.T) {
	if _, err := NewEvidenceFrame("p", "", nil, nil, 0); err != ErrEvidenceEmptySessionID {
		t.Errorf("want ErrEvidenceEmptySessionID, got %v", err)
	}
}

func TestEvidenceFrame_NegativeToken(t *testing.T) {
	if _, err := NewEvidenceFrame("p", "s", nil, nil, -1); err != ErrEvidenceNegativeToken {
		t.Errorf("want ErrEvidenceNegativeToken, got %v", err)
	}
}

func TestEvidenceFrame_HashDeterminism(t *testing.T) {
	f1, _ := NewEvidenceFrame("p", "s", []WriteRef{{WriteID: 1}}, nil, 0)
	f2, _ := NewEvidenceFrame("p", "s", []WriteRef{{WriteID: 1}}, nil, 0)
	f1.ComposedAtValue = f2.ComposedAtValue
	h1, _ := f1.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Errorf("hash determinism failed")
	}
}

func TestEvidenceFrame_Stale(t *testing.T) {
	f, _ := NewEvidenceFrame("p", "s", nil, nil, 0)
	f.ComposedAtValue = time.Now().Add(-2 * MaxEvidenceFrameAge)
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("want stale, got %v", err)
	}
}

func TestEvidenceFrame_RenderOmitsEmpty(t *testing.T) {
	f, _ := NewEvidenceFrame("p", "s", nil, nil, 0)
	b, err := f.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(b), `"project_id":"p"`) {
		t.Errorf("expected project_id in render: %s", string(b))
	}
}
