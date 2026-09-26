package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/auditgate"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
)

// checkAuditGate computes the artifact's SHA-256 and consults the
// configured gate. Returns:
//
//   - (provenance, nil)     on accept
//   - (nil,        nil)     on bypass (no gate, no resolvable body)
//   - (provenance, err)     on reject-with-evidence (publisher pubkey
//     available so operator can diagnose)
//   - (nil,        err)     on missing record (no evidence)
//
// SHA-256 derivation (priority order):
//
//  1. in.Artifact.ArtifactRef — resolve via artifact.Resolver,
//     SHA the resolved bytes. Same Resolver the LLM-judge pipeline
//     uses, so gate bytes == judge bytes.
//  2. in.Artifact.Text — SHA the raw text. Equivalent to what
//     drift_judge sees in the Phase-1 backward-compat path. Will
//     diverge in v2.22.0 when Text is removed.
//  3. neither — bypass (URL-only or empty artifact).
func (o *Orchestrator) checkAuditGate(ctx context.Context, in PublishVibeInput) (*auditgate.Provenance, error) {
	if o.AuditGate == nil {
		return nil, nil
	}
	now := o.now()
	var body []byte
	if in.Artifact.ArtifactRef != nil {
		// Same Resolver the LLM-judge pipeline uses (T08). When
		// the Materializer is injected, the text→ref path is
		// covered by it upstream of this hook.
		res := o.ensureDriftJudgeResolver()
		resolved, err := res.Resolve(ctx, *in.Artifact.ArtifactRef)
		if err != nil {
			return nil, fmt.Errorf("audit gate: resolve artifact_ref: %w", err)
		}
		body = resolved.Bytes
	} else if strings.TrimSpace(in.Artifact.Text) != "" {
		body = []byte(in.Artifact.Text)
	}
	if body == nil {
		// No body resolvable → bypass. Record at info so operators
		// notice if they expected the gate to fire.
		o.RecordError(ctx, "publish_vibe", in.SessionID,
			errors.New("audit gate: no resolvable body, gate bypassed (URL-only or empty artifact)"),
			errorobs.SeverityWarn)
		return nil, nil
	}
	sha := auditgate.HashFileBytes(body)
	rec, err := o.AuditGate.Check(sha, now)
	if err != nil {
		if rec != nil {
			p := rec.ToProvenance(false) // not trusted
			return &p, err
		}
		return nil, err
	}
	p := rec.ToProvenance(true) // trusted
	return &p, nil
}
