// gate_test.go: parallel to dark-cli/internal/audit/gate_test.go.
package auditgate

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// seedAndKey is a helper: deterministic seed → matching pubkey.
func seedAndKey(seedByte byte) (seed []byte, pub ed25519.PublicKey) {
	seed = make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = seedByte
	}
	pub = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return
}

// errorsIs wraps errors.Is so the tests can branch on sentinel errors
// even when the gate wraps them with %w.
func errorsIs(err, target error) bool { return errors.Is(err, target) }

// makeTrustedRoot returns (seed, pubkey) for a fresh trust root.
// Different from fixedPub, so we can exercise the untrusted path.
func makeTrustedRoot() (seed []byte, pub ed25519.PublicKey) {
	return seedAndKey(0xa0)
}

func TestExample_Gate_AcceptsTrustedRecord(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	_, rootPub := makeTrustedRoot()
	// The publisher (fixedPub from fixedSeed) is NOT trusted by
	// rootPub alone, so we add fixedPub as a second trust root to
	// exercise the accept path.
	g := NewReferenceGate(s, []ed25519.PublicKey{rootPub, fixedPub})
	got, err := g.Check(fixedSHA, fixedNow)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if got == nil || got.ArtifactSHA256 != fixedSHA {
		t.Fatalf("unexpected record: %+v", got)
	}
}

func TestExample_Gate_RejectsUntrustedRecord(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	_, rootPub := makeTrustedRoot() // NOT fixedPub
	g := NewReferenceGate(s, []ed25519.PublicKey{rootPub})
	got, err := g.Check(fixedSHA, fixedNow)
	if err != ErrGateUntrusted {
		t.Fatalf("err=%v want ErrGateUntrusted", err)
	}
	if got == nil {
		t.Fatalf("expected evidence record on rejection, got nil")
	}
}

func TestExample_Gate_RejectsMissingRecord(t *testing.T) {
	g := NewReferenceGate(Open(t.TempDir()), []ed25519.PublicKey{fixedPub})
	_, err := g.Check(fixedSHA, fixedNow)
	if err != ErrGateNoRecord {
		t.Fatalf("err=%v want ErrGateNoRecord", err)
	}
}

func TestExample_Gate_RejectsBadSHA(t *testing.T) {
	g := NewReferenceGate(Open(t.TempDir()), []ed25519.PublicKey{fixedPub})
	_, err := g.Check("not-a-sha", fixedNow)
	if !errorsIs(err, ErrRecordNoArtifact) {
		t.Fatalf("err=%v want ErrRecordNoArtifact", err)
	}
}

func TestExample_Gate_RejectsStaleRecord(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	g := NewReferenceGate(s, []ed25519.PublicKey{fixedPub})
	_, err := g.Check(fixedSHA, fixedNow.Add(FreshnessWindow+time.Second))
	if err != ErrRecordStale {
		t.Fatalf("err=%v want ErrRecordStale", err)
	}
}

func TestExample_Gate_AcceptsMultipleTrustedRoots(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	_, rootA := makeTrustedRoot()
	_, rootB := seedAndKey(0xc0)
	// fixedPub is in the middle of the root list to ensure the
	// loop continues past it.
	g := NewReferenceGate(s, []ed25519.PublicKey{rootA, fixedPub, rootB})
	if _, err := g.Check(fixedSHA, fixedNow); err != nil {
		t.Fatalf("check: %v", err)
	}
}

func TestExample_Gate_EmptyTrustRootsRejectsAll(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	_ = s.PutTestOnly(rec)
	g := NewReferenceGate(s, nil)
	_, err := g.Check(fixedSHA, fixedNow)
	if err != ErrGateUntrusted {
		t.Fatalf("err=%v want ErrGateUntrusted", err)
	}
}

func TestExample_Gate_NilGateReturnsDisabled(t *testing.T) {
	var g *ReferenceGate // nil receiver
	_, err := g.Check(fixedSHA, fixedNow)
	if err != ErrGateDisabled {
		t.Fatalf("err=%v want ErrGateDisabled", err)
	}
}

func TestExample_ToProvenance_ProjectsOnlyAllowedFields(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json", "abc",
		fixedSHA, []string{"skills/x.md"}, "installed", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	p := rec.ToProvenance(true)
	if p.ArtifactSHA256 != fixedSHA {
		t.Fatalf("sha drift")
	}
	if p.TemplateName != "opita-market" {
		t.Fatalf("template name drift")
	}
	if p.TemplateVersion != "0.3.0" {
		t.Fatalf("template version drift")
	}
	if p.DarkCLIVersion != "0.1.0-alpha.8" {
		t.Fatalf("dark_cli_version drift")
	}
	if !p.Trusted {
		t.Fatalf("trusted flag not propagated")
	}
	// Provenance must NOT carry pubkey/signature (those are
	// gate-internal, not for downstream consumers).
	type provPublic = struct {
		ArtifactSHA256  string
		Kind            Kind
		TemplateName    string
		TemplateVersion string
		VibeCase        string
		Harness         string
		DarkCLIVersion  string
		DarkCLICommit   string
		Outcome         string
		FilesWritten    []string
		RecordedAt      int64
		Trusted         bool
	}
	_ = provPublic(p) // compile-time check: only allowed fields
}
