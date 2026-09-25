// Package judge defines the contract for LLM-as-judge evaluations.
// The vibe-loop uses this interface to drive drift_judge verdicts
// without depending on a specific LLM provider.
//
// In v4-alpha, tests use a FakeJudge; the real LLM-backed Judge
// (HTTP client to a configured provider) lands in a later slice.
package judge

import (
	"context"
	"errors"
	"fmt"
)

// Canonical verdict values returned by any Judge implementation.
// These strings match the vibe.Verdict constants used in the
// persistence layer; the Pipeline layer bridges the two.
const (
	VerdictAligned       = "aligned"
	VerdictDriftDetected = "drift_detected"
	VerdictNeedsHuman    = "needs_human"
)

// Verdict is one judge's verdict on one artifact.
//
// Invariants (enforced by Validate):
//   - Verdict is one of the canonical three.
//   - Confidence is in [0.0, 1.0] inclusive.
type Verdict struct {
	Verdict    string
	Confidence float64
	Reasoning  string
}

// Validate returns nil for a fully valid verdict, or an error
// describing the failed invariant.
func (v *Verdict) Validate() error {
	switch v.Verdict {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman:
		// ok
	default:
		return fmt.Errorf("judge: invalid verdict %q", v.Verdict)
	}
	if v.Confidence < 0.0 || v.Confidence > 1.0 {
		return fmt.Errorf("judge: confidence %f out of [0,1]", v.Confidence)
	}
	return nil
}

// IsValidVerdict reports whether v is a canonical verdict string.
// Used by Pipeline to bridge judge verdicts into the persistence
// layer's verdict enum.
func IsValidVerdict(v string) bool {
	switch v {
	case VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman:
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

// Judge is the contract for LLM-as-judge evaluations. Any
// implementation MUST return a verdict that passes Validate.
//
// Evaluate is called with:
//   - evalType: "drift_judge" or another canonical evaluation type
//   - specIntent: the one-paragraph "what should this artifact be"
//     hypothesis the operator passed to vibe_publish
//   - ref: the artifact reference (file/git_sha/url/spec_id/artifact_id)
//     or nil for purely-textual artifacts
type Judge interface {
	Evaluate(ctx context.Context, evalType, specIntent string, ref *Ref) (*Verdict, error)
}

// Errors returned by Judge implementations.
var (
	ErrUnavailable = errors.New("judge: unavailable")
)
