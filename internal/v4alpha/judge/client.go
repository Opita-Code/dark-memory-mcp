// Package judge defines the contract for LLM-as-judge evaluations.
// The vibe-loop uses this interface to drive drift_judge verdicts
// without depending on a specific LLM provider.
//
// In v4-alpha, tests use a FakeJudge; the real LLM-backed Judge
// (HTTP client to a configured provider) lands in a later slice.
//
// ADR-007 commit 1 introduces an extended Verdict (see types.go) and
// the Pipeline orchestrator (see pipeline.go). The legacy Judge
// interface below is preserved for v4-alpha.1 callers (the vibe-loop
// pipeline in transport/mcp/server.go still wires it directly); the
// new Pipeline type is the path forward for any new code.
package judge

import (
	"context"
	"errors"
)

// Canonical verdict values returned by any Judge implementation.
// These strings match the vibe.Verdict constants used in the
// persistence layer; the Pipeline layer bridges the two.
//
// "errored" is a non-canonical 4th value used by the Pipeline when
// a fatal edge case short-circuits (EC-002, EC-003). It is allowed
// by Verdict.Validate but is NOT returned by the legacy Judge
// interface (which only emits the three canonical strings).
const (
	VerdictAligned       = "aligned"
	VerdictDriftDetected = "drift_detected"
	VerdictNeedsHuman    = "needs_human"
	VerdictErrored       = "errored"
)

// IsValidVerdict reports whether v is a canonical verdict string.
// Used by Pipeline to bridge judge verdicts into the persistence
// layer's verdict enum. Returns true for all 4 values (the 3
// canonical + "errored" which only the Pipeline emits).
func IsValidVerdict(v string) bool {
	switch v {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman, VerdictErrored:
		return true
	}
	return false
}

// Ref is the polymorphic artifact reference passed to a Judge. It
// mirrors the vibe.ArtifactRef shape so the Pipeline layer can
// convert without loss.
type Ref struct {
	Kind       string
	Path       string
	GitSHA     string
	GitRepo    string
	URL        string
	SpecID     int64
	ArtifactID int64
	MaxBytes   int
}

// Judge is the legacy contract for LLM-as-judge evaluations. Any
// implementation MUST return a verdict that passes Validate.
//
// Evaluate is called with:
//   - evalType: "drift_judge" or another canonical evaluation type
//   - specIntent: the one-paragraph "what should this artifact be"
//     hypothesis the operator passed to vibe_publish
//   - ref: the artifact reference (file/git_sha/url/spec_id/artifact_id)
//     or nil for purely-textual artifacts
//
// New code SHOULD use Pipeline (see pipeline.go) instead of Judge
// directly; Judge is preserved for backward compat with the v4-
// alpha.1 vibe-loop wiring in transport/mcp/server.go.
type Judge interface {
	Evaluate(ctx context.Context, evalType, specIntent string, ref *Ref) (*Verdict, error)
}

// Errors returned by Judge implementations.
var (
	ErrUnavailable = errors.New("judge: unavailable")
)
