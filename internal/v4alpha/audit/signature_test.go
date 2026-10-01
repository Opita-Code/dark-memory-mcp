// Package audit_test — Phase 6 alpha.18.1 ADR-017 tests (Ed25519 signature).
package audit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

// TestParsePrivateKey_RoundTrip verifies base64 encoding/decoding
// of a freshly generated Ed25519 private key.
func TestParsePrivateKey_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	privB64 := base64.StdEncoding.EncodeToString(priv)
	parsed, err := audit.ParsePrivateKey(privB64)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	if !ed25519.PrivateKey(parsed).Equal(priv) {
		t.Errorf("parsed private key does not equal original")
	}
	if !ed25519.PrivateKey(parsed).Public().(ed25519.PublicKey).Equal(pub) {
		t.Errorf("parsed private key derives different public key")
	}
}

// TestParsePrivateKey_EmptyRejected verifies the empty string is rejected.
func TestParsePrivateKey_EmptyRejected(t *testing.T) {
	_, err := audit.ParsePrivateKey("")
	if err == nil {
		t.Fatal("expected error for empty private key, got nil")
	}
}

// TestParsePrivateKey_BadBase64Rejected verifies non-base64 input is rejected.
func TestParsePrivateKey_BadBase64Rejected(t *testing.T) {
	_, err := audit.ParsePrivateKey("not-base64-!!!")
	if err == nil {
		t.Fatal("expected error for non-base64 input, got nil")
	}
}

// TestParsePrivateKey_WrongLengthRejected verifies wrong-length
// decoded bytes are rejected.
func TestParsePrivateKey_WrongLengthRejected(t *testing.T) {
	shortB64 := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := audit.ParsePrivateKey(shortB64)
	if err == nil {
		t.Fatal("expected error for short private key, got nil")
	}
}

// TestSignRowHash_Deterministic verifies the same row_hash + key
// produces the same signature (Ed25519 is deterministic for the
// same inputs — no nonce in the signature itself).
func TestSignRowHash_Deterministic(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	rowHash := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	sig1 := audit.SignRowHash(rowHash, priv)
	sig2 := audit.SignRowHash(rowHash, priv)
	if len(sig1) != ed25519.SignatureSize {
		t.Errorf("sig1 length = %d, want %d", len(sig1), ed25519.SignatureSize)
	}
	if !bytesEq(sig1, sig2) {
		t.Errorf("Ed25519 produced different signatures for the same (key, message)")
	}
}

// TestVerifyRowHashSignature_Valid verifies a freshly signed row
// verifies with the matching public key.
func TestVerifyRowHashSignature_Valid(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	rowHash := []byte("0123456789abcdef0123456789abcdef")
	sig := audit.SignRowHash(rowHash, priv)
	if err := audit.VerifyRowHashSignature(rowHash, sig, pub); err != nil {
		t.Errorf("VerifyRowHashSignature returned %v on valid signature", err)
	}
}

// TestVerifyRowHashSignature_WrongKey verifies a signature signed
// by key A does NOT verify under key B.
func TestVerifyRowHashSignature_WrongKey(t *testing.T) {
	_, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	rowHash := []byte("0123456789abcdef0123456789abcdef")
	sig := audit.SignRowHash(rowHash, privA)
	if err := audit.VerifyRowHashSignature(rowHash, sig, pubB); err == nil {
		t.Error("VerifyRowHashSignature accepted a signature from a different key")
	}
}

// TestVerifyRowHashSignature_ModifiedRowHash verifies that changing
// even 1 byte of the row_hash invalidates the signature.
func TestVerifyRowHashSignature_ModifiedRowHash(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	rowHash := []byte("0123456789abcdef0123456789abcdef")
	sig := audit.SignRowHash(rowHash, priv)
	rowHash[0] ^= 1
	if err := audit.VerifyRowHashSignature(rowHash, sig, pub); err == nil {
		t.Error("VerifyRowHashSignature accepted a signature over modified row_hash")
	}
}

// TestRowHashPublicKey_Deterministic verifies the SHA-256 derivation
// is stable (no nonce, no random component).
func TestRowHashPublicKey_Deterministic(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id1 := audit.RowHashPublicKey(pub)
	id2 := audit.RowHashPublicKey(pub)
	if len(id1) != 32 {
		t.Errorf("pubkey hash length = %d, want 32", len(id1))
	}
	if !bytesEq(id1, id2) {
		t.Errorf("RowHashPublicKey is not deterministic for the same public key")
	}
}

// TestApplySignatureColumns_Idempotent verifies the migration can be
// applied multiple times without error.
func TestApplySignatureColumns_Idempotent(t *testing.T) {
	ctx := context.Background()
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	if err := audit.ApplySignatureColumns(ctx, db); err != nil {
		t.Fatalf("ApplySignatureColumns (first): %v", err)
	}
	if err := audit.ApplySignatureColumns(ctx, db); err != nil {
		t.Fatalf("ApplySignatureColumns (second): %v", err)
	}

	for _, col := range []string{"signature", "sig_pubkey"} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('audit_log') WHERE name = ?",
			col,
		).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 1 {
			t.Errorf("column %s not present after ApplySignatureColumns", col)
		}
	}
}

// TestWriter_SetSigner_ProducesSignature verifies Write with SetSigner
// configured produces a non-NULL signature column.
func TestWriter_SetSigner_ProducesSignature(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	w := audit.NewWriter(db)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	w.SetSigner(priv)

	ctx := context.Background()
	id, err := w.Write(ctx, "test-actor", "", []byte("payload"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var sig []byte
	if err := db.QueryRowContext(ctx,
		"SELECT signature FROM audit_log WHERE audit_id = ?", id,
	).Scan(&sig); err != nil {
		t.Fatalf("query signature: %v", err)
	}
	if sig == nil {
		t.Error("expected non-NULL signature after Write with signer configured")
	}
	if len(sig) != ed25519.SignatureSize {
		t.Errorf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
}

// TestWriter_NoSigner_NoSignature verifies Write without SetSigner
// leaves the signature column NULL.
func TestWriter_NoSigner_NoSignature(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	w := audit.NewWriter(db)
	ctx := context.Background()
	id, err := w.Write(ctx, "test-actor", "", []byte("payload"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var sig []byte
	if err := db.QueryRowContext(ctx,
		"SELECT signature FROM audit_log WHERE audit_id = ?", id,
	).Scan(&sig); err != nil {
		t.Fatalf("query signature: %v", err)
	}
	if sig != nil {
		t.Errorf("expected NULL signature without SetSigner, got %d bytes", len(sig))
	}
}

// TestVerifyWithSignature_ValidChain verifies a clean signed chain verifies.
func TestVerifyWithSignature_ValidChain(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	w := audit.NewWriter(db)
	w.SetSigner(priv)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		payload := []byte{'p', byte('a' + i)}
		if _, err := w.Write(ctx, "test-actor", "", payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	res, err := audit.VerifyWithSignature(ctx, db, 0, 0, pub)
	if err != nil {
		t.Fatalf("VerifyWithSignature: %v", err)
	}
	if !res.Verified {
		t.Errorf("Verified = false; BrokenAt=%d, SignatureFailures=%d",
			res.BrokenAt, res.SignatureFailures)
	}
	if res.SignaturesChecked != 5 {
		t.Errorf("SignaturesChecked = %d, want 5", res.SignaturesChecked)
	}
	if res.SignaturesMissing != 0 {
		t.Errorf("SignaturesMissing = %d, want 0", res.SignaturesMissing)
	}
}

// TestVerifyWithSignature_DetectsForgery verifies modifying a row
// after signing invalidates the signature.
func TestVerifyWithSignature_DetectsForgery(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	w := audit.NewWriter(db)
	w.SetSigner(priv)

	ctx := context.Background()
	id, err := w.Write(ctx, "test-actor", "", []byte("original payload"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		"UPDATE audit_log SET payload = ? WHERE audit_id = ?",
		[]byte("tampered payload"), id,
	); err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}

	res, err := audit.VerifyWithSignature(ctx, db, 0, 0, pub)
	if err != nil {
		t.Fatalf("VerifyWithSignature: %v", err)
	}
	if res.Verified {
		t.Error("Verified = true on tampered row; expected false")
	}
	if res.BrokenAt != id && res.FirstSignatureFailure != id {
		t.Errorf("detection missed tampered row: BrokenAt=%d, FirstSignatureFailure=%d",
			res.BrokenAt, res.FirstSignatureFailure)
	}
}

// TestVerifyWithSignature_DetectsWrongKey verifies a row signed by
// a different key fails verification.
func TestVerifyWithSignature_DetectsWrongKey(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, wrongPriv, _ := ed25519.GenerateKey(rand.Reader)

	w := audit.NewWriter(db)
	w.SetSigner(wrongPriv)

	ctx := context.Background()
	if _, err := w.Write(ctx, "test-actor", "", []byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := audit.VerifyWithSignature(ctx, db, 0, 0, pub)
	if err != nil {
		t.Fatalf("VerifyWithSignature: %v", err)
	}
	if res.Verified {
		t.Error("Verified = true with wrong verification key; expected false")
	}
	if res.SignatureFailures != 1 {
		t.Errorf("SignatureFailures = %d, want 1", res.SignatureFailures)
	}
}

// TestVerifyWithSignature_LegacyRowsTolerated verifies rows without
// signatures (legacy, NULL) are counted in SignaturesMissing but do
// not fail verification.
func TestVerifyWithSignature_LegacyRowsTolerated(t *testing.T) {
	db, _ := sqlOpenMemory()
	if err := recover(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := audit.ApplySignatureColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplySignatureColumns: %v", err)
	}

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	w := audit.NewWriter(db)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		payload := []byte{'p', byte('a' + i)}
		if _, err := w.Write(ctx, "test-actor", "", payload); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	res, err := audit.VerifyWithSignature(ctx, db, 0, 0, pub)
	if err != nil {
		t.Fatalf("VerifyWithSignature: %v", err)
	}
	if !res.Verified {
		t.Error("Verified = false on legacy rows; expected true (chain intact, no signatures to check)")
	}
	if res.SignaturesChecked != 0 {
		t.Errorf("SignaturesChecked = %d, want 0", res.SignaturesChecked)
	}
	if res.SignaturesMissing != 3 {
		t.Errorf("SignaturesMissing = %d, want 3", res.SignaturesMissing)
	}
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
