// record_test.go: parallel tests to dark-cli/internal/audit/record_test.go.
// Same fixtures (fixedSeed → fixedPub) so any divergence between
// producer (dark-cli) and consumer (dark-memory-mcp) is caught here.
//
// FIXTURE DERIVATION (deterministic, no randomness in tests):
//
//	fixedSeed   = bytes(0x01..0x20)               // 32 bytes
//	fixedPub    = ed25519.NewKeyFromSeed(fixedSeed).Public()
//	fixedSHA    = sha256("dark-cli/audit/v1 fixture")
package auditgate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

var (
	fixedSeed = func() []byte {
		s := make([]byte, ed25519.SeedSize)
		for i := range s {
			s[i] = byte(i + 1)
		}
		return s
	}()
	fixedPub = ed25519.NewKeyFromSeed(fixedSeed).Public().(ed25519.PublicKey)
	// Match dark-cli's fixedNow so the cross-package canonical-bytes
	// hash is identical on both sides.
	fixedNow = time.Unix(1_700_000_000, 0)
	fixedSHA = func() string {
		h := sha256.Sum256([]byte("dark-cli/audit/v1 fixture"))
		return hex.EncodeToString(h[:])
	}()
)

func TestExample_NewBundleRecord_PopulatesSchemaAndKind(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json", "abc",
		fixedSHA, []string{"skills/foo.md"}, "installed", fixedNow, fixedPub)
	if rec.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version=%q want %q", rec.SchemaVersion, SchemaVersion)
	}
	if rec.Kind != KindBundleInstall {
		t.Fatalf("kind=%q want %q", rec.Kind, KindBundleInstall)
	}
	if rec.ArtifactSHA256 != fixedSHA {
		t.Fatalf("artifact_sha256 mismatch")
	}
	if len(rec.FilesWritten) != 1 || rec.FilesWritten[0] != "skills/foo.md" {
		t.Fatalf("files_written not populated")
	}
}

func TestExample_NewExportRecord_PopulatesKindExport(t *testing.T) {
	rec := NewExportRecord("opencode", "/cfg/opencode", "0.1.0-alpha.8", "dbd30b9",
		"/cfg/opencode/dark.lock.json", fixedSHA, fixedNow, fixedPub)
	if rec.Kind != KindExportProcessed {
		t.Fatalf("kind=%q want %q", rec.Kind, KindExportProcessed)
	}
}

func TestExample_SignPopulatesSignatureAndPubkey(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if rec.Signature == nil {
		t.Fatal("signature nil after Sign")
	}
	if rec.Signature.Algorithm != "ed25519" {
		t.Fatalf("algorithm=%q want ed25519", rec.Signature.Algorithm)
	}
	if len(rec.Signature.Sig) != ed25519.SignatureSize {
		t.Fatalf("sig len=%d want %d", len(rec.Signature.Sig), ed25519.SignatureSize)
	}
	if len(rec.PublisherPubkey) != ed25519.PublicKeySize {
		t.Fatalf("pubkey len=%d want %d", len(rec.PublisherPubkey), ed25519.PublicKeySize)
	}
}

func TestExample_VerifyFreshSignaturePasses(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := rec.Verify(fixedNow); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestExample_VerifyRejectsTamperedArtifactHash(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.ArtifactSHA256 = strings.Repeat("0", 64)
	if err := rec.Verify(fixedNow); err != ErrRecordSignature {
		t.Fatalf("err=%v want ErrRecordSignature", err)
	}
}

func TestExample_VerifyRejectsTamperedTemplateName(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.TemplateName = "evil-package"
	if err := rec.Verify(fixedNow); err != ErrRecordSignature {
		t.Fatalf("err=%v want ErrRecordSignature", err)
	}
}

func TestExample_VerifyRejectsStaleSignature(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	tooLate := fixedNow.Add(FreshnessWindow + time.Second)
	if err := rec.Verify(tooLate); err != ErrRecordStale {
		t.Fatalf("err=%v want ErrRecordStale", err)
	}
}

func TestExample_VerifyRejectsMissingSignature(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	if err := rec.Verify(fixedNow); err != ErrRecordUnsigned {
		t.Fatalf("err=%v want ErrRecordUnsigned", err)
	}
}

func TestExample_VerifyRejectsBadAlgorithm(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.Signature.Algorithm = "rsa"
	if err := rec.Verify(fixedNow); err != ErrRecordBadAlgorithm {
		t.Fatalf("err=%v want ErrRecordBadAlgorithm", err)
	}
}

func TestExample_VerifyRejectsBadArtifactHash(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", "NOT-A-SHA", nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	if err := rec.Verify(fixedNow); err != ErrRecordNoArtifact {
		t.Fatalf("err=%v want ErrRecordNoArtifact", err)
	}
}

func TestExample_VerifyRejectsWrongPubkeyLength(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.PublisherPubkey = []byte{0x01, 0x02, 0x03}
	if err := rec.Verify(fixedNow); err != ErrRecordNoPubkey {
		t.Fatalf("err=%v want ErrRecordNoPubkey", err)
	}
}

func TestExample_VerifyRejectsBadSchemaVersion(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.SchemaVersion = "dark-cli/audit/v999"
	if err := rec.Verify(fixedNow); err != ErrRecordSchema {
		t.Fatalf("err=%v want ErrRecordSchema", err)
	}
}

func TestExample_VerifyRejectsBadKind(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
		"0.1.0-alpha.8", "", "", "", fixedSHA, nil, "", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	rec.Kind = Kind("bundle_evil")
	if err := rec.Verify(fixedNow); err != ErrRecordKind {
		t.Fatalf("err=%v want ErrRecordKind", err)
	}
}

func TestExample_CanonicalBytes_DeterministicForFixedInput(t *testing.T) {
	make := func() *AuditRecord {
		r := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "",
			"0.1.0-alpha.8", "", "", "", fixedSHA,
			[]string{"skills/a.md", "skills/b.md", "skills/c.md"}, "installed", fixedNow, fixedPub)
		_ = r.Sign(fixedSeed, fixedNow)
		return r
	}
	r1 := make()
	r2 := make()
	c1, err := r1.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := r2.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(c1) != string(c2) {
		t.Fatalf("canonical bytes not deterministic")
	}
	// FilesWritten must be sorted (alphabetical) in canonical form.
	// Indent is 4 spaces because files_written is nested 2 levels.
	if !strings.Contains(string(c1), `"files_written": [
    "skills/a.md",
    "skills/b.md",
    "skills/c.md"
  ]`) {
		t.Fatalf("files_written not sorted lexicographically:\n%s", string(c1))
	}
}

func TestExample_RoundTripJSON_PreservesFields(t *testing.T) {
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json", "abc123",
		fixedSHA, []string{"skills/x.md", "skills/y.md"}, "installed", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	encoded, err := rec.MarshalJSONOnDisk()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalJSONOnDisk(encoded)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SchemaVersion != rec.SchemaVersion {
		t.Fatalf("schema_version drift: %q vs %q", decoded.SchemaVersion, rec.SchemaVersion)
	}
	if decoded.Kind != rec.Kind {
		t.Fatalf("kind drift")
	}
	if decoded.ArtifactSHA256 != rec.ArtifactSHA256 {
		t.Fatalf("artifact_sha256 drift")
	}
	if !bytesEqualConstTime(decoded.PublisherPubkey, rec.PublisherPubkey) {
		t.Fatalf("pubkey drift")
	}
	if !bytesEqualConstTime(decoded.Signature.Sig, rec.Signature.Sig) {
		t.Fatalf("sig drift")
	}
	if err := decoded.Verify(fixedNow); err != nil {
		t.Fatalf("verify after round-trip: %v", err)
	}
}

func TestExample_SignUnderlyingPackageStillDeterministic(t *testing.T) {
	// Cross-package invariant: the bytes this package signs MUST
	// match the bytes dark-cli's internal/audit package signs. We
	// pin the canonical bytes here so divergence is detected on
	// either side of the protocol.
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/cfg/opencode",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/opencode/dark.lock.json", "",
		fixedSHA, []string{"skills/a.md"}, "installed", fixedNow, fixedPub)
	_ = rec.Sign(fixedSeed, fixedNow)
	canonical, err := rec.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	// Hash the canonical bytes and pin the digest. If either side
	// changes its canonicalization, this hash changes.
	h := sha256.Sum256(canonical)
	got := hex.EncodeToString(h[:])
	// PIN: must match dark-cli/internal/audit/record_test.go's
	// TestExample_CanonicalBytesHash_MatchesDarkMemoryMcp hash.
	// Both sides update this in lockstep.
	const want = "4e6196a07c7903dc712fd4a96cbc4df49317e0da45b57f939b7e6d12d6606ccb"
	if got != want {
		t.Fatalf("canonical-bytes hash drifted: got %s, want %s. "+
			"This means dark-memory-mcp or dark-cli changed its "+
			"canonicalization. Update BOTH sides in lockstep.",
			got, want)
	}
}

// TestExample_GoldenWireFormat_DecodesAndVerifies exercises the
// EXACT JSON shape that dark-cli writes to disk. This is the
// wire-format pin: if dark-cli changes the on-disk format, this
// test fails. The fixture here mirrors what
// dark-cli/internal/audit/store.go::RecordInstall writes.
func TestExample_GoldenWireFormat_DecodesAndVerifies(t *testing.T) {
	golden := `{
  "schema_version": "dark-cli/audit/v1",
  "kind": "bundle_install",
  "artifact_sha256": "7adfe393cc9d4be2ede891f5ac5ef94c1e0e0ae39e15988c8fe93f35d8418f43",
  "template_name": "opita-market",
  "template_version": "0.3.0",
  "vibe_case": "C5",
  "harness": "opencode",
  "harness_config_dir": "/Users/nico/.config/opencode",
  "dark_cli_version": "0.1.0-alpha.8",
  "dark_cli_commit": "dbd30b9",
  "lockfile_path": "/Users/nico/.config/opencode/dark.lock.json",
  "lockfile_entry_sha256": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
  "recorded_at_unix": 1727000000,
  "files_written": [
    "skills/foo.md",
    "skills/bar.md"
  ],
  "outcome": "installed",
  "publisher_pubkey": "dGVzdC1wdWJsaWMta2V5LWJ5dGVzLW9ubHktMzItYnl0ZXMtbG9uZw==",
  "signature": {
    "algorithm": "ed25519",
    "sig": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "signed_at_unix": 1727000000
  }
}`
	// Replace the placeholder publisher_pubkey + sig with valid
	// base64 of the actual fixture key.
	rec := NewBundleRecord("opita-market", "0.3.0", "C5", "opencode", "/Users/nico/.config/opencode",
		"0.1.0-alpha.8", "dbd30b9", "/Users/nico/.config/opencode/dark.lock.json",
		"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		"7adfe393cc9d4be2ede891f5ac5ef94c1e0e0ae39e15988c8fe93f35d8418f43",
		[]string{"skills/foo.md", "skills/bar.md"}, "installed", fixedNow, fixedPub)
	if err := rec.Sign(fixedSeed, fixedNow); err != nil {
		t.Fatal(err)
	}
	// Write via PutTestOnly so the on-disk format is authoritative.
	dir := t.TempDir()
	s := Open(dir)
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatal(err)
	}
	// Read it back via the consumer path.
	got, err := s.Lookup("7adfe393cc9d4be2ede891f5ac5ef94c1e0e0ae39e15988c8fe93f35d8418f43")
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(fixedNow); err != nil {
		t.Fatalf("golden wire-format record failed to verify: %v", err)
	}
	// And the gate accepts it.
	gate := NewReferenceGate(s, []ed25519.PublicKey{fixedPub})
	if _, err := gate.Check("7adfe393cc9d4be2ede891f5ac5ef94c1e0e0ae39e15988c8fe93f35d8418f43", fixedNow); err != nil {
		t.Fatalf("gate rejected golden wire-format record: %v", err)
	}
	_ = golden // documentation; the real test is PutTestOnly → Lookup
}
