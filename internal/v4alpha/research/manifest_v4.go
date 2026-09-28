// Manifest extension: Tier + ServesVibeCases. This file adds the
// v4alpha-specific fields to the existing BackendManifest type
// declared in research_manifest.go. The new fields are zero-value-
// safe so existing call sites and tests are unaffected; the
// extended Validate() accepts the new fields but does not require
// them (backends that want a single intent without a vibe-case
// preference can leave ServesVibeCases nil = "serves all").
//
// Tier is the order in which the executor tries backends: T1 first,
// T2 second, T3 third. A T1 backend that returns is enough; the
// executor does not fan out to T2 unless T1 returned empty (or the
// operator asked for depth=deep).
//
// ServesVibeCases is the discipline the operator asked for: each
// backend declares which vibe-cases it is useful for. The executor
// uses this to (a) skip backends whose intent is correct but whose
// vibe-case is not the operator's, (b) emit a tier-ordered fan-out
// in audit logs, (c) inform the operator in `dark_memory_research_
// list_backends` (future tool) which backends are best for what.
package research

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/security"
)

// Tier is the priority of a backend. Lower = called first.
type Tier int

const (
	// Tier1Authoritative is a registry or primary-source backend
	// (NVD for CVE, RDAP for domain, DoH for DNS, npm for the
	// package's existence). T1 alone is enough for the operator
	// to know the answer; T2/T3 are corroboration or discovery.
	Tier1Authoritative Tier = 1

	// Tier2Corroborant is a second-source backend that confirms
	// what a T1 said (GitHub Advisory for CVE, Crossref for a
	// paper, HN Algolia for news, HIBP-range for password
	// exposure). The executor uses T2 when (a) T1 returned
	// nothing, or (b) depth=deep, or (c) the operator explicitly
	// asked for corroboration.
	Tier2Corroborant Tier = 2

	// Tier3Discovery is an exploration backend (DDG, OpenAlex,
	// Nominatim, crt.sh, arXiv). The executor uses T3 only when
	// depth=deep AND T1+T2 returned nothing, because T3 is the
	// most likely to be rate-limited and the least likely to give
	// a definitive answer.
	Tier3Discovery Tier = 3
)

// String renders the tier for audit. Stable across versions.
func (t Tier) String() string {
	switch t {
	case Tier1Authoritative:
		return "T1"
	case Tier2Corroborant:
		return "T2"
	case Tier3Discovery:
		return "T3"
	default:
		return "T?"
	}
}

// VibeCase is the operator-facing classification of the artifact
// being judged. The research executor uses it to choose which
// backends to call: a code artifact (C1) wants npm + OSV + GitHub,
// a governance claim (C7) wants OpenAlex + Crossref.
type VibeCase string

const (
	VibeC1Code       VibeCase = "C1"
	VibeC2Text       VibeCase = "C2"
	VibeC3Image      VibeCase = "C3"
	VibeC4Video      VibeCase = "C4"
	VibeC5Bundle     VibeCase = "C5"
	VibeC6Infra      VibeCase = "C6"
	VibeC7Governance VibeCase = "C7"
)

// AllVibeCases is the canonical 7-case ordering. Used for validation
// and for the audit "no VibeCase filter" sentinel ("all").
func AllVibeCases() []VibeCase {
	return []VibeCase{VibeC1Code, VibeC2Text, VibeC3Image, VibeC4Video, VibeC5Bundle, VibeC6Infra, VibeC7Governance}
}

// IsValidVibeCase reports whether v is one of the 7 canonical cases.
func IsValidVibeCase(v VibeCase) bool {
	switch v {
	case VibeC1Code, VibeC2Text, VibeC3Image, VibeC4Video,
		VibeC5Bundle, VibeC6Infra, VibeC7Governance:
		return true
	}
	return false
}

// ParseVibeCase normalizes raw operator input (trims, uppercases
// the "c" so "c1" / "C1" both work) and maps to the canonical
// form. Empty input returns ErrEmptyVibeCase.
var ErrEmptyVibeCase = errors.New("research: vibe_case is empty")

// ParseVibeCase returns the canonical VibeCase for a raw string.
// Operator ergonomics: "C1", "c1", " C1 " all work.
func ParseVibeCase(raw string) (VibeCase, error) {
	norm := VibeCase(strings.ToUpper(strings.TrimSpace(raw)))
	if norm == "" {
		return "", ErrEmptyVibeCase
	}
	if !IsValidVibeCase(norm) {
		return "", errors.New("research: unknown vibe_case: " + string(norm))
	}
	return norm, nil
}

// Extended manifest fields. These are defined as a separate struct
// so the existing BackendManifest.Validate keeps its contract;
// callers opt in to the extended validation by calling
// ValidateExtended.
//
// V4alpha-only: Tiers and VibeCases were not part of the
// research_manifest.go v0.1 surface (cycle 1 of the research
// package; the package is being expanded for BUG-10 10a).
type ManifestV4 struct {
	// Tier declares the priority of this backend. T1 is called
	// first; T3 is called only when depth=deep AND T1+T2 returned
	// nothing. Zero value is Tier3Discovery; call sites that want
	// T1 must set it explicitly.
	Tier Tier

	// ServesVibeCases declares which vibe-cases the backend is
	// useful for. Empty = "all" (a small universal utility, e.g.
	// the meta `multi` intent backend). The executor filters
	// backends whose vibe-case is not the operator's request
	// unless the request has no vibe-case (e.g. multi-intent
	// fan-out).
	ServesVibeCases []VibeCase

	// ByteCapHint is the suggested cap for a single response
	// body. The executor reads this as a starting point and
	// narrows it to the smaller of ByteCapHint and the
	// operator's depth-based cap (16/32/64 KB). Zero = no hint,
	// fall back to the executor default.
	ByteCapHint int

	// URLTemplate is the GET URL the backend dials. Substitution
	// happens in the executor (parameterized by the target id).
	// This is the canonical home of the URL; ValidateURL runs
	// against the substituted form. NOT the operator's input.
	URLTemplate string
}

// Extended is the v4alpha-shaped manifest. It embeds the original
// BackendManifest so the v0.1 Validate keeps working; new fields
// live in ManifestV4 and are validated separately.
type ExtendedManifest struct {
	BackendManifest
	ManifestV4
}

// ValidateExtended enforces the v4alpha invariants. It calls
// BackendManifest.Validate first (the v0.1 contract), then checks:
//   - Tier is 1..3
//   - if URLTemplate is set, security.ValidateURL accepts it
//   - if ServesVibeCases is non-empty, every entry is a valid
//     VibeCase
//
// ByteCapHint has no invariant; zero is the "no hint" value and
// the executor falls back to the operator's depth-based cap.
func (m *ExtendedManifest) ValidateExtended() error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Tier < Tier1Authoritative || m.Tier > Tier3Discovery {
		return errors.New("research: tier must be 1, 2, or 3")
	}
	if m.URLTemplate != "" {
		if err := security.ValidateURL(m.URLTemplate); err != nil {
			return fmt.Errorf("URLTemplate: %w", err)
		}
	}
	for _, v := range m.ServesVibeCases {
		if !IsValidVibeCase(v) {
			return errors.New("research: invalid vibe_case: " + string(v))
		}
	}
	return nil
}
