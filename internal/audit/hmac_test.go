// Package audit — hmac_test.go: hermetic tests for the HMAC chain
// primitives. No SQLite, no Store; uses pure in-memory inputs.
package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

// newTestKey returns a deterministic 32-byte HMAC secret for tests.
func newTestKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func TestNewKeyring_Valid(t *testing.T) {
	secret := newTestKey(t)
	hexStr := hex.EncodeToString(secret)
	k, err := NewKeyring("v1:" + hexStr)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if k == nil {
		t.Fatal("nil keyring")
	}
	if _, _, ok := k.Primary(); !ok {
		t.Error("Primary() returned !ok on non-empty keyring")
	}
	if _, ok := k.Get("v1"); !ok {
		t.Error("Get(\"v1\") returned !ok after NewKeyring")
	}
}

func TestNewKeyring_MissingColon(t *testing.T) {
	_, err := NewKeyring("no-colon-here")
	if err == nil {
		t.Fatal("expected error for missing ':'")
	}
	if !strings.Contains(err.Error(), "missing ':'") {
		t.Errorf("error message lacks 'missing colon' hint: %v", err)
	}
}

func TestNewKeyring_EmptyID(t *testing.T) {
	_, err := NewKeyring(":" + strings.Repeat("ab", 16))
	if err == nil {
		t.Fatal("expected error for empty id")
	}
	if !strings.Contains(err.Error(), "empty key id") {
		t.Errorf("error message lacks 'empty key id' hint: %v", err)
	}
}

func TestNewKeyring_ShortSecret(t *testing.T) {
	_, err := NewKeyring("v1:ab") // 1 byte — too short
	if err == nil {
		t.Fatal("expected error for short secret")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error message lacks 'too short' hint: %v", err)
	}
}

func TestNewKeyring_NoPairs(t *testing.T) {
	_, err := NewKeyring()
	if err == nil {
		t.Fatal("expected error for empty pairs")
	}
}

func TestKeyring_AddGetPrimary(t *testing.T) {
	k, err := NewKeyring("v1:" + hex.EncodeToString(newTestKey(t)))
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	// Add a v2 key
	k.Add("v2", []byte("supersecret-32-bytes-12345678AB"))
	if _, ok := k.Get("v2"); !ok {
		t.Error("Get(\"v2\") returned !ok after Add")
	}
	// IDs should contain both
	ids := k.IDs()
	if len(ids) != 2 {
		t.Errorf("IDs() length = %d; want 2", len(ids))
	}
}

func TestKeyringFromEnv_Empty(t *testing.T) {
	k, err := KeyringFromEnv("")
	if err != nil {
		t.Fatalf("KeyringFromEnv: %v", err)
	}
	if _, _, ok := k.Primary(); ok {
		t.Error("Primary() returned ok on empty keyring")
	}
}

func TestKeyringFromEnv_MultipleKeys(t *testing.T) {
	k1 := hex.EncodeToString(newTestKey(t))
	k2 := hex.EncodeToString([]byte("another-32-byte-secret-1234ABCD"))
	env := "v1:" + k1 + ",v2:" + k2
	k, err := KeyringFromEnv(env)
	if err != nil {
		t.Fatalf("KeyringFromEnv: %v", err)
	}
	if _, ok := k.Get("v1"); !ok {
		t.Error("v1 missing")
	}
	if _, ok := k.Get("v2"); !ok {
		t.Error("v2 missing")
	}
}

func TestFormatKey_RoundTrip(t *testing.T) {
	secret := newTestKey(t)
	s := FormatKey("v1", secret)
	id, parsed, err := parseKeyPair(s)
	if err != nil {
		t.Fatalf("parseKeyPair: %v", err)
	}
	if id != "v1" {
		t.Errorf("id = %q; want v1", id)
	}
	if !bytes.Equal(parsed, secret) {
		t.Errorf("secret round-trip mismatch")
	}
}

func TestFormatKeyFromInt_Deterministic(t *testing.T) {
	a := FormatKeyFromInt(42)
	b := FormatKeyFromInt(42)
	if a != b {
		t.Errorf("FormatKeyFromInt(42) not deterministic: %q vs %q", a, b)
	}
	c := FormatKeyFromInt(43)
	if a == c {
		t.Errorf("FormatKeyFromInt(42) == FormatKeyFromInt(43): %q", a)
	}
}

func TestRedactKey_PreservesID(t *testing.T) {
	got := redactKey("v1:abcdef0123456789")
	if got != "v1:***" {
		t.Errorf("redactKey = %q; want %q", got, "v1:***")
	}
}

func TestCanonicalBytes_StableAcrossCalls(t *testing.T) {
	ev := WriteEvent{
		ID:            42,
		TableName:     "agent_memory",
		RowID:         7,
		ProjectID:     "default",
		Actor:         "agent_memory_save",
		WritePath:     "SaveAgentMemory",
		ContentSHA256: "deadbeef",
		CanaryPresent: false,
		CreatedAt:     "2026-10-01T18:00:00Z",
	}
	a, err := CanonicalBytes(ev)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	b, err := CanonicalBytes(ev)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("CanonicalBytes not deterministic:\n  a=%s\n  b=%s", a, b)
	}
	// The chain fields must NOT appear in the canonical bytes (they
	// would self-reference).
	if bytes.Contains(a, []byte("chain_prev")) || bytes.Contains(a, []byte("chain_self")) || bytes.Contains(a, []byte("chain_key_id")) {
		t.Errorf("CanonicalBytes leaks chain fields: %s", a)
	}
}

func TestCanonicalBytes_ChainFieldsCleared(t *testing.T) {
	// Pre-fill chain fields; ensure they don't survive in canonical bytes.
	ev := WriteEvent{
		ID:         1,
		ChainPrev:  "should-be-zeroed",
		ChainSelf:  "should-be-zeroed",
		ChainKeyID: "should-be-zeroed",
	}
	canon, err := CanonicalBytes(ev)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if bytes.Contains(canon, []byte("should-be-zeroed")) {
		t.Errorf("chain fields leaked into canonical bytes: %s", canon)
	}
}

func TestComputeChain_GenesisVsContinuation(t *testing.T) {
	secret := newTestKey(t)
	ev := WriteEvent{ID: 1, TableName: "agent_memory", RowID: 1, ProjectID: "p", Actor: "a"}
	_, self1, err := ComputeChain(secret, "", ev)
	if err != nil {
		t.Fatalf("ComputeChain genesis: %v", err)
	}
	if self1 == "" {
		t.Fatal("genesis chain self is empty")
	}
	// Same input twice → same output (deterministic).
	_, self1c, err := ComputeChain(secret, "", ev)
	if err != nil {
		t.Fatalf("ComputeChain genesis 2: %v", err)
	}
	if self1 != self1c {
		t.Errorf("genesis HMAC not deterministic: %q vs %q", self1, self1c)
	}
	// Different prev → different output (chain continuity).
	_, self2, err := ComputeChain(secret, self1, ev)
	if err != nil {
		t.Fatalf("ComputeChain continuation: %v", err)
	}
	if self2 == self1 {
		t.Errorf("continuation HMAC equals genesis: %q", self1)
	}
}

func TestComputeChain_EmptySecret(t *testing.T) {
	_, _, err := ComputeChain(nil, "", WriteEvent{ID: 1})
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestVerifyChain_OK(t *testing.T) {
	secret := newTestKey(t)
	ev := WriteEvent{ID: 1, TableName: "agent_memory", RowID: 1, ProjectID: "p", Actor: "a"}
	prev, self, err := ComputeChain(secret, "", ev)
	if err != nil {
		t.Fatalf("ComputeChain: %v", err)
	}
	ev.ChainPrev = prev
	ev.ChainSelf = self
	ok, err := VerifyChain(secret, prev, ev)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !ok {
		t.Error("VerifyChain returned false on valid input")
	}
}

func TestVerifyChain_TamperedField(t *testing.T) {
	secret := newTestKey(t)
	ev := WriteEvent{ID: 1, TableName: "agent_memory", RowID: 1, ProjectID: "p", Actor: "a"}
	prev, self, err := ComputeChain(secret, "", ev)
	if err != nil {
		t.Fatalf("ComputeChain: %v", err)
	}
	ev.ChainPrev = prev
	ev.ChainSelf = self
	// Tamper with Actor after signing.
	ev.Actor = "attacker"
	ok, err := VerifyChain(secret, prev, ev)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if ok {
		t.Error("VerifyChain returned true after tamper")
	}
}

func TestVerifyChain_WrongSecret(t *testing.T) {
	secret1 := newTestKey(t)
	// Make a true copy (bytes.NewBuffer(...).Bytes() aliases the
	// underlying array — bad UX). Use append() to copy.
	secret2 := append([]byte(nil), secret1...)
	secret2[0] ^= 0xFF // flip one byte
	ev := WriteEvent{ID: 1, Actor: "a"}
	prev, self, err := ComputeChain(secret1, "", ev)
	if err != nil {
		t.Fatalf("ComputeChain: %v", err)
	}
	ev.ChainPrev = prev
	ev.ChainSelf = self
	ok, err := VerifyChain(secret2, prev, ev)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if ok {
		t.Error("VerifyChain returned true with wrong secret")
	}
}

func TestVerifyChain_EmptySecret(t *testing.T) {
	_, err := VerifyChain(nil, "", WriteEvent{ID: 1})
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

// === Round-trip with ExportJSONL + VerifyJSONL ===

type realListerStub struct {
	rows []WriteEvent
}

func (s *realListerStub) ListWrites(ctx context.Context, _ ListFilters) ([]WriteEvent, error) {
	_ = ctx
	return s.rows, nil
}