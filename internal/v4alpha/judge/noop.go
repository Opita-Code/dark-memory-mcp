// Package judge — NoOp implementation for v4-alpha.1.
//
// v4-alpha.1 ships without an LLM-backed Judge. The vibe-loop
// pipeline still works end-to-end: Publish inserts the artifact
// and emits a drift row whose verdict is always VerdictAligned
// with Confidence=1.0. This lets us exercise the persistence +
// audit path during BUG-8 without depending on a configured LLM
// key.
//
// The real LLM-backed Judge lands in BUG-9, alongside the
// provider router that reads DARK_JUDGE_PROVIDER. Until then,
// every artifact published via vibe_publish will be marked
// "aligned" — operators must rely on their own review for
// correctness.
package judge

import "context"

// NoOpJudge is the default Judge when no LLM provider is
// configured. It always returns VerdictAligned with maximum
// confidence.
type NoOpJudge struct{}

// NewNoOpJudge returns a NoOpJudge.
func NewNoOpJudge() *NoOpJudge { return &NoOpJudge{} }

// Evaluate implements the Judge interface. It returns
// VerdictAligned with confidence 1.0 and a one-line reasoning
// explaining the NoOp behaviour.
func (NoOpJudge) Evaluate(ctx context.Context, evalType, specIntent string, ref *Ref) (*Verdict, error) {
	return &Verdict{
		Verdict:    VerdictAligned,
		Confidence: 1.0,
		Reasoning:  "noop judge (BUG-8): LLM-backed judge lands in BUG-9; verdict always aligned",
	}, nil
}

// Compile-time interface check.
var _ Judge = (*NoOpJudge)(nil)
