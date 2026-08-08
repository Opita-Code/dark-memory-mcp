package atomic

import (
	"strings"
	"testing"
	"time"
)

// Wave 3 mutation-killing coverage for PersonaFrame. The original
// suite tested constructor error paths but never called Validate()
// per field on hand-built frames, never exercised
// VerifyAgainstIdentityFrame's constitution-ver mismatch (only the
// nil-identity case), and never pinned the MaxPersonaFrameAge
// constant at the +/-1-minute thresholds.

func personaFrame() *PersonaFrame {
	f, _ := NewPersonaFrame("c", "v", "brand", "voice prose", "policy", "", "formal")
	return f
}

func TestPersonaFrame_Validate_Nil(t *testing.T) {
	var f *PersonaFrame
	if err := f.Validate(); err != ErrPersonaZeroComposed {
		t.Errorf("want ErrPersonaZeroComposed, got %v", err)
	}
}

func TestPersonaFrame_Validate_EmptyConstitutionID(t *testing.T) {
	f := personaFrame()
	f.ConstitutionID = ""
	if err := f.Validate(); err != ErrPersonaEmptyConstitutionID {
		t.Errorf("want ErrPersonaEmptyConstitutionID, got %v", err)
	}
}

func TestPersonaFrame_Validate_EmptyConstitutionVer(t *testing.T) {
	f := personaFrame()
	f.ConstitutionVer = ""
	if err := f.Validate(); err != ErrPersonaEmptyConstitutionVer {
		t.Errorf("want ErrPersonaEmptyConstitutionVer, got %v", err)
	}
}

func TestPersonaFrame_Validate_EmptyVoice(t *testing.T) {
	f := personaFrame()
	f.Voice = ""
	if err := f.Validate(); err != ErrPersonaEmptyVoice {
		t.Errorf("want ErrPersonaEmptyVoice, got %v", err)
	}
}

func TestPersonaFrame_Validate_EmptyClaimsPolicy(t *testing.T) {
	f := personaFrame()
	f.ClaimsPolicy = ""
	if err := f.Validate(); err != ErrPersonaEmptyClaimsPolicy {
		t.Errorf("want ErrPersonaEmptyClaimsPolicy, got %v", err)
	}
}

func TestPersonaFrame_Validate_EmptyTone(t *testing.T) {
	f := personaFrame()
	f.Tone = ""
	if err := f.Validate(); err != ErrPersonaEmptyTone {
		t.Errorf("want ErrPersonaEmptyTone, got %v", err)
	}
}

func TestPersonaFrame_Validate_ZeroComposed(t *testing.T) {
	f := personaFrame()
	f.ComposedAtValue = time.Time{}
	if err := f.Validate(); err != ErrPersonaZeroComposed {
		t.Errorf("want ErrPersonaZeroComposed, got %v", err)
	}
}

func TestPersonaFrame_Validate_OK(t *testing.T) {
	if err := personaFrame().Validate(); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestMT_PersonaFrame_VerifyAgainstIdentityFrame_OK(t *testing.T) {
	id, _ := NewIdentityFrame("a", "op", "s", "c", "v", true)
	if err := personaFrame().VerifyAgainstIdentityFrame(id); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}

func TestPersonaFrame_VerifyAgainstIdentityFrame_NilIdentity(t *testing.T) {
	if err := personaFrame().VerifyAgainstIdentityFrame(nil); err != ErrPersonaIdentityMismatch {
		t.Errorf("want ErrPersonaIdentityMismatch, got %v", err)
	}
}

func TestPersonaFrame_VerifyAgainstIdentityFrame_ConstitutionIDMismatch(t *testing.T) {
	id, _ := NewIdentityFrame("a", "op", "s", "other", "v", true)
	err := personaFrame().VerifyAgainstIdentityFrame(id)
	if err == nil || !strings.Contains(err.Error(), "constitution") {
		t.Errorf("want constitution mismatch error, got %v", err)
	}
}

func TestPersonaFrame_VerifyAgainstIdentityFrame_ConstitutionVerMismatch(t *testing.T) {
	// Kills mutations persona_frame.go.17/.18 (`false ||` and `|| false`
	// replacing one of the two constitution comparisons). Session-level
	// ID matches but Ver differs — original errors, mutant falls through.
	id, _ := NewIdentityFrame("a", "op", "s", "c", "other", true)
	err := personaFrame().VerifyAgainstIdentityFrame(id)
	if err == nil || !strings.Contains(err.Error(), "constitution") {
		t.Errorf("want constitution mismatch error, got %v", err)
	}
}

// --- Constant threshold tests (kill MaxPersonaFrameAge +/- 1m) ---

func TestPersonaFrame_Validate_Age14m30s_NotStale(t *testing.T) {
	f := personaFrame()
	f.ComposedAtValue = time.Now().Add(-14*time.Minute - 30*time.Second)
	if err := f.Validate(); err != nil {
		t.Errorf("14m30s age must be fresh under 15m budget; got %v", err)
	}
}

func TestPersonaFrame_Validate_Age15m30s_Stale(t *testing.T) {
	f := personaFrame()
	f.ComposedAtValue = time.Now().Add(-15*time.Minute - 30*time.Second)
	if err := f.Validate(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("15m30s age must be stale under 15m budget; got %v", err)
	}
}

// --- Accessor branches ---

func TestPersonaFrame_HasBrand(t *testing.T) {
	if !personaFrame().HasBrand() {
		t.Error("HasBrand must be true when BrandID set")
	}
	f := personaFrame()
	f.BrandID = ""
	if f.HasBrand() {
		t.Error("HasBrand must be false when BrandID empty")
	}
}

func TestPersonaFrame_HasRefusalPattern(t *testing.T) {
	if personaFrame().HasRefusalPattern() {
		t.Error("HasRefusalPattern must be false when empty")
	}
	f := personaFrame()
	f.RefusalPattern = `{"refuse":"x"}`
	if !f.HasRefusalPattern() {
		t.Error("HasRefusalPattern must be true when set")
	}
}
