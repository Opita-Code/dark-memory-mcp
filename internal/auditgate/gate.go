// Gate: the consumer-side decision API used by PublishVibe. The gate
// wraps a Store + a set of trust roots (pubkeys). Check(sha256, now)
// returns (record, error):
//
//   - record != nil, err == nil  →  accept; record.ToProvenance() is
//     attached to the artifact metadata.
//   - record != nil, err != nil  →  reject-with-evidence (caller can
//     surface the record to the operator for diagnosis without
//     admitting the artifact).
//   - record == nil, err == ErrGateNoRecord →  reject: dark-cli never
//     saw this artifact. This is the COMMON path during dev (dark-cli
//     not installed or audit dir wrong).
//   - record == nil, err == <other>  →  I/O / parse error; treated as
//     reject by the gate (fail closed).
package auditgate

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

// Gate is the abstract decision surface. ReferenceGate is the
// production implementation; tests can stub it.
type Gate interface {
	Check(artifactSHA256 string, now time.Time) (*AuditRecord, error)
	TrustRoots() []ed25519.PublicKey
}

// ErrGateNoRecord is the "no audit found" rejection. Distinct from
// ErrRecordNotFound (returned by Store.Lookup for missing files) so
// the gate layer can produce a clean operator-facing message.
var ErrGateNoRecord = errors.New("auditgate: no audit record for artifact")

// ErrGateUntrusted is the "signed by non-trusted pubkey" rejection.
var ErrGateUntrusted = errors.New("auditgate: signed by untrusted publisher")

// ErrGateDisabled is the "gate not configured" sentinel. Returned by
// Check on a nil or unconfigured gate. Production never reaches this
// (the orchestrator short-circuits when AuditGate is nil), but tests
// use it to assert the bypass path.
var ErrGateDisabled = errors.New("auditgate: gate not configured")

// ReferenceGate is the production Gate impl. roots is the set
// of ed25519 public keys (raw bytes) that the gate considers
// authoritative. The set is set at construction time and frozen
// (callers that want dynamic trust must build a new gate).
type ReferenceGate struct {
	Store *Store
	roots []ed25519.PublicKey
}

// NewReferenceGate wires a gate with the given store + trust roots.
// store may be nil — Check then always returns ErrGateDisabled. trust
// may be empty — Check then always rejects (fail-closed).
func NewReferenceGate(store *Store, trust []ed25519.PublicKey) *ReferenceGate {
	roots := make([]ed25519.PublicKey, len(trust))
	copy(roots, trust)
	return &ReferenceGate{Store: store, roots: roots}
}

// TrustRoots returns a defensive copy (callers cannot mutate).
func (g *ReferenceGate) TrustRoots() []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, len(g.roots))
	copy(out, g.roots)
	return out
}

// Check returns (record, error). On accept both are non-nil and err
// is nil. On rejection with evidence, record is non-nil so the
// orchestrator can include the publisher pubkey in operator-facing
// diagnostics. On missing record or I/O error, record is nil.
//
// isTrusted is the only security-critical function in this package:
// it MUST be constant-time per pubkey, MUST handle length-mismatched
// pubkeys safely, and MUST short-circuit on the FIRST trusted root
// (to keep the lookup time predictable across key sets).
func (g *ReferenceGate) Check(artifactSHA256 string, now time.Time) (*AuditRecord, error) {
	if g == nil || g.Store == nil {
		return nil, ErrGateDisabled
	}
	rec, err := g.Store.Lookup(artifactSHA256)
	if errors.Is(err, ErrRecordNotFound) {
		return nil, ErrGateNoRecord
	}
	if err != nil {
		return nil, fmt.Errorf("auditgate: store lookup: %w", err)
	}
	// Trust root membership. If the set is empty, fail closed.
	if len(g.roots) == 0 {
		return rec, ErrGateUntrusted
	}
	if !g.isTrusted(ed25519.PublicKey(rec.PublisherPubkey)) {
		return rec, ErrGateUntrusted
	}
	if verr := rec.Verify(now); verr != nil {
		return rec, verr
	}
	return rec, nil
}

// isTrusted reports whether pub equals any trust root. Constant-time
// per root (uses bytesEqualConstTime which XORs every byte). NOT
// constant-time across roots — the first match returns early. This
// is acceptable because:
//   - the trust root set is small (single-digit in v0.1)
//   - the loop is bounded (operator-set, not attacker-controlled)
//   - the early return does not leak WHICH root matched (the pub
//     itself is the secret, and is constant-time compared)
//
// The constant-time guarantee per root is what matters: an attacker
// probing the gate cannot measure WHICH root matched by timing.
func (g *ReferenceGate) isTrusted(pub ed25519.PublicKey) bool {
	for _, root := range g.roots {
		if bytesEqualConstTime(pub, root) {
			return true
		}
	}
	return false
}

// bytesEqualConstTime is constant-time equality on []byte. Returns
// false on length mismatch (so length is not a timing oracle either).
// Used by isTrusted. Same implementation as dark-cli's
// internal/audit/gate.go::bytesEqualConstTime — divergence here is
// caught by gate_test.go's TestExample_Gate_RejectsUntrustedRecord.
func bytesEqualConstTime(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// Provenance is the projection the orchestrator attaches to the
// artifact metadata. Only fields safe to expose to downstream
// consumers (NOT the publisher pubkey or signature, which are
// gate-internal). Mirrors dark-cli/internal/audit/gate.go::Provenance.
type Provenance struct {
	ArtifactSHA256  string   `json:"artifact_sha256"`
	Kind            Kind     `json:"kind"`
	TemplateName    string   `json:"template_name,omitempty"`
	TemplateVersion string   `json:"template_version,omitempty"`
	VibeCase        string   `json:"vibe_case,omitempty"`
	Harness         string   `json:"harness"`
	DarkCLIVersion  string   `json:"dark_cli_version"`
	DarkCLICommit   string   `json:"dark_cli_commit,omitempty"`
	Outcome         string   `json:"outcome,omitempty"`
	FilesWritten    []string `json:"files_written,omitempty"`
	RecordedAt      int64    `json:"recorded_at_unix"`
	Trusted         bool     `json:"trusted"`
}

// ToProvenance projects a record into the Provenance shape. Safe to
// call on a record returned by Check (whether accepted or rejected-
// with-evidence) — the projection does not depend on verification
// outcome. The Trusted field carries the verification verdict so
// downstream consumers can branch without re-verifying.
func (r *AuditRecord) ToProvenance(trusted bool) Provenance {
	return Provenance{
		ArtifactSHA256:  r.ArtifactSHA256,
		Kind:            r.Kind,
		TemplateName:    r.TemplateName,
		TemplateVersion: r.TemplateVersion,
		VibeCase:        r.VibeCase,
		Harness:         r.Harness,
		DarkCLIVersion:  r.DarkCLIVersion,
		DarkCLICommit:   r.DarkCLICommit,
		Outcome:         r.Outcome,
		FilesWritten:    append([]string(nil), r.FilesWritten...),
		RecordedAt:      r.RecordedAt,
		Trusted:         trusted,
	}
}

// asTime removed: Check now takes time.Time directly.
