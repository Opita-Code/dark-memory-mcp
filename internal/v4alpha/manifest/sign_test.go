package manifest

import (
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"
)

// TestExample_CanonicalBytesStable — same entry produces identical
// canonical bytes (signing is deterministic).
func TestExample_CanonicalBytesStable(t *testing.T) {
	e := validManifestEntryChecked(t)
	a := CanonicalBytes(e)
	b := CanonicalBytes(e)
	if string(a) != string(b) {
		t.Errorf("CanonicalBytes not stable: %q vs %q", a, b)
	}
}

// TestExample_CanonicalBytesFormat — confirms the exact pipe-delimited
// layout. Pinning the format prevents accidental breakage.
func TestExample_CanonicalBytesFormat(t *testing.T) {
	e := validManifestEntry(func(x *Entry) {
		x.ArtifactID = 42
		x.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		x.SignedBy = "operator-nico"
		x.SignedAt = mustUnixTime(t, 2026, 9, 25, 12, 0, 0)
	})
	got := string(CanonicalBytes(e))
	want := "42|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|operator-nico|" +
		strconv.FormatInt(mustUnixTime(t, 2026, 9, 25, 12, 0, 0).Unix(), 10)
	if got != want {
		t.Errorf("CanonicalBytes:\n got  %q\n want %q", got, want)
	}
}

// TestExample_CanonicalBytesNilReturnsNil — defensive guard.
func TestExample_CanonicalBytesNilReturnsNil(t *testing.T) {
	if CanonicalBytes(nil) != nil {
		t.Errorf("CanonicalBytes(nil): expected nil")
	}
}

// TestExample_CanonicalBytesChangesWhenFieldsChange — different fields
// produce different bytes (signature will differ).
func TestExample_CanonicalBytesChangesWhenFieldsChange(t *testing.T) {
	base := validManifestEntryChecked(t)
	baseBytes := CanonicalBytes(base)

	diffArt := validManifestEntry(func(x *Entry) { x.ArtifactID = base.ArtifactID + 1 })
	if string(CanonicalBytes(diffArt)) == string(baseBytes) {
		t.Errorf("CanonicalBytes unchanged after ArtifactID change")
	}

	diffSHA := validManifestEntry(func(x *Entry) {
		x.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	})
	if string(CanonicalBytes(diffSHA)) == string(baseBytes) {
		t.Errorf("CanonicalBytes unchanged after SHA256 change")
	}

	diffBy := validManifestEntry(func(x *Entry) { x.SignedBy = base.SignedBy + "-2" })
	if string(CanonicalBytes(diffBy)) == string(baseBytes) {
		t.Errorf("CanonicalBytes unchanged after SignedBy change")
	}

	diffAt := validManifestEntry(func(x *Entry) { x.SignedAt = base.SignedAt.Add(time.Second) })
	if string(CanonicalBytes(diffAt)) == string(baseBytes) {
		t.Errorf("CanonicalBytes unchanged after SignedAt change")
	}
}

// TestExample_SignRoundTrip — Sign then Verify with the matching public
// key returns true. The happy path.
func TestExample_SignRoundTrip(t *testing.T) {
	priv, pub := mustKeyPair(t)
	e := validManifestEntryChecked(t)
	// Signature can be empty before Sign; Sign overwrites it.
	e.Signature = nil
	if err := Sign(e, priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(e.Signature) != ed25519.SignatureSize {
		t.Errorf("Signature length: got %d want %d", len(e.Signature), ed25519.SignatureSize)
	}
	ok, err := Verify(e, pub)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("Verify returned false on valid signature")
	}
}

// TestExample_SignDeterministic — Ed25519 is deterministic: signing
// the same canonical bytes twice yields the same signature. (This is
// a property of Ed25519 itself, not our wrapper; verifying it here
// confirms our canonical bytes are stable too.)
func TestExample_SignDeterministic(t *testing.T) {
	priv, _ := mustKeyPair(t)
	e1 := validManifestEntryChecked(t)
	e2 := validManifestEntryChecked(t)
	if err := Sign(e1, priv); err != nil {
		t.Fatalf("Sign e1: %v", err)
	}
	if err := Sign(e2, priv); err != nil {
		t.Fatalf("Sign e2: %v", err)
	}
	if string(e1.Signature) != string(e2.Signature) {
		t.Errorf("Sign not deterministic:\n  e1: %x\n  e2: %x", e1.Signature, e2.Signature)
	}
}

// TestExample_SignRejectsBadInputs — validation runs before signing.
func TestExample_SignRejectsBadInputs(t *testing.T) {
	priv, _ := mustKeyPair(t)
	cases := []struct {
		name string
		mut  func(*Entry)
	}{
		{"zero ArtifactID", func(x *Entry) { x.ArtifactID = 0 }},
		{"bad SHA256", func(x *Entry) { x.SHA256 = "abc" }},
		{"empty SignedBy", func(x *Entry) { x.SignedBy = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validManifestEntry(tc.mut)
			if err := Sign(e, priv); err == nil {
				t.Errorf("Sign: expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestExample_SignRejectsNilKey.
func TestExample_SignRejectsNilKey(t *testing.T) {
	e := validManifestEntryChecked(t)
	if err := Sign(e, nil); !errors.Is(err, ErrSignNilKey) {
		t.Fatalf("Sign(nil): expected ErrSignNilKey, got %v", err)
	}
	if err := Sign(e, ed25519.PrivateKey{}); !errors.Is(err, ErrSignNilKey) {
		t.Fatalf("Sign(empty): expected ErrSignNilKey, got %v", err)
	}
}

// TestExample_SignRejectsNilEntry.
func TestExample_SignRejectsNilEntry(t *testing.T) {
	priv, _ := mustKeyPair(t)
	if err := Sign(nil, priv); err == nil {
		t.Error("Sign(nil, priv): expected error")
	}
}

// TestExample_VerifyRejectsTamperedSHA256 — the most important
// security property: changing one byte of the canonical form breaks
// the signature.
func TestExample_VerifyRejectsTamperedSHA256(t *testing.T) {
	priv, pub := mustKeyPair(t)
	e := validManifestEntryChecked(t)
	if err := Sign(e, priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Tamper with SHA256.
	e.SHA256 = flipOneHexChar(e.SHA256)
	ok, err := Verify(e, pub)
	if err == nil {
		t.Error("Verify(tampered SHA256): expected error, got nil")
	}
	if ok {
		t.Error("Verify(tampered SHA256): expected ok=false")
	}
}

// TestExample_VerifyRejectsWrongKey — a different public key does not
// validate the signature (defends against key confusion attacks).
func TestExample_VerifyRejectsWrongKey(t *testing.T) {
	priv, _ := mustKeyPair(t)
	_, otherPub := mustKeyPair(t) // independent keypair
	e := validManifestEntryChecked(t)
	if err := Sign(e, priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ok, err := Verify(e, otherPub)
	if err == nil {
		t.Error("Verify(wrong key): expected error, got nil")
	}
	if ok {
		t.Error("Verify(wrong key): expected ok=false")
	}
}

// TestExample_VerifyRejectsBadSignatureLength.
func TestExample_VerifyRejectsBadSignatureLength(t *testing.T) {
	_, pub := mustKeyPair(t)
	e := validManifestEntry(func(x *Entry) { x.Signature = []byte("too-short") })
	_, err := Verify(e, pub)
	if !errors.Is(err, ErrBadSignatureLength) {
		t.Fatalf("Verify(short sig): expected ErrBadSignatureLength, got %v", err)
	}
}

// TestExample_VerifyRejectsNilKey.
func TestExample_VerifyRejectsNilKey(t *testing.T) {
	e := validManifestEntryChecked(t)
	_, err := Verify(e, nil)
	if !errors.Is(err, ErrVerifyNilKey) {
		t.Fatalf("Verify(nil pub): expected ErrVerifyNilKey, got %v", err)
	}
}

// TestExample_VerifyRejectsNilEntry.
func TestExample_VerifyRejectsNilEntry(t *testing.T) {
	_, pub := mustKeyPair(t)
	_, err := Verify(nil, pub)
	if err == nil {
		t.Error("Verify(nil, pub): expected error")
	}
}

// TestExample_VerifyContent — the secondary path: signature over
// the hex SHA-256 of raw content.
func TestExample_VerifyContent(t *testing.T) {
	priv, pub := mustKeyPair(t)
	content := []byte("hello world")
	hash := HashBytes(content)
	sig := ed25519.Sign(priv, []byte(hash))
	ok, err := VerifyContent(content, sig, pub)
	if err != nil {
		t.Fatalf("VerifyContent: %v", err)
	}
	if !ok {
		t.Error("VerifyContent returned false on valid signature")
	}
}

// TestExample_VerifyContentRejectsTampered.
func TestExample_VerifyContentRejectsTampered(t *testing.T) {
	priv, pub := mustKeyPair(t)
	content := []byte("hello world")
	hash := HashBytes(content)
	sig := ed25519.Sign(priv, []byte(hash))
	tampered := []byte("hello WORLD") // different content
	ok, err := VerifyContent(tampered, sig, pub)
	if err == nil {
		t.Error("VerifyContent(tampered): expected error")
	}
	if ok {
		t.Error("VerifyContent(tampered): expected ok=false")
	}
}

// TestExample_GenerateKey — sanity check on the helper.
func TestExample_GenerateKey(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("priv length: got %d want %d", len(priv), ed25519.PrivateKeySize)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("pub length: got %d want %d", len(pub), ed25519.PublicKeySize)
	}
	// pub must be derivable from priv
	if !pub.Equal(priv.Public()) {
		t.Error("GenerateKey: pub != priv.Public()")
	}
}
