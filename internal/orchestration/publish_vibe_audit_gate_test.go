// publish_vibe_audit_gate_test.go: end-to-end test of the audit gate
// hook in PublishVibe. Three scenarios:
//  1. Gate not configured → publish proceeds (bypass).
//  2. Gate configured + matching audit record → publish succeeds +
//     result.AuditProvenance is populated.
//  3. Gate configured + missing audit record → publish rejected with
//     verdict=drift_detected + drift_log persisted.
//
// Uses an in-memory SQLite-backed store (via store/runtime.Open) so
// the SaveSpec/SaveArtifact/SaveDriftReport calls actually persist.
package orchestration

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/auditgate"
	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// testSeedPub returns a deterministic (seed, pubkey) pair for tests
// so any divergence between auditgate fixtures and this test is
// caught. The pubkey is derived from the seed via ed25519 so any
// mismatch is automatically caught.
func testSeedPub(t *testing.T) (seed []byte, pub ed25519.PublicKey) {
	t.Helper()
	seed = []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}
	pub = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return
}

// setupOrchForGate builds a minimal orchestrator pointed at an
// in-memory SQLite store + the given gate. No LLM judge is wired —
// this test is about the gate hook, not drift_judge.
func setupOrchForGate(t *testing.T, gate *auditgate.ReferenceGate) *Orchestrator {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "gate-test.db")
	st, err := runtime.Open(context.Background(), store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dsn,
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	o := New(st, &safety.Holder{})
	if gate != nil {
		o.WithAuditGate(gate, "")
	}
	return o
}

func TestExample_PublishVibe_GateNotConfigured_Bypasses(t *testing.T) {
	o := setupOrchForGate(t, nil)
	ctx := context.Background()
	res, err := o.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{VibeCase: "C1"},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "memory://test/1",
			Text:         "hello world",
		},
		AutoDriftCheck: ptrBool(false), // skip drift_judge for determinism
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.AuditProvenance != nil {
		t.Fatalf("expected nil provenance on bypass, got %+v", res.AuditProvenance)
	}
}

func TestExample_PublishVibe_GateAcceptsMatchingAudit(t *testing.T) {
	// Pre-populate the gate store with a signed record matching
	// the artifact body we're about to publish.
	seed, pub := testSeedPub(t)
	body := []byte("opita-market bundle v0.3.0 — install me")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	s := auditgate.Open(dir)
	// Sign at the orchestrator's current time (which is
	// time.Now().UTC() by default). The orchestrator passes its
	// own `now` to gate.Check, so they MUST match for the
	// freshness window to pass.
	now := time.Now().UTC()
	rec := auditgate.NewBundleRecord(
		"opita-market", "0.3.0", "C5", "opencode", "/cfg",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/dark.lock.json", "abc",
		sha, []string{"skills/a.md"}, "installed", now, pub,
	)
	if err := rec.Sign(seed, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	gate := auditgate.NewReferenceGate(s, []ed25519.PublicKey{pub})
	o := setupOrchForGate(t, gate)

	ctx := context.Background()
	res, err := o.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{VibeCase: "C5"},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "memory://test/2",
			Text:         string(body),
		},
		AutoDriftCheck: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.AuditProvenance == nil {
		t.Fatalf("expected non-nil provenance on accept")
	}
	if !res.AuditProvenance.Trusted {
		t.Fatalf("provenance.Trusted=false on accept path")
	}
	if res.AuditProvenance.ArtifactSHA256 != sha {
		t.Fatalf("sha drift: %s vs %s", res.AuditProvenance.ArtifactSHA256, sha)
	}
}

func TestExample_PublishVibe_GateRejectsMissingAudit(t *testing.T) {
	// Gate configured, but the store is empty. Publish should be
	// rejected with verdict=drift_detected + drift_log persisted.
	dir := t.TempDir()
	s := auditgate.Open(dir)
	_, pub := testSeedPub(t)
	gate := auditgate.NewReferenceGate(s, []ed25519.PublicKey{pub})
	o := setupOrchForGate(t, gate)

	ctx := context.Background()
	res, err := o.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{VibeCase: "C5"},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "memory://test/3",
			Text:         "untethered artifact",
		},
		AutoDriftCheck: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Verdict != "drift_detected" {
		t.Fatalf("verdict=%q want drift_detected", res.Verdict)
	}
	if res.NextAction != "reconcile" {
		t.Fatalf("next_action=%q want reconcile", res.NextAction)
	}
	if res.AuditProvenance != nil {
		t.Fatalf("expected nil provenance on missing-record rejection")
	}
	if !contains(res.Reasoning, "audit gate rejected") {
		t.Fatalf("reasoning should explain gate rejection: %q", res.Reasoning)
	}
	// The drift_log should have been persisted (DriftID > 0).
	if res.DriftID == 0 {
		t.Fatalf("drift_id=0 expected non-zero from gate rejection")
	}
}

func TestExample_PublishVibe_GateRejectsUntrustedPublisher(t *testing.T) {
	// Audit record signed by a DIFFERENT publisher. Gate has
	// different trust roots → reject with evidence (record
	// returned alongside the error so provenance carries the
	// untrusted publisher pubkey).
	dir := t.TempDir()
	s := auditgate.Open(dir)
	signerSeed := []byte{
		0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
		0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf,
		0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7,
		0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
	}
	// Derive pubkey from the signer seed so they match.
	pubFromSigner := ed25519.NewKeyFromSeed(signerSeed).Public().(ed25519.PublicKey)
	body := []byte("evil bundle that pretends to be opita-market")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])

	now := time.Now().UTC()
	rec := auditgate.NewBundleRecord(
		"opita-market", "0.3.0", "C5", "opencode", "/cfg",
		"0.1.0-alpha.8", "dbd30b9", "/cfg/dark.lock.json", "abc",
		sha, []string{"skills/evil.md"}, "installed", now, pubFromSigner,
	)
	if err := rec.Sign(signerSeed, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := s.PutTestOnly(rec); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Trust root is the GOOD pubkey, not the signer's.
	_, goodPub := testSeedPub(t)
	gate := auditgate.NewReferenceGate(s, []ed25519.PublicKey{goodPub})
	o := setupOrchForGate(t, gate)

	ctx := context.Background()
	res, err := o.PublishVibe(ctx, PublishVibeInput{
		Spec: PublishSpecInput{VibeCase: "C5"},
		Artifact: PublishArtifactInput{
			ArtifactType: "text",
			ArtifactURL:  "memory://test/4",
			Text:         string(body),
		},
		AutoDriftCheck: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Verdict != "drift_detected" {
		t.Fatalf("verdict=%q want drift_detected", res.Verdict)
	}
	// Provenance should be present (evidence) but marked untrusted.
	if res.AuditProvenance == nil {
		t.Fatalf("expected provenance on reject-with-evidence")
	}
	if res.AuditProvenance.Trusted {
		t.Fatalf("provenance.Trusted=true on reject path")
	}
}

// contains is a tiny substring helper (avoids importing strings for
// one call).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ptrBool returns *bool literal — saves typing.
func ptrBool(b bool) *bool { return &b }

// silence "imported and not used" if any helper ends up unused after
// future refactors.
var _ = t_TempDir_marker

// t_TempDir_marker is a sentinel so the test file compiles when all
// helpers above are inlined. Removed by the test runner when present.
const t_TempDir_marker = "ok"

// _ keeps filepath referenced (used by setupOrchForGate above).
var _ = filepath.Join
