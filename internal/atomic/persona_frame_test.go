package atomic

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPersonaFrame_HappyPath(t *testing.T) {
	f, err := NewPersonaFrame(
		"dark-agents/dark-memory-mcp-cerebro", // constitution_id
		"1.0.0",                               // constitution_ver
		"brand-pivote",                        // brand_id
		"Technical, precise, defensive-by-default. Never performative.", // voice
		"Claims allowed: codebase state, audit findings, drift verdict. Claims NOT allowed: predictions, identity leakage, hallucinated CVE IDs.", // claims_policy
		`{"refuse_if_off_scope":true}`,        // refusal_pattern
		"technical",                          // tone
	)
	if err != nil {
		t.Fatalf("NewPersonaFrame: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !f.HasBrand() {
		t.Errorf("HasBrand should be true")
	}
	if !f.HasRefusalPattern() {
		t.Errorf("HasRefusalPattern should be true")
	}
}

func TestPersonaFrame_HappyPath_NoBrandNoPattern(t *testing.T) {
	f, err := NewPersonaFrame("c", "v", "", "voice", "claims", "", "technical")
	if err != nil {
		t.Fatalf("NewPersonaFrame without brand/pattern: %v", err)
	}
	if f.HasBrand() || f.HasRefusalPattern() {
		t.Errorf("HasBrand/HasRefusalPattern should be false")
	}
}

func TestPersonaFrame_EmptyConstitutionID(t *testing.T) {
	if _, err := NewPersonaFrame("", "v", "b", "v", "c", "", "tone"); err != ErrPersonaEmptyConstitutionID {
		t.Errorf("want ErrPersonaEmptyConstitutionID, got %v", err)
	}
}

func TestPersonaFrame_EmptyConstitutionVer(t *testing.T) {
	if _, err := NewPersonaFrame("c", "", "b", "v", "c", "", "tone"); err != ErrPersonaEmptyConstitutionVer {
		t.Errorf("want ErrPersonaEmptyConstitutionVer, got %v", err)
	}
}

func TestPersonaFrame_EmptyVoice(t *testing.T) {
	if _, err := NewPersonaFrame("c", "v", "b", "", "c", "", "tone"); err != ErrPersonaEmptyVoice {
		t.Errorf("want ErrPersonaEmptyVoice, got %v", err)
	}
}

func TestPersonaFrame_EmptyClaimsPolicy(t *testing.T) {
	if _, err := NewPersonaFrame("c", "v", "b", "v", "", "", "tone"); err != ErrPersonaEmptyClaimsPolicy {
		t.Errorf("want ErrPersonaEmptyClaimsPolicy, got %v", err)
	}
}

func TestPersonaFrame_EmptyTone(t *testing.T) {
	if _, err := NewPersonaFrame("c", "v", "b", "v", "c", "", ""); err != ErrPersonaEmptyTone {
		t.Errorf("want ErrPersonaEmptyTone, got %v", err)
	}
}

func TestPersonaFrame_VerifyAgainstIdentityFrame_OK(t *testing.T) {
	f, _ := NewPersonaFrame("c", "v", "b", "v", "c", "", "tone")
	id, _ := NewIdentityFrame("a", "op", "s", "c", "v", true)
	if err := f.VerifyAgainstIdentityFrame(id); err != nil {
		t.Errorf("VerifyAgainstIdentityFrame: %v", err)
	}
}

func TestPersonaFrame_VerifyAgainstIdentityFrame_ConstitutionMismatch(t *testing.T) {
	f, _ := NewPersonaFrame("c1", "v1", "b", "v", "c", "", "tone")
	id, _ := NewIdentityFrame("a", "op", "s", "c2", "v2", true)
	err := f.VerifyAgainstIdentityFrame(id)
	if !errors.Is(err, ErrPersonaIdentityMismatch) {
		t.Errorf("want ErrPersonaIdentityMismatch, got %v", err)
	}
}

func TestPersonaFrame_HashDeterminism(t *testing.T) {
	f1, _ := NewPersonaFrame("c", "v", "b", "v", "c", "", "tone")
	f2, _ := NewPersonaFrame("c", "v", "b", "v", "c", "", "tone")
	f1.ComposedAtValue = f2.ComposedAtValue
	h1, _ := f1.Hash()
	h2, _ := f2.Hash()
	if h1 != h2 {
		t.Errorf("hash determinism failed")
	}
}

func TestPersonaFrame_Stale(t *testing.T) {
	f, _ := NewPersonaFrame("c", "v", "b", "v", "c", "", "tone")
	f.ComposedAtValue = time.Now().Add(-2 * MaxPersonaFrameAge)
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("want stale, got %v", err)
	}
}
